package ablage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

// Repository stores entities of type T, a struct, in one table. See
// newMapping in mapping.go for how struct fields become columns.
//
// A Repository is safe for concurrent use. Its methods return copies of
// itself (With, Unscoped, WithDeleted); they share the configuration.
type Repository[T any] struct {
	store       Store
	table       Table
	m           *mapping
	cfg         *config
	unscoped    bool
	withDeleted bool
}

type config struct {
	scope    func(context.Context) (Cond, error)
	indexes  []Index
	now      func() time.Time
	newID    func() string
	attempts int
}

// Option configures a repository.
type Option func(*config)

// WithScope restricts the repository to rows matching the condition fn
// returns, typically the current tenant taken from the context. It applies
// to every operation: reads and counts only see matching rows, updates and
// deletes only touch them, and Create, Replace and Upsert fail with
// ErrOutOfScope for an entity outside it. If fn returns an error, the
// operation fails with it, so a missing tenant never means "all tenants".
// Use Unscoped for jobs that must see everything.
func WithScope(fn func(ctx context.Context) (Cond, error)) Option {
	return func(c *config) { c.scope = fn }
}

// WithIndex adds an index over several fields, e.g. ("tenant_id", "name").
func WithIndex(fields ...string) Option {
	return func(c *config) { c.indexes = append(c.indexes, Index{Fields: fields}) }
}

// WithUniqueIndex adds a unique index over several fields, e.g.
// ("tenant_id", "email") for addresses unique per tenant.
func WithUniqueIndex(fields ...string) Option {
	return func(c *config) { c.indexes = append(c.indexes, Index{Fields: fields, Unique: true}) }
}

// WithClock sets the clock for created, updated and deleted times.
func WithClock(now func() time.Time) Option { return func(c *config) { c.now = now } }

// WithIDs sets how ids are made for entities created without one; the
// default is NewID.
func WithIDs(newID func() string) Option { return func(c *config) { c.newID = newID } }

// WithUpdateAttempts sets how often Update tries again after another write
// got in between (default 5).
func WithUpdateAttempts(n int) Option { return func(c *config) { c.attempts = max(n, 1) } }

// NewRepository declares the table for T on the store. Call Migrate on the
// store after creating all repositories.
func NewRepository[T any](store Store, table string, opts ...Option) (*Repository[T], error) {
	m, err := newMapping(reflect.TypeFor[T](), table)
	if err != nil {
		return nil, err
	}
	cfg := &config{now: time.Now, newID: NewID, attempts: 5}
	for _, o := range opts {
		o(cfg)
	}
	t := m.table(table, cfg.indexes)
	if err := store.Declare(t); err != nil {
		return nil, err
	}
	return &Repository[T]{store: store, table: t, m: m, cfg: cfg}, nil
}

// Table returns the table definition derived from T.
func (r *Repository[T]) Table() Table { return r.table }

// Store returns the store the repository uses.
func (r *Repository[T]) Store() Store { return r.store }

// With returns the repository bound to another store, usually the tx of a
// transaction:
//
//	store.Tx(ctx, func(tx ablage.Store) error {
//		p, err := projects.With(tx).Get(ctx, id)
//		...
//	})
func (r *Repository[T]) With(store Store) *Repository[T] {
	c := *r
	c.store = store
	return &c
}

// Unscoped returns the repository without its scope (see WithScope), for
// jobs and administration that must see every tenant.
func (r *Repository[T]) Unscoped() *Repository[T] {
	c := *r
	c.unscoped = true
	return &c
}

// WithDeleted returns the repository seeing soft-deleted entities as well.
func (r *Repository[T]) WithDeleted() *Repository[T] {
	c := *r
	c.withDeleted = true
	return &c
}

