package knowl_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHostServesAuthenticatedMCPContract(t *testing.T) {
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
	config.ListenAddr = "127.0.0.1:0"
	config.OperatorToken = "test-token"
	config.IngestOptions.AutoApply = true
	maintainer := provider.Fixture{Result: domain.ModelEditPlan{
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{hostSourceRef},
		Edits:        []domain.FileEdit{{Path: "wiki/entities/one.md", Content: []byte(hostPageContent)}},
	}}
	host, err := knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("compose host: %v", err)
	}
	if err := host.Start(ctx); err != nil {
		t.Fatalf("start host: %v", err)
	}
	defer shutdownHost(t, host)

	unauthorized, err := http.Get("http://" + host.Addr() + "/mcp")
	if err != nil {
		t.Fatalf("unauthorized MCP request: %v", err)
	}
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized MCP status = %d, want %d", unauthorized.StatusCode, http.StatusUnauthorized)
	}

	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "knowl-test-client", Version: "1.0.0"},
		nil,
	)
	httpClient := &http.Client{Transport: bearerTransport{
		token: "test-token",
		base:  http.DefaultTransport,
	}}
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint:             "http://" + host.Addr() + "/mcp",
		HTTPClient:           httpClient,
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
		"knowl_retrieve":  false,
		"knowl_ingest":    false,
		"knowl_operation": false,
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
		Name: "knowl_ingest",
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

	operation, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "knowl_operation",
		Arguments: map[string]any{"id": operationID},
	})
	if err != nil || operation.IsError {
		t.Fatalf("call knowl_operation = (%v, %#v)", err, operation)
	}
	retrieved, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "knowl_retrieve",
		Arguments: map[string]any{"query": "One"},
	})
	if err != nil || retrieved.IsError {
		t.Fatalf("call knowl_retrieve = (%v, %#v)", err, retrieved)
	}
	forbidden, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "knowl_retrieve",
		Arguments: map[string]any{"query": "One", "scope": "other"},
	})
	if err != nil {
		t.Fatalf("call scoped knowl_retrieve: %v", err)
	}
	if !forbidden.IsError {
		t.Fatal("scoped knowl_retrieve succeeded, want tool error")
	}
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(cloned)
}
