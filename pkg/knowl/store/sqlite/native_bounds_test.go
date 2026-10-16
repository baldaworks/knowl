package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	boundsPageID       = "bounded-page"
	boundsAbsentSource = "bounds-no-source"
	boundsBytePage     = "byte-page"
)

// Driver-returned columns/values classify readiness, candidate and evidence
// transfer; these assertions never inspect generated SQL source text.
func TestSQLiteNativePublicBounds(t *testing.T) {
	store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{{ID: boundsPageID, Path: "wiki/bounded-page.md", Digest: boundsPageID, Body: "Original bounded evidence"}})
	transfer := &validationTransfers{}
	observed := sql.OpenDB(validationConnector{driver: store.db.Driver(), path: store.path, transfer: transfer})
	original := store.db
	store.db = observed
	defer func() { store.db = original; _ = observed.Close() }()
	for _, count := range []int{1, 64, 8192} {
		if _, err := original.ExecContext(t.Context(), "DELETE FROM knowl_embedding_chunks WHERE scope=? AND ordinal>0", snapshot.Scope); err != nil {
			t.Fatal(err)
		}
		if count > 1 {
			if _, err := original.ExecContext(t.Context(), `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
    INSERT INTO knowl_embedding_chunks SELECT scope,space,page_id,page_digest,i,content_hash,dimensions,vector FROM knowl_embedding_chunks CROSS JOIN n WHERE scope=? AND ordinal=0`, count-1, snapshot.Scope); err != nil {
				t.Fatal(err)
			}
		}
		coverage, err := hybrid.EncodeCoverage([]hybrid.PageCoverage{{PageID: boundsPageID, PageDigest: boundsPageID, Chunks: count}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := original.ExecContext(t.Context(), "UPDATE knowl_embedding_state SET chunk_count=?,coverage=? WHERE scope=?", count, coverage, snapshot.Scope); err != nil {
			t.Fatal(err)
		}
		*transfer = validationTransfers{}
		refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 1}, nil)
		if err != nil || len(refs) != 1 || report.ScannedChunks != count || report.VectorCandidates != 1 || transfer.denseRows != 1 || transfer.evidenceRows != 1 || transfer.vectorBytes != 0 || transfer.coverageBytes != len(coverage) || transfer.metadataRows != 1 {
			t.Fatalf("count=%d refs=%v report=%+v transfers=%+v err=%v", count, refs, report, transfer, err)
		}
		t.Logf("Search chunks=%d candidates=%d evidence=%d stored-vector-bytes=%d coverage-bytes=%d metadata-rows=%d total-rows=%d", count, transfer.denseRows, transfer.evidenceRows, transfer.vectorBytes, transfer.coverageBytes, transfer.metadataRows, transfer.rows)
		*transfer = validationTransfers{}
		ids, report, _, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeQueryNeedle}, knowl.ReadLimits{Pages: 1})
		if err != nil || len(ids) != 1 || report.ScannedChunks != count || report.VectorCandidates != 1 || transfer.denseRows != 1 || transfer.evidenceRows != 0 || transfer.vectorBytes != 0 || transfer.coverageBytes != len(coverage) {
			t.Fatalf("count=%d ids=%v report=%+v transfers=%+v err=%v", count, ids, report, transfer, err)
		}
		t.Logf("Context chunks=%d candidates=%d evidence=%d stored-vector-bytes=%d coverage-bytes=%d total-rows=%d", count, transfer.denseRows, transfer.evidenceRows, transfer.vectorBytes, transfer.coverageBytes, transfer.rows)
		*transfer = validationTransfers{}
		_, report, err = store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 1}, []knowl.SourceID{boundsAbsentSource})
		if err != nil || report.ScannedChunks != count || report.VectorCandidates != 0 || transfer.denseRows != 0 || transfer.evidenceRows != 0 || transfer.vectorBytes != 0 {
			t.Fatalf("unselected count=%d report=%+v transfers=%+v err=%v", count, report, transfer, err)
		}
		t.Logf("empty eligibility chunks=%d candidates=%d stored-vector-bytes=%d", count, transfer.denseRows, transfer.vectorBytes)
	}
	// The extra row is foreign-space but must count before space/source filtering.
	if _, err := original.ExecContext(t.Context(), `INSERT INTO knowl_embedding_chunks SELECT scope,?,page_id,page_digest,ordinal,content_hash,dimensions,vector FROM knowl_embedding_chunks WHERE scope=? AND ordinal=0`, strings.Repeat("f", 64), snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	assertSQLitePublicBoundFailure(t, store, snapshot.Scope, knowl.RetrievalProjectionCapacity)
	store.db = original
	rebuildErr := store.Rebuild(t.Context(), snapshot)
	store.db = observed
	if rebuildErr != nil {
		t.Fatal(rebuildErr)
	}
	coverage, err := hybrid.EncodeCoverage([]hybrid.PageCoverage{{PageID: boundsPageID, PageDigest: boundsPageID, Chunks: 1}})
	if err != nil {
		t.Fatal(err)
	}
	coverage += strings.Repeat(" ", 1048576-len(coverage))
	if _, err := original.ExecContext(t.Context(), "UPDATE knowl_embedding_state SET coverage=? WHERE scope=?", coverage, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	*transfer = validationTransfers{}
	_, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 1}, nil)
	if err != nil || report.ScannedChunks != 1 || transfer.coverageBytes != 1048576 || transfer.vectorBytes != 0 || transfer.denseRows != 1 {
		t.Fatalf("exact coverage report=%+v transfer=%+v err=%v", report, transfer, err)
	}
	t.Logf("coverage boundary bytes=%d candidates=%d stored-vector-bytes=%d", transfer.coverageBytes, transfer.denseRows, transfer.vectorBytes)
	*transfer = validationTransfers{}
	_, report, _, err = store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeQueryNeedle}, knowl.ReadLimits{Pages: 1})
	if err != nil || report.ScannedChunks != 1 || transfer.coverageBytes != 1048576 || transfer.denseRows != 1 || transfer.vectorBytes != 0 {
		t.Fatalf("exact context coverage report=%+v transfer=%+v err=%v", report, transfer, err)
	}
	if _, err := original.ExecContext(t.Context(), "PRAGMA ignore_check_constraints=ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := original.ExecContext(t.Context(), "UPDATE knowl_embedding_state SET coverage=coverage||' ' WHERE scope=?", snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	*transfer = validationTransfers{}
	assertSQLitePublicBoundFailure(t, store, snapshot.Scope, knowl.RetrievalProjectionDrift)
	if transfer.coverageBytes != 0 || transfer.vectorBytes != 0 {
		t.Fatalf("oversized readiness transferred before bound: %+v", transfer)
	}
}