func (r *Repository[T]) scope(ctx context.Context) (Cond, error) {
	if r.cfg.scope == nil || r.unscoped {
		return All, nil
	}
	c, err := r.cfg.scope(ctx)
	if err != nil {
		return All, fmt.Errorf("ablage: scope of %s: %w", r.table.Name, err)
	}
	if _, err := r.table.CheckCond(c); err != nil {
		return All, fmt.Errorf("ablage: scope of %s: %w", r.table.Name, err)
	}
	return c, nil
}

// where combines c with the scope and, unless WithDeleted, the soft delete
// filter.
func (r *Repository[T]) where(ctx context.Context, c Cond) (Cond, error) {
	s, err := r.scope(ctx)
	if err != nil {
		return All, err
	}
	conds := []Cond{c, s}
	if r.m.deleted != nil && !r.withDeleted {
		conds = append(conds, Null(r.m.deleted.field.Name))
	}
	return And(conds...), nil
}

// inScope checks that a row about to be written lies inside the scope.
func (r *Repository[T]) inScope(ctx context.Context, row Row) error {
	s, err := r.scope(ctx)
	if err != nil {
		return err
	}
	checked, err := r.table.CheckCond(s)
	if err != nil {
		return err
	}
	norm, err := r.table.CheckInsert(row)
	if err != nil {
		return err
	}
	if !checked.Match(norm) {
		return fmt.Errorf("%w: %s %q", ErrOutOfScope, r.table.Name, row["id"])
	}
	return nil
}

func (r *Repository[T]) now() time.Time { return NormalizeTime(r.cfg.now()) }

func (r *Repository[T]) entity(row Row) (T, error) {
	var e T
	err := r.m.fill(reflect.ValueOf(&e).Elem(), row)
	return e, err
}

func (r *Repository[T]) notFound(id string) error {
	return fmt.Errorf("%w: %s %q", ErrNotFound, r.table.Name, id)
}

// Get returns the entity with the id; ErrNotFound if there is none in scope.
func (r *Repository[T]) Get(ctx context.Context, id string) (T, error) {
	e, err := r.First(ctx, Query{Where: Eq("id", id)})
	if errors.Is(err, ErrNotFound) {
		var zero T
		return zero, r.notFound(id)
	}
	return e, err
}

// GetMany returns the entities with the given ids that exist, in no
// particular order. Any number of ids works, so related entities of a whole
// page can be loaded in one call.
func (r *Repository[T]) GetMany(ctx context.Context, ids ...string) ([]T, error) {
	return r.Find(ctx, Query{Where: In("id", ids...)})
}

