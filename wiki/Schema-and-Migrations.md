# Schema and migrations

A repository derives its table from the struct. Without repositories, describe tables directly and use the `Store` methods on rows:

```go
store.Declare(ablage.Table{
	Name: "tasks",
	Fields: []ablage.Field{
		{Name: "project_id", Type: ablage.String, Ref: "projects", OnDelete: ablage.Cascade},
		{Name: "title", Type: ablage.String},
		{Name: "due", Type: ablage.Time, Nullable: true, Index: true},
	},
	Indexes: []ablage.Index{{Fields: []string{"project_id", "title"}, Unique: true}},
})
```

Every table has the string primary key `id`. Names: lower case letters, digits and `_`.

## Types

`String`, `Int` (int64), `Float` (float64), `Bool`, `Time` (UTC, microseconds), `Bytes`, `JSON`. A field that is not nullable holds its zero value when not given (`""`, `0`, `false`, the zero time, empty bytes, JSON `null`).

Values every store keeps the same way, and nothing else (the others are refused with an error):

- strings: valid UTF-8 without NUL characters (use `Bytes` for binary data)
- floats: anything but NaN (infinity is fine)
- times: years 1 to 9999
- JSON: valid, without NUL characters in strings. Postgres stores it as JSONB, which normalizes it: key order, whitespace, number format, and of duplicate keys only the last is kept. Read JSON as data, not as exact bytes.
- `Incr`: keep the result within int64; memory and Postgres refuse an overflow, SQLite turns the column into a float that can no longer be read.

## Indexes and uniqueness

`Unique` and `Index` on a field, or `Indexes` over several. Unique means unique among rows where none of the fields is NULL. On a table with soft delete, only rows that are not deleted count, so a deleted entity does not block its name.

A unique index can serve as the key of an upsert: `Repository.UpsertOn(ctx, &e, "project_id", "title")` (or `ablage.UpsertOn` on a store) updates the live row with these values instead of failing with `ErrDuplicate`.

Uniqueness is checked row by row, as SQL databases do: an update that shifts unique values of several rows (`pos = pos + 1`) can fail on the way even if the end result would be unique. Do such shifts in two steps.

## References

`Ref: "projects"` makes a field hold the id of a row there. Writing an id that does not exist fails with `ErrReference`. When the referenced row is removed (not soft-deleted):

- `Restrict` (default): the removal fails with `ErrReference`
- `Cascade`: referencing rows are removed too
- `SetNull`: the field becomes NULL (must be nullable)

Tables may be declared in any order; Migrate creates referenced tables first. A table may reference itself; other cycles are refused.

## Migrate

`store.Migrate(ctx)` creates missing tables, columns and indexes in one transaction, holding a lock on Postgres so several instances can start at once. For data it only adds:

- New fields are added to existing tables; fields that are not nullable get their zero value as default, so old rows stay valid.
- A new reference that is not nullable cannot be added to an existing table (make it nullable).
- No table or column is dropped, renamed or changed. Renaming a struct field or tag creates a new, empty column; keep the old name with a `db` tag instead.
- Indexes follow the declaration: an index ablage created earlier that is no longer declared is dropped (indexes hold no data). This is how a unique index becomes one over live rows when soft delete is added. Indexes you create yourself are left alone.

### Tables that existed before ablage

Migrate also takes over tables an app created before it used ablage (with its own SQL or an older data layer), as long as their columns have the types ablage uses (see [Stores](Stores)): declare the table with the same field names and it adds only what is missing. An existing index that is exactly the one a declaration asks for (same columns in the same order, same uniqueness, ascending, no condition, no expression) is replaced by ablage's own under ablage's name, so the table does not end up with every index twice. Any other index of yours stays.

Declaring the same table twice with different definitions in one process is an error.

`sqlstore.Store.DDL()` returns the statements for an empty database, for apps that keep their own migration files. `sqlstore.WithPrefix("app_")` puts a prefix before every table name, so several libraries can share a database.
