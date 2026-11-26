# AGENTS.md

Guidance for AI coding agents (and people) working on ablage. User
documentation lives in [`wiki/`](wiki/) and is copied to the GitHub wiki by
a workflow: edit it there.

## What this is

ablage is a database layer for Go apps, in the spirit of
[wemogy/libs-infrastructure-database](https://github.com/wemogy/libs-infrastructure-database):
typed repositories over structs (`Repository[T]`) on top of a small,
row-based `Store` interface with three implementations that behave the same:
SQLite and Postgres (`sqlstore`, through `database/sql`) and memory
(`memstore`, for tests). It is meant for all Go projects of its maintainer
(gpanel, sesam, ...), not shaped around one of them.

## Layout

```
*.go (root)        package ablage: Store, Table/Field/Index/Catalog, Cond/Query,
                   Row and value conversion, errors, NewID, Repository[T] and
                   the struct mapping (mapping.go)
sqlstore/          database/sql, dialects SQLite and Postgres, Migrate, DDL
memstore/          in memory, with transactions (undo log)
storetest/         conformance suite every Store must pass (exported)
internal/testdb/   opens the test databases; the only package importing drivers
examples/todo/     a small app on SQLite or Postgres
wiki/              documentation (synced to the GitHub wiki)
scripts/           commit-type.sh (Conventional Commits check) and its test
```

## Rules that are easy to break

- **Every store behaves the same.** A behaviour of one store is a behaviour
  of all: it goes into the `Store` docs, into every store, and into
  `storetest` in the same change. `Cond.Match`, `Compare` and `Query.Less`
  in the root package define the semantics; the SQL stores must render
  exactly that (NULL handling, `Not`, byte order for strings, NULLs first in
  ascending order, id as the last sort key).
- **Values reach SQL only as parameters.** Identifiers come from validated
  table and field names (`namePattern`) and are quoted; never build SQL from
  values.
- **Scope applies to every operation** of a repository, writes included.
  A scope function error fails the operation; it never widens it.
- **Migrations only add data structures.** `Migrate` creates tables,
  columns and indexes. It never drops, renames or changes a table or column.
  Fields that are not nullable get a default (their zero value), so rows of
  older versions stay valid. The one exception: indexes ablage created
  (names ending in `_<8 hex>_key|idx`) that are no longer declared are
  dropped.
- **Values every store keeps alike, nothing else.** `convert` in value.go
  refuses what one store would change (invalid UTF-8, NUL, NaN, years
  outside 1-9999). A new type or operator needs the same care.
- **No database drivers in non-test code** (examples aside). Apps bring
  their own driver and pass a `*sql.DB`.
- **Inside `Tx`, use the store passed to the function** (and
  `repo.With(tx)`). With SQLite on one connection and with `memstore`, the
  outer store waits forever.
- **New dependencies** need a good reason; prefer the standard library. Pin
  GitHub Actions to commit SHAs with a version comment. No GPL/AGPL
  dependencies: ablage is Apache-2.0.

## Commands

```sh
make test            # go vet + go test -race (memory and SQLite; Postgres tests skip)
make postgres        # throwaway Postgres 18 in Docker on port 55433
make test-postgres   # everything, including Postgres
make postgres-stop
make lint            # shellcheck, script tests, actionlint
go run ./examples/todo -list home add "Buy milk"
```

Postgres tests need `ABLAGE_TEST_POSTGRES` (a DSN); each test runs in its own
schema, which is dropped afterwards. CI runs them and fails if they skip.

## Done means tested

Run `make test-postgres` for what you touched. New store behaviour gets a
test in `storetest`, new repository behaviour one in `repository_test.go`
(both run on every store). For changes to how apps use ablage, run
`examples/todo` on SQLite and Postgres. If something could not be tested end
to end, say so explicitly.

## Git, pull requests, releases

- `main` is protected: pull requests only, merged with a **merge commit**.
  Required checks: `go`, `lint`, `commits`. CI skips jobs whose files did
  not change.
- **Conventional Commits** for every commit on `main` and every PR title
  (`feat:`, `fix:`, `perf:`, `docs:`, `chore:`, `ci:`, `refactor:`,
  `test:`, `build:`, optional scope and `!`). The `commits` check enforces it.
- Merging `main` into `release` tags a new version computed from the
  commits (`fix:` patch, `feat:` minor, `!` major) and publishes a GitHub
  release. Before 1.0, breaking changes are expected; mark them with `!`.
- Update the wiki (and README where it covers the same) in the same PR as
  the behaviour it describes.
