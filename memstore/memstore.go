// Package memstore keeps tables in memory. It is meant for tests: it passes
// the same conformance suite as the SQL stores (storetest), so code tested
// against it behaves the same on SQLite and Postgres. Data is lost when the
// process ends.
//
// Every operation and every transaction holds one lock, so transactions are
// serializable. Inside Store.Tx, use only the store passed to the function:
// the outer store would wait for the lock forever.
package memstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/danielgtmn/ablage"
)

// Store is an in-memory ablage.Store.
type Store struct {
	shared *shared
	tx     *undoLog // nil outside a transaction
}

type shared struct {
	mu     sync.Mutex
	cat    ablage.Catalog
	tables map[string]map[string]ablage.Row // migrated tables: id -> row
}

type undoLog struct {
	entries []undo
	done    bool // the transaction has ended; the tx store must not be used
}

// undo restores a row to what it was before a change (old nil: absent).
type undo struct {
	table, id string
	old       ablage.Row
}

// New returns an empty store.
func New() *Store {
	return &Store{shared: &shared{tables: map[string]map[string]ablage.Row{}}}
}

var _ ablage.Store = (*Store)(nil)

// run executes one operation atomically: on error its changes are undone.
func (s *Store) run(ctx context.Context, fn func(op *op) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.tx == nil {
		s.shared.mu.Lock()
		defer s.shared.mu.Unlock()
	} else if s.tx.done {
		return errors.New("memstore: the transaction has ended (use the outer store)")
	}
	o := &op{sh: s.shared}
	if err := fn(o); err != nil {
		o.rollback()
		return err
	}
	if s.tx != nil {
		s.tx.entries = append(s.tx.entries, o.log...)
	}
	return nil
}

