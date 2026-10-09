package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type gatedProjectionProvider struct {
	entered chan struct{}
	release chan struct{}
	block   bool
	vector  []float32
}

func (provider *gatedProjectionProvider) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if provider.block {
		close(provider.entered)
		select {
		case <-provider.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = append([]float32(nil), provider.vector...)
	}
	return vectors, nil
}

type recordingProjectionProvider struct{ inputs []string }

const (
	projectDigestA1    = "a-1"
	projectDigestA2    = "a-2"
	projectPathA       = "wiki/a.md"
	projectBodyAlpha   = "# Alpha\nFirst section"
	projectBodyUpdated = "# Alpha\nUpdated section"
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
	updated.Pages[0].Digest = projectDigestA2
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
	snapshot.Pages[0].Digest = projectDigestA2
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

func TestSQLiteProjectRepairsSameCountLexicalAndProvenanceDrift(t *testing.T) {
	provider := &recordingProjectionProvider{}
	store, err := Open(t.Context(), t.TempDir()+"/drift.sqlite", app.EmbeddingOptions{Provider: provider, Space: app.EmbeddingSpace{Model: testFixture, Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := knowl.WorkspaceSnapshot{Scope: "drift", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "a", Path: projectPathA, Digest: projectDigestA1, Title: "A", Body: projectBodyAlpha, SourceDocuments: []knowl.SourceDocument{{SourceID: "source", DocumentID: "doc", Revision: "1"}}}}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	initial := len(provider.inputs)
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_pages_fts SET body=? WHERE scope=?`, "corrupt body", snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_page_sources SET revision=? WHERE scope=?`, "corrupt", snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	var body, revision string
	if err := store.db.QueryRowContext(t.Context(), `SELECT body FROM knowl_pages_fts WHERE scope=?`, snapshot.Scope).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(), `SELECT revision FROM knowl_page_sources WHERE scope=?`, snapshot.Scope).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if body == "corrupt body" || revision != "1" {
		t.Fatalf("projection drift remained: body=%q revision=%q", body, revision)
	}
	if len(provider.inputs) != initial {
		t.Fatalf("repair embedded %d unchanged inputs", len(provider.inputs)-initial)
	}
}

func TestSQLiteProjectRejectsPeerSameDigestReplacement(t *testing.T) {
	path := t.TempDir() + "/peer.sqlite"
	provider := &gatedProjectionProvider{entered: make(chan struct{}), release: make(chan struct{}), vector: []float32{1, 0}}
	space := app.EmbeddingSpace{Model: testFixture, Revision: "1", Dimensions: 2}
	store, err := Open(t.Context(), path, app.EmbeddingOptions{Provider: provider, Space: space})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	peerProvider := &gatedProjectionProvider{vector: []float32{0, 1}}
	peer, err := Open(t.Context(), path, app.EmbeddingOptions{Provider: peerProvider, Space: space})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	snapshot := knowl.WorkspaceSnapshot{Scope: "peer", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "a", Path: projectPathA, Digest: projectDigestA1, Title: "A", Body: projectBodyAlpha}}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Pages[0].Digest = projectDigestA2
	snapshot.Pages[0].Body = projectBodyUpdated
	provider.block = true
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- store.Project(ctx, knowl.ContentCommit{Snapshot: snapshot}) }()
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := peer.Rebuild(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	assertEmbeddingFailure(t, <-done, knowl.RetrievalProjectionDrift)
	if err := peer.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	state, chunks, err := readEmbeddingFixture(t.Context(), peer, snapshot.Scope, hybrid.ProjectionState{Space: peer.embedding.Fingerprint, Dimensions: space.Dimensions})
	if err != nil || state.Mode != knowl.RetrievalHybrid || len(chunks) != 1 || chunks[0].Vector[0] != 0 || chunks[0].Vector[1] != 1 {
		t.Fatalf("peer vectors changed: state=%+v chunks=%v err=%v", state, chunks, err)
	}
}

func TestSQLiteProjectIgnoresUnrelatedPeerWrites(t *testing.T) {
	for _, kind := range []string{"other scope", "operation"} {
		t.Run(kind, func(t *testing.T) {
			path := t.TempDir() + "/unrelated.sqlite"
			provider := &gatedProjectionProvider{entered: make(chan struct{}), release: make(chan struct{}), vector: []float32{1, 0}}
			space := app.EmbeddingSpace{Model: testFixture, Revision: "1", Dimensions: 2}
			store, err := Open(t.Context(), path, app.EmbeddingOptions{Provider: provider, Space: space})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			peer, err := Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = peer.Close() })
			snapshot := knowl.WorkspaceSnapshot{Scope: "active", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "a", Path: projectPathA, Digest: projectDigestA1, Title: "A", Body: projectBodyAlpha}}}
			if err := store.Rebuild(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			snapshot.Pages[0].Digest = projectDigestA2
			snapshot.Pages[0].Body = projectBodyUpdated
			provider.block = true
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- store.Project(ctx, knowl.ContentCommit{Snapshot: snapshot}) }()
			select {
			case <-provider.entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			switch kind {
			case "other scope":
				foreign := snapshot
				foreign.Scope = "foreign"
				if err := peer.Rebuild(ctx, foreign); err != nil {
					t.Fatal(err)
				}
			case "operation":
				key, meta := executionFixture("foreign", "unrelated", time.Unix(1, 0).UTC())
				if _, err := peer.Reserve(ctx, key, meta); err != nil {
					t.Fatal(err)
				}
			}
			close(provider.release)
			if err := <-done; err != nil {
				t.Fatalf("unrelated peer write rejected Project: %v", err)
			}
			if err := store.CheckProjection(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSQLiteProjectRejectsPeerReplacementBeforeLexicalWrite(t *testing.T) {
	path := t.TempDir() + "/prior.sqlite"
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
	snapshot := knowl.WorkspaceSnapshot{Scope: "prior", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "a", Path: projectPathA, Digest: projectDigestA1, Title: "A", Body: projectBodyAlpha}}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	observed, err := readProjectionIdentity(t.Context(), store.db, snapshot.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	var nonce int64
	assertEmbeddingFailure(t, store.rebuildLexicalChecked(t.Context(), snapshot, &observed, &nonce), knowl.RetrievalProjectionDrift)
	if err := peer.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteProjectRejectsPeerFirstProjection(t *testing.T) {
	path := t.TempDir() + "/first.sqlite"
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
	snapshot := knowl.WorkspaceSnapshot{Scope: "first", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "a", Path: projectPathA, Digest: projectDigestA1, Title: "A", Body: projectBodyAlpha}}}
	observed, err := readProjectionIdentity(t.Context(), store.db, snapshot.Scope)
	if err != nil || observed.exists {
		t.Fatalf("first projection identity=%+v err=%v", observed, err)
	}
	if err := peer.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	var nonce int64
	assertEmbeddingFailure(t, store.rebuildLexicalChecked(t.Context(), snapshot, &observed, &nonce), knowl.RetrievalProjectionDrift)
	if err := peer.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteProjectCancellationDuringInference(t *testing.T) {
	path := t.TempDir() + "/cancel.sqlite"
	provider := &gatedProjectionProvider{entered: make(chan struct{}), release: make(chan struct{}), vector: []float32{1, 0}}
	store, err := Open(t.Context(), path, app.EmbeddingOptions{Provider: provider, Space: app.EmbeddingSpace{Model: testFixture, Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := knowl.WorkspaceSnapshot{Scope: "cancel", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "a", Path: projectPathA, Digest: projectDigestA1, Title: "A", Body: projectBodyAlpha}}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Pages[0].Digest = projectDigestA2
	snapshot.Pages[0].Body = projectBodyUpdated
	provider.block = true
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- store.Project(ctx, knowl.ContentCommit{Snapshot: snapshot}) }()
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Project did not reach inference")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Project error=%v", err)
	}
	provider.block = false
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatalf("repair after cancellation: %v", err)
	}
}
