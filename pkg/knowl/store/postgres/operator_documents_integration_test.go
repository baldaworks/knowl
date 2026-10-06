//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
)

func runOperatorDocumentsPostgres(t *testing.T, dsn string) {
	store, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// A localized default must not change bytewise pagination or its comparator.
	if _, err := store.db.ExecContext(t.Context(), `CREATE COLLATION knowl_document_fixture (provider=icu,locale='en')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `ALTER TABLE knowl_source_documents ALTER COLUMN document_id TYPE TEXT COLLATE knowl_document_fixture`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := store.db.ExecContext(ctx, `ALTER TABLE knowl_source_documents ALTER COLUMN document_id TYPE TEXT COLLATE "default"`); err != nil {
			t.Error(err)
		}
		if _, err := store.db.ExecContext(ctx, `DROP COLLATION knowl_document_fixture`); err != nil {
			t.Error(err)
		}
	})
	storetest.RunOperatorDocuments(t, store, "operator-documents")
}
