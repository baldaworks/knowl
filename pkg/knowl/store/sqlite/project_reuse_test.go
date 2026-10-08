package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type recordingProjectionProvider struct{ inputs []string }

const (
	projectDigestA1  = "a-1"
	projectPathA     = "wiki/a.md"
	projectBodyAlpha = "# Alpha\nFirst section"
)

func (provider *recordingProjectionProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	provider.inputs = append(provider.inputs, inputs...)
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestSQLiteProjectReusesUnchangedInputs(t *testing.T) {
	provider := &recordingProjectionProvider{}
	store, err := Open(t.Context(), t.TempDir()+"/project.sqlite", app.EmbeddingOptions{Provider: provider, Space: app.EmbeddingSpace{Model: testFixture, Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := knowl.WorkspaceSnapshot{Scope: "project", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{
		{ID: "a", Path: projectPathA, Digest: projectDigestA1, Title: "A", Body: projectBodyAlpha},
		{ID: "b", Path: "wiki/b.md", Digest: "b-1", Title: "B", Body: "# Beta\nSecond section"},
	}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	initial := len(provider.inputs)
	if initial == 0 {
		t.Fatal("Rebuild did not embed")
	}
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if len(provider.inputs) != initial {
		t.Fatalf("identical Project embedded %d inputs", len(provider.inputs)-initial)
	}
	updated := snapshot
	updated.Pages = append([]knowl.PageSnapshot(nil), snapshot.Pages...)
	updated.Pages[0].SourceDocuments = []knowl.SourceDocument{{SourceID: "source", DocumentID: "doc", Revision: "2"}}
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: updated}); err != nil {
		t.Fatal(err)
	}
	if len(provider.inputs) != initial {
		t.Fatalf("provenance Project embedded %d inputs", len(provider.inputs)-initial)
	}
	if err := store.CheckProjection(t.Context(), updated); err != nil {
		t.Fatal(err)
	}
	updated.Pages[0].Digest = "a-2"
	updated.Pages[0].Body = "# Alpha\nChanged first section"
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: updated}); err != nil {
		t.Fatal(err)
	}
	if got := len(provider.inputs) - initial; got != 1 {
		t.Fatalf("changed Project embedded %d inputs, want 1", got)
	}
	if err := store.CheckProjection(t.Context(), updated); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteProjectRepairsCorruptPriorInput(t *testing.T) {
	provider := &recordingProjectionProvider{}
	store, err := Open(t.Context(), t.TempDir()+"/corrupt.sqlite", app.EmbeddingOptions{Provider: provider, Space: app.EmbeddingSpace{Model: testFixture, Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := knowl.WorkspaceSnapshot{Scope: "corrupt", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "a", Path: projectPathA, Digest: projectDigestA1, Title: "A", Body: projectBodyAlpha}}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	initial := len(provider.inputs)
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_chunks SET content_hash=? WHERE scope=?`, strings.Repeat("f", 64), snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if got := len(provider.inputs) - initial; got != initial {
		t.Fatalf("repair embedded %d inputs, want %d", got, initial)
	}
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteProjectReusesLaterSectionAfterInsertion(t *testing.T) {
	provider := &recordingProjectionProvider{}
	store, err := Open(t.Context(), t.TempDir()+"/sections.sqlite", app.EmbeddingOptions{Provider: provider, Space: app.EmbeddingSpace{Model: testFixture, Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := knowl.WorkspaceSnapshot{Scope: "sections", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "a", Path: projectPathA, Digest: projectDigestA1, Title: "A", Body: "# Alpha\nFirst section\n\n# Beta\nSecond section"}}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	initial := len(provider.inputs)
	if initial != 2 {
		t.Fatalf("initial section inputs=%d, want 2", initial)
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

func TestSQLiteProjectRepairsMissingLexicalRows(t *testing.T) {
	provider := &recordingProjectionProvider{}
	store, err := Open(t.Context(), t.TempDir()+"/lexical.sqlite", app.EmbeddingOptions{Provider: provider, Space: app.EmbeddingSpace{Model: testFixture, Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := knowl.WorkspaceSnapshot{Scope: "lexical", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "a", Path: projectPathA, Digest: projectDigestA1, Title: "A", Body: projectBodyAlpha}}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	initial := len(provider.inputs)
	if _, err := store.db.ExecContext(t.Context(), `DELETE FROM knowl_pages_fts WHERE scope=?`, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM knowl_pages_fts WHERE scope=?`, snapshot.Scope).Scan(&count); err != nil || count != 1 {
		t.Fatalf("lexical rows=%d, error=%v", count, err)
	}
	if got := len(provider.inputs) - initial; got != initial {
		t.Fatalf("invalid prior projection embedded %d inputs, want %d", got, initial)
	}
}

func TestSQLiteProjectModelChangeReembeds(t *testing.T) {
	path := t.TempDir() + "/model.sqlite"
	oldProvider := &recordingProjectionProvider{}
	oldStore, err := Open(t.Context(), path, app.EmbeddingOptions{Provider: oldProvider, Space: app.EmbeddingSpace{Model: testFixture, Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := knowl.WorkspaceSnapshot{Scope: "model", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "a", Path: projectPathA, Digest: projectDigestA1, Title: "A", Body: projectBodyAlpha}}}
	if err := oldStore.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := oldStore.Close(); err != nil {
		t.Fatal(err)
	}
	newProvider := &recordingProjectionProvider{}
	newStore, err := Open(t.Context(), path, app.EmbeddingOptions{Provider: newProvider, Space: app.EmbeddingSpace{Model: testFixture, Revision: "2", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = newStore.Close() })
	if err := newStore.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if len(newProvider.inputs) != len(oldProvider.inputs) {
		t.Fatalf("new model embedded %d inputs, want %d", len(newProvider.inputs), len(oldProvider.inputs))
	}
	if err := newStore.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
}
