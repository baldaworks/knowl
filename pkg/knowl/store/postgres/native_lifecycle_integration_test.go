//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/jackc/pgx/v5/pgconn"
)

// Both public interfaces must expose the same durable lifecycle state. Driver
// details must not escape a capability/history failure into public errors.
func assertPostgresNativeLifecycle(t *testing.T, store *Store, scope knowl.ScopeRef, query string, effective knowl.RetrievalMode, reason knowl.RetrievalFailure) {
	t.Helper()
	refs, searchReport, searchErr := store.SearchWithReport(t.Context(), scope, query, knowl.ReadLimits{Pages: 2}, nil)
	ids, contextReport, _, contextErr := store.SelectContextWithDiagnostics(t.Context(), scope, knowl.SourceSummary{Title: query}, knowl.ReadLimits{Pages: 2})
	for _, result := range []struct {
		report knowl.RetrievalReport
		err    error
	}{{searchReport, searchErr}, {contextReport, contextErr}} {
		if effective == knowl.RetrievalFailed {
			assertEmbeddingFailure(t, result.err, reason)
		} else if result.err != nil {
			t.Fatal(result.err)
		}
		if result.report.Effective != effective || result.report.Reason != reason {
			t.Fatalf("lifecycle report=%+v want=%s/%s", result.report, effective, reason)
		}
		if effective != knowl.RetrievalHybrid && result.report.ScannedChunks != 0 {
			t.Fatalf("false complete validation=%+v", result.report)
		}
		var driverError *pgconn.PgError
		if errors.As(result.err, &driverError) {
			t.Fatalf("unredacted SQLSTATE escaped: %s", driverError.Code)
		}
	}
	if effective == knowl.RetrievalHybrid && (len(refs) == 0 || len(ids) == 0 || searchReport.VectorCandidates == 0 || contextReport.VectorCandidates == 0) {
		t.Fatalf("ready lifecycle returned no native candidates: refs=%v ids=%v search=%+v context=%+v", refs, ids, searchReport, contextReport)
	}
}

func assertPostgresNativeLifecycleFailure(t *testing.T, store *Store, scope knowl.ScopeRef, query string, reason knowl.RetrievalFailure) {
	t.Helper()
	previous := store.embedding.FailurePolicy
	defer func() { store.embedding.FailurePolicy = previous }()
	store.embedding.FailurePolicy = app.EmbeddingStrict
	assertPostgresNativeLifecycle(t, store, scope, query, knowl.RetrievalFailed, reason)
	store.embedding.FailurePolicy = app.EmbeddingFallbackLexical
	assertPostgresNativeLifecycle(t, store, scope, query, knowl.RetrievalDegraded, reason)
}

func assertPostgresNativeLifecycleRebuild(t *testing.T, store *Store) {
	t.Helper()
	snapshot := nativePublicationSnapshot()
	snapshot.Scope = "lifecycle-rebuilt"
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	assertPostgresNativeLifecycle(t, store, snapshot.Scope, nativeQuery, knowl.RetrievalHybrid, "")
}
