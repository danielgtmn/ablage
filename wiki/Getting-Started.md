# Getting started

```sh
go get github.com/danielgtmn/ablage
go get modernc.org/sqlite      # SQLite without cgo; or github.com/jackc/pgx/v5 for Postgres
```

## 1. Open a store

ablage imports no driver. Open the database yourself and hand it over:

```go
import (
	"database/sql"

	_ "modernc.org/sqlite"

	"github.com/danielgtmn/ablage"
	"github.com/danielgtmn/ablage/sqlstore"
)

db, err := sql.Open("sqlite", "app.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_txlock=immediate")
store, err := sqlstore.New(db, sqlstore.SQLite)
```

For Postgres: `sql.Open("pgx", dsn)` with `_ "github.com/jackc/pgx/v5/stdlib"` and `sqlstore.Postgres`. In tests: `store := memstore.New()`. See [Stores](Stores).

## 2. Describe an entity

```go
type Project struct {
	ID        string                 // the primary key, filled in on create
	Version   int64      `db:",version"`
	CreatedAt time.Time  `db:",created"`
	UpdatedAt time.Time  `db:",updated"`
	DeletedAt *time.Time `db:",deleted"` // soft delete
	TenantID  string     `db:",index"`
	Name      string
	Labels    []string // JSON
}
```

## 3. Create repositories, then migrate once

```go
projects, err := ablage.NewRepository[Project](store, "projects",
	ablage.WithUniqueIndex("tenant_id", "name"),
	ablage.WithScope(tenantScope),
)
// ... more repositories on the same store
err = store.Migrate(ctx)
```

`NewRepository` only declares the table; `Migrate` creates what is missing. Run it on every start.

## 4. Use it

```go
p := Project{TenantID: "acme", Name: "Website"}
err = projects.Create(ctx, &p)
p, err = projects.Get(ctx, p.ID)
list, err := projects.Find(ctx, ablage.Query{Where: ablage.Prefix("name", "Web")})
p, err = projects.Update(ctx, p.ID, func(p *Project) error { p.Name = "Site"; return nil })
err = projects.Delete(ctx, p.ID)
```

Next: [Repositories](Repositories).
