package ablage

import (
	"context"
	"fmt"
	"slices"
)

// UniqueUpserter is a Store that upserts on a unique index instead of the
// id. Every store of this module is one; use UpsertOn or
// Repository.UpsertOn, which work on any Store and fail with
// ErrUnsupported for those that are not.
type UniqueUpserter interface {
	// UpsertOn is Store.Upsert with the conflict on the unique index over
	// the fields on (see Table.CheckConflict) instead of the id: it inserts
	// row, or if a row with the same values in those fields exists, applies
	// update to that row (keeping its id) if it matches where, and reports
	// false if it does not. With soft delete only live rows conflict, as the
	// index covers only those; a row with NULL in one of the fields never
	// conflicts. Other unique values, the id included, fail with
	// ErrDuplicate as in Insert.
	UpsertOn(ctx context.Context, table string, on []string, row, update Row, where Cond) (bool, error)
}

// UpsertOn upserts on the unique index over the fields on, if the store is
// a UniqueUpserter; see UniqueUpserter.UpsertOn.
func UpsertOn(ctx context.Context, s Store, table string, on []string, row, update Row, where Cond) (bool, error) {
	u, ok := s.(UniqueUpserter)
	if !ok {
		return false, fmt.Errorf("%w: upsert on a unique index (%T)", ErrUnsupported, s)
	}
	return u.UpsertOn(ctx, table, on, row, update, where)
}

// CheckConflict checks that the fields name a unique index of the table,
// in any order, or are just "id", and returns them in the index's order.
func (t Table) CheckConflict(on []string) ([]string, error) {
	if len(on) == 1 && on[0] == "id" {
		return []string{"id"}, nil
	}
	want := slices.Sorted(slices.Values(on))
	for _, ix := range t.AllIndexes() {
		if ix.Unique && slices.Equal(slices.Sorted(slices.Values(ix.Fields)), want) {
			return ix.Fields, nil
		}
	}
	return nil, fmt.Errorf("ablage: %s has no unique index on %v", t.Name, on)
}

// Conflicts reports whether a checked row (see Table.CheckInsert) conflicts
// with the stored row have on the unique index over the fields on (as
// returned by CheckConflict): same values, none NULL, and with soft delete
// both live. It defines the conflict for UniqueUpserter.UpsertOn.
func (t Table) Conflicts(on []string, row, have Row) bool {
	if t.SoftDelete != "" && !(len(on) == 1 && on[0] == "id") && (row[t.SoftDelete] != nil || have[t.SoftDelete] != nil) {
		return false
	}
	for _, f := range on {
		if row[f] == nil || Compare(row[f], have[f]) != 0 {
			return false
		}
	}
	return true
}
