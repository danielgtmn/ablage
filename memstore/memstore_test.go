package memstore_test

import (
	"context"
	"testing"

	"github.com/danielgtmn/ablage"
	"github.com/danielgtmn/ablage/memstore"
)

func TestTxStoreAfterTx(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()
	if err := s.Declare(ablage.Table{Name: "items"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var leaked ablage.Store
	if err := s.Tx(ctx, func(tx ablage.Store) error { leaked = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := leaked.Insert(ctx, "items", ablage.Row{"id": "a"}); err == nil {
		t.Error("the store of an ended transaction still writes")
	}
}
