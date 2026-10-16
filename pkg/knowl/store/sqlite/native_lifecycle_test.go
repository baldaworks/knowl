package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	driverSQLite "modernc.org/sqlite"
	lib "modernc.org/sqlite/lib"
)

const lifecycleQuery = "lifecycle"

func assertSQLiteLifecycleRetrieval(t *testing.T, store *Store, scope knowl.ScopeRef, effective knowl.RetrievalMode, reason knowl.RetrievalFailure) {
	t.Helper()
	refs, searchReport, searchErr := store.SearchWithReport(t.Context(), scope, lifecycleQuery, knowl.ReadLimits{Pages: 2}, nil)
	ids, contextReport, _, contextErr := store.SelectContextWithDiagnostics(t.Context(), scope, knowl.SourceSummary{Title: lifecycleQuery}, knowl.ReadLimits{Pages: 2})
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
			t.Fatalf("report=%+v want=%s/%s", result.report, effective, reason)
		}
		if effective != knowl.RetrievalHybrid && result.report.ScannedChunks != 0 {
			t.Fatalf("false full validation=%+v", result.report)
		}
	}
	if effective != knowl.RetrievalFailed && (len(refs) == 0 || len(ids) == 0) {
		t.Fatalf("retained lexical/current evidence absent: refs=%v ids=%v", refs, ids)
	}
}

