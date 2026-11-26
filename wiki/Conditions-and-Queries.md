# Conditions and queries

Conditions name columns (not Go fields) and are checked against the table: an unknown column or a value of the wrong type is an error, not an empty result.

| Function | Matches rows where |
| --- | --- |
| `All` | always (the zero `Cond`) |
| `Eq(f, v)`, `Ne(f, v)` | `f` equals / differs from `v`. `Ne` includes NULL. `Eq(f, nil)` is `Null(f)` |
| `Lt`, `Lte`, `Gt`, `Gte` | compared with `v` |
| `In(f, vs...)` | `f` is one of `vs`; no values: nothing; any number of values |
| `Prefix(f, p)` | the string starts with `p`, case-sensitive, no wildcards |
| `Null(f)`, `NotNull(f)` | `f` is (not) NULL |
| `And(...)`, `Or(...)`, `Not(c)` | combinations; `Or()` matches nothing |

Values may be any Go value of a fitting kind: named types, any integer, pointers, `TextMarshaler` for string columns.

## NULL

Comparisons are false for NULL, and `Not` turns false into true, so `Not(Gt("age", 40))` includes rows without an age, the same as in Go. JSON columns can only be tested with `Null`.

## Loading related rows

ablage has no joins, on purpose. Load the rows of a page, collect the ids
they refer to, and load those in one call:

```go
tasks, _ := taskRepo.Find(ctx, ablage.Query{Where: ablage.Eq("done", false), Limit: 50})
ids := make([]string, len(tasks))
for i, t := range tasks {
	ids[i] = t.ProjectID
}
projects, _ := projectRepo.GetMany(ctx, ids...) // or Find with ablage.In("id", ids...)
```

`In` takes any number of values, also more than a database accepts
parameters in one statement (32,766 in SQLite, 65,535 in Postgres): the SQL
stores pass long lists as a single parameter. Duplicates do not matter.

## Sorting and paging

```go
ablage.Query{
	Where:   ablage.Eq("done", false),
	OrderBy: []ablage.Order{ablage.Desc("priority"), ablage.Asc("created_at")},
	Limit:   50,
	Offset:  100,
}
```

`Limit: 0` means no limit. Rows are always sorted, finally by id, so pages are stable. NULL comes first ascending and last descending. Strings sort by bytes (`"B" < "a" < "Ä"`) in every store; for locale-aware sorting, sort in Go. JSON columns cannot be sorted by.

Ids from `NewID` sort by creation time, so `Asc("id")` is creation order and keyset paging works: `Where: ablage.Gt("id", lastID), Limit: n`.