func assertSQLitePublicBoundFailure(t *testing.T, store *Store, scope knowl.ScopeRef, reason knowl.RetrievalFailure) {
	t.Helper()
	prior := store.embedding.FailurePolicy
	defer func() { store.embedding.FailurePolicy = prior }()
	for _, policy := range []app.EmbeddingFailurePolicy{app.EmbeddingStrict, app.EmbeddingFallbackLexical} {
		store.embedding.FailurePolicy = policy
		_, searchReport, searchErr := store.SearchWithReport(t.Context(), scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 1}, []knowl.SourceID{boundsAbsentSource})
		_, contextReport, _, contextErr := store.SelectContextWithDiagnostics(t.Context(), scope, knowl.SourceSummary{Title: nativeQueryNeedle}, knowl.ReadLimits{Pages: 1})
		for _, result := range []struct {
			report knowl.RetrievalReport
			err    error
		}{{searchReport, searchErr}, {contextReport, contextErr}} {
			if policy == app.EmbeddingStrict {
				assertEmbeddingFailure(t, result.err, reason)
			} else if result.err != nil || result.report.Effective != knowl.RetrievalDegraded {
				t.Fatalf("fallback report=%+v err=%v", result.report, result.err)
			}
			if result.report.Reason != reason || result.report.ScannedChunks != 0 || result.report.VectorCandidates != 0 {
				t.Fatalf("capacity/drift report=%+v", result.report)
			}
		}
	}
}

