//go:build integration

package knowl_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestCodexACPIngestProducesValidPlan(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex executable is not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}

	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.ListenAddr = hostListenAddr
	factory := agentfactory.New(map[string]agentconfig.Config{
		"codex": {
			Type: agentconfig.AgentTypeCodexACP,
			CodexACP: &agentconfig.ACPConfig{
				Model:           "gpt-5.4",
				ReasoningEffort: "xhigh",
			},
		},
	}, mcpregistry.New(nil))
	host, err := knowl.New(ctx, knowl.Options{
		Config:         config,
		RuntimeFactory: factory,
		ProviderID:     "codex",
	})
	if err != nil {
		t.Fatalf("compose host: %v", err)
	}
	if err := host.Start(ctx); err != nil {
		t.Fatalf("start host: %v", err)
	}
	defer shutdownHost(t, host)

	session := connectMCPClient(t, ctx, host)
	defer closeMCPClient(t, session)
	operationID := callCodexIntegrationIngest(
		t,
		ctx,
		session,
		"# Balda\n\nОсновной исходник Balda: https://github.com/baldaworks/balda",
		"source-1",
		"1",
	)
	operation := waitForMCPHostOperationStatusUntil(t, ctx, session, operationID)
	if operation["status"] == hostFailedStatus {
		t.Fatalf("Codex ACP ingest failed: %#v", operation["failure"])
	}
	if operation["status"] != hostCompletedStatus {
		t.Fatalf("operation = %#v, want completed", operation)
	}
	assertCodexIntegrationRetrieve(t, ctx, session, "Balda")

	secondID := callCodexIntegrationIngest(t, ctx, session, "second source", "source-2", "1")
	second := waitForMCPHostOperationStatusUntil(t, ctx, session, secondID)
	if second["status"] != hostCompletedStatus {
		t.Fatalf("second operation = %#v, want completed with reused ACP runtime", second)
	}
}

func assertCodexIntegrationRetrieve(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, query string) {
	t.Helper()
	response, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      hostRetrieveToolName,
		Arguments: map[string]any{hostQueryKey: query},
	})
	if err != nil || response.IsError {
		t.Fatalf("call knowl_retrieve = (%v, %#v)", err, response)
	}
	result, ok := response.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("retrieve result type = %T", response.StructuredContent)
	}
	evidence, ok := result["evidence"].([]any)
	if !ok || len(evidence) == 0 {
		t.Fatalf("retrieve result = %#v, want evidence for %q", result, query)
	}
}

func callCodexIntegrationIngest(
	t *testing.T,
	ctx context.Context,
	session *sdkmcp.ClientSession,
	content string,
	origin string,
	idempotencyKey string,
) string {
	t.Helper()
	response, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: hostIngestToolName,
		Arguments: map[string]any{
			hostSourceContentKey:         content,
			hostSourceOriginKey:          origin,
			hostSourceIdempotencyKeyName: idempotencyKey,
		},
	})
	if err != nil || response.IsError {
		t.Fatalf("call knowl_ingest = (%v, %#v)", err, response)
	}
	result, ok := response.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("ingest result type = %T", response.StructuredContent)
	}
	operationID, _ := result["operation_id"].(string)
	if operationID == "" {
		t.Fatalf("ingest operation ID missing: %#v", result)
	}
	return operationID
}

func waitForMCPHostOperationStatusUntil(
	t *testing.T,
	ctx context.Context,
	session *sdkmcp.ClientSession,
	operationID string,
) map[string]any {
	t.Helper()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		operation, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
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
		case "awaiting_review", hostCompletedStatus, hostFailedStatus:
			return result
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for operation %q: %v", operationID, ctx.Err())
		case <-ticker.C:
		}
	}
}