// Find returns the entities matching the query.
func (r *Repository[T]) Find(ctx context.Context, q Query) ([]T, error) {
	w, err := r.where(ctx, q.Where)
	if err != nil {
		return nil, err
	}
	q.Where = w
	rows, err := r.store.Find(ctx, r.table.Name, q)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		e, err := r.entity(row)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// First returns the first entity matching the query; ErrNotFound if none.
func (r *Repository[T]) First(ctx context.Context, q Query) (T, error) {
	q.Limit = 1
	list, err := r.Find(ctx, q)
	if err != nil {
		var zero T
		return zero, err
	}
	if len(list) == 0 {
		var zero T
		return zero, fmt.Errorf("%w: %s", ErrNotFound, r.table.Name)
	}
	return list[0], nil
}

// Count returns the number of entities matching where.
func (r *Repository[T]) Count(ctx context.Context, where Cond) (int64, error) {
	w, err := r.where(ctx, where)
	if err != nil {
		return 0, err
	}
	return r.store.Count(ctx, r.table.Name, w)
}

// Exists reports whether an entity with the id exists in scope.
func (r *Repository[T]) Exists(ctx context.Context, id string) (bool, error) {
	n, err := r.Count(ctx, Eq("id", id))
	return n > 0, err
}

// Create inserts the entity. An empty id is filled in; version is set to 1,
// created (unless already set) and updated to now. ErrDuplicate if the id or
// a unique value is taken.
func (r *Repository[T]) Create(ctx context.Context, e *T) error {
	v := reflect.ValueOf(e).Elem()
	row, err := r.m.row(v)
	if err != nil {
		return err
	}
	if row["id"] == "" {
		row["id"] = r.cfg.newID()
	}
	now := r.now()
	if r.m.version != nil {
		row[r.m.version.field.Name] = int64(1)
	}
	if c := r.m.created; c != nil && (row[c.field.Name] == nil || row[c.field.Name].(time.Time).IsZero()) {
		row[c.field.Name] = now
	}
	if r.m.updated != nil {
		row[r.m.updated.field.Name] = now
	}
	if r.m.deleted != nil {
		row[r.m.deleted.field.Name] = nil
	}
	if err := r.inScope(ctx, row); err != nil {
		return err
	}
	if err := r.store.Insert(ctx, r.table.Name, row); err != nil {
		return err
	}
	return r.m.fill(v, row)
}

// managed lists the columns the repository sets itself.
func (r *Repository[T]) managed() []*column {
	return []*column{r.m.id, r.m.version, r.m.created, r.m.updated, r.m.deleted}
}

// Replace writes all fields of the entity, except its id, created time and
// deleted mark. With a version field it succeeds only if the stored version
// equals e's (else ErrStale) and increments it; without one, the last write
// wins. ErrNotFound if the entity does not exist in scope.
func (r *Repository[T]) Replace(ctx context.Context, e *T) error {
	v := reflect.ValueOf(e).Elem()
	row, err := r.m.row(v)
	if err != nil {
		return err
	}
	id := row["id"].(string)
	if err := r.inScope(ctx, row); err != nil {
		return err
	}
	set := Row{}
	for name, val := range row {
		set[name] = val
	}
	for _, c := range r.managed() {
		if c != nil {
			delete(set, c.field.Name)
		}
	}
	cond := Eq("id", id)
	if r.m.version != nil {
		cond = And(cond, Eq(r.m.version.field.Name, row[r.m.version.field.Name]))
		set[r.m.version.field.Name] = Incr(1)
	}
	now := r.now()
	if r.m.updated != nil {
		set[r.m.updated.field.Name] = now
	}
	if len(set) == 0 {
		// Nothing to write; still report a missing entity.
		ok, err := r.Exists(ctx, id)
		if err == nil && !ok {
			err = r.notFound(id)
		}
		return err
	}
	w, err := r.where(ctx, cond)
	if err != nil {
		return err
	}
	n, err := r.store.Update(ctx, r.table.Name, set, w)
	if err != nil {
		return err
	}
	if n == 0 {
		return r.missing(ctx, id)
	}
	if r.m.version != nil {
		row[r.m.version.field.Name] = row[r.m.version.field.Name].(int64) + 1
	}
	if r.m.updated != nil {
		row[r.m.updated.field.Name] = now
	}
	return r.m.fill(v, row)
}

// missing tells why a write by id touched no row: stale or not found.
func (r *Repository[T]) missing(ctx context.Context, id string) error {
	ok, err := r.Exists(ctx, id)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("%w: %s %q was changed in the meantime", ErrStale, r.table.Name, id)
	}
	return r.notFound(id)
}

// Update reads the entity, applies fn and writes it back with Replace. If
// another write got in between (ErrStale), it starts over, up to the
// configured number of attempts, so fn may run more than once and must not
// have side effects. Without a version field there is no such check.
func (r *Repository[T]) Update(ctx context.Context, id string, fn func(*T) error) (T, error) {
	var zero T
	for attempt := 1; ; attempt++ {
		e, err := r.Get(ctx, id)
		if err != nil {
			return zero, err
		}
		read := reflect.ValueOf(e)
		if err := fn(&e); err != nil {
			return zero, err
		}
		v := reflect.ValueOf(&e).Elem()
		if got := v.FieldByIndex(r.m.id.index).String(); got != id {
			return zero, fmt.Errorf("ablage: Update of %s %q changed the id", r.table.Name, id)
		}
		// The repository manages these; fn cannot change them.
		for _, c := range []*column{r.m.version, r.m.created, r.m.deleted} {
			if c != nil {
				v.FieldByIndex(c.index).Set(read.FieldByIndex(c.index))
			}
		}
		err = r.Replace(ctx, &e)
		if errors.Is(err, ErrStale) && attempt < r.cfg.attempts {
			continue
		}
		if err != nil {
			return zero, err
		}
		return e, nil
	}
}

