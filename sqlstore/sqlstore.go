// Package sqlstore keeps tables in a SQL database through database/sql,
// with the dialects SQLite and Postgres. It imports no driver: the app opens
// the *sql.DB with the driver of its choice.
package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/danielgtmn/ablage"
)

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Store is an ablage.Store on a SQL database.
type Store struct {
	db     *sql.DB
	q      querier
	d      dialect
	prefix string
	cat    *catalog
	inTx   bool
}

type catalog struct {
	mu sync.RWMutex
	ablage.Catalog
}

// Option configures a store.
type Option func(*Store)

// WithPrefix puts a prefix before every table name, e.g. "app_", so several
// libraries can share a database without name clashes.
func WithPrefix(p string) Option { return func(s *Store) { s.prefix = p } }

var _ ablage.Store = (*Store)(nil)

// New returns a store on db. For SQLite it checks that foreign keys are on.
// With SQLite and db.SetMaxOpenConns(1), never use the outer store inside
// Tx: it waits for the one connection forever.
func New(db *sql.DB, d Dialect, opts ...Option) (*Store, error) {
	if d.d == nil {
		return nil, errors.New("sqlstore: no dialect (use sqlstore.SQLite or sqlstore.Postgres)")
	}
	s := &Store{db: db, q: db, d: d.d, cat: &catalog{}}
	for _, o := range opts {
		o(s)
	}
	if s.prefix != "" && !validPrefix(s.prefix) {
		return nil, fmt.Errorf("sqlstore: invalid prefix %q (lower case letters, digits and _)", s.prefix)
	}
	if _, ok := s.d.(sqliteDialect); ok {
		var on int
		if err := db.QueryRow("PRAGMA foreign_keys").Scan(&on); err != nil {
			return nil, fmt.Errorf("sqlstore: check foreign keys: %w", err)
		}
		if on != 1 {
			return nil, errors.New("sqlstore: SQLite foreign keys are off; turn them on for every connection (modernc.org/sqlite: ?_pragma=foreign_keys(1), mattn/go-sqlite3: ?_foreign_keys=1)")
		}
	}
	return s, nil
}

