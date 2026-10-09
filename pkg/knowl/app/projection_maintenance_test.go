package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type maintenanceProjectionProvider struct{ passages int }

const maintenanceSourceOne = "source-1"

func (provider *maintenanceProjectionProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	vectors := make([][]float32, len(inputs))
	for i, input := range inputs {
		if strings.HasPrefix(input, "passage: ") {
			provider.passages++
		}
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestTwoCanonicalMaintenanceCommitsEmbedOnlyNewPages(t *testing.T) {
	ctx := t.Context()
	workspace, operations, _, _ := newWorkflow(t, false, nil)
	provider := &maintenanceProjectionProvider{}
	index, err := sqlite.Open(ctx, operations.Path(), app.EmbeddingOptions{Provider: provider, Space: app.EmbeddingSpace{Model: "maintenance-projection", Revision: "1", Dimensions: 2, PassagePrefix: "passage: ", QueryPrefix: "query: "}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	snapshot, err := workspace.Snapshot(ctx, testSourceScope)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	service, err := app.NewIngestService(workspace, operations, index, applyFactMaintainer{}, app.IngestOptions{AutoApply: true})
	if err != nil {
		t.Fatal(err)
	}
	for ordinal, sourceID := range []string{maintenanceSourceOne, "source-2"} {
		result, err := service.Ingest(ctx, applyEnvelope(sourceID))
		if err != nil || result.Operation.Status != knowl.StatusCommitted {
			t.Fatalf("maintenance %s result=%+v err=%v", sourceID, result, err)
		}
		if provider.passages != ordinal+1 {
			t.Fatalf("maintenance %s passage inputs=%d, want %d", sourceID, provider.passages, ordinal+1)
		}
	}
	current, err := workspace.Snapshot(ctx, testSourceScope)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.CheckProjection(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := index.Project(ctx, knowl.ContentCommit{Snapshot: current}); err != nil {
		t.Fatal(err)
	}
	if provider.passages != 2 {
		t.Fatalf("unchanged canonical projection passage inputs=%d", provider.passages)
	}
	refs, report, err := index.SearchWithReport(ctx, testSourceScope, "Recorded", knowl.ReadLimits{Pages: 5, Characters: 200}, nil)
	if err != nil || report.Effective != knowl.RetrievalHybrid || len(refs) != 2 {
		t.Fatalf("canonical search refs=%+v report=%+v err=%v", refs, report, err)
	}
}
