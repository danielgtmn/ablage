package storetest_test

import (
	"testing"

	"github.com/danielgtmn/ablage/internal/testdb"
	"github.com/danielgtmn/ablage/storetest"
)

func TestStores(t *testing.T) {
	for _, b := range testdb.All() {
		t.Run(b.Name, func(t *testing.T) { storetest.Run(t, storetest.Opener(b.Open)) })
	}
}
