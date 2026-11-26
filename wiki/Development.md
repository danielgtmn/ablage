# Development

```sh
make test            # vet and tests on memory and SQLite
make postgres        # Postgres 18 in Docker on 127.0.0.1:55433
make test-postgres   # everything
make lint            # shellcheck, script tests, actionlint
```

The repository tests (`repository_test.go`) and the store suite (`storetest`) run on every store: memory, SQLite with a pool, SQLite with one connection, and Postgres (each test in its own schema). CI fails if the Postgres tests skip.

Rules and conventions: [AGENTS.md](https://github.com/danielgtmn/ablage/blob/main/AGENTS.md). Pull requests: [CONTRIBUTING.md](https://github.com/danielgtmn/ablage/blob/main/CONTRIBUTING.md).