// This composes populated migration rollback, invalidation and a real rebuild
// with both query APIs; helper-only schema admission is insufficient.
func TestSQLiteNativeLifecycleLegacyUpgrade(t *testing.T) {
	store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{{ID: "legacy-page", Path: "wiki/legacy-page.md", Digest: "legacy-page", Body: "Retained lifecycle evidence"}})
	state, chunks, _, err := store.previousProjection(t.Context(), snapshot)
	if err != nil || state == nil {
		t.Fatalf("prior=%v err=%v", state, err)
	}
	provider := nativeMigrationProvider(t, store.db)
	if _, err := provider.DownTo(t.Context(), 25); err != nil {
		t.Fatal(err)
	}
	insertLegacyNativeFixture(t, store, snapshot.Scope, *state, chunks)
	if _, err := store.Reserve(t.Context(), knowl.OperationKey{Scope: snapshot.Scope, Source: knowl.SourceRef{Adapter: testFixture, ID: "lifecycle-operation"}, Version: knowl.SourceVersion{Version: "1", Digest: "lifecycle-operation"}}, knowl.OperationMeta{SchemaDigest: testSchemaDigest}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO knowl_sources(scope,source_id,source_type,config_digest,created_at,updated_at) VALUES('native-search','lifecycle-source','fixture','config','before','before')`,
		`INSERT INTO knowl_source_documents(scope,source_id,document_id,revision,accepted_source,last_seen_run_id,created_at,updated_at) VALUES('native-search','lifecycle-source','lifecycle-document','1','{"raw":"retained"}','run','before','before')`,
		`CREATE TRIGGER interrupt_lifecycle_upgrade BEFORE DELETE ON knowl_embedding_state BEGIN SELECT RAISE(ABORT,'fixture interruption'); END`,
	} {
		if _, err := store.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	before := nativeAuthoritativeRows(t, store.db)
	_, err = provider.Up(t.Context())
	var classified *driverSQLite.Error
	if !errors.As(err, &classified) || classified.Code()&255 != lib.SQLITE_CONSTRAINT {
		t.Fatalf("migration interruption=%v", err)
	}
	assertNativeMigrationState(t, provider, store.db, 25, 1, len(chunks), 0)
	engine := store.embedding
	store.embedding = nil
	assertSQLiteLifecycleRetrieval(t, store, snapshot.Scope, knowl.RetrievalLexical, "")
	store.embedding = engine
	if _, err := store.db.ExecContext(t.Context(), `DROP TRIGGER interrupt_lifecycle_upgrade`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNativeMigrationState(t, provider, store.db, 26, 0, 0, 1)
	if !reflect.DeepEqual(before, nativeAuthoritativeRows(t, store.db)) {
		t.Fatal("migration changed authoritative page/raw/source/operation rows")
	}
	assertSQLiteLifecycleRetrieval(t, store, snapshot.Scope, knowl.RetrievalFailed, knowl.RetrievalProjectionNotReady)
	store.embedding.FailurePolicy = app.EmbeddingFallbackLexical
	assertSQLiteLifecycleRetrieval(t, store, snapshot.Scope, knowl.RetrievalDegraded, knowl.RetrievalProjectionNotReady)
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	store.embedding.FailurePolicy = app.EmbeddingStrict
	assertSQLiteLifecycleRetrieval(t, store, snapshot.Scope, knowl.RetrievalHybrid, "")
}

// An insertion failure after readiness was written must roll back readiness
// and all chunks, so readers still retrieve the previous complete generation.
func TestSQLiteNativeLifecycleInterruptedPublication(t *testing.T) {
	store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{{ID: "publication-page", Path: "wiki/publication-page.md", Digest: "publication-page", Body: "Retained lifecycle evidence"}})
	state, chunks, _, err := store.previousProjection(t.Context(), snapshot)
	if err != nil || state == nil {
		t.Fatalf("prior=%v err=%v", state, err)
	}
	before := nativeAuthoritativeRows(t, store.db)
	if _, err := store.db.ExecContext(t.Context(), `CREATE TRIGGER interrupt_lifecycle_publication BEFORE INSERT ON knowl_embedding_chunks BEGIN SELECT RAISE(ABORT,'fixture interruption'); END`); err != nil {
		t.Fatal(err)
	}
	chunks[0].Vector = []float32{0, 1}
	err = store.publishEmbeddings(t.Context(), snapshot.Scope, *state, chunks)
	var classified *driverSQLite.Error
	if !errors.As(err, &classified) || classified.Code()&255 != lib.SQLITE_CONSTRAINT {
		t.Fatalf("publication interruption=%v", err)
	}
	assertSQLiteLifecycleRetrieval(t, store, snapshot.Scope, knowl.RetrievalHybrid, "")
	_, retained, _, err := store.previousProjection(t.Context(), snapshot)
	if err != nil || len(retained) != 1 || !slices.Equal(retained[0].Vector, []float32{1, 0}) {
		t.Fatalf("partial publication escaped: chunks=%v err=%v", retained, err)
	}
	if !reflect.DeepEqual(before, nativeAuthoritativeRows(t, store.db)) {
		t.Fatal("failed publication changed authoritative rows")
	}
	if _, err := store.db.ExecContext(t.Context(), `DROP TRIGGER interrupt_lifecycle_publication`); err != nil {
		t.Fatal(err)
	}
	if err := store.publishEmbeddings(t.Context(), snapshot.Scope, *state, chunks); err != nil {
		t.Fatal(err)
	}
	assertSQLiteLifecycleRetrieval(t, store, snapshot.Scope, knowl.RetrievalHybrid, "")
}

type lifecycleProvider struct {
	calls int
	fail  bool
}

func (p *lifecycleProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	p.calls++
	if p.fail {
		return nil, &app.EmbeddingError{Code: knowl.RetrievalUnavailable}
	}
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

// This catches unauthenticated reuse after a hash-shaped mutation and tests
// public failure/degradation through a durable provider-failure/rebuild cycle.
func TestSQLiteNativeLifecycleReuseFailureAndCorruption(t *testing.T) {
	store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{
		{ID: "included-page", Path: "wiki/included-page.md", Digest: "included-page", Body: "Included lifecycle evidence", SourceDocuments: []knowl.SourceDocument{{SourceID: "lifecycle-selected", DocumentID: "lifecycle-member", Revision: "1"}}},
		{ID: "excluded-page", Path: "wiki/excluded-page.md", Digest: "excluded-page", Body: "Excluded lifecycle evidence"},
	})
	p := &lifecycleProvider{}
	store.embedding.Provider = p
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 {
		t.Fatalf("authenticated reuse inferred %d times", p.calls)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_chunks SET content_hash=? WHERE scope=? AND page_id='excluded-page'`, strings.Repeat("f", 64), snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if p.calls != len(snapshot.Pages) {
		t.Fatalf("unauthenticated prior reused; inference calls=%d", p.calls)
	}
	assertSQLiteLifecycleRetrieval(t, store, snapshot.Scope, knowl.RetrievalHybrid, "")
	for _, policy := range []app.EmbeddingFailurePolicy{app.EmbeddingStrict, app.EmbeddingFallbackLexical} {
		store.embedding.FailurePolicy = policy
		setNativeSearchVector(t, store, snapshot.Scope, "excluded-page", []float32{0, 1})
		if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_chunks SET vector=zeroblob(8) WHERE scope=? AND page_id='excluded-page'`, snapshot.Scope); err != nil {
			t.Fatal(err)
		}
		_, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, lifecycleQuery, knowl.ReadLimits{Pages: 1}, []knowl.SourceID{"lifecycle-selected"})
		if policy == app.EmbeddingStrict {
			assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
		} else if err != nil || report.Effective != knowl.RetrievalDegraded {
			t.Fatalf("fallback=%+v err=%v", report, err)
		}
		if report.ScannedChunks != 0 {
			t.Fatalf("partial scan=%+v", report)
		}
		want := knowl.RetrievalFailed
		if policy == app.EmbeddingFallbackLexical {
			want = knowl.RetrievalDegraded
		}
		assertSQLiteLifecycleRetrieval(t, store, snapshot.Scope, want, knowl.RetrievalProjectionDrift)
		if err := store.Rebuild(t.Context(), snapshot); err != nil {
			t.Fatal(err)
		}
	}
	p.fail = true
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	p.fail = false // Query inference works; durable state still records provider failure.
	assertSQLiteLifecycleRetrieval(t, store, snapshot.Scope, knowl.RetrievalDegraded, knowl.RetrievalUnavailable)
	store.embedding.FailurePolicy = app.EmbeddingStrict
	assertSQLiteLifecycleRetrieval(t, store, snapshot.Scope, knowl.RetrievalFailed, knowl.RetrievalUnavailable)
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	assertSQLiteLifecycleRetrieval(t, store, snapshot.Scope, knowl.RetrievalHybrid, "")
}

// Readers on an independent connection observe each completed generation;
// a stale writer cannot overwrite a peer's same-digest replacement.
func TestSQLiteNativeLifecycleConcurrentSameDigest(t *testing.T) {
	path := t.TempDir() + "/lifecycle-peer.sqlite"
	provider := &gatedProjectionProvider{entered: make(chan struct{}), release: make(chan struct{}), vector: []float32{1, 0}}
	options := app.EmbeddingOptions{Provider: provider, FailurePolicy: app.EmbeddingStrict, Space: app.EmbeddingSpace{Model: "lifecycle-peer", Revision: "1", Dimensions: 2}}
	store, err := Open(t.Context(), path, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	options.Provider = &gatedProjectionProvider{vector: []float32{0, 1}}
	peer, err := Open(t.Context(), path, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	snapshot := knowl.WorkspaceSnapshot{Scope: "lifecycle-peer", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "peer-page", Path: "wiki/peer-page.md", Digest: "same-digest", Body: "Original lifecycle evidence"}}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	assertSQLiteLifecycleRetrieval(t, peer, snapshot.Scope, knowl.RetrievalHybrid, "")
	provider.block = true
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- store.Rebuild(ctx, snapshot) }()
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Preparation runs outside write locks and publishes no partial readiness.
	assertSQLiteLifecycleRetrieval(t, peer, snapshot.Scope, knowl.RetrievalFailed, knowl.RetrievalProjectionNotReady)
	if err := peer.Rebuild(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	assertSQLiteLifecycleRetrieval(t, peer, snapshot.Scope, knowl.RetrievalHybrid, "")
	close(provider.release)
	select {
	case err := <-done:
		assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assertSQLiteLifecycleRetrieval(t, peer, snapshot.Scope, knowl.RetrievalHybrid, "")
	_, chunks, _, err := peer.previousProjection(t.Context(), snapshot)
	if err != nil || len(chunks) != 1 || !slices.Equal(chunks[0].Vector, []float32{0, 1}) {
		t.Fatalf("peer's complete projection overwritten: %v err=%v", chunks, err)
	}
	if err := peer.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(fmt.Errorf("peer projection: %w", err))
	}
}
