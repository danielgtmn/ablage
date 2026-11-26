# Transactions

```go
err := store.Tx(ctx, func(tx ablage.Store) error {
	from, err := accounts.With(tx).Get(ctx, fromID)
	if err != nil {
		return err
	}
	// ...
	return transfers.With(tx).Create(ctx, &t)
})
```

The function's error, or a panic, rolls everything back; `nil` commits. Inside, use only `tx` and repositories bound with `With(tx)`: the outer store would wait forever for the lock (memory) or the only connection (SQLite with one connection). Do not keep `tx` after the function returns.

A failed operation inside (e.g. `ErrDuplicate`) does not end the transaction in any store; on Postgres each write runs under a savepoint for that. You may handle the error and carry on.

A `Tx` inside a `Tx` joins the outer one: if the inner function fails and the outer one ignores the error, the inner writes are committed with the outer ones.

`Update` retries on `ErrStale`; inside a transaction a retry may see the same data again, so set the attempts low or check versions yourself there.
