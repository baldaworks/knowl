package knowl

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type readinessEmbeddings struct {
	calls int
	fail  bool
}

func (provider *readinessEmbeddings) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	provider.calls++
	if provider.fail {
		return nil, &app.EmbeddingError{Code: domain.RetrievalUnavailable}
	}
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestEnsureProjectionRetriesDegradationOnceAndRepairsOnStartup(t *testing.T) {
	provider := &readinessEmbeddings{fail: true}
	index, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), app.EmbeddingOptions{Provider: provider, Space: app.EmbeddingSpace{Model: "readiness-fixture", Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	snapshot := domain.WorkspaceSnapshot{Scope: "readiness", SchemaDigest: "schema", Pages: []domain.PageSnapshot{{ID: "fact", Path: "wiki/fact.md", Title: "Original evidence", Body: "Factual material", Digest: "fact-1"}}}
	if err := ensureProjection(t.Context(), index, index, snapshot); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("cold startup calls=%d", provider.calls)
	}
	if err := ensureProjection(t.Context(), index, index, snapshot); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 {
		t.Fatalf("degraded startup calls=%d", provider.calls)
	}
	provider.fail = false
	if err := ensureProjection(t.Context(), index, index, snapshot); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 3 {
		t.Fatalf("repair calls=%d", provider.calls)
	}
	if err := ensureProjection(t.Context(), index, index, snapshot); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 3 {
		t.Fatal("ready startup rebuilt vectors")
	}
}

func TestEmbeddingRuntimeTransportDoesNotChangeMaintenanceIdentity(t *testing.T) {
	first := testEmbeddingConfig()
	first.Dimensions = 2
	options, err := embeddingStoreOptions(first)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.Endpoint = "https://other.example.test/v1/embeddings"
	second.APIKeyEnv = "KNOWL_TEST_EMBEDDING_KEY"
	t.Setenv(second.APIKeyEnv, "secret-only-in-provider")
	other, err := embeddingStoreOptions(second)
	if err != nil {
		t.Fatal(err)
	}
	stores := make([]*sqlite.Store, 0, 2)
	for _, configured := range [][]app.EmbeddingOptions{options, other} {
		store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), configured...)
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, store)
		t.Cleanup(func() { _ = store.Close() })
	}
	policy := app.SourceMaintenancePolicy("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", app.DefaultReadLimits(), app.DefaultPlanLimits())
	policy.Retrieval = stores[0].MaintenanceRetrievalPolicy()
	generation, err := app.MaintenancePolicyGeneration(policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.Retrieval = stores[1].MaintenanceRetrievalPolicy()
	otherGeneration, err := app.MaintenancePolicyGeneration(policy)
	if err != nil || generation != otherGeneration {
		t.Fatalf("transport changed generation: %v", err)
	}
	disabled := second
	disabled.Enabled = false
	disabled.APIKeyEnv = "DOES_NOT_EXIST"
	disabled.Endpoint = "malformed runtime endpoint"
	off, err := embeddingStoreOptions(disabled)
	if err != nil || len(off) != 0 {
		t.Fatalf("disabled configuration consulted provider: %v", err)
	}
	if _, err := embeddingStoreOptions(EmbeddingsConfig{Enabled: true}); !errors.Is(err, app.ErrEmbedding) {
		t.Fatalf("invalid enabled config=%v", err)
	}
}