// Patch sets single fields by column name, without reading the entity
// first. Values are given as in the struct (e.g. a slice for a JSON field);
// an Incr adds to an integer field in one atomic step instead. The id,
// managed fields and fields of the scope cannot be patched. It increments
// the version and sets updated. ErrNotFound if the entity does not exist in
// scope (or is soft-deleted, unless WithDeleted).
func (r *Repository[T]) Patch(ctx context.Context, id string, set Row) error {
	if len(set) == 0 {
		return fmt.Errorf("ablage: Patch of %s %q sets nothing", r.table.Name, id)
	}
	n, err := r.UpdateWhere(ctx, Eq("id", id), set)
	if err != nil {
		return err
	}
	if n == 0 {
		return r.notFound(id)
	}
	return nil
}

// UpdateWhere sets fields on every entity matching where, as Patch does for
// one: values as in the struct, Incr to add, not the id, managed fields or
// fields of the scope; the version is incremented and updated set on each.
// Only entities in scope are changed, and soft-deleted ones only WithDeleted.
// It returns how many entities it changed.
func (r *Repository[T]) UpdateWhere(ctx context.Context, where Cond, set Row) (int64, error) {
	if len(set) == 0 {
		return 0, fmt.Errorf("ablage: update of %s sets nothing", r.table.Name)
	}
	var scopeFields []string
	if r.cfg.scope != nil && !r.unscoped {
		s, err := r.cfg.scope(ctx)
		if err != nil {
			return 0, fmt.Errorf("ablage: scope of %s: %w", r.table.Name, err)
		}
		scopeFields = s.Fields()
	}
	out := Row{}
	for name, val := range set {
		c, ok := r.m.byName[name]
		if !ok {
			return 0, fmt.Errorf("ablage: unknown field %s.%s", r.table.Name, name)
		}
		if c.role != roleNone {
			return 0, fmt.Errorf("ablage: %s.%s is managed by the repository and cannot be patched", r.table.Name, name)
		}
		if slices.Contains(scopeFields, name) {
			return 0, fmt.Errorf("ablage: %s.%s is part of the scope and cannot be patched", r.table.Name, name)
		}
		if inc, ok := val.(Incr); ok {
			// The store adds it; it checks the field is an integer that is
			// not nullable.
			out[name] = inc
			continue
		}
		cv, err := c.value(val)
		if err == nil {
			// Writing it into the struct field must work too, else the
			// entity could no longer be read (e.g. 1000 for an int8).
			err = c.set(reflect.New(r.m.typ).Elem().FieldByIndex(c.index), cv)
		}
		if err != nil {
			return 0, fmt.Errorf("ablage: %s.%s: %w", r.table.Name, name, err)
		}
		out[name] = cv
	}
	r.stamp(out)
	w, err := r.where(ctx, where)
	if err != nil {
		return 0, err
	}
	return r.store.Update(ctx, r.table.Name, out, w)
}

// stamp adds the version increment and the updated time to an update.
func (r *Repository[T]) stamp(set Row) {
	if r.m.version != nil {
		set[r.m.version.field.Name] = Incr(1)
	}
	if r.m.updated != nil {
		set[r.m.updated.field.Name] = r.now()
	}
}

