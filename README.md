# ablage

Typed repositories for Go on SQLite, Postgres and memory, with tenant
scopes, soft delete and optimistic concurrency built in.

```go
type Customer struct {
	ID        string
	Version   int64      `db:",version"`
	CreatedAt time.Time  `db:",created"`
	UpdatedAt time.Time  `db:",updated"`
	DeletedAt *time.Time `db:",deleted"`
	TenantID  string     `db:",index"`
	Email     string
	Name      string
	Tags      []string // stored as JSON
}

store, _ := sqlstore.New(db, sqlstore.Postgres) // or sqlstore.SQLite, or memstore.New()
customers, _ := ablage.NewRepository[Customer](store, "customers",
	ablage.WithUniqueIndex("tenant_id", "email"),
	ablage.WithScope(func(ctx context.Context) (ablage.Cond, error) {
		return ablage.Eq("tenant_id", tenantFrom(ctx)), nil
	}),
)
store.Migrate(ctx) // creates the table and its indexes

c := Customer{TenantID: "acme", Email: "ada@example.org", Name: "Ada"}
customers.Create(ctx, &c) // id, version, times filled in

list, _ := customers.Find(ctx, ablage.Query{
	Where:   ablage.And(ablage.Prefix("name", "A"), ablage.NotNull("email")),
	OrderBy: []ablage.Order{ablage.Desc("created_at")},
	Limit:   20,
})

customers.Update(ctx, c.ID, func(c *Customer) error { // retried if someone else wrote in between
	c.Name = "Ada Lovelace"
	return nil
})
customers.Delete(ctx, c.ID) // soft delete: the email is free again
```

## What you get

- **One API, three stores that behave the same.** SQLite and Postgres through
  `database/sql` (bring your own driver), and an in-memory store for tests.
  All pass the same conformance suite, down to NULL handling and sort order.
- **Structs in, structs out.** Fields map to columns by name (snake_case) or
  `db` tag. Pointers are nullable; slices, maps and structs are stored as
  JSON; types like `netip.Addr` as text.
- **Scopes** (e.g. tenants) apply to every read *and* write. A missing tenant
  is an error, never "all tenants".
- **Optimistic concurrency** with a version field: `Replace` fails with
  `ErrStale`, `Update` retries.
- **Soft delete** with `Restore` and `Purge`; unique indexes only count rows
  that are not deleted.
- **References** between tables with cascade, set-null or restrict, enforced
  in every store.
- **Migrations that only add** tables, columns and indexes, safe to run on
  every start, also by several instances at once.
- **Transactions** with `store.Tx` and `repo.With(tx)`.

Errors to test with `errors.Is`: `ErrNotFound`, `ErrDuplicate`,
`ErrReference`, `ErrStale`, `ErrOutOfScope`.

## Install

```sh
go get github.com/danielgtmn/ablage
go get modernc.org/sqlite           # or github.com/jackc/pgx/v5 for Postgres
```

## Documentation

See the [wiki](https://github.com/danielgtmn/ablage/wiki): getting started,
repositories, conditions and queries, schema and migrations, stores,
transactions, and how to write a store of your own. `examples/todo` is a
small app to try it.

## Status

Before 1.0: the API may still change; breaking changes are marked in the
release notes. Inspired by
[wemogy/libs-infrastructure-database](https://github.com/wemogy/libs-infrastructure-database).

## License

[Apache License 2.0](LICENSE)
