package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/pressly/goose/v3"
	"slices"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	"reflect"
	"runtime"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const embeddingTestDigest = "embedding-fixture-digest"

func TestRetrievalReportPersists(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/embeddings.sqlite"
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key := knowl.OperationKey{Scope: testLocalScope, Source: knowl.SourceRef{Adapter: testFixture, ID: "report"}, Version: knowl.SourceVersion{Version: "1", Digest: embeddingTestDigest}}
	reserved, err := store.Reserve(ctx, key, knowl.OperationMeta{SchemaDigest: testSchemaDigest})
	if err != nil {
		t.Fatal(err)
	}
	report := knowl.RetrievalReport{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalDegraded, Reason: knowl.RetrievalUnavailable, LexicalCandidates: 2}
	if err := store.SaveRetrievalReport(ctx, key.Scope, reserved.ID, 0, report); err != nil {
		t.Fatal(err)
	}
	operation, err := store.Operation(ctx, key.Scope, reserved.ID)
	if err != nil || !reflect.DeepEqual(operation.Retrieval, &report) {
		t.Fatalf("persisted report = %#v, %v; want %#v", operation.Retrieval, err, report)
	}
}

func embeddingFixture(t *testing.T) (*Store, knowl.WorkspaceSnapshot, hybrid.ProjectionState, []hybrid.Chunk) {
	t.Helper()
	store, err := Open(t.Context(), t.TempDir()+"/vectors.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := genericSQLiteSnapshot()
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	state := hybrid.ProjectionState{Space: strings.Repeat("a", 64), SnapshotDigest: snapshotDigest(snapshot), Dimensions: 2, Mode: knowl.RetrievalHybrid, ChunkCount: len(snapshot.Pages), ReadyAt: snapshot.CapturedAt}
	chunks := make([]hybrid.Chunk, 0, len(snapshot.Pages))
	for _, page := range snapshot.Pages {
		chunks = append(chunks, hybrid.Chunk{PageID: page.ID, PageDigest: page.Digest, ContentHash: strings.Repeat("b", 64), Vector: []float32{1, 0}})
	}
	if err := store.publishEmbeddings(t.Context(), snapshot.Scope, state, chunks); err != nil {
		t.Fatal(err)
	}
	return store, snapshot, state, chunks
}

func readEmbeddingFixture(ctx context.Context, store *Store, scope knowl.ScopeRef, state hybrid.ProjectionState) (hybrid.ProjectionState, []hybrid.Chunk, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return hybrid.ProjectionState{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return embeddingProjectionTx(ctx, tx, scope, state.Space, state.Dimensions)
}

func assertEmbeddingFailure(t *testing.T, err error, code knowl.RetrievalFailure) {
	t.Helper()
	var classified *app.EmbeddingError
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v; want embedding code %s", err, code)
	}
}

func TestSQLiteEmbeddingPersistenceAndScope(t *testing.T) {
	store, snapshot, state, chunks := embeddingFixture(t)
	actual, rows, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
	if err != nil || actual != state || len(rows) != len(chunks) {
		t.Fatalf("stored state=%+v chunks=%v err=%v", actual, rows, err)
	}
	foreign := snapshot
	foreign.Scope = "foreign"
	if err := store.Rebuild(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	foreignState := state
	foreignState.SnapshotDigest = snapshotDigest(foreign)
	foreignChunks := slices.Clone(chunks)
	for i := range foreignChunks {
		foreignChunks[i].Vector = []float32{0, 1}
	}
	if err := store.publishEmbeddings(t.Context(), foreign.Scope, foreignState, foreignChunks); err != nil {
		t.Fatal(err)
	}
	_, originalRows, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
	if err != nil || originalRows[0].Vector[0] != 1 {
		t.Fatalf("foreign publication changed local scope: %v %v", originalRows, err)
	}
	_, foreignRows, err := readEmbeddingFixture(t.Context(), store, foreign.Scope, foreignState)
	if err != nil || foreignRows[0].Vector[1] != 1 {
		t.Fatalf("foreign scope publication=%v %v", foreignRows, err)
	}
	var blob []byte
	if err := store.db.QueryRowContext(t.Context(), `SELECT vector FROM knowl_embedding_chunks WHERE scope=? LIMIT 1`, snapshot.Scope).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(blob, []byte{0, 0, 128, 63, 0, 0, 0, 0}) {
		t.Fatalf("portable blob=%v", blob)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), store.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	_, rows, err = readEmbeddingFixture(t.Context(), reopened, snapshot.Scope, state)
	if err != nil || len(rows) != len(chunks) {
		t.Fatalf("restart rows=%v err=%v", rows, err)
	}
	_, _, err = readEmbeddingFixture(t.Context(), reopened, "absent", state)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionNotReady)
	incompatible := state
	incompatible.Space = strings.Repeat("c", 64)
	_, _, err = readEmbeddingFixture(t.Context(), reopened, snapshot.Scope, incompatible)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
	incompatible = state
	incompatible.Dimensions = 3
	_, _, err = readEmbeddingFixture(t.Context(), reopened, snapshot.Scope, incompatible)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
}

func TestSQLiteEmbeddingPublicationRollsBackAndInvalidates(t *testing.T) {
	store, snapshot, state, chunks := embeddingFixture(t)
	bad := slices.Clone(chunks)
	bad[len(bad)-1].PageDigest = "stale"
	assertEmbeddingFailure(t, store.publishEmbeddings(t.Context(), snapshot.Scope, state, bad), knowl.RetrievalProjectionDrift)
	_, rows, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
	if err != nil || len(rows) != len(chunks) {
		t.Fatalf("failed publication replaced ready state: %v %v", rows, err)
	}
	broken := snapshot
	broken.Pages = slices.Clone(snapshot.Pages)
	broken.Pages[0].Body = strings.Repeat("a ", 131073)
	if err := store.Rebuild(t.Context(), broken); !errors.Is(err, lexical.ErrInvalidProjection) {
		t.Fatalf("bad lexical rebuild error=%v", err)
	}
	if _, _, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state); err != nil {
		t.Fatalf("failed lexical transaction invalidated vectors: %v", err)
	}
	peer, err := Open(t.Context(), store.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	changed := snapshot
	changed.Pages = slices.Clone(snapshot.Pages[1:])
	changed.Pages[0].Digest = "updated"
	if err := peer.Rebuild(t.Context(), changed); err != nil {
		t.Fatal(err)
	}
	assertEmbeddingFailure(t, store.publishEmbeddings(t.Context(), snapshot.Scope, state, chunks), knowl.RetrievalProjectionDrift)
	_, _, err = readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionNotReady)
	refs, err := store.Search(t.Context(), snapshot.Scope, "cafe", knowl.ReadLimits{}, nil)
	if err != nil || len(refs) != 1 {
		t.Fatalf("lexical projection after update/delete: %v %v", refs, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.publishEmbeddings(canceled, snapshot.Scope, state, chunks); !errors.Is(err, context.Canceled) {
		t.Fatalf("publication cancellation=%v", err)
	}
}

func TestSQLiteEmbeddingCorruptionFailsClosed(t *testing.T) {
	cases := []struct {
		name, statement string
		args            []any
	}{
		{"missing", `DELETE FROM knowl_embedding_chunks WHERE scope=? AND page_id=?`, []any{testGenericAccentID}},
		{"page digest", `UPDATE knowl_embedding_chunks SET page_digest=? WHERE scope=?`, []any{"wrong"}},
		{"space", `UPDATE knowl_embedding_chunks SET space=? WHERE scope=?`, []any{strings.Repeat("c", 64)}},
		{"zero vector", `UPDATE knowl_embedding_chunks SET vector=? WHERE scope=?`, []any{make([]byte, 8)}},
		{"ordinal gap", `UPDATE knowl_embedding_chunks SET ordinal=2 WHERE scope=?`, nil},
		{"count", `UPDATE knowl_embedding_state SET chunk_count=1 WHERE scope=?`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, snapshot, state, _ := embeddingFixture(t)
			args := slices.Clone(tc.args)
			if tc.name == "missing" {
				args = []any{snapshot.Scope, testGenericAccentID}
			} else {
				args = append(args, snapshot.Scope)
			}
			if _, err := store.db.ExecContext(t.Context(), tc.statement, args...); err != nil {
				t.Fatal(err)
			}
			_, _, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
			assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
		})
	}
}

func TestSQLiteEmbeddingDegradedAndCapacity(t *testing.T) {
	store, snapshot, state, chunks := embeddingFixture(t)
	degraded := state
	degraded.Mode = knowl.RetrievalDegraded
	degraded.Reason = knowl.RetrievalUnavailable
	degraded.ChunkCount = 0
	if err := store.publishEmbeddings(t.Context(), snapshot.Scope, degraded, nil); err != nil {
		t.Fatal(err)
	}
	actual, rows, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
	if err != nil || actual.Mode != knowl.RetrievalDegraded || len(rows) != 0 {
		t.Fatalf("degraded state=%+v rows=%v err=%v", actual, rows, err)
	}
	overflow := make([]hybrid.Chunk, hybrid.MaxChunks+1)
	oversized := state
	oversized.ChunkCount = len(overflow)
	assertEmbeddingFailure(t, store.publishEmbeddings(t.Context(), snapshot.Scope, oversized, overflow), knowl.RetrievalProjectionCapacity)
	if err := store.publishEmbeddings(t.Context(), snapshot.Scope, state, chunks); err != nil {
		t.Fatal(err)
	}
	// Actual persisted overflow must be counted before matching spaces or pages.
	if _, err := store.db.ExecContext(t.Context(), `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= hybrid.MaxChunks; i++ {
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO knowl_embedding_chunks(scope,space,page_id,page_digest,ordinal,content_hash,dimensions,vector) VALUES(?,?,?,?,?,?,?,?)`, snapshot.Scope, fmt.Sprintf("foreign-%d", i), chunks[0].PageID, chunks[0].PageDigest, 0, chunks[0].ContentHash, 2, []byte{0, 0, 128, 63, 0, 0, 0, 0}); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, _, err = readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionCapacity)
}