func validPrefix(p string) bool {
	for _, c := range p {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return len(p) <= 15
}

func quote(name string) string { return `"` + name + `"` }

func (s *Store) tableName(name string) string { return quote(s.prefix + name) }

func (s *Store) table(name string) (ablage.Table, error) {
	s.cat.mu.RLock()
	defer s.cat.mu.RUnlock()
	return s.cat.Table(name)
}

// Declare implements ablage.Store.
func (s *Store) Declare(tables ...ablage.Table) error {
	if s.inTx {
		return errors.New("sqlstore: Declare in a transaction")
	}
	s.cat.mu.Lock()
	defer s.cat.mu.Unlock()
	return s.cat.Declare(tables...)
}

// DDL returns the statements Migrate runs on an empty database, for apps
// that keep their own migration files.
func (s *Store) DDL() ([]string, error) {
	s.cat.mu.RLock()
	tables, err := s.cat.Tables()
	s.cat.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range tables {
		out = append(out, s.createTable(t))
		out = append(out, s.createIndexes(t)...)
	}
	return out, nil
}

func (s *Store) column(f ablage.Field) string {
	var b strings.Builder
	b.WriteString(quote(f.Name) + " " + s.d.columnType(f.Type))
	if !f.Nullable {
		b.WriteString(" NOT NULL")
		if f.Ref == "" {
			b.WriteString(" DEFAULT " + s.d.zero(f.Type))
		}
	}
	if f.Ref != "" {
		b.WriteString(" REFERENCES " + s.tableName(f.Ref) + " (" + quote("id") + ")")
		switch f.OnDelete {
		case ablage.Cascade:
			b.WriteString(" ON DELETE CASCADE")
		case ablage.SetNull:
			b.WriteString(" ON DELETE SET NULL")
		}
	}
	return b.String()
}

func (s *Store) createTable(t ablage.Table) string {
	cols := []string{quote("id") + " " + s.d.columnType(ablage.String) + " PRIMARY KEY"}
	for _, f := range t.Fields {
		cols = append(cols, s.column(f))
	}
	return "CREATE TABLE " + s.tableName(t.Name) + " (\n\t" + strings.Join(cols, ",\n\t") + "\n)"
}

// index is an index as created in the database.
type index struct {
	name string
	stmt string
}

func (s *Store) indexes(t ablage.Table) []index {
	var out []index
	for _, ix := range t.AllIndexes() {
		kind, suffix, where := "INDEX", "idx", ""
		if ix.Unique {
			kind, suffix = "UNIQUE INDEX", "key"
			if t.SoftDelete != "" {
				// Only live rows: a deleted row does not block its values.
				where = " WHERE " + quote(t.SoftDelete) + " IS NULL"
			}
		}
		cols := make([]string, len(ix.Fields))
		for i, f := range ix.Fields {
			cols[i] = quote(f)
		}
		name := indexName(s.prefix+t.Name, ix.Fields, kind+where, suffix)
		out = append(out, index{name, "CREATE " + kind + " IF NOT EXISTS " + quote(name) + " ON " + s.tableName(t.Name) +
			" (" + strings.Join(cols, ", ") + ")" + where})
	}
	return out
}

func (s *Store) createIndexes(t ablage.Table) []string {
	var out []string
	for _, ix := range s.indexes(t) {
		out = append(out, ix.stmt)
	}
	return out
}

// indexName names an index readably (table and fields) and unambiguously:
// a hash of the exact definition, so two different indexes never share a
// name and a changed definition gets a new one. At most 63 bytes, the
// Postgres limit. managedIndex recognizes the names.
func indexName(table string, fields []string, def, suffix string) string {
	sum := sha256.Sum256([]byte(table + "\x00" + strings.Join(fields, "\x00") + "\x00" + def))
	h := hex.EncodeToString(sum[:4])
	base := table + "_" + strings.Join(fields, "_")
	if max := 63 - len(h) - len(suffix) - 2; len(base) > max {
		base = base[:max]
	}
	return base + "_" + h + "_" + suffix
}

var managedIndex = regexp.MustCompile(`_[0-9a-f]{8}_(key|idx)$`)

// migrateLock is the Postgres advisory lock key that serializes migrations
// of app instances starting at once ("ablage" in ASCII).
const migrateLock = 0x61626c616765

// Migrate implements ablage.Store.
func (s *Store) Migrate(ctx context.Context) error {
	if s.inTx {
		return errors.New("sqlstore: Migrate in a transaction")
	}
	s.cat.mu.RLock()
	tables, err := s.cat.Tables()
	s.cat.mu.RUnlock()
	if err != nil {
		return err
	}
	return s.Tx(ctx, func(txs ablage.Store) error {
		tx := txs.(*Store)
		if _, ok := s.d.(postgresDialect); ok {
			if _, err := tx.q.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(migrateLock)); err != nil {
				return fmt.Errorf("sqlstore: lock for migration: %w", err)
			}
		}
		for _, t := range tables {
			have, err := tx.columns(ctx, s.prefix+t.Name)
			if err != nil {
				return err
			}
			if len(have) == 0 {
				if _, err := tx.q.ExecContext(ctx, s.createTable(t)); err != nil {
					return fmt.Errorf("sqlstore: create table %s: %w", t.Name, err)
				}
			} else {
				for _, f := range t.Fields {
					if have[f.Name] {
						continue
					}
					if f.Ref != "" && !f.Nullable {
						return fmt.Errorf("sqlstore: cannot add the reference %s.%s to an existing table unless it is nullable", t.Name, f.Name)
					}
					stmt := "ALTER TABLE " + s.tableName(t.Name) + " ADD COLUMN " + s.column(f)
					if _, err := tx.q.ExecContext(ctx, stmt); err != nil {
						return fmt.Errorf("sqlstore: add field %s.%s: %w", t.Name, f.Name, err)
					}
				}
			}
			if err := tx.syncIndexes(ctx, t); err != nil {
				return err
			}
		}
		return nil
	})
}

