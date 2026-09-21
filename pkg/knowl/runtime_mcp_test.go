package knowl_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/mcpsdk"
	knowl "github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSharedSDKAdapterMatchesRegistryTools(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	host, err := knowl.NewHost(ctx, config, provider.Fixture{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownHost(t, host) })
	server, err := mcpsdk.NewServer(host.MCP())
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(ctx, serverTransport) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: hostMCPClientName, Version: hostMCPClientVersion}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := host.MCP().Tools()
	if len(listed.Tools) != len(want) {
		t.Fatalf("SDK tool count = %d, want %d", len(listed.Tools), len(want))
	}
	listedByName := make(map[string]*sdkmcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		listedByName[tool.Name] = tool
	}
	for _, definition := range want {
		got := listedByName[definition.Name]
		if got == nil || got.Description != definition.Description || got.Annotations == nil || got.Annotations.ReadOnlyHint != definition.ReadOnly {
			t.Fatalf("SDK tool %q = %#v, want %#v", definition.Name, got, definition)
		}
	}
	failure, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: hostRetrieveToolName, Arguments: map[string]any{"scope": "other", hostQueryKey: hostQuery}})
	if err != nil {
		t.Fatal(err)
	}
	if !failure.IsError || failure.StructuredContent.(map[string]any)["error"] != "scope_override_forbidden" {
		t.Fatalf("SDK classified failure = %#v", failure)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-serverDone; err != nil && err != context.Canceled {
		t.Fatalf("SDK server exit: %v", err)
	}
}

func TestHostServesMCPContract(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	schema, err := workspace.Schema(ctx, "local")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.ListenAddr = hostListenAddr
	config.IngestOptions.AutoApply = true
	maintainer := provider.Fixture{Result: domain.ModelEditPlan{
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{hostSourceRef},
		Edits:        []domain.FileEdit{{Path: hostPagePath, Content: []byte(hostPageContent)}},
	}}
	host, err := knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("compose host: %v", err)
	}
	if err := host.Start(ctx); err != nil {
		t.Fatalf("start host: %v", err)
	}
	defer shutdownHost(t, host)

	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: hostMCPClientName, Version: hostMCPClientVersion},
		nil,
	)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint:             "http://" + host.Addr() + "/mcp",
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("close MCP session: %v", err)
		}
	}()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list MCP tools: %v", err)
	}
	wantNames := map[string]bool{
		hostRetrieveToolName:  false,
		hostIngestToolName:    false,
		hostOperationToolName: false,
	}
	if len(listed.Tools) != len(wantNames) {
		t.Fatalf("tool count = %d, want %d", len(listed.Tools), len(wantNames))
	}
	for _, tool := range listed.Tools {
		if _, exists := wantNames[tool.Name]; !exists {
			t.Fatalf("unexpected tool name %q", tool.Name)
		}
		wantNames[tool.Name] = true
	}
	for name, found := range wantNames {
		if !found {
			t.Fatalf("tool %q missing", name)
		}
	}

	ingested, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: hostIngestToolName,
		Arguments: map[string]any{
			hostSourceContentKey:         hostSourceContent,
			hostSourceOriginKey:          hostSourceOrigin,
			hostSourceIdempotencyKeyName: hostSourceIdempotencyKey,
		},
	})
	if err != nil {
		t.Fatalf("call knowl_ingest: %v", err)
	}
	if ingested.IsError {
		t.Fatalf("knowl_ingest returned tool error: %#v", ingested.Content)
	}
	ingestResult, ok := ingested.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("knowl_ingest structured result type = %T", ingested.StructuredContent)
	}
	operationID, _ := ingestResult["operation_id"].(string)
	if operationID == "" {
		t.Fatalf("knowl_ingest operation ID missing: %#v", ingestResult)
	}
	waitForMCPHostOperation(t, session, operationID)

	operation, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      hostOperationToolName,
		Arguments: map[string]any{"id": operationID},
	})
	if err != nil || operation.IsError {
		t.Fatalf("call knowl_operation = (%v, %#v)", err, operation)
	}
	retrieved, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      hostRetrieveToolName,
		Arguments: map[string]any{hostQueryKey: hostQuery},
	})
	if err != nil || retrieved.IsError {
		t.Fatalf("call knowl_retrieve = (%v, %#v)", err, retrieved)
	}
	forbidden, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      hostRetrieveToolName,
		Arguments: map[string]any{hostQueryKey: hostQuery, "scope": "other"},
	})
	if err != nil {
		t.Fatalf("call scoped knowl_retrieve: %v", err)
	}
	if !forbidden.IsError {
		t.Fatal("scoped knowl_retrieve succeeded, want tool error")
	}
}

