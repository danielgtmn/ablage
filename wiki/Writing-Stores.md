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
- `Cond.Match`, `Compare`, `Query.Less`: the semantics every store must reproduce. `In` lists can be very long (the suite uses 70,000 values); `CheckCond` hands them over sorted and without duplicates
- `Row.Clone`, `CloneValue`, `NormalizeTime`

Optional interfaces, which every store of this module implements and the
suite tests when a store has them:

- `ablage.Aggregator` (`Aggregate`): check with `Table.CheckAggregation`; `Aggregation.Apply` defines the result. Without it, `ablage.Aggregate` and `Repository.Aggregate` fail with `ErrUnsupported`.

`memstore` is the reference implementation.

Before 1.0, the `Store` interface may grow (e.g. for iterating or bulk writes); such changes are marked as breaking in the release notes.