func TestSQLiteRetrievalReportGuardsAndRestart(t *testing.T) {
	ctx := t.Context()
	store, snapshot, _, _ := embeddingFixture(t)
	key, _ := executionFixture(snapshot.Scope, "guard", time.Unix(1, 0).UTC())
	reserved, err := store.Reserve(ctx, key, knowl.OperationMeta{SchemaDigest: testSchemaDigest})
	if err != nil {
		t.Fatal(err)
	}
	id := reserved.ID
	if reserved.Retrieval != nil {
		t.Fatal("legacy NULL produced a report")
	}
	report := knowl.RetrievalReport{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalDegraded, Reason: knowl.RetrievalUnavailable}
	if err := store.SaveRetrievalReport(ctx, "foreign", id, 0, report); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong scope=%v", err)
	}
	if err := store.SaveRetrievalReport(ctx, key.Scope, id, 1, report); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("wrong attempt=%v", err)
	}
	if err := store.SaveRetrievalReport(ctx, key.Scope, id, 0, report); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRetrievalReport(ctx, key.Scope, id, 0, report); err != nil {
		t.Fatal(err)
	}
	changed := report
	changed.Reason = knowl.RetrievalDeadline
	if err := store.SaveRetrievalReport(ctx, key.Scope, id, 0, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same attempt replaced report: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE knowl_operations SET work_attempt=1 WHERE operation_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRetrievalReport(ctx, key.Scope, id, 0, report); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("late old attempt=%v", err)
	}
	if err := store.SaveRetrievalReport(ctx, key.Scope, id, 1, changed); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail(ctx, id, knowl.Failure{Class: testFailureClass}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRetrievalReport(ctx, key.Scope, id, 1, changed); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRetrievalReport(ctx, key.Scope, id, 1, report); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal report replaced: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, store.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	operation, err := reopened.Operation(ctx, key.Scope, id)
	if err != nil || !reflect.DeepEqual(operation.Retrieval, &changed) {
		t.Fatalf("restart report=%+v err=%v", operation.Retrieval, err)
	}
	if _, err := reopened.db.ExecContext(ctx, `UPDATE knowl_operations SET retrieval_report_attempt=2 WHERE operation_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Operation(ctx, key.Scope, id); !errors.Is(err, app.ErrRetrievalReportInvalid) {
		t.Fatalf("future report attempt accepted: %v", err)
	}
	if _, err := reopened.db.ExecContext(ctx, `UPDATE knowl_operations SET retrieval_report_attempt=1 WHERE operation_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.db.ExecContext(ctx, `UPDATE knowl_operations SET retrieval_report=NULL WHERE operation_id=?`, id); err != nil {
		t.Fatal(err)
	}
	legacy, err := reopened.Operation(ctx, key.Scope, id)
	if err != nil || legacy.Retrieval != nil || legacy.RetrievalAttempt != 0 {
		t.Fatalf("NULL report exposed attempt: %+v %v", legacy, err)
	}
	if err := reopened.SaveRetrievalReport(ctx, key.Scope, id, 1, changed); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("terminal missing report changed: %v", err)
	}
	if _, err := reopened.db.ExecContext(ctx, `UPDATE knowl_operations SET retrieval_report=? WHERE operation_id=?`, `{"secret":"invalid"}`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Operation(ctx, key.Scope, id); !errors.Is(err, app.ErrRetrievalReportInvalid) {
		t.Fatalf("corrupt report=%v", err)
	}
	invalid := report
	invalid.Reason = "http://secret.invalid"
	if err := reopened.SaveRetrievalReport(ctx, key.Scope, id, 1, invalid); !errors.Is(err, app.ErrRetrievalReportInvalid) {
		t.Fatalf("unbounded/unallowlisted report=%v", err)
	}
}

func TestSQLiteEmbeddingMigrationDownPreservesLexicalAndOperations(t *testing.T) {
	store, snapshot, _, _ := embeddingFixture(t)
	reserved, err := store.Reserve(t.Context(), knowl.OperationKey{Scope: snapshot.Scope, Source: knowl.SourceRef{Adapter: testFixture, ID: "down"}, Version: knowl.SourceVersion{Version: "1", Digest: embeddingTestDigest}}, knowl.OperationMeta{SchemaDigest: testSchemaDigest})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, store.db, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(t.Context(), 15); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatalf("down invalidated lexical snapshot: %v", err)
	}
	refs, err := store.Search(t.Context(), snapshot.Scope, "café", knowl.ReadLimits{}, nil)
	if err != nil || len(refs) != 1 {
		t.Fatalf("down lost lexical search: %v %v", refs, err)
	}
	var id knowl.OperationID
	if err := store.db.QueryRowContext(t.Context(), `SELECT operation_id FROM knowl_operations WHERE operation_id=?`, reserved.ID).Scan(&id); err != nil || id != reserved.ID {
		t.Fatalf("down lost operation: %s %v", id, err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	operation, err := store.Operation(t.Context(), snapshot.Scope, id)
	if err != nil || operation.Retrieval != nil {
		t.Fatalf("re-up operation=%+v err=%v", operation, err)
	}
}

func TestSQLiteEmbeddingReadUsesOneSnapshot(t *testing.T) {
	store, snapshot, state, _ := embeddingFixture(t)
	peer, err := Open(t.Context(), store.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	tx, err := store.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, chunks, err := embeddingProjectionTx(t.Context(), tx, snapshot.Scope, state.Space, state.Dimensions)
	if err != nil || len(chunks) != state.ChunkCount {
		t.Fatalf("snapshot chunks=%v err=%v", chunks, err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	changed := snapshot
	changed.Pages = slices.Clone(snapshot.Pages)
	changed.Pages[0].Digest = "concurrent-change"
	go func() { close(started); done <- peer.Rebuild(t.Context(), changed) }()
	<-started
	var digest string
	if err := tx.QueryRowContext(t.Context(), `SELECT digest FROM knowl_pages WHERE scope=? AND page_id=?`, snapshot.Scope, snapshot.Pages[0].ID).Scan(&digest); err != nil || digest != snapshot.Pages[0].Digest {
		t.Fatalf("read mixed old vectors/new page: %q %v", digest, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, _, err = readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionNotReady)
}

func TestSQLiteEmbeddingExactChunkCapacity(t *testing.T) {
	store, snapshot, state, _ := embeddingFixture(t)
	snapshot.Pages = nil
	chunks := make([]hybrid.Chunk, 0, hybrid.MaxChunks)
	for i := 0; i < hybrid.MaxChunks/hybrid.PageChunks; i++ {
		id := knowl.PageID(fmt.Sprintf("capacity-%d", i))
		snapshot.Pages = append(snapshot.Pages, knowl.PageSnapshot{ID: id, Path: fmt.Sprintf("wiki/capacity/%d.md", i), Title: "Capacity", Body: "bounded original", Digest: embeddingTestDigest})
		for ordinal := 0; ordinal < hybrid.PageChunks; ordinal++ {
			chunks = append(chunks, hybrid.Chunk{PageID: id, PageDigest: embeddingTestDigest, Ordinal: ordinal, ContentHash: strings.Repeat("b", 64), Vector: []float32{1, 0}})
		}
	}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	state.SnapshotDigest = snapshotDigest(snapshot)
	state.ChunkCount = len(chunks)
	if err := store.publishEmbeddings(t.Context(), snapshot.Scope, state, chunks); err != nil {
		t.Fatalf("exact capacity publication: %v", err)
	}
	_, rows, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
	if err != nil || len(rows) != hybrid.MaxChunks {
		t.Fatalf("exact capacity rows=%d err=%v", len(rows), err)
	}
}

func TestSQLiteRetrievalReportRetainsHistoricalAttempt(t *testing.T) {
	store, snapshot, _, _ := embeddingFixture(t)
	key, meta := executionFixture(snapshot.Scope, "claim-report", time.Unix(1, 0).UTC())
	reserved, err := store.Reserve(t.Context(), key, meta)
	if err != nil {
		t.Fatal(err)
	}
	report := knowl.RetrievalReport{Requested: knowl.RetrievalLexical, Effective: knowl.RetrievalLexical}
	if err := store.SaveRetrievalReport(t.Context(), key.Scope, reserved.ID, 0, report); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimOperation(t.Context(), key.Scope, reserved.ID, knowl.WorkLease{Token: "report-worker", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Operation.WorkAttempt != 1 || !reflect.DeepEqual(claim.Operation.Retrieval, &report) || claim.Operation.RetrievalAttempt != 0 {
		t.Fatalf("new worker lost distinguishable historical report: %+v", claim.Operation)
	}
	current, err := store.Operation(t.Context(), key.Scope, reserved.ID)
	if err != nil || !reflect.DeepEqual(current.Retrieval, &report) || current.RetrievalAttempt != 0 {
		t.Fatalf("new attempt lost historical report=%+v %v", current.Retrieval, err)
	}
	if err := store.SaveRetrievalReport(t.Context(), key.Scope, reserved.ID, claim.Operation.WorkAttempt, report); err != nil {
		t.Fatal(err)
	}
	current, err = store.Operation(t.Context(), key.Scope, reserved.ID)
	if err != nil || !reflect.DeepEqual(current.Retrieval, &report) || current.RetrievalAttempt != claim.Operation.WorkAttempt {
		t.Fatalf("current report=%+v %v", current.Retrieval, err)
	}
}

func TestSQLiteEmbeddingOversizedMetadataFailsClosed(t *testing.T) {
	for _, field := range []string{"reason", "space", "snapshot_digest", "ready_at", "canonical_digest"} {
		t.Run(field, func(t *testing.T) {
			store, snapshot, state, _ := embeddingFixture(t)
			// Emulate corrupt persisted metadata independently of normal write guards.
			if _, err := store.db.ExecContext(t.Context(), `PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			statement := `UPDATE knowl_embedding_state SET ` + field + `=? WHERE scope=?`
			if field == "canonical_digest" {
				statement = `UPDATE knowl_pages SET digest=? WHERE scope=?`
			}
			if _, err := store.db.ExecContext(t.Context(), statement, strings.Repeat("x", 2<<20), snapshot.Scope); err != nil {
				t.Fatal(err)
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, _, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
			runtime.ReadMemStats(&after)
			assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
			allocated := after.TotalAlloc - before.TotalAlloc
			t.Logf("read allocated %d bytes", allocated)
			if allocated > 1<<20 {
				t.Fatalf("oversized metadata was materialized: %d Go allocation bytes", allocated)
			}
		})
	}
}