func TestStreamableMCPIngestRunsInBackground(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	schema, err := workspace.Schema(ctx, "local")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	release := make(chan struct{})
	maintainer := &blockingMaintainer{
		started: make(chan struct{}),
		release: release,
		plan: domain.ModelEditPlan{
			SchemaDigest: schema.Digest,
			SourceRefs:   []string{hostSourceRef},
			Edits:        []domain.FileEdit{{Path: hostPagePath, Content: []byte(hostPageContent)}},
		},
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.ListenAddr = hostListenAddr
	config.IngestOptions.AutoApply = true
	host, err := knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("compose host: %v", err)
	}
	if err := host.Start(ctx); err != nil {
		t.Fatalf("start host: %v", err)
	}
	defer shutdownHost(t, host)

	session := connectMCPClient(t, ctx, host)
	defer closeMCPClient(t, session)

	response, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: hostIngestToolName,
		Arguments: map[string]any{
			hostSourceContentKey:         hostSourceContent,
			hostSourceOriginKey:          hostSourceOrigin,
			hostSourceIdempotencyKeyName: hostSourceIdempotencyKey,
		},
	})
	if err != nil || response.IsError {
		t.Fatalf("call knowl_ingest = (%v, %#v)", err, response)
	}
	result, ok := response.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("ingest result type = %T", response.StructuredContent)
	}
	if result["status"] != hostQueuedStatus {
		t.Fatalf("ingest result = %#v, want queued", result)
	}
	operationID, _ := result["operation_id"].(string)
	if operationID == "" {
		t.Fatalf("ingest operation ID missing: %#v", result)
	}
	select {
	case <-maintainer.started:
	case <-time.After(time.Second):
		t.Fatal("background MCP ingest did not start planning")
	}
	close(release)
	waitForMCPHostOperation(t, session, operationID)
}

func waitForMCPHostOperation(t *testing.T, session *sdkmcp.ClientSession, operationID string) {
	t.Helper()
	operation := waitForMCPHostOperationStatus(t, session, operationID)
	if operation["status"] == hostFailedStatus {
		t.Fatalf("operation %q failed", operationID)
	}
}

func waitForMCPHostOperationStatus(t *testing.T, session *sdkmcp.ClientSession, operationID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
			Name:      hostOperationToolName,
			Arguments: map[string]any{"id": operationID},
		})
		if err != nil || operation.IsError {
			t.Fatalf("call knowl_operation = (%v, %#v)", err, operation)
		}
		result, ok := operation.StructuredContent.(map[string]any)
		if !ok {
			t.Fatalf("operation result type = %T", operation.StructuredContent)
		}
		switch result["status"] {
		case "completed":
			return result
		case hostFailedStatus:
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("operation %q did not complete", operationID)
	return nil
}

func connectMCPClient(t *testing.T, ctx context.Context, host *knowl.Host) *sdkmcp.ClientSession {
	t.Helper()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: hostMCPClientName, Version: hostMCPClientVersion}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint:             "http://" + host.Addr() + "/mcp",
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	return session
}

func closeMCPClient(t *testing.T, session *sdkmcp.ClientSession) {
	t.Helper()
	if err := session.Close(); err != nil {
		t.Errorf("close MCP session: %v", err)
	}
}
