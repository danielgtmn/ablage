package ablage

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"
	"time"
)

// Errors that stores and repositories return, wrapped with details. Test for
// them with errors.Is.
var (
	// ErrNotFound: no row with this id, or not one the caller may see.
	ErrNotFound = errors.New("ablage: not found")
	// ErrDuplicate: the id or a unique field is taken.
	ErrDuplicate = errors.New("ablage: duplicate")
	// ErrReference: a reference points to a row that does not exist, or a
	// delete is refused because other rows reference the row.
	ErrReference = errors.New("ablage: reference")
	// ErrStale: the row was changed since it was read (its version differs).
	ErrStale = errors.New("ablage: stale")
	// ErrOutOfScope: the row is outside the repository's scope, e.g. it
	// belongs to another tenant.
	ErrOutOfScope = errors.New("ablage: out of scope")
	// ErrUnsupported: the store lacks an optional interface the operation
	// needs, e.g. Aggregator.
	ErrUnsupported = errors.New("ablage: not supported by the store")
)

// Store keeps rows in tables. Implementations: sqlstore (SQLite, Postgres)
// and memstore (in memory, for tests). All implementations pass the suite in
// storetest, so code tested against one behaves the same on another.
//
// Values in rows and conditions are converted to the canonical type of
// their field (see Type and Field.Value). Conditions are evaluated as
// Cond.Match does; an In list may hold any number of values (tens of
// thousands), in every operation.
//
// Optional interfaces add operations: Aggregator. Every store of this
// module implements them; storetest checks them where present.
type Store interface {
	// Declare adds tables to the store; see Catalog.Declare. Usually called
	// by NewRepository. Declare all tables, then Migrate.
	Declare(tables ...Table) error
	// Migrate creates missing tables, fields and indexes. It only adds: it
	// never drops or changes a column, so data is never lost. A field that
	// is not nullable is added with its zero value as default; one that
	// references another table cannot be added to an existing table (make it
	// nullable).
	Migrate(ctx context.Context) error

	// Insert adds a row. Missing fields are NULL, or their zero value if
	// not nullable. ErrDuplicate if the id or a unique value is taken.
	Insert(ctx context.Context, table string, row Row) error
	// Upsert inserts row, or if a row with its id exists and matches where,
	// applies update to it. It reports false if the existing row does not
	// match where; nothing is changed then. An empty update changes nothing
	// but still reports whether the existing row matches.
	Upsert(ctx context.Context, table string, row, update Row, where Cond) (bool, error)
	// Find returns the rows matching the query.
	Find(ctx context.Context, table string, q Query) ([]Row, error)
	// Count returns the number of rows matching where.
	Count(ctx context.Context, table string, where Cond) (int64, error)
	// Update sets fields on every row matching where and returns how many
	// rows matched.
	Update(ctx context.Context, table string, set Row, where Cond) (int64, error)
	// Delete removes every row matching where and returns how many.
	Delete(ctx context.Context, table string, where Cond) (int64, error)

	// Tx runs fn in a transaction: committed if fn returns nil, else rolled
	// back. Inside fn, use only tx (and repositories bound with With(tx));
	// using the outer store there can deadlock. A Tx inside a Tx joins the
	// outer transaction.
	Tx(ctx context.Context, fn func(tx Store) error) error
}

// NewID returns a new random id: 26 characters, lower case letters and
// digits, sorting by creation time (48 bits of milliseconds, 80 random
// bits). Ids made by one process are strictly ascending, also within the
// same millisecond.
func NewID() string {
	b := nextID()
	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	var out [26]byte
	// 128 bits, most significant first, 5 bits per character; the last
	// character carries the remaining 3 bits.
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	for i := range 26 {
		shift := 123 - 5*i // bit position of this character's lowest bit
		var v uint64
		switch {
		case shift >= 64:
			v = hi >> (shift - 64)
		case shift > 0:
			v = hi<<(64-shift) | lo>>shift
		default:
			v = lo << -shift
		}
		out[i] = alphabet[v&31]
	}
	return string(out[:])
}

var idState struct {
	sync.Mutex
	last [16]byte
}

func nextID() [16]byte {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli())<<16)
	rand.Read(b[6:])
	idState.Lock()
	defer idState.Unlock()
	// Not after the last id (same millisecond, or the clock went back):
	// continue from the last one instead.
	if string(b[:6]) <= string(idState.last[:6]) {
		b = idState.last
		for i := 15; i >= 0; i-- {
			b[i]++
			if b[i] != 0 {
				break
			}
		}
	}
	idState.last = b
	return b
}
