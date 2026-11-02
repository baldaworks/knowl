package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
)

func TestSQLiteHierarchyRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hierarchy.sqlite")
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	peer, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	storetest.RunHierarchyRetryContract(t, store, peer)
}
