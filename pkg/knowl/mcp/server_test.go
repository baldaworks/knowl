package mcp_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testContentKey        = "content"
	testOriginKey         = "origin"
	testIdempotencyKey    = "idempotency_key"
	testIngestContent     = "source"
	testIngestOrigin      = "source-1"
	testIngestIdempotency = "1"
	testRetrieveQuery     = "One"
	testEngineeringSource = "engineering"
	testTransportQuery    = "transportbeacon"
	testQueryArgument     = "query"
	testSourcesArgument   = "sources"
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
	waker := &recordingWaker{}
	server, err := mcp.NewServer(query, ingest, waker, "local", knowl.ReadLimits{Pages: 1})
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
	properties, ok := tools[0].InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("retrieve properties = %#v", tools[0].InputSchema["properties"])
	}
	sourcesSchema, ok := properties["sources"].(map[string]any)
	if !ok || sourcesSchema["type"] != "array" {
		t.Fatalf("retrieve sources schema = %#v", properties["sources"])
	}
	ingested, err := server.Call(ctx, "knowl_ingest", map[string]any{
		testContentKey:     testIngestContent,
		testOriginKey:      testIngestOrigin,
		testIdempotencyKey: testIngestIdempotency,
	})
	if err != nil {
		t.Fatalf("knowl_ingest: %v", err)
	}
	ingestResult, ok := ingested.(mcp.IngestResult)
	if !ok || ingestResult.Status != "queued" || ingestResult.OperationID == "" {
		t.Fatalf("knowl_ingest result = %#v", ingested)
	}
	replayed, err := server.Call(ctx, "knowl_ingest", map[string]any{
		testContentKey:     testIngestContent,
		testOriginKey:      testIngestOrigin,
		testIdempotencyKey: testIngestIdempotency,
	})
	if err != nil {
		t.Fatalf("replay knowl_ingest: %v", err)
	}
	replayResult, ok := replayed.(mcp.IngestResult)
	if !ok || replayResult.OperationID != ingestResult.OperationID || replayResult.Status != "queued" {
		t.Fatalf("replayed ingest = %#v, want same queued operation", replayed)
	}
	if got := waker.IDs(); len(got) != 2 || got[0] != ingestResult.OperationID || got[1] != ingestResult.OperationID {
		t.Fatalf("wake IDs = %#v, want new and non-terminal replay", got)
	}
	if maintainer.calls() != 0 {
		t.Fatalf("transport invoked maintainer %d times", maintainer.calls())
	}
	claim, err := store.ClaimReady(ctx, "local", knowl.WorkLease{Token: "test-worker", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("claim submitted operation: %v", err)
	}
	if result, err := ingest.RunToTerminal(ctx, claim); err != nil || result.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("run submitted operation = %#v, err = %v", result, err)
	}
	value, err := server.Call(ctx, "knowl_retrieve", map[string]any{testQueryArgument: testRetrieveQuery})
	if err != nil {
		t.Fatalf("knowl_retrieve: %v", err)
	}
	retrieveResult, ok := value.(mcp.RetrieveResult)
	if !ok || len(retrieveResult.Evidence) != 1 || retrieveResult.Evidence[0].PageID != "entities/one" {
		t.Fatalf("knowl_retrieve result = %#v", value)
	}
	if _, err := server.Call(ctx, "knowl_retrieve", map[string]any{testQueryArgument: testRetrieveQuery, testSourcesArgument: []any{testEngineeringSource, 1}}); !errors.Is(err, mcp.ErrInvalidArguments) {
		t.Fatalf("structural sources error = %v, want invalid arguments", err)
	}
	if err := store.Rebuild(ctx, transportSearchSnapshot()); err != nil {
		t.Fatalf("rebuild transport search fixture: %v", err)
	}
	filtered, err := server.Call(ctx, "knowl_retrieve", map[string]any{testQueryArgument: testTransportQuery, testSourcesArgument: []any{testEngineeringSource}})
	if err != nil {
		t.Fatalf("filtered knowl_retrieve: %v", err)
	}
	filteredResult, ok := filtered.(mcp.RetrieveResult)
	if !ok || len(filteredResult.Evidence) != 1 || filteredResult.Evidence[0].SourceID != testEngineeringSource || filteredResult.Evidence[0].DocumentID != "shared.md" || filteredResult.Evidence[0].Revision != "revision-1" || filteredResult.Evidence[0].URI == "" || !filteredResult.Evidence[0].Untrusted {
		t.Fatalf("filtered knowl_retrieve result = %#v", filtered)
	}
	unknown, err := server.Call(ctx, "knowl_retrieve", map[string]any{testQueryArgument: testTransportQuery, testSourcesArgument: []string{"ghost"}})
	if err != nil {
		t.Fatalf("unknown-source knowl_retrieve: %v", err)
	}
	if result := unknown.(mcp.RetrieveResult); len(result.Evidence) != 0 {
		t.Fatalf("unknown-source evidence = %#v", result.Evidence)
	}
	if _, err := server.Call(ctx, "knowl_retrieve", map[string]any{testQueryArgument: testRetrieveQuery, "scope": "other"}); !errors.Is(err, mcp.ErrScopeOverride) {
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
	terminalReplay, err := server.Call(ctx, "knowl_ingest", map[string]any{
		testContentKey:     testIngestContent,
		testOriginKey:      testIngestOrigin,
		testIdempotencyKey: testIngestIdempotency,
	})
	if err != nil {
		t.Fatalf("terminal replay knowl_ingest: %v", err)
	}
	if result, ok := terminalReplay.(mcp.IngestResult); !ok || result.Status != "completed" {
		t.Fatalf("terminal replay = %#v, want completed", terminalReplay)
	}
	if got := waker.IDs(); len(got) != 2 {
		t.Fatalf("terminal replay emitted wake: %#v", got)
	}
}

func transportSearchSnapshot() knowl.WorkspaceSnapshot {
	document := func(sourceID knowl.SourceID) *knowl.SourceDocument {
		return &knowl.SourceDocument{SourceID: sourceID, DocumentID: "shared.md", Revision: "revision-1", URI: "file:///" + string(sourceID) + "/shared.md"}
	}
	return knowl.WorkspaceSnapshot{Scope: "local", Pages: []knowl.PageSnapshot{
		{ID: "curated", Path: "wiki/curated.md", Title: "Transportbeacon Curated", Content: testTransportQuery, Digest: "curated"},
		{ID: testEngineeringSource, Path: "wiki/sources/engineering/shared.md", Title: "Transportbeacon Engineering", Content: testTransportQuery, Digest: testEngineeringSource, SourceDocument: document(testEngineeringSource)},
		{ID: "operations", Path: "wiki/sources/operations/shared.md", Title: "Transportbeacon Operations", Content: testTransportQuery, Digest: "operations", SourceDocument: document("operations")},
	}}
}

type recordingWaker struct {
	mu  sync.Mutex
	ids []knowl.OperationID
}

func (waker *recordingWaker) Wake(id knowl.OperationID) {
	waker.mu.Lock()
	defer waker.mu.Unlock()
	waker.ids = append(waker.ids, id)
}

func (waker *recordingWaker) IDs() []knowl.OperationID {
	waker.mu.Lock()
	defer waker.mu.Unlock()
	return append([]knowl.OperationID(nil), waker.ids...)
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

func (maintainer *countingMaintainer) calls() int {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	return maintainer.counter
}
