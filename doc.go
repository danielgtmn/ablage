// Package ablage stores Go structs in SQLite, Postgres or memory through
// typed repositories, with tenant scopes, soft delete and optimistic
// concurrency built in.
//
// A Repository[T] maps a struct to a table (see NewRepository and the
// struct tags described there) on a Store. Stores: sqlstore (SQLite and
// Postgres through database/sql) and memstore (in memory, for tests). Every
// store passes the conformance suite in storetest, so all behave the same.
//
//	store, _ := sqlstore.New(db, sqlstore.SQLite)
//	projects, _ := ablage.NewRepository[Project](store, "projects")
//	_ = store.Migrate(ctx)
//	_ = projects.Create(ctx, &Project{Name: "Website"})
//
// Struct mapping: every exported field is a column, named by its `db` tag
// or in snake_case; `db:"-"` skips it. Tag options after the name: id,
// version, created, updated, deleted, unique, index, ref=<table>, cascade,
// setnull, json. Pointers are nullable; slices, maps and structs are stored
// as JSON; types implementing encoding.TextMarshaler and TextUnmarshaler as
// strings.
//
// Documentation: https://github.com/danielgtmn/ablage/wiki
package ablage