// syncIndexes creates the declared indexes and drops those ablage created
// earlier that are no longer declared (e.g. a unique index over all rows
// after soft delete was added). Dropping an index loses no data.
func (s *Store) syncIndexes(ctx context.Context, t ablage.Table) error {
	want := s.indexes(t)
	have, err := s.list(ctx, s.d.indexesQuery(), s.prefix+t.Name)
	if err != nil {
		return fmt.Errorf("sqlstore: read indexes of %s: %w", t.Name, err)
	}
	adopt, err := s.adoptIndexes(ctx, t)
	if err != nil {
		return err
	}
	for name := range have {
		if !adopt[name] && (!managedIndex.MatchString(name) || slices.ContainsFunc(want, func(ix index) bool { return ix.name == name })) {
			continue
		}
		if _, err := s.q.ExecContext(ctx, "DROP INDEX "+quote(name)); err != nil {
			return fmt.Errorf("sqlstore: drop the old index %s: %w", name, err)
		}
	}
	for _, ix := range want {
		if _, err := s.q.ExecContext(ctx, ix.stmt); err != nil {
			return fmt.Errorf("sqlstore: index on %s: %w", t.Name, s.wrap(err))
		}
	}
	return nil
}

// plainIndex is an index over columns as they are (no expressions, no
// condition, ascending, the column's collation, the default operator
// class), as ablage creates them on tables without soft delete.
type plainIndex struct {
	name    string
	unique  bool
	columns []string
}

// adoptIndexes returns the indexes of t that ablage did not create but that
// are identical to one it is about to create: same columns in the same
// order, same uniqueness, and no condition on either. They come from
// tables created before the app used ablage (or by hand); Migrate replaces
// them by its own, so the table does not end up with every index twice.
// Nothing changes for the data: the new index enforces exactly the same.
func (s *Store) adoptIndexes(ctx context.Context, t ablage.Table) (map[string]bool, error) {
	var plain []ablage.Index
	for _, ix := range t.AllIndexes() {
		if !ix.Unique || t.SoftDelete == "" {
			plain = append(plain, ix)
		}
	}
	if len(plain) == 0 {
		return nil, nil
	}
	have, err := s.d.plainIndexes(ctx, s.q, s.prefix+t.Name)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: read indexes of %s: %w", t.Name, err)
	}
	out := map[string]bool{}
	for _, h := range have {
		if managedIndex.MatchString(h.name) {
			continue
		}
		if slices.ContainsFunc(plain, func(ix ablage.Index) bool {
			return ix.Unique == h.unique && slices.Equal(ix.Fields, h.columns)
		}) {
			out[h.name] = true
		}
	}
	return out, nil
}

func (s *Store) columns(ctx context.Context, table string) (map[string]bool, error) {
	out, err := s.list(ctx, s.d.columnsQuery(), table)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: read columns of %s: %w", table, err)
	}
	return out, nil
}

