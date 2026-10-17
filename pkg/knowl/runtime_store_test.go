package knowl

import (
	"context"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestOpenStoreExposesSourceStateWithoutConfiguredSources(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.StorePath = config.Workspace + "/state.sqlite"
	config.Sources = nil

	store, err := openStore(context.Background(), config)
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.closer.Close() })
	if store.operations == nil || store.index == nil || store.sources == nil || store.lifecycle == nil {
		t.Fatalf("openStore() ports = operations:%v index:%v sources:%v lifecycle:%v", store.operations != nil, store.index != nil, store.sources != nil, store.lifecycle != nil)
	}
}

type startupTrustProvider struct{ calls int }

func (p *startupTrustProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	p.calls++
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestEnsureProjectionTrustsReadyAndRepairsDegraded(t *testing.T) {
	p := &startupTrustProvider{}
	store, err := sqlite.Open(t.Context(), t.TempDir()+"/startup.sqlite", app.EmbeddingOptions{Provider: p, Space: app.EmbeddingSpace{Model: "startup-trust-model", Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	snapshot := domain.WorkspaceSnapshot{Scope: "startup-trust", SchemaDigest: "startup-trust-schema", Pages: []domain.PageSnapshot{{ID: "a", Path: "a.md", Digest: "a", Body: "Original startup evidence"}}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	before := p.calls
	if err := ensureProjection(t.Context(), store, store, snapshot); err != nil {
		t.Fatal(err)
	}
	if p.calls != before {
		t.Fatalf("ready startup rebuilt: calls=%d before=%d", p.calls, before)
	}
	if err := store.ProjectWithoutInference(t.Context(), domain.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := ensureProjection(t.Context(), store, store, snapshot); err != nil {
		t.Fatal(err)
	}
	if p.calls != before+1 {
		t.Fatalf("startup repair calls=%d want=%d", p.calls, before+1)
	}
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
}