// Delete deletes the entity: with a deleted field it is only marked (soft
// delete, see Restore and Purge), else removed. ErrNotFound if it does not
// exist in scope. ErrReference if other rows reference it (removal only).
func (r *Repository[T]) Delete(ctx context.Context, id string) error {
	n, err := r.DeleteWhere(ctx, Eq("id", id))
	if err == nil && n == 0 {
		err = r.notFound(id)
	}
	return err
}

// DeleteWhere deletes every entity matching where, as Delete does, and
// returns how many.
func (r *Repository[T]) DeleteWhere(ctx context.Context, where Cond) (int64, error) {
	if r.m.deleted == nil {
		return r.purge(ctx, where)
	}
	w, err := r.where(ctx, where)
	if err != nil {
		return 0, err
	}
	set := Row{r.m.deleted.field.Name: r.now()}
	r.stamp(set)
	return r.store.Update(ctx, r.table.Name, set, And(w, Null(r.m.deleted.field.Name)))
}

// Restore undoes a soft delete. ErrNotFound if there is no deleted entity
// with the id in scope.
func (r *Repository[T]) Restore(ctx context.Context, id string) error {
	if r.m.deleted == nil {
		return fmt.Errorf("ablage: %s has no soft delete", r.table.Name)
	}
	s, err := r.scope(ctx)
	if err != nil {
		return err
	}
	set := Row{r.m.deleted.field.Name: nil}
	r.stamp(set)
	n, err := r.store.Update(ctx, r.table.Name, set, And(Eq("id", id), s, NotNull(r.m.deleted.field.Name)))
	if err != nil {
		return err
	}
	if n == 0 {
		return r.notFound(id)
	}
	return nil
}

// Purge removes the entity for good, also if soft-deleted. ErrNotFound if
// it does not exist in scope.
func (r *Repository[T]) Purge(ctx context.Context, id string) error {
	n, err := r.purge(ctx, Eq("id", id))
	if err == nil && n == 0 {
		err = r.notFound(id)
	}
	return err
}

func (r *Repository[T]) purge(ctx context.Context, where Cond) (int64, error) {
	s, err := r.scope(ctx)
	if err != nil {
		return 0, err
	}
	return r.store.Delete(ctx, r.table.Name, And(where, s))
}

// Upsert creates the entity or, if one with its id exists, overwrites it
// like Replace but without the version check (the last write wins), also
// restoring it if soft-deleted. ErrOutOfScope if the existing entity lies
// outside the scope. e is updated with the stored version and times.
func (r *Repository[T]) Upsert(ctx context.Context, e *T) error {
	v := reflect.ValueOf(e).Elem()
	row, err := r.m.row(v)
	if err != nil {
		return err
	}
	if row["id"] == "" {
		row["id"] = r.cfg.newID()
	}
	id := row["id"].(string)
	if err := r.inScope(ctx, row); err != nil {
		return err
	}
	now := r.now()
	update := row.Clone()
	delete(update, "id")
	if r.m.created != nil {
		delete(update, r.m.created.field.Name)
		if t, _ := row[r.m.created.field.Name].(time.Time); t.IsZero() {
			row[r.m.created.field.Name] = now
		}
	}
	if r.m.version != nil {
		row[r.m.version.field.Name] = int64(1)
		update[r.m.version.field.Name] = Incr(1)
	}
	if r.m.updated != nil {
		row[r.m.updated.field.Name] = now
		update[r.m.updated.field.Name] = now
	}
	if r.m.deleted != nil {
		row[r.m.deleted.field.Name] = nil
		update[r.m.deleted.field.Name] = nil
	}
	s, err := r.scope(ctx)
	if err != nil {
		return err
	}
	ok, err := r.store.Upsert(ctx, r.table.Name, row, update, s)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s %q", ErrOutOfScope, r.table.Name, id)
	}
	stored, err := r.Unscoped().WithDeleted().Get(ctx, id)
	if err != nil {
		return err
	}
	*e = stored
	return nil
}
