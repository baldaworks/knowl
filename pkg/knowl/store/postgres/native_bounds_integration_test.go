//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	boundsPageID       = "bounded-page"
	boundsAbsentSource = "bounds-no-source"
	boundsBytePage     = "byte-page"
)

// Driver-returned columns/values classify readiness, candidate and evidence
// transfer; these assertions never inspect generated SQL source text.
func TestPostgresNativePublicBounds(t *testing.T) {
	dsn := nativeOracleContainer(t)
	store := nativeOpen(t, dsn, nativeOptions())
	snapshot := nativeSearchSnapshot([]knowl.PageSnapshot{{ID: boundsPageID, Path: "wiki/bounded-page.md", Digest: boundsPageID, Body: "Original bounded evidence"}})
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	transfer := &nativeTransferCounts{}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	observed := sql.OpenDB(nativeObservedConnector{Connector: stdlib.GetConnector(*config), counts: transfer})
	original := store.db
	store.db = observed
	defer func() { store.db = original; _ = observed.Close() }()
	for _, count := range []int{1, 64, 8192} {
		if _, err := original.ExecContext(t.Context(), "DELETE FROM knowl_embedding_chunks WHERE scope=$1 AND ordinal>0", snapshot.Scope); err != nil {
			t.Fatal(err)
		}
		if count > 1 {
			if _, err := original.ExecContext(t.Context(), `INSERT INTO knowl_embedding_chunks SELECT scope,space,page_id,page_digest,n,content_hash,dimensions,vector FROM knowl_embedding_chunks CROSS JOIN generate_series(1,$1::integer) n WHERE scope=$2 AND ordinal=0`, count-1, snapshot.Scope); err != nil {
				t.Fatal(err)
			}
		}
		coverage, err := hybrid.EncodeCoverage([]hybrid.PageCoverage{{PageID: boundsPageID, PageDigest: boundsPageID, Chunks: count}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := original.ExecContext(t.Context(), "UPDATE knowl_embedding_state SET chunk_count=$1,coverage=$2 WHERE scope=$3", count, coverage, snapshot.Scope); err != nil {
			t.Fatal(err)
		}
		*transfer = nativeTransferCounts{}
		refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 1}, nil)
		if err != nil || len(refs) != 1 || report.ScannedChunks != count || report.VectorCandidates != 1 || transfer.denseRows != 1 || transfer.evidenceRows != 1 || transfer.vectorBytes != 0 || transfer.coverageBytes != len(coverage) || transfer.metadataRows != 1 {
			t.Fatalf("count=%d refs=%v report=%+v transfers=%+v err=%v", count, refs, report, transfer, err)
		}
		t.Logf("Search chunks=%d candidates=%d evidence=%d stored-vector-bytes=%d coverage-bytes=%d metadata-rows=%d total-rows=%d", count, transfer.denseRows, transfer.evidenceRows, transfer.vectorBytes, transfer.coverageBytes, transfer.metadataRows, transfer.rows)
		*transfer = nativeTransferCounts{}
		ids, report, _, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeSearchNeedle}, knowl.ReadLimits{Pages: 1})
		if err != nil || len(ids) != 1 || report.ScannedChunks != count || report.VectorCandidates != 1 || transfer.denseRows != 1 || transfer.evidenceRows != 0 || transfer.vectorBytes != 0 || transfer.coverageBytes != len(coverage) {
			t.Fatalf("count=%d ids=%v report=%+v transfers=%+v err=%v", count, ids, report, transfer, err)
		}
		t.Logf("Context chunks=%d candidates=%d evidence=%d stored-vector-bytes=%d coverage-bytes=%d total-rows=%d", count, transfer.denseRows, transfer.evidenceRows, transfer.vectorBytes, transfer.coverageBytes, transfer.rows)
		*transfer = nativeTransferCounts{}
		_, report, err = store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 1}, []knowl.SourceID{boundsAbsentSource})
		if err != nil || report.ScannedChunks != count || report.VectorCandidates != 0 || transfer.denseRows != 0 || transfer.evidenceRows != 0 || transfer.vectorBytes != 0 {
			t.Fatalf("unselected count=%d report=%+v transfers=%+v err=%v", count, report, transfer, err)
		}
		t.Logf("empty eligibility chunks=%d candidates=%d stored-vector-bytes=%d", count, transfer.denseRows, transfer.vectorBytes)
	}
	// The extra row is foreign-space but must count before space/source filtering.
	if _, err := original.ExecContext(t.Context(), `INSERT INTO knowl_embedding_chunks SELECT scope,$1,page_id,page_digest,ordinal,content_hash,dimensions,vector FROM knowl_embedding_chunks WHERE scope=$2 AND ordinal=0`, strings.Repeat("f", 64), snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	assertPostgresPublicBoundFailure(t, store, snapshot.Scope, knowl.RetrievalProjectionCapacity)
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	coverage, err := hybrid.EncodeCoverage([]hybrid.PageCoverage{{PageID: boundsPageID, PageDigest: boundsPageID, Chunks: 1}})
	if err != nil {
		t.Fatal(err)
	}
	coverage += strings.Repeat(" ", 1048576-len(coverage))
	if _, err := original.ExecContext(t.Context(), "UPDATE knowl_embedding_state SET coverage=$1 WHERE scope=$2", coverage, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	*transfer = nativeTransferCounts{}
	_, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 1}, nil)
	if err != nil || report.ScannedChunks != 1 || transfer.coverageBytes != 1048576 || transfer.vectorBytes != 0 || transfer.denseRows != 1 {
		t.Fatalf("exact coverage report=%+v transfer=%+v err=%v", report, transfer, err)
	}
	t.Logf("coverage boundary bytes=%d candidates=%d stored-vector-bytes=%d", transfer.coverageBytes, transfer.denseRows, transfer.vectorBytes)
	*transfer = nativeTransferCounts{}
	_, report, _, err = store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeSearchNeedle}, knowl.ReadLimits{Pages: 1})
	if err != nil || report.ScannedChunks != 1 || transfer.coverageBytes != 1048576 || transfer.denseRows != 1 || transfer.vectorBytes != 0 {
		t.Fatalf("exact context coverage report=%+v transfer=%+v err=%v", report, transfer, err)
	}
	if _, err := original.ExecContext(t.Context(), "ALTER TABLE knowl_embedding_state DROP CONSTRAINT knowl_embedding_state_coverage_check"); err != nil {
		t.Fatal(err)
	}
	if _, err := original.ExecContext(t.Context(), "UPDATE knowl_embedding_state SET coverage=coverage||' ' WHERE scope=$1", snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	*transfer = nativeTransferCounts{}
	assertPostgresPublicBoundFailure(t, store, snapshot.Scope, knowl.RetrievalProjectionDrift)
	if transfer.coverageBytes != 0 || transfer.vectorBytes != 0 {
		t.Fatalf("oversized readiness transferred before bound: %+v", transfer)
	}
}

