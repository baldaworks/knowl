package sqlite

import (
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
)

func TestOperatorDocuments(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/documents.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	storetest.RunOperatorDocuments(t, store, "operator-documents")
}
