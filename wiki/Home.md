# ablage

Typed repositories for Go on SQLite, Postgres and memory, with tenant scopes, soft delete and optimistic concurrency built in.

- [Getting started](Getting-Started): install, open a store, the first repository
- [Repositories](Repositories): struct mapping, every operation, scopes, soft delete, versions
- [Conditions and queries](Conditions-and-Queries): filtering, sorting, paging, NULL rules
- [Schema and migrations](Schema-and-Migrations): types, indexes, references, what Migrate does
- [Stores](Stores): SQLite, Postgres, memory, and how they store values
- [Transactions](Transactions)
- [Writing a store](Writing-Stores): the `Store` interface and its conformance suite
- [Development](Development) and [Roadmap](Roadmap)

## Packages

| Package | What it is |
| --- | --- |
| `ablage` | `Repository[T]`, `Store`, conditions, schema, errors |
| `ablage/sqlstore` | SQLite and Postgres through `database/sql` |
| `ablage/memstore` | in memory, for tests |
| `ablage/storetest` | the conformance suite every store passes |
