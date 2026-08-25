package knowl_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/normahq/runtime/v2/agentfactory"
	adkagent "google.golang.org/adk/v2/agent"
)

const hostSourceRef = "inline:source-1@1"
const hostCompletedStatus = "completed"
const hostFailedStatus = "failed"
const hostMCPClientName = "knowl-test-client"
const hostMCPClientVersion = "1.0.0"
const hostSourceContent = "source text"
const hostSourceOrigin = "source-1"
const hostSourceIdempotencyKey = "1"
const hostSourceContentKey = "content"
const hostSourceOriginKey = "origin"
const hostSourceIdempotencyKeyName = "idempotency_key"
const hostListenAddr = "127.0.0.1:0"
const hostPagePath = "wiki/entities/one.md"
const hostPageID = "entities/one"
const hostQuery = "One"
const hostQueryKey = "query"
const hostRetrieveToolName = "knowl_retrieve"
const hostIngestToolName = "knowl_ingest"
const hostOperationToolName = "knowl_operation"
const hostProviderID = "provider"

func TestHostOperatorTokenProtectsBusinessEndpoints(t *testing.T) {
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
	config.OperatorToken = "local-secret"
	host, err := knowl.NewHost(context.Background(), config, provider.Fixture{})
	if err != nil {
		t.Fatalf("compose host: %v", err)
	}
	defer shutdownHost(t, host)

	tests := []struct {
		name          string
		path          string
		authorization string
		wantStatus    int
	}{
		{name: "health remains public", path: "/healthz", wantStatus: http.StatusOK},
		{name: "http requires token", path: "/v1/retrieve?query=test", wantStatus: http.StatusUnauthorized},
		{name: "mcp requires token", path: "/mcp", wantStatus: http.StatusUnauthorized},
		{name: "valid token reaches service", path: "/v1/retrieve?query=test", authorization: "Bearer local-secret", wantStatus: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://knowl"+test.path, nil)
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			response := httptest.NewRecorder()
			host.Handler().ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestHostPublicAPIKISSContractAndRestart(t *testing.T) {
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
	preStart := httptest.NewRecorder()
	preStartRequest := httptest.NewRequest(http.MethodGet, "http://knowl/readyz", nil)
	host.Handler().ServeHTTP(preStart, preStartRequest)
	if preStart.Code != http.StatusServiceUnavailable {
		t.Fatalf("pre-start readiness status = %d, want %d", preStart.Code, http.StatusServiceUnavailable)
	}
	body, status, err := doHostRequest(t, host, http.MethodGet, "/v1/retrieve?query=before-start&scope=other", nil)
	if err != nil {
		t.Fatalf("pre-start scope override request: %v", err)
	}
	if status != http.StatusForbidden {
		t.Fatalf("pre-start scope override status = %d, body %s", status, body)
	}
	assertErrorClass(t, body, "scope_override_forbidden")
	body, status, err = doHostRequest(t, host, http.MethodGet, "/v1/retrieve?query=before-start", nil)
	if err != nil {
		t.Fatalf("pre-start query request: %v", err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("pre-start query status = %d, body %s", status, body)
	}
	assertErrorClass(t, body, "not_ready")
	if err := host.Start(ctx); err != nil {
		t.Fatalf("start host: %v", err)
	}
	defer shutdownHost(t, host)

	if _, status, _ := doHostRequest(t, host, http.MethodGet, "/healthz", nil); status != http.StatusOK {
		t.Fatalf("health status = %d, want %d", status, http.StatusOK)
	}
	if _, status, _ := doHostRequest(t, host, http.MethodGet, "/readyz", nil); status != http.StatusOK {
		t.Fatalf("readiness status = %d, want %d", status, http.StatusOK)
	}
	body, status, err = doHostRequest(t, host, http.MethodGet, "/v1/retrieve", nil)
	if err != nil {
		t.Fatalf("retrieve without query request: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("retrieve without query status = %d, body %s", status, body)
	}
	assertErrorClass(t, body, "query_required")
	body, status, err = doHostRequest(t, host, http.MethodGet, "/v1/unknown", nil)
	if err != nil {
		t.Fatalf("unknown route request: %v", err)
	}
	if status != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, body %s", status, body)
	}
	assertErrorClass(t, body, "not_found")

	ingestRequest := map[string]string{
		hostSourceContentKey:         hostSourceContent,
		hostSourceOriginKey:          hostSourceOrigin,
		hostSourceIdempotencyKeyName: hostSourceIdempotencyKey,
	}
	encoded, err := json.Marshal(ingestRequest)
	if err != nil {
		t.Fatalf("encode ingest request: %v", err)
	}
	body, status, err = doHostRequest(t, host, http.MethodPost, "/v1/ingest", encoded)
	if err != nil {
		t.Fatalf("ingest request: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("ingest status = %d, body %s", status, body)
	}
	var ingested struct {
		OperationID string `json:"operation_id"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(body, &ingested); err != nil {
		t.Fatalf("decode ingest result: %v", err)
	}
	pagePath := filepath.Join(workspace.Root(), "wiki", "entities", "one.md")
	if ingested.Status != hostQueuedStatus {
		t.Fatalf("ingest status = %q, want queued", ingested.Status)
	}
	operation := waitForHostOperation(t, host, ingested.OperationID)
	if _, err := os.Stat(pagePath); err != nil {
		t.Fatalf("committed page missing: %v", err)
	}

	operationPath := "/v1/operations/" + url.PathEscape(ingested.OperationID)
	if operation.ID != ingested.OperationID || operation.Status != hostCompletedStatus {
		t.Fatalf("operation status after commit = %q", operation.Status)
	}
	body, status, headers, err := doHostRequestDetailed(t, host, http.MethodGet, "/v1/ingest", nil)
	if err != nil {
		t.Fatalf("get ingest request: %v", err)
	}
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("get ingest status = %d, body %s", status, body)
	}
	if allow := headers.Get("Allow"); !strings.Contains(allow, http.MethodPost) {
		t.Fatalf("get ingest Allow header = %q, want %q", allow, http.MethodPost)
	}
	assertErrorClass(t, body, "method_not_allowed")
	body = waitForHostRetrieve(t, host)
	var retrieve struct {
		Query    string `json:"query"`
		Evidence []struct {
			PageID string `json:"page_id"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(body, &retrieve); err != nil {
		t.Fatalf("decode retrieve result: %v", err)
	}
	if retrieve.Query != hostQuery || len(retrieve.Evidence) == 0 || retrieve.Evidence[0].PageID != hostPageID {
		t.Fatalf("retrieve result = %#v", retrieve)
	}

	shutdownHost(t, host)

	host, err = knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("reopen committed host: %v", err)
	}
	defer shutdownHost(t, host)
	if err := host.Start(ctx); err != nil {
		t.Fatalf("restart host: %v", err)
	}
	body, status, err = doHostRequest(t, host, http.MethodGet, operationPath, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("reopened operation request = %d, %v, body %s", status, err, body)
	}
	if err := json.Unmarshal(body, &operation); err != nil {
		t.Fatalf("decode reopened operation: %v", err)
	}
	if operation.Status != hostCompletedStatus {
		t.Fatalf("reopened operation status = %q, want committed", operation.Status)
	}
}

func TestHostRestartResumesAcceptedOperationThroughHTTPAndMCP(t *testing.T) {
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
	maintainer := provider.Fixture{Result: domain.ModelEditPlan{
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{hostSourceRef},
		Edits:        []domain.FileEdit{{Path: hostPagePath, Content: []byte(hostPageContent)}},
	}}
	firstHost, err := knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("compose first host: %v", err)
	}
	value, err := firstHost.MCP().Call(ctx, hostIngestToolName, map[string]any{
		hostSourceContentKey: hostSourceContent, hostSourceOriginKey: hostSourceOrigin,
		hostSourceIdempotencyKeyName: hostSourceIdempotencyKey,
	})
	if err != nil {
		t.Fatalf("accept source before crash: %v", err)
	}
	accepted, ok := value.(mcp.IngestResult)
	if !ok || accepted.OperationID == "" || accepted.Status != hostQueuedStatus {
		t.Fatalf("accepted operation = %#v, want queued", value)
	}
	shutdownHost(t, firstHost)

	restarted, err := knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("compose restarted host: %v", err)
	}
	defer shutdownHost(t, restarted)
	if err := restarted.Start(ctx); err != nil {
		t.Fatalf("start restarted host: %v", err)
	}
	httpOperation := waitForHostOperation(t, restarted, string(accepted.OperationID))
	mcpValue, err := restarted.MCP().Call(ctx, hostOperationToolName, map[string]any{"id": string(accepted.OperationID)})
	if err != nil {
		t.Fatalf("read resumed MCP operation: %v", err)
	}
	mcpOperation, ok := mcpValue.(mcp.OperationResult)
	if !ok || mcpOperation.Status != httpOperation.Status || mcpOperation.ID != accepted.OperationID {
		t.Fatalf("public operation mismatch: HTTP=%#v MCP=%#v", httpOperation, mcpValue)
	}
	body := []byte(`{"content":"source text","origin":"source-1","idempotency_key":"1"}`)
	replayBody, status, err := doHostRequest(t, restarted, http.MethodPost, "/v1/ingest", body)
	if err != nil || status != http.StatusOK {
		t.Fatalf("terminal HTTP replay = %d, %v, body %s", status, err, replayBody)
	}
	var replay struct {
		OperationID string `json:"operation_id"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(replayBody, &replay); err != nil {
		t.Fatalf("decode terminal replay: %v", err)
	}
	if replay.OperationID != string(accepted.OperationID) || replay.Status != hostCompletedStatus {
		t.Fatalf("terminal replay = %#v, want same completed operation", replay)
	}
	logContent, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read provenance log: %v", err)
	}
	if count := bytes.Count(logContent, []byte(accepted.OperationID)); count != 1 {
		t.Fatalf("resumed operation log entries = %d, want one", count)
	}
}

func TestHostHTTPAndMCPSharePublicContractBehavior(t *testing.T) {
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
	config.IngestOptions.AutoApply = false
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

	mcpValue, err := host.MCP().Call(ctx, "knowl_ingest", map[string]any{
		hostSourceContentKey:         hostSourceContent,
		hostSourceOriginKey:          hostSourceOrigin,
		hostSourceIdempotencyKeyName: hostSourceIdempotencyKey,
	})
	if err != nil {
		t.Fatalf("mcp ingest: %v", err)
	}
	mcpIngest, ok := mcpValue.(mcp.IngestResult)
	if !ok {
		t.Fatalf("mcp ingest type = %T, want mcp.IngestResult", mcpValue)
	}
	if mcpIngest.Status != hostQueuedStatus {
		t.Fatalf("mcp ingest status = %q, want queued", mcpIngest.Status)
	}

	encoded, err := json.Marshal(map[string]string{
		hostSourceContentKey:         hostSourceContent,
		hostSourceOriginKey:          hostSourceOrigin,
		hostSourceIdempotencyKeyName: hostSourceIdempotencyKey,
	})
	if err != nil {
		t.Fatalf("encode ingest request: %v", err)
	}
	body, status, err := doHostRequest(t, host, http.MethodPost, "/v1/ingest", encoded)
	if err != nil {
		t.Fatalf("http ingest: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("http ingest status = %d, body %s", status, body)
	}
	var httpIngest struct {
		OperationID string `json:"operation_id"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(body, &httpIngest); err != nil {
		t.Fatalf("decode http ingest: %v", err)
	}
	if httpIngest.Status != hostQueuedStatus {
		t.Fatalf("http ingest status = %q, want queued", httpIngest.Status)
	}
	if httpIngest.OperationID != string(mcpIngest.OperationID) {
		t.Fatalf("operation IDs differ: http=%q mcp=%q", httpIngest.OperationID, mcpIngest.OperationID)
	}
	_ = waitForHostOperation(t, host, httpIngest.OperationID)

	body = waitForHostRetrieve(t, host)
	mcpRetrieveValue, err := host.MCP().Call(ctx, hostRetrieveToolName, map[string]any{hostQueryKey: hostQuery})
	if err != nil {
		t.Fatalf("mcp retrieve: %v", err)
	}
	mcpRetrieve, ok := mcpRetrieveValue.(mcp.RetrieveResult)
	if !ok {
		t.Fatalf("mcp retrieve type = %T, want mcp.RetrieveResult", mcpRetrieveValue)
	}
	var httpRetrieve struct {
		Query    string `json:"query"`
		Evidence []struct {
			PageID string `json:"page_id"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(body, &httpRetrieve); err != nil {
		t.Fatalf("decode http retrieve: %v", err)
	}
	if len(mcpRetrieve.Evidence) != 1 || len(httpRetrieve.Evidence) != 1 {
		t.Fatalf("unexpected evidence counts: mcp=%d http=%d", len(mcpRetrieve.Evidence), len(httpRetrieve.Evidence))
	}
	if mcpRetrieve.Evidence[0].PageID != domain.PageID(httpRetrieve.Evidence[0].PageID) {
		t.Fatalf("retrieve page mismatch: mcp=%q http=%q", mcpRetrieve.Evidence[0].PageID, httpRetrieve.Evidence[0].PageID)
	}

	mcpOperationValue, err := host.MCP().Call(ctx, "knowl_operation", map[string]any{"id": string(mcpIngest.OperationID)})
	if err != nil {
		t.Fatalf("mcp operation: %v", err)
	}
	mcpOperation, ok := mcpOperationValue.(mcp.OperationResult)
	if !ok {
		t.Fatalf("mcp operation type = %T, want mcp.OperationResult", mcpOperationValue)
	}
	body, status, err = doHostRequest(t, host, http.MethodGet, "/v1/operations/"+url.PathEscape(httpIngest.OperationID), nil)
	if err != nil {
		t.Fatalf("http operation: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("http operation status = %d, body %s", status, body)
	}
	var httpOperation struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &httpOperation); err != nil {
		t.Fatalf("decode http operation: %v", err)
	}
	if mcpOperation.ID != domain.OperationID(httpOperation.ID) || mcpOperation.Status != httpOperation.Status {
		t.Fatalf("operation mismatch: mcp=%#v http=%#v", mcpOperation, httpOperation)
	}
}

func TestHostIngestReturnsBeforeMaintainerPlanCompletes(t *testing.T) {
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
		release: release,
		started: make(chan struct{}),
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

	body, status, err := doHostRequest(t, host, http.MethodPost, "/v1/ingest", []byte(`{"content":"source text","origin":"source-1","idempotency_key":"1"}`))
	if err != nil || status != http.StatusOK {
		t.Fatalf("ingest request = %d, %v, body %s", status, err, body)
	}
	var submitted struct {
		OperationID string `json:"operation_id"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(body, &submitted); err != nil {
		t.Fatalf("decode ingest response: %v", err)
	}
	if submitted.Status != hostQueuedStatus || submitted.OperationID == "" {
		t.Fatalf("ingest response = %#v, want queued operation", submitted)
	}
	select {
	case <-maintainer.started:
	case <-time.After(time.Second):
		t.Fatal("host worker did not begin maintainer plan")
	}
	close(release)
	_ = waitForHostOperation(t, host, submitted.OperationID)
}

func TestHostStartSchedulesDurablePreStartWorkWithoutWaitingForProvider(t *testing.T) {
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
		release: release,
		started: make(chan struct{}),
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
	host, err := knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("compose host: %v", err)
	}
	defer shutdownHost(t, host)
	value, err := host.MCP().Call(ctx, hostIngestToolName, map[string]any{
		"content": hostSourceContent, "origin": hostSourceOrigin, "idempotency_key": hostSourceIdempotencyKey,
	})
	if err != nil {
		t.Fatalf("pre-start MCP ingest: %v", err)
	}
	submitted, ok := value.(mcp.IngestResult)
	if !ok || submitted.Status != hostQueuedStatus || submitted.OperationID == "" {
		t.Fatalf("pre-start submission = %#v, want queued operation", value)
	}
	if host.Ready() {
		t.Fatal("host became ready before Start initial inspection")
	}
	startedHost := make(chan error, 1)
	go func() { startedHost <- host.Start(ctx) }()
	select {
	case err := <-startedHost:
		if err != nil {
			t.Fatalf("start host: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start waited for provider completion instead of initial scan")
	}
	if !host.Ready() {
		t.Fatal("host not ready after durable initial scan handshake")
	}
	select {
	case <-maintainer.started:
	case <-time.After(time.Second):
		t.Fatal("pre-start durable operation was not scheduled")
	}
	close(release)
	_ = waitForHostOperation(t, host, string(submitted.OperationID))
}

func TestHostStopAllowsActiveOperationToFinishAndIsIdempotent(t *testing.T) {
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
		release: release,
		started: make(chan struct{}),
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
	host, err := knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("compose host: %v", err)
	}
	if err := host.Start(ctx); err != nil {
		t.Fatalf("start host: %v", err)
	}
	if err := host.Start(ctx); err != nil {
		t.Fatalf("repeat start host: %v", err)
	}
	body, status, err := doHostRequest(t, host, http.MethodPost, "/v1/ingest", []byte(`{"content":"source text","origin":"source-1","idempotency_key":"1"}`))
	if err != nil || status != http.StatusOK {
		t.Fatalf("ingest request = %d, %v, body %s", status, err, body)
	}
	select {
	case <-maintainer.started:
	case <-time.After(time.Second):
		t.Fatal("active operation did not start")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- host.Stop(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for host.Ready() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if host.Ready() {
		t.Fatal("host remained ready during shutdown")
	}
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before active work completed: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("stop host: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish after active work completed")
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), hostPagePath)); err != nil {
		t.Fatalf("active operation did not retain terminal content: %v", err)
	}
	if err := host.Stop(ctx); err != nil {
		t.Fatalf("repeat stop host: %v", err)
	}
	if err := host.Start(ctx); err == nil {
		t.Fatal("Start after Stop unexpectedly succeeded")
	}
}

func TestNewAllowsProviderFreeHost(t *testing.T) {
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

	host, err := knowl.New(context.Background(), knowl.Options{Config: config})
	if err != nil {
		t.Fatalf("compose provider-free host: %v", err)
	}
	defer shutdownHost(t, host)
	if err := host.Start(context.Background()); err != nil {
		t.Fatalf("start provider-free host: %v", err)
	}
	if !host.Ready() {
		t.Fatal("provider-free host is not ready")
	}
	if tools := host.MCP().Tools(); len(tools) != 3 || tools[0].Name != hostRetrieveToolName || tools[1].Name != hostIngestToolName || tools[2].Name != hostOperationToolName {
		t.Fatalf("provider-free MCP tools = %#v", tools)
	}
	if _, err := host.MCP().Call(context.Background(), hostIngestToolName, map[string]any{
		hostSourceContentKey: hostSourceContent, hostSourceOriginKey: hostSourceOrigin,
		hostSourceIdempotencyKeyName: hostSourceIdempotencyKey,
	}); !errors.Is(err, app.ErrMaintainerUnavailable) {
		t.Fatalf("provider-free MCP ingest error = %v", err)
	}
	body := []byte(`{"content":"source text","origin":"source-1","idempotency_key":"1"}`)
	responseBody, status, err := doHostRequest(t, host, http.MethodPost, "/v1/ingest", body)
	if err != nil {
		t.Fatalf("provider-free HTTP ingest: %v", err)
	}
	if status != http.StatusServiceUnavailable || !bytes.Contains(responseBody, []byte(`"error":"maintainer_unavailable"`)) {
		t.Fatalf("provider-free HTTP ingest = status %d, body %s", status, responseBody)
	}
}

func TestNewRejectsPartialRuntimeProviderConfiguration(t *testing.T) {
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

	tests := []struct {
		name    string
		options knowl.Options
		want    string
	}{
		{name: "provider without factory", options: knowl.Options{Config: config, ProviderID: hostProviderID}, want: "runtime provider factory is required"},
		{name: "factory without provider", options: knowl.Options{Config: config, RuntimeFactory: &validatingRuntimeFactory{}}, want: "knowl.provider is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, newErr := knowl.New(context.Background(), test.options); newErr == nil || !strings.Contains(newErr.Error(), test.want) {
				t.Fatalf("New() error = %v, want %q", newErr, test.want)
			}
		})
	}
}

func TestNewBuildsRuntimeMaintainerLazily(t *testing.T) {
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
	factory := &validatingRuntimeFactory{providerID: hostProviderID}

	host, err := knowl.New(context.Background(), knowl.Options{
		Config:         config,
		RuntimeFactory: factory,
		ProviderID:     hostProviderID,
	})
	if err != nil {
		t.Fatalf("compose host: %v", err)
	}
	defer shutdownHost(t, host)
	if host.Ready() {
		t.Fatal("host should not be ready before Start")
	}
	if factory.validations != 1 {
		t.Fatalf("provider validations = %d, want one", factory.validations)
	}
	if factory.builds != 0 {
		t.Fatalf("provider builds = %d, want lazy provider", factory.builds)
	}
}

type validatingRuntimeFactory struct {
	providerID  string
	validations int
	builds      int
}

type blockingMaintainer struct {
	plan    domain.ModelEditPlan
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (maintainer *blockingMaintainer) Plan(ctx context.Context, _ domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	maintainer.once.Do(func() { close(maintainer.started) })
	select {
	case <-maintainer.release:
		return maintainer.plan, nil
	case <-ctx.Done():
		return domain.ModelEditPlan{}, ctx.Err()
	}
}

func (factory *validatingRuntimeFactory) ValidateAgent(providerID string) error {
	factory.validations++
	if providerID != factory.providerID {
		return errors.New("unexpected provider ID")
	}
	return nil
}

func (factory *validatingRuntimeFactory) Build(context.Context, agentfactory.BuildRequest) (adkagent.Agent, error) {
	factory.builds++
	return nil, errors.New("provider build should remain lazy")
}

const hostPageContent = "---\nid: entities/one\ntitle: One\ntype: entity\nsource_refs:\n  - " + hostSourceRef + "\n---\n# One\n"
const hostQueuedStatus = "queued"

func doHostRequest(t *testing.T, host *knowl.Host, method, path string, body []byte) ([]byte, int, error) {
	t.Helper()
	content, status, _, err := doHostRequestDetailed(t, host, method, path, body)
	return content, status, err
}

func doHostRequestDetailed(t *testing.T, host *knowl.Host, method, path string, body []byte) ([]byte, int, http.Header, error) {
	t.Helper()
	request := httptest.NewRequest(method, "http://knowl"+path, bytes.NewReader(body))
	response := httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	return response.Body.Bytes(), response.Code, response.Header(), nil
}

type hostOperationResponse struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

func waitForHostOperation(t *testing.T, host *knowl.Host, id string) hostOperationResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	path := "/v1/operations/" + url.PathEscape(id)
	for time.Now().Before(deadline) {
		body, status, err := doHostRequest(t, host, http.MethodGet, path, nil)
		if err != nil || status != http.StatusOK {
			t.Fatalf("operation status request = %d, %v, body %s", status, err, body)
		}
		var operation hostOperationResponse
		if err := json.Unmarshal(body, &operation); err != nil {
			t.Fatalf("decode operation: %v", err)
		}
		switch operation.Status {
		case hostCompletedStatus:
			return operation
		case "failed":
			t.Fatalf("operation %q failed", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("operation %q did not complete", id)
	return hostOperationResponse{}
}

func waitForHostRetrieve(t *testing.T, host *knowl.Host) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body, status, err := doHostRequest(t, host, http.MethodGet, "/v1/retrieve?query=One", nil)
		if err != nil || status != http.StatusOK {
			t.Fatalf("retrieve request = %d, %v, body %s", status, err, body)
		}
		var retrieve struct {
			Evidence []json.RawMessage `json:"evidence"`
		}
		if err := json.Unmarshal(body, &retrieve); err != nil {
			t.Fatalf("decode retrieve response: %v", err)
		}
		if len(retrieve.Evidence) > 0 {
			return body
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("retrieve did not observe the projected page")
	return nil
}

func assertErrorClass(t *testing.T, body []byte, want string) {
	t.Helper()
	var response map[string]string
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if got := response["error"]; got != want {
		t.Fatalf("error class = %q, want %q", got, want)
	}
}

func shutdownHost(t *testing.T, host *knowl.Host) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := host.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown host: %v", err)
	}
}
