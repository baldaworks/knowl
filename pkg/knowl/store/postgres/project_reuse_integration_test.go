//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type recordingProjectProvider struct{ inputs []string }

func (provider *recordingProjectProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	provider.inputs = append(provider.inputs, inputs...)
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func projectFixture(t *testing.T, dsn string) (*Store, *recordingProjectProvider, knowl.WorkspaceSnapshot) {
	t.Helper()
	provider := &recordingProjectProvider{}
	store, err := Open(t.Context(), dsn, app.EmbeddingOptions{Provider: provider, Space: app.EmbeddingSpace{Model: "project-fixture", Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := knowl.ScopeRef("project_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	snapshot := knowl.WorkspaceSnapshot{Scope: scope, SchemaDigest: embeddingTestDigest, Pages: []knowl.PageSnapshot{
		{ID: "a", Path: "wiki/a.md", Digest: "a-1", Title: "A", Body: "# Alpha\nFirst section\n\n# Beta\nSecond section", SourceDocuments: []knowl.SourceDocument{{SourceID: "source", DocumentID: "doc", Revision: "1"}}},
		{ID: "b", Path: "wiki/b.md", Digest: "b-1", Title: "B", Body: "# Gamma\nThird section"},
	}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	return store, provider, snapshot
}

func runPostgresProjectReusesPreparedInputs(t *testing.T, dsn string) {
	store, provider, snapshot := projectFixture(t, dsn)
	initial := len(provider.inputs)
	if initial != 3 {
		t.Fatalf("initial prepared inputs=%d, want 3", initial)
	}
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if len(provider.inputs) != initial {
		t.Fatalf("identical Project embedded %d inputs", len(provider.inputs)-initial)
	}
	snapshot.Pages[0].SourceDocuments[0].Revision = "2"
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if len(provider.inputs) != initial {
		t.Fatalf("provenance Project embedded %d inputs", len(provider.inputs)-initial)
	}
	var revision string
	if err := store.db.QueryRowContext(t.Context(), `SELECT revision FROM knowl_page_sources WHERE scope=$1 AND page_id=$2`, snapshot.Scope, "a").Scan(&revision); err != nil || revision != "2" {
		t.Fatalf("projected revision=%q, err=%v", revision, err)
	}
	snapshot.Pages[0].Digest = "a-2"
	snapshot.Pages[0].Body = "# Alpha\nFirst section\n\n# Inserted\nNew section\n\n# Beta\nSecond section"
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if got := len(provider.inputs) - initial; got != 1 {
		t.Fatalf("section insertion embedded %d inputs, want 1", got)
	}
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func runPostgresProjectRepairsCorruptPriorInputs(t *testing.T, dsn string) {
	store, provider, snapshot := projectFixture(t, dsn)
	initial := len(provider.inputs)
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_chunks SET content_hash=$1 WHERE scope=$2`, strings.Repeat("f", 64), snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if got := len(provider.inputs) - initial; got != initial {
		t.Fatalf("corrupt prior hash caused %d inputs, want %d", got, initial)
	}
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func runPostgresProjectRepairsSameCountLexicalDrift(t *testing.T, dsn string) {
	store, provider, snapshot := projectFixture(t, dsn)
	initial := len(provider.inputs)
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_pages SET search_vector=to_tsvector('simple', 'corrupted'), source_documents='[]'::jsonb WHERE scope=$1 AND page_id=$2`, snapshot.Scope, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_page_sources SET revision=$1 WHERE scope=$2 AND page_id=$3`, "corrupted", snapshot.Scope, "a"); err != nil {
		t.Fatal(err)
	}
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if len(provider.inputs) != initial {
		t.Fatalf("lexical repair embedded %d unchanged inputs", len(provider.inputs)-initial)
	}
	var revision string
	if err := store.db.QueryRowContext(t.Context(), `SELECT revision FROM knowl_page_sources WHERE scope=$1 AND page_id=$2`, snapshot.Scope, "a").Scan(&revision); err != nil || revision != "1" {
		t.Fatalf("repaired revision=%q, err=%v", revision, err)
	}
	refs, err := store.Search(t.Context(), snapshot.Scope, "First", knowl.ReadLimits{Pages: 5, Characters: 500}, nil)
	if err != nil || len(refs) == 0 || refs[0].ID != "a" {
		t.Fatalf("repaired search refs=%v, err=%v", refs, err)
	}
}

func runPostgresProjectModelChangeReembeds(t *testing.T, dsn string) {
	oldStore, oldProvider, snapshot := projectFixture(t, dsn)
	initial := len(oldProvider.inputs)
	if err := oldStore.Close(); err != nil {
		t.Fatal(err)
	}
	newProvider := &recordingProjectProvider{}
	newStore, err := Open(t.Context(), dsn, app.EmbeddingOptions{Provider: newProvider, Space: app.EmbeddingSpace{Model: "project-fixture", Revision: "2", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = newStore.Close() })
	if err := newStore.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if len(newProvider.inputs) != initial {
		t.Fatalf("new model embedded %d inputs, want %d", len(newProvider.inputs), initial)
	}
	if err := newStore.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
}

type failingProjectProvider struct{}

func (failingProjectProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, &app.EmbeddingError{Code: knowl.RetrievalUnavailable}
}

func runPostgresProjectProviderFailure(t *testing.T, dsn string) {
	store, _, snapshot := projectFixture(t, dsn)
	store.embedding.Provider = failingProjectProvider{}
	store.embedding.FailurePolicy = app.EmbeddingStrict
	snapshot.Pages[0].Digest = "a-2"
	snapshot.Pages[0].Body = "# Alpha\nChanged section\n\n# Beta\nSecond section"
	err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot})
	assertEmbeddingFailure(t, err, knowl.RetrievalUnavailable)
	assertEmbeddingFailure(t, store.CheckProjection(t.Context(), snapshot), knowl.RetrievalUnavailable)
	var count int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM knowl_embedding_chunks WHERE scope=$1`, snapshot.Scope).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed Project left %d dense chunks", count)
	}
}
