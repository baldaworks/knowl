package searchtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type budgetEmbeddings struct{ remaining []time.Duration }

func (provider *budgetEmbeddings) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	deadline, _ := ctx.Deadline()
	provider.remaining = append(provider.remaining, time.Until(deadline))
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

// RunHybridOperationalTimeouts verifies that store-level rebuild and publication
// wrappers do not silently truncate a configured complete-build budget.
func RunHybridOperationalTimeouts(t *testing.T, factory HybridFactory) {
	t.Helper()
	provider := &budgetEmbeddings{}
	index := factory(t, app.EmbeddingOptions{Provider: provider, Space: app.EmbeddingSpace{Model: "operational-budget", Revision: "1", Dimensions: 2}, RebuildTimeout: time.Hour})
	t.Cleanup(func() { _ = index.Close() })
	snapshot := knowl.WorkspaceSnapshot{Scope: "budget_contract", SchemaDigest: strings.Repeat("a", 64), Pages: []knowl.PageSnapshot{{ID: "budget", Path: "wiki/budget.md", Digest: "v1", Title: "Budget", Body: "Initial evidence"}}}
	if err := index.Rebuild(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Pages[0].Body = "Updated evidence"
	snapshot.Pages[0].Digest = "v2"
	projector, ok := index.(interface {
		Project(ctx context.Context, commit knowl.ContentCommit) error
	})
	if !ok {
		t.Fatal("hybrid index does not support projection publication")
	}
	if err := projector.Project(context.Background(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if len(provider.remaining) != 2 {
		t.Fatalf("inference calls=%d", len(provider.remaining))
	}
	for _, remaining := range provider.remaining {
		if remaining < 59*time.Minute || remaining > time.Hour {
			t.Fatalf("store truncated complete-build budget: %v", remaining)
		}
	}
	if err := index.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
}
