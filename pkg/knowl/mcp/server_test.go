package mcp_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestServerExposesKISSToolsAndPinsScope(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(workspace.Root(), "state.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	schema, err := workspace.Schema(ctx, "local")
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	maintainer := &countingMaintainer{plan: knowl.ModelEditPlan{
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{"inline:source-1@1"},
		Edits: []knowl.FileEdit{
			{Path: "wiki/entities/one.md", Content: []byte("---\nid: entities/one\ntitle: One\ntype: entity\nsource_refs:\n  - inline:source-1@1\n---\n# One\n")},
		},
	}}
	ingest, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{AutoApply: false})
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}
	query, err := app.NewQueryService(workspace, store, store, nil, app.QueryOptions{})
	if err != nil {
		t.Fatalf("new query service: %v", err)
	}
	server, err := mcp.NewServer(query, ingest, "local", knowl.ReadLimits{Pages: 1})
	if err != nil {
		t.Fatalf("new MCP server: %v", err)
	}
	tools := server.Tools()
	if len(tools) != 3 {
		t.Fatalf("tool count = %d, want 3", len(tools))
	}
	if tools[0].Name != "knowl_retrieve" || tools[1].Name != "knowl_ingest" || tools[2].Name != "knowl_operation" {
		t.Fatalf("tool names = %#v", tools)
	}
	if tools[0].ReadOnly != true || tools[1].ReadOnly != false || tools[2].ReadOnly != true {
		t.Fatalf("read-only flags = %#v", tools)
	}
	ingested, err := server.Call(ctx, "knowl_ingest", map[string]any{
		"content":         "source",
		"origin":          "source-1",
		"idempotency_key": "1",
	})
	if err != nil {
		t.Fatalf("knowl_ingest: %v", err)
	}
	ingestResult, ok := ingested.(mcp.IngestResult)
	if !ok || ingestResult.Status != "completed" || ingestResult.OperationID == "" {
		t.Fatalf("knowl_ingest result = %#v", ingested)
	}
	value, err := server.Call(ctx, "knowl_retrieve", map[string]any{"query": "One"})
	if err != nil {
		t.Fatalf("knowl_retrieve: %v", err)
	}
	retrieveResult, ok := value.(mcp.RetrieveResult)
	if !ok || len(retrieveResult.Evidence) != 1 || retrieveResult.Evidence[0].PageID != "entities/one" {
		t.Fatalf("knowl_retrieve result = %#v", value)
	}
	if _, err := server.Call(ctx, "knowl_retrieve", map[string]any{"query": "One", "scope": "other"}); !errors.Is(err, mcp.ErrScopeOverride) {
		t.Fatalf("scope override error = %v, want scope override", err)
	}
	polled, err := server.Call(ctx, "knowl_operation", map[string]any{"id": string(ingestResult.OperationID)})
	if err != nil {
		t.Fatalf("knowl_operation: %v", err)
	}
	polledResult, ok := polled.(mcp.OperationResult)
	if !ok || polledResult.Status != "completed" || polledResult.ID != ingestResult.OperationID {
		t.Fatalf("knowl_operation result = %#v", polled)
	}
}

type countingMaintainer struct {
	mu      sync.Mutex
	plan    knowl.ModelEditPlan
	counter int
}

func (maintainer *countingMaintainer) Plan(_ context.Context, _ knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	maintainer.counter++
	return maintainer.plan, nil
}
