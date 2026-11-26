# Repositories

`ablage.NewRepository[T](store, table, options...)` stores structs of type `T` in one table.

## Struct mapping

Every exported field is a column. The name is the `db` tag, or the field name in snake_case (`TenantID` → `tenant_id`). `db:"-"` skips a field; embedded structs without a tag are flattened, so a shared `Base` struct works.

| Go type | Stored as |
| --- | --- |
| `string` and named string types | String |
| all integer types (`uint64` up to the int64 maximum) | Int |
| `float32`, `float64` | Float |
| `bool` | Bool |
| `time.Time` | Time (UTC, microseconds) |
| `[]byte` | Bytes |
| types with `MarshalText`/`UnmarshalText` (e.g. `netip.Addr`) | String |
| structs, maps, other slices | JSON |
| any of these as a pointer | the same, nullable (`nil` is NULL) |

Tag options after the name, e.g. `db:"mail,unique"` or `db:",version"`:

| Option | Meaning |
| --- | --- |
| `id` | the primary key (default: the field named `ID`); a string, column `id` |
| `version` | integer, incremented on every write; enables `ErrStale` |
| `created` | `time.Time`, set on create unless already set |
| `updated` | `time.Time`, set on every write |
| `deleted` | `*time.Time`; enables soft delete |
| `unique`, `index` | an index on this field |
| `ref=table` | holds the id of a row in `table` (see [Schema and migrations](Schema-and-Migrations)) |
| `cascade`, `setnull` | with `ref`: what happens when the referenced row is removed |
| `json` | store as JSON regardless of the type |

## Operations

| Method | What it does |
| --- | --- |
| `Create(ctx, &e)` | inserts; fills in an empty id (`NewID`), version 1, times. `ErrDuplicate` if the id or a unique value is taken |
| `Get(ctx, id)` | `ErrNotFound` if missing, deleted or out of scope |
| `GetMany(ctx, ids...)` | the ones that exist |
| `Find(ctx, query)`, `First(ctx, query)`, `Count(ctx, cond)`, `Exists(ctx, id)` | see [Conditions and queries](Conditions-and-Queries) |
| `Replace(ctx, &e)` | writes all fields but id, created and deleted (`e` keeps whatever it holds there); checks the version |
| `Update(ctx, id, fn)` | reads, applies `fn`, replaces; retries on `ErrStale` (5 attempts, `WithUpdateAttempts`). `fn` may run more than once: no side effects in it. Changes `fn` makes to the id, version, created or deleted field are refused or ignored |
| `Patch(ctx, id, ablage.Row{"name": "x"})` | sets single columns without reading; values as in the struct, `ablage.Incr(n)` adds `n` to an integer field in one atomic step. Not for the id, managed fields or scope fields. Increments the version, sets updated |
| `UpdateWhere(ctx, cond, ablage.Row{...})` | `Patch` for every entity matching `cond` (in scope, not soft-deleted unless `WithDeleted`); returns how many it changed |
| `Upsert(ctx, &e)` | creates, or overwrites without a version check (last write wins); also restores a soft-deleted entity |
| `Delete(ctx, id)`, `DeleteWhere(ctx, cond)` | soft delete if there is a `deleted` field, else removal |
| `Restore(ctx, id)` | undoes a soft delete |
| `Purge(ctx, id)` | removes for good, also soft-deleted entities |

`Incr` is checked against the column (an integer that is not nullable), not
against a smaller Go type: adding to an `int8` or `uint` field past its range
leaves a value the entity cannot be read with.

## Scopes

```go
ablage.WithScope(func(ctx context.Context) (ablage.Cond, error) {
	t, ok := tenant.From(ctx)
	if !ok {
		return ablage.All, errors.New("no tenant")
	}
	return ablage.Eq("tenant_id", t), nil
})
```

The condition is added to every read, count, update and delete. `Create`, `Replace` and `Upsert` fail with `ErrOutOfScope` if the entity lies outside it, so an entity can neither be created in nor moved to another tenant; `Patch` refuses scope fields. If the function returns an error, the operation fails with it.

`repo.Unscoped()` drops the scope, for jobs and administration. Anyone holding the repository can call it, so hand tenant-bound code only what it needs. `repo.WithDeleted()` also sees soft-deleted entities.

**References are not scoped.** A reference (`ref=table`) is checked for existence only, not against the scope of the referenced table, and a cascade removes referencing rows of every tenant. Before linking to a parent, read the parent through its own scoped repository:

```go
if _, err := projects.Get(ctx, task.ProjectID); err != nil {
	return err // ErrNotFound also for another tenant's project
}
return tasks.Create(ctx, &task)
```

## Versions

With a `version` field, `Replace` only succeeds if the stored version equals the entity's; otherwise `ErrStale`, and nothing is written. Read, change, `Replace` — or let `Update` do the loop. Without a version field the last write wins.

## Other options

`WithIndex(fields...)`, `WithUniqueIndex(fields...)` for indexes over several fields; `WithClock(fn)` and `WithIDs(fn)` for tests or other id formats.
