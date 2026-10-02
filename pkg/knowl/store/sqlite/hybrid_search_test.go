package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type fixedEmbeddingProvider struct{}

func (fixedEmbeddingProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestSQLiteHybridSemanticReference(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/hybrid.sqlite", app.EmbeddingOptions{Provider: fixedEmbeddingProvider{}, Space: app.EmbeddingSpace{Model: "fixture", Revision: "1", Dimensions: 2, QueryPrefix: "query: ", PassagePrefix: "passage: "}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := knowl.WorkspaceSnapshot{Scope: "hybrid", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "semantic", Path: "wiki/semantic.md", Title: "Durable sessions", Body: "Original canonical evidence", Digest: embeddingTestDigest, SourceRefs: []string{"raw:original@1"}}}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, "paraphrasedneedle", knowl.ReadLimits{Pages: 1, Characters: 200}, nil)
	if err != nil || len(refs) != 1 || refs[0].ID != "semantic" || report.Effective != knowl.RetrievalHybrid {
		t.Fatalf("semantic refs=%v report=%+v err=%v", refs, report, err)
	}
	if refs[0].Snippet != "Durable sessions\n\nOriginal canonical evidence" || refs[0].SourceRefs[0] != "raw:original@1" {
		t.Fatalf("semantic evidence changed: %+v", refs[0])
	}
}

func TestSQLiteSharedHybridContract(t *testing.T) {
	searchtest.RunHybrid(t, func(t *testing.T, options ...app.EmbeddingOptions) searchtest.HybridIndex {
		t.Helper()
		store, err := Open(t.Context(), t.TempDir()+"/hybrid.sqlite", options...)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}, func(err error) bool { return errors.Is(err, ErrProjectionDrift) })
}