// Declare implements ablage.Store.
func (s *Store) Declare(tables ...ablage.Table) error {
	if s.tx != nil {
		return errors.New("memstore: Declare in a transaction")
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	return s.shared.cat.Declare(tables...)
}

// Migrate implements ablage.Store.
func (s *Store) Migrate(ctx context.Context) error {
	if s.tx != nil {
		return errors.New("memstore: Migrate in a transaction")
	}
	return s.run(ctx, func(o *op) error {
		tables, err := o.sh.cat.Tables()
		if err != nil {
			return err
		}
		for _, t := range tables {
			if o.sh.tables[t.Name] != nil {
				continue
			}
			o.sh.tables[t.Name] = map[string]ablage.Row{}
		}
		return nil
	})
}

// Tx implements ablage.Store.
func (s *Store) Tx(ctx context.Context, fn func(tx ablage.Store) error) error {
	if s.tx != nil {
		return fn(s)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	child := &Store{shared: s.shared, tx: &undoLog{}}
	commit := false
	defer func() {
		child.tx.done = true
		if !commit { // an error or a panic
			(&op{sh: s.shared, log: child.tx.entries}).rollback()
		}
	}()
	if err := fn(child); err != nil {
		return err
	}
	commit = true
	return nil
}

// Insert implements ablage.Store.
func (s *Store) Insert(ctx context.Context, table string, row ablage.Row) error {
	return s.run(ctx, func(o *op) error {
		t, rows, err := o.table(table)
		if err != nil {
			return err
		}
		row, err := t.CheckInsert(row)
		if err != nil {
			return err
		}
		return o.insert(t, rows, row)
	})
}

// Upsert implements ablage.Store.
func (s *Store) Upsert(ctx context.Context, table string, row, update ablage.Row, where ablage.Cond) (bool, error) {
	return s.UpsertOn(ctx, table, []string{"id"}, row, update, where)
}

var _ ablage.UniqueUpserter = (*Store)(nil)

// UpsertOn implements ablage.UniqueUpserter.
func (s *Store) UpsertOn(ctx context.Context, table string, on []string, row, update ablage.Row, where ablage.Cond) (bool, error) {
	var matched bool
	err := s.run(ctx, func(o *op) error {
		t, rows, err := o.table(table)
		if err != nil {
			return err
		}
		if on, err = t.CheckConflict(on); err != nil {
			return err
		}
		if row, err = t.CheckInsert(row); err != nil {
			return err
		}
		if update, err = t.CheckUpdate(update); err != nil {
			return err
		}
		if where, err = t.CheckCond(where); err != nil {
			return err
		}
		var have ablage.Row
		if on[0] == "id" {
			have = rows[row["id"].(string)]
		} else {
			for _, r := range rows {
				if t.Conflicts(on, row, r) {
					have = r
					break
				}
			}
		}
		if have == nil {
			matched = true
			return o.insert(t, rows, row)
		}
		if !where.Match(have) {
			return nil
		}
		matched = true
		if len(update) == 0 {
			return nil
		}
		return o.update(t, rows, []ablage.Row{have}, update)
	})
	return matched, err
}

// Find implements ablage.Store.
func (s *Store) Find(ctx context.Context, table string, q ablage.Query) ([]ablage.Row, error) {
	var out []ablage.Row
	err := s.run(ctx, func(o *op) error {
		t, rows, err := o.table(table)
		if err != nil {
			return err
		}
		if q, err = t.CheckQuery(q); err != nil {
			return err
		}
		list := match(rows, q.Where)
		slices.SortFunc(list, func(a, b ablage.Row) int {
			switch {
			case q.Less(a, b):
				return -1
			case q.Less(b, a):
				return 1
			}
			return 0
		})
		list = list[min(q.Offset, len(list)):]
		if q.Limit > 0 && len(list) > q.Limit {
			list = list[:q.Limit]
		}
		out = make([]ablage.Row, len(list))
		for i, r := range list {
			out[i] = r.Clone()
		}
		return nil
	})
	return out, err
}

// Count implements ablage.Store.
func (s *Store) Count(ctx context.Context, table string, where ablage.Cond) (int64, error) {
	var n int64
	err := s.run(ctx, func(o *op) error {
		t, rows, err := o.table(table)
		if err != nil {
			return err
		}
		if where, err = t.CheckCond(where); err != nil {
			return err
		}
		n = int64(len(match(rows, where)))
		return nil
	})
	return n, err
}

// Update implements ablage.Store.
func (s *Store) Update(ctx context.Context, table string, set ablage.Row, where ablage.Cond) (int64, error) {
	var n int64
	err := s.run(ctx, func(o *op) error {
		t, rows, err := o.table(table)
		if err != nil {
			return err
		}
		if set, err = t.CheckUpdate(set); err != nil {
			return err
		}
		if len(set) == 0 {
			return fmt.Errorf("memstore: update of %s sets nothing", table)
		}
		if where, err = t.CheckCond(where); err != nil {
			return err
		}
		list := match(rows, where)
		n = int64(len(list))
		return o.update(t, rows, list, set)
	})
	return n, err
}

// Delete implements ablage.Store.
func (s *Store) Delete(ctx context.Context, table string, where ablage.Cond) (int64, error) {
	var n int64
	err := s.run(ctx, func(o *op) error {
		t, rows, err := o.table(table)
		if err != nil {
			return err
		}
		if where, err = t.CheckCond(where); err != nil {
			return err
		}
		list := match(rows, where)
		n = int64(len(list))
		ids := make([]string, len(list))
		for i, r := range list {
			ids[i] = r["id"].(string)
		}
		return o.delete(table, ids)
	})
	return n, err
}

var _ ablage.Aggregator = (*Store)(nil)

// Aggregate implements ablage.Aggregator.
func (s *Store) Aggregate(ctx context.Context, table string, a ablage.Aggregation) ([]ablage.Row, error) {
	var out []ablage.Row
	err := s.run(ctx, func(o *op) error {
		t, rows, err := o.table(table)
		if err != nil {
			return err
		}
		if a, err = t.CheckAggregation(a); err != nil {
			return err
		}
		out, err = a.Apply(match(rows, a.Where))
		return err
	})
	return out, err
}

func match(rows map[string]ablage.Row, where ablage.Cond) []ablage.Row {
	var out []ablage.Row
	for _, r := range rows {
		if where.Match(r) {
			out = append(out, r)
		}
	}
	return out
}

// op is one operation in progress; log undoes its changes.
type op struct {
	sh  *shared
	log []undo
}

func (o *op) rollback() {
	for i := len(o.log) - 1; i >= 0; i-- {
		u := o.log[i]
		if u.old == nil {
			delete(o.sh.tables[u.table], u.id)
		} else {
			o.sh.tables[u.table][u.id] = u.old
		}
	}
	o.log = nil
}

func (o *op) table(name string) (ablage.Table, map[string]ablage.Row, error) {
	t, err := o.sh.cat.Table(name)
	if err != nil {
		return t, nil, err
	}
	rows := o.sh.tables[name]
	if rows == nil {
		return t, nil, fmt.Errorf("memstore: table %s does not exist yet (run Migrate)", name)
	}
	return t, rows, nil
}

func (o *op) put(table string, rows map[string]ablage.Row, row ablage.Row) {
	id := row["id"].(string)
	o.log = append(o.log, undo{table: table, id: id, old: rows[id]})
	rows[id] = row
}

func (o *op) remove(table string, rows map[string]ablage.Row, id string) {
	o.log = append(o.log, undo{table: table, id: id, old: rows[id]})
	delete(rows, id)
}

func (o *op) insert(t ablage.Table, rows map[string]ablage.Row, row ablage.Row) error {
	id := row["id"].(string)
	if _, ok := rows[id]; ok {
		return fmt.Errorf("%w: %s id %q", ablage.ErrDuplicate, t.Name, id)
	}
	row = row.Clone()
	o.put(t.Name, rows, row)
	return o.check(t, rows, []ablage.Row{row})
}

// update writes rows one by one in id order and checks each right away, as
// SQL databases check unique constraints row by row.
func (o *op) update(t ablage.Table, rows map[string]ablage.Row, list []ablage.Row, set ablage.Row) error {
	slices.SortFunc(list, func(a, b ablage.Row) int { return ablage.Compare(a["id"], b["id"]) })
	for _, old := range list {
		row := old.Clone()
		for name, v := range set {
			if inc, ok := v.(ablage.Incr); ok {
				sum, overflow := addInt(row[name].(int64), int64(inc))
				if overflow {
					return fmt.Errorf("memstore: %s.%s overflows an int64", t.Name, name)
				}
				row[name] = sum
			} else {
				row[name] = ablage.CloneValue(v)
			}
		}
		o.put(t.Name, rows, row)
		if err := o.check(t, rows, []ablage.Row{row}); err != nil {
			return err
		}
	}
	return nil
}

func addInt(a, b int64) (int64, bool) {
	c := a + b
	return c, (b > 0 && c < a) || (b < 0 && c > a)
}

// check enforces unique indexes and references for rows just written.
func (o *op) check(t ablage.Table, rows map[string]ablage.Row, written []ablage.Row) error {
	live := func(r ablage.Row) bool { return t.SoftDelete == "" || r[t.SoftDelete] == nil }
	for _, ix := range t.AllIndexes() {
		if !ix.Unique {
			continue
		}
		for _, w := range written {
			if !live(w) || hasNull(w, ix.Fields) {
				continue
			}
			for id, r := range rows {
				if id == w["id"] || !live(r) {
					continue
				}
				if sameValues(w, r, ix.Fields) {
					return fmt.Errorf("%w: %s %v", ablage.ErrDuplicate, t.Name, ix.Fields)
				}
			}
		}
	}
	for _, f := range t.Fields {
		if f.Ref == "" {
			continue
		}
		target := o.sh.tables[f.Ref]
		for _, w := range written {
			id, ok := w[f.Name].(string)
			if !ok {
				continue
			}
			if _, exists := target[id]; !exists {
				return fmt.Errorf("%w: %s.%s points to the missing %s %q", ablage.ErrReference, t.Name, f.Name, f.Ref, id)
			}
		}
	}
	return nil
}

func hasNull(r ablage.Row, fields []string) bool {
	for _, f := range fields {
		if r[f] == nil {
			return true
		}
	}
	return false
}

func sameValues(a, b ablage.Row, fields []string) bool {
	for _, f := range fields {
		if ablage.Compare(a[f], b[f]) != 0 {
			return false
		}
	}
	return true
}

// delete removes rows and applies the OnDelete rules of the fields
// referencing them. A Restrict reference fails only if the referencing row
// still exists after the whole delete, as in SQL (NO ACTION).
func (o *op) delete(table string, ids []string) error {
	type item struct {
		table string
		id    string
	}
	queue := make([]item, len(ids))
	for i, id := range ids {
		queue[i] = item{table, id}
	}
	var deleted []item
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		rows := o.sh.tables[it.table]
		if _, ok := rows[it.id]; !ok {
			continue // already removed by a cascade
		}
		o.remove(it.table, rows, it.id)
		deleted = append(deleted, it)
		for _, ref := range o.sh.cat.Referencing(it.table) {
			refRows := o.sh.tables[ref.Table]
			for rid, r := range refRows {
				if r[ref.Field.Name] != it.id {
					continue
				}
				switch ref.Field.OnDelete {
				case ablage.Cascade:
					queue = append(queue, item{ref.Table, rid})
				case ablage.SetNull:
					nr := r.Clone()
					nr[ref.Field.Name] = nil
					o.put(ref.Table, refRows, nr)
				}
			}
		}
	}
	for _, it := range deleted {
		for _, ref := range o.sh.cat.Referencing(it.table) {
			if ref.Field.OnDelete != ablage.Restrict {
				continue
			}
			for _, r := range o.sh.tables[ref.Table] {
				if r[ref.Field.Name] == it.id {
					return fmt.Errorf("%w: %s %q is still referenced by %s.%s", ablage.ErrReference, it.table, it.id, ref.Table, ref.Field.Name)
				}
			}
		}
	}
	return nil
}
