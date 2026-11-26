# Writing a store

Implement `ablage.Store` (see its documentation) and run the suite:

```go
func TestMyStore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) ablage.Store { return mystore.New() })
}
```

Building blocks in the root package:

- `Catalog`: declared tables, `Tables()` in reference order, `Referencing(table)`
- `Table.CheckInsert`, `CheckUpdate`, `CheckCond`, `CheckQuery`: validate and convert values to their canonical types
- `Cond.Match`, `Compare`, `Query.Less`: the semantics every store must reproduce
- `Row.Clone`, `CloneValue`, `NormalizeTime`

`memstore` is the reference implementation.

Before 1.0, the `Store` interface may grow (e.g. for iterating or bulk writes); such changes are marked as breaking in the release notes.