func assertPostgresPublicBoundFailure(t *testing.T, store *Store, scope knowl.ScopeRef, reason knowl.RetrievalFailure) {
	t.Helper()
	prior := store.embedding.FailurePolicy
	defer func() { store.embedding.FailurePolicy = prior }()
	for _, policy := range []app.EmbeddingFailurePolicy{app.EmbeddingStrict, app.EmbeddingFallbackLexical} {
		store.embedding.FailurePolicy = policy
		_, searchReport, searchErr := store.SearchWithReport(t.Context(), scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 1}, []knowl.SourceID{boundsAbsentSource})
		_, contextReport, _, contextErr := store.SelectContextWithDiagnostics(t.Context(), scope, knowl.SourceSummary{Title: nativeSearchNeedle}, knowl.ReadLimits{Pages: 1})
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
func TestPostgresNativePublicLogicalBytes(t *testing.T) {
	const dimensions = 4096
	vector := make([]float32, dimensions)
	vector[0] = 1
	options := app.EmbeddingOptions{Provider: searchtest.NativeOracleProvider{Vector: vector}, FailurePolicy: app.EmbeddingStrict, Space: app.EmbeddingSpace{Model: "public-byte-bound", Revision: "1", Dimensions: dimensions}}
	dsn := nativeOracleContainer(t)
	store, err := Open(t.Context(), dsn, options)
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
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_embedding_chunks SELECT scope,space,page_id,page_digest,n,content_hash,dimensions,vector FROM knowl_embedding_chunks CROSS JOIN generate_series(1,3999) n WHERE scope=$1 AND ordinal=0`, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), "UPDATE knowl_embedding_state SET chunk_count=4000,coverage=$1 WHERE scope=$2", coverage, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	var headerBytes int
	if err := store.db.QueryRowContext(t.Context(), "SELECT min(pg_column_size(vector)-4*dimensions) FROM knowl_embedding_chunks WHERE scope=$1", snapshot.Scope).Scan(&headerBytes); err != nil || headerBytes <= 0 {
		t.Fatalf("native header=%d err=%v", headerBytes, err)
	}
	t.Logf("native physical vector header bytes=%d per chunk; excluded from logical cap", headerBytes)
	_, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 1}, []knowl.SourceID{boundsAbsentSource})
	if err != nil || report.ScannedChunks != count || report.VectorCandidates != 0 {
		t.Fatalf("exact 64MiB report=%+v err=%v", report, err)
	}
	ids, report, _, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeSearchNeedle}, knowl.ReadLimits{Pages: 1})
	if err != nil || len(ids) != 1 || report.ScannedChunks != count {
		t.Fatalf("exact 64MiB context=%v report=%+v err=%v", ids, report, err)
	}
	t.Logf("logical projection bytes=67108864 validated chunks=%d coverage-bytes=%d", report.ScannedChunks, len(coverage))
	store.embedding.FailurePolicy = app.EmbeddingFallbackLexical
	for _, contextSelection := range []bool{false, true} {
		deadline, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		if contextSelection {
			_, report, _, err = store.SelectContextWithDiagnostics(deadline, snapshot.Scope, knowl.SourceSummary{Title: nativeSearchNeedle}, knowl.ReadLimits{Pages: 1})
		} else {
			_, report, err = store.SearchWithReport(deadline, snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 1}, nil)
		}
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || report.ScannedChunks != 0 {
			t.Fatalf("scan deadline context=%v report=%+v err=%v", contextSelection, report, err)
		}
	}
	if _, err := store.db.ExecContext(t.Context(), "UPDATE knowl_embedding_state SET coverage=coverage||' ' WHERE scope=$1", snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	assertPostgresPublicBoundFailure(t, store, snapshot.Scope, knowl.RetrievalProjectionCapacity)
}

func TestPostgresNativePublicWaitCancellationAndInvalidDimensions(t *testing.T) {
	dsn := nativeOracleContainer(t)
	for _, dimensions := range []int{0, 4097} {
		_, err := Open(t.Context(), dsn, app.EmbeddingOptions{Provider: structuredEvidenceProvider{}, Space: app.EmbeddingSpace{Model: "bounds-invalid", Revision: "1", Dimensions: dimensions}})
		assertEmbeddingFailure(t, err, knowl.RetrievalInvalidConfiguration)
	}
	store := nativeOpen(t, dsn, nativeOptions())
	snapshot := nativeSearchSnapshot([]knowl.PageSnapshot{{ID: "wait-page", Path: "wiki/wait-page.md", Digest: "wait-page", Body: "Wait evidence"}})
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	store.db.SetMaxOpenConns(1)
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	store.embedding.FailurePolicy = app.EmbeddingFallbackLexical
	for _, contextSelection := range []bool{false, true} {
		deadline, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		if contextSelection {
			_, _, _, err = store.SelectContextWithDiagnostics(deadline, snapshot.Scope, knowl.SourceSummary{Title: nativeSearchNeedle}, knowl.ReadLimits{Pages: 1})
		} else {
			_, _, err = store.SearchWithReport(deadline, snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 1}, nil)
		}
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("connection wait context=%v err=%v", contextSelection, err)
		}
	}
}
