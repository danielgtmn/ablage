# Stores

All stores pass the same conformance suite (`storetest`), so tests on memory hold on SQLite and Postgres.

## SQLite

```go
db, _ := sql.Open("sqlite", file+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_txlock=immediate")
store, err := sqlstore.New(db, sqlstore.SQLite)
```

With `modernc.org/sqlite` (no cgo) or `github.com/mattn/go-sqlite3` (`?_foreign_keys=1`). Foreign keys must be on: `New` checks it. `_txlock=immediate` avoids lock upgrades failing under concurrent transactions. With `db.SetMaxOpenConns(1)` (a common choice for SQLite), never use the outer store inside `Tx`.

Storage: Int, Bool (0/1) and Time (microseconds since 1970, UTC) as INTEGER, Float as REAL, String and JSON as TEXT, Bytes as BLOB.

## Postgres

```go
db, _ := sql.Open("pgx", "postgres://user@host/db")
store, err := sqlstore.New(db, sqlstore.Postgres)
```

Tables go into the `search_path` schema. Storage: TEXT with collation "C" (byte order, as in the other stores), BIGINT, DOUBLE PRECISION, BOOLEAN, TIMESTAMPTZ, BYTEA, JSONB.

## Memory

`memstore.New()`: for tests. Every operation and transaction holds one lock (serializable); data is gone when the process ends.

## Errors

Constraint violations are mapped to `ablage.ErrDuplicate` and `ablage.ErrReference`, wrapping the driver error. `Delete` returns the number of rows the condition matched, not counting rows of other tables removed by a cascade.