// Exact logical bytes include manifest padding and metadata, not native storage
// headers. An original ordinal-zero chunk remains the only winning evidence.
func TestSQLiteNativePublicLogicalBytes(t *testing.T) {
	const dimensions = 4096
	vector := make([]float32, dimensions)
	vector[0] = 1
	options := app.EmbeddingOptions{Provider: searchtest.NativeOracleProvider{Vector: vector}, FailurePolicy: app.EmbeddingStrict, Space: app.EmbeddingSpace{Model: "public-byte-bound", Revision: "1", Dimensions: dimensions}}
	store, err := Open(t.Context(), t.TempDir()+"/byte-bound.sqlite", options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	snapshot := knowl.WorkspaceSnapshot{Scope: "public-byte-bound", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: boundsBytePage, Path: "wiki/byte-page.md", Digest: boundsBytePage, Body: "Original byte bound evidence"}}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	const count = 4000
	coverage, err := hybrid.EncodeCoverage([]hybrid.PageCoverage{{PageID: boundsBytePage, PageDigest: boundsBytePage, Chunks: count}})
	if err != nil {
		t.Fatal(err)
	}
	rowBytes := 4*dimensions + len(boundsBytePage) + len(boundsBytePage) + 64 + 64 + 32
	padding := 67108864 - 256 - count*rowBytes - len(coverage)
	if padding < 0 || len(coverage)+padding > 1048576 {
		t.Fatalf("invalid fixture padding=%d", padding)
	}
	coverage += strings.Repeat(" ", padding)
	if _, err := store.db.ExecContext(t.Context(), `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<3999)
  INSERT INTO knowl_embedding_chunks SELECT scope,space,page_id,page_digest,i,content_hash,dimensions,vector FROM knowl_embedding_chunks CROSS JOIN n WHERE scope=? AND ordinal=0`, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), "UPDATE knowl_embedding_state SET chunk_count=4000,coverage=? WHERE scope=?", coverage, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	_, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 1}, []knowl.SourceID{boundsAbsentSource})
	if err != nil || report.ScannedChunks != count || report.VectorCandidates != 0 {
		t.Fatalf("exact 64MiB report=%+v err=%v", report, err)
	}
	ids, report, _, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeQueryNeedle}, knowl.ReadLimits{Pages: 1})
	if err != nil || len(ids) != 1 || report.ScannedChunks != count {
		t.Fatalf("exact 64MiB context=%v report=%+v err=%v", ids, report, err)
	}
	t.Logf("logical projection bytes=67108864 validated chunks=%d coverage-bytes=%d", report.ScannedChunks, len(coverage))
	store.embedding.FailurePolicy = app.EmbeddingFallbackLexical
	for _, contextSelection := range []bool{false, true} {
		deadline, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		if contextSelection {
			_, report, _, err = store.SelectContextWithDiagnostics(deadline, snapshot.Scope, knowl.SourceSummary{Title: nativeQueryNeedle}, knowl.ReadLimits{Pages: 1})
		} else {
			_, report, err = store.SearchWithReport(deadline, snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 1}, nil)
		}
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || report.ScannedChunks != 0 {
			t.Fatalf("scan deadline context=%v report=%+v err=%v", contextSelection, report, err)
		}
	}
	if _, err := store.db.ExecContext(t.Context(), "UPDATE knowl_embedding_state SET coverage=coverage||' ' WHERE scope=?", snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	assertSQLitePublicBoundFailure(t, store, snapshot.Scope, knowl.RetrievalProjectionCapacity)
}

func TestSQLiteNativePublicWaitCancellationAndInvalidDimensions(t *testing.T) {
	for _, dimensions := range []int{0, 4097} {
		_, err := Open(t.Context(), t.TempDir()+fmt.Sprintf("/invalid-%d.sqlite", dimensions), app.EmbeddingOptions{Provider: fixedEmbeddingProvider{}, Space: app.EmbeddingSpace{Model: "bounds-invalid", Revision: "1", Dimensions: dimensions}})
		assertEmbeddingFailure(t, err, knowl.RetrievalInvalidConfiguration)
	}
	store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{{ID: "wait-page", Path: "wiki/wait-page.md", Digest: "wait-page", Body: "Wait evidence"}})
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	store.embedding.FailurePolicy = app.EmbeddingFallbackLexical
	for _, contextSelection := range []bool{false, true} {
		deadline, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		if contextSelection {
			_, _, _, err = store.SelectContextWithDiagnostics(deadline, snapshot.Scope, knowl.SourceSummary{Title: nativeQueryNeedle}, knowl.ReadLimits{Pages: 1})
		} else {
			_, _, err = store.SearchWithReport(deadline, snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 1}, nil)
		}
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("connection wait context=%v err=%v", contextSelection, err)
		}
	}
}