// list runs a query returning names, with the table name as argument.
func (s *Store) list(ctx context.Context, query, table string) (map[string]bool, error) {
	rows, err := s.q.QueryContext(ctx, query, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// Tx implements ablage.Store.
func (s *Store) Tx(ctx context.Context, fn func(ablage.Store) error) error {
	if s.inTx {
		return fn(s)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	child := *s
	child.q = tx
	child.inTx = true
	commit := false
	defer func() {
		if !commit { // an error or a panic
			_ = tx.Rollback()
		}
	}()
	if err := fn(&child); err != nil {
		return err
	}
	commit = true
	return tx.Commit()
}

// exec runs a write. In a Postgres transaction it runs under a savepoint, so
// a failed statement (e.g. a duplicate) leaves the transaction usable, as
// it does in SQLite and memory.
func (s *Store) exec(ctx context.Context, stmt string, vals []any) (sql.Result, error) {
	if _, pg := s.d.(postgresDialect); !pg || !s.inTx {
		res, err := s.q.ExecContext(ctx, stmt, vals...)
		return res, s.wrap(err)
	}
	if _, err := s.q.ExecContext(ctx, "SAVEPOINT ablage_write"); err != nil {
		return nil, err
	}
	res, err := s.q.ExecContext(ctx, stmt, vals...)
	if err != nil {
		if _, rbErr := s.q.ExecContext(ctx, "ROLLBACK TO SAVEPOINT ablage_write"); rbErr != nil {
			return nil, errors.Join(s.wrap(err), rbErr)
		}
		return nil, s.wrap(err)
	}
	if _, err := s.q.ExecContext(ctx, "RELEASE SAVEPOINT ablage_write"); err != nil {
		return nil, err
	}
	return res, nil
}

// wrap marks constraint violations with ablage.ErrDuplicate or
// ablage.ErrReference.
func (s *Store) wrap(err error) error {
	if err == nil {
		return nil
	}
	if kind := s.d.errKind(err); kind != nil {
		return fmt.Errorf("%w: %w", kind, err)
	}
	return err
}

// args collects query parameters and hands out their placeholders.
type args struct {
	d    dialect
	vals []any
}

func (p *args) add(t ablage.Type, v any) string {
	p.vals = append(p.vals, p.d.encode(t, v))
	return p.d.placeholder(len(p.vals))
}

// where renders a checked condition. qual qualifies column names (for the
// WHERE of an upsert), or is empty.
func (s *Store) where(t ablage.Table, c ablage.Cond, p *args, qual string) string {
	col := func(name string) string { return qual + quote(name) }
	switch c.Op {
	case ablage.OpAll:
		return "1=1"
	case ablage.OpAnd, ablage.OpOr:
		if len(c.Conds) == 0 {
			if c.Op == ablage.OpAnd {
				return "1=1"
			}
			return "1=0"
		}
		parts := make([]string, len(c.Conds))
		for i, x := range c.Conds {
			parts[i] = "(" + s.where(t, x, p, qual) + ")"
		}
		sep := " AND "
		if c.Op == ablage.OpOr {
			sep = " OR "
		}
		return strings.Join(parts, sep)
	case ablage.OpNot:
		// A comparison with NULL is NULL in SQL; treat it as false first, so
		// NOT matches those rows as the in-memory store does.
		return "NOT COALESCE((" + s.where(t, c.Conds[0], p, qual) + "), " + s.d.falseLit() + ")"
	case ablage.OpNull:
		return col(c.Field) + " IS NULL"
	}
	f, _ := t.Field(c.Field)
	switch c.Op {
	case ablage.OpEq:
		return col(c.Field) + " = " + p.add(f.Type, c.Value)
	case ablage.OpNe:
		return s.d.ne(col(c.Field), p.add(f.Type, c.Value))
	case ablage.OpLt:
		return s.d.ordered(col(c.Field), f.Type) + " < " + p.add(f.Type, c.Value)
	case ablage.OpLte:
		return s.d.ordered(col(c.Field), f.Type) + " <= " + p.add(f.Type, c.Value)
	case ablage.OpGt:
		return s.d.ordered(col(c.Field), f.Type) + " > " + p.add(f.Type, c.Value)
	case ablage.OpGte:
		return s.d.ordered(col(c.Field), f.Type) + " >= " + p.add(f.Type, c.Value)
	case ablage.OpIn:
		list := c.Value.([]any)
		if len(list) == 0 {
			return "1=0"
		}
		phs := make([]string, len(list))
		for i, v := range list {
			phs[i] = p.add(f.Type, v)
		}
		return col(c.Field) + " IN (" + strings.Join(phs, ", ") + ")"
	case ablage.OpPrefix:
		// A range instead of LIKE: case-sensitive in both dialects, no
		// escaping, and it uses an index.
		prefix := c.Value.(string)
		column := s.d.ordered(col(c.Field), f.Type)
		lower := column + " >= " + p.add(f.Type, prefix)
		if upper, ok := successor(prefix); ok {
			return lower + " AND " + column + " < " + p.add(f.Type, upper)
		}
		return lower
	}
	panic(fmt.Sprintf("sqlstore: operator %d", c.Op))
}

// successor returns the smallest string greater than every string starting
// with p, comparing bytes: p with its last character incremented.
func successor(p string) (string, bool) {
	r := []rune(p)
	for len(r) > 0 {
		last := r[len(r)-1] + 1
		if last == 0xD800 {
			last = 0xE000 // skip surrogates, which UTF-8 cannot hold
		}
		if last <= utf8.MaxRune {
			r[len(r)-1] = last
			return string(r), true
		}
		r = r[:len(r)-1]
	}
	return "", false
}

func (s *Store) selectCols(t ablage.Table) string {
	cols := []string{quote("id")}
	for _, f := range t.Fields {
		cols = append(cols, s.d.selectExpr(quote(f.Name), f.Type))
	}
	return strings.Join(cols, ", ")
}

// Insert implements ablage.Store.
func (s *Store) Insert(ctx context.Context, table string, row ablage.Row) error {
	t, err := s.table(table)
	if err != nil {
		return err
	}
	row, err = t.CheckInsert(row)
	if err != nil {
		return err
	}
	stmt, p := s.insert(t, row)
	_, err = s.exec(ctx, stmt, p.vals)
	return err
}

func (s *Store) insert(t ablage.Table, row ablage.Row) (string, *args) {
	p := &args{d: s.d}
	cols := make([]string, 0, len(row))
	phs := make([]string, 0, len(row))
	for _, name := range t.Columns() {
		f, _ := t.Field(name)
		cols = append(cols, quote(name))
		phs = append(phs, p.add(f.Type, row[name]))
	}
	return "INSERT INTO " + s.tableName(t.Name) + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(phs, ", ") + ")", p
}

func (s *Store) set(t ablage.Table, set ablage.Row, p *args, qual string) string {
	parts := make([]string, 0, len(set))
	for _, name := range t.Columns() {
		v, ok := set[name]
		if !ok {
			continue
		}
		f, _ := t.Field(name)
		if inc, ok := v.(ablage.Incr); ok {
			parts = append(parts, quote(name)+" = "+qual+quote(name)+" + "+p.add(ablage.Int, int64(inc)))
			continue
		}
		parts = append(parts, quote(name)+" = "+p.add(f.Type, v))
	}
	return strings.Join(parts, ", ")
}

// Upsert implements ablage.Store.
func (s *Store) Upsert(ctx context.Context, table string, row, update ablage.Row, where ablage.Cond) (bool, error) {
	t, err := s.table(table)
	if err != nil {
		return false, err
	}
	if row, err = t.CheckInsert(row); err != nil {
		return false, err
	}
	if update, err = t.CheckUpdate(update); err != nil {
		return false, err
	}
	if where, err = t.CheckCond(where); err != nil {
		return false, err
	}
	stmt, p := s.insert(t, row)
	qual := s.tableName(t.Name) + "."
	set := s.set(t, update, p, qual)
	if set == "" {
		set = quote("id") + " = " + qual + quote("id")
	}
	stmt += " ON CONFLICT (" + quote("id") + ") DO UPDATE SET " + set + " WHERE " + s.where(t, where, p, qual)
	res, err := s.exec(ctx, stmt, p.vals)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Find implements ablage.Store.
func (s *Store) Find(ctx context.Context, table string, q ablage.Query) ([]ablage.Row, error) {
	t, err := s.table(table)
	if err != nil {
		return nil, err
	}
	if q, err = t.CheckQuery(q); err != nil {
		return nil, err
	}
	p := &args{d: s.d}
	var b strings.Builder
	b.WriteString("SELECT " + s.selectCols(t) + " FROM " + s.tableName(t.Name) + " WHERE " + s.where(t, q.Where, p, ""))
	order := make([]string, len(q.OrderBy))
	for i, o := range q.OrderBy {
		f, _ := t.Field(o.Field)
		order[i] = s.d.ordered(quote(o.Field), f.Type) + s.d.nullsOrder(o.Desc)
	}
	b.WriteString(" ORDER BY " + strings.Join(order, ", "))
	if q.Limit > 0 {
		fmt.Fprintf(&b, " LIMIT %d", q.Limit)
	} else if q.Offset > 0 {
		b.WriteString(" LIMIT " + s.d.noLimit())
	}
	if q.Offset > 0 {
		fmt.Fprintf(&b, " OFFSET %d", q.Offset)
	}
	rows, err := s.q.QueryContext(ctx, b.String(), p.vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := t.Columns()
	var out []ablage.Row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(ablage.Row, len(cols))
		for i, name := range cols {
			f, _ := t.Field(name)
			v, err := s.d.decode(f.Type, vals[i])
			if err != nil {
				return nil, fmt.Errorf("%w (%s.%s)", err, t.Name, name)
			}
			row[name] = v
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Count implements ablage.Store.
func (s *Store) Count(ctx context.Context, table string, where ablage.Cond) (int64, error) {
	t, err := s.table(table)
	if err != nil {
		return 0, err
	}
	if where, err = t.CheckCond(where); err != nil {
		return 0, err
	}
	p := &args{d: s.d}
	stmt := "SELECT COUNT(*) FROM " + s.tableName(t.Name) + " WHERE " + s.where(t, where, p, "")
	rows, err := s.q.QueryContext(ctx, stmt, p.vals...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int64
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

// Update implements ablage.Store.
func (s *Store) Update(ctx context.Context, table string, set ablage.Row, where ablage.Cond) (int64, error) {
	t, err := s.table(table)
	if err != nil {
		return 0, err
	}
	if set, err = t.CheckUpdate(set); err != nil {
		return 0, err
	}
	if len(set) == 0 {
		return 0, fmt.Errorf("sqlstore: update of %s sets nothing", table)
	}
	if where, err = t.CheckCond(where); err != nil {
		return 0, err
	}
	p := &args{d: s.d}
	stmt := "UPDATE " + s.tableName(t.Name) + " SET " + s.set(t, set, p, "") + " WHERE " + s.where(t, where, p, "")
	res, err := s.exec(ctx, stmt, p.vals)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Delete implements ablage.Store.
func (s *Store) Delete(ctx context.Context, table string, where ablage.Cond) (int64, error) {
	t, err := s.table(table)
	if err != nil {
		return 0, err
	}
	if where, err = t.CheckCond(where); err != nil {
		return 0, err
	}
	if _, ok := s.d.(sqliteDialect); ok && selfCascade(t) && !s.inTx {
		// SQLite counts only the rows a DELETE removes itself, not those its
		// cascade removes first; count the matching rows before, in one
		// transaction, so the number is the same as in the other stores.
		var n int64
		err := s.Tx(ctx, func(tx ablage.Store) error {
			var err error
			n, err = tx.Delete(ctx, table, where)
			return err
		})
		return n, err
	}
	p := &args{d: s.d}
	cond := s.where(t, where, p, "")
	var n int64 = -1
	if _, ok := s.d.(sqliteDialect); ok && selfCascade(t) {
		rows, err := s.q.QueryContext(ctx, "SELECT COUNT(*) FROM "+s.tableName(t.Name)+" WHERE "+cond, p.vals...)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return 0, err
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
	}
	res, err := s.exec(ctx, "DELETE FROM "+s.tableName(t.Name)+" WHERE "+cond, p.vals)
	if err != nil {
		return 0, err
	}
	if n >= 0 {
		return n, nil
	}
	return res.RowsAffected()
}

// selfCascade reports whether rows of t can cascade-delete other rows of t.
func selfCascade(t ablage.Table) bool {
	for _, f := range t.Fields {
		if f.Ref == t.Name && f.OnDelete == ablage.Cascade {
			return true
		}
	}
	return false
}
