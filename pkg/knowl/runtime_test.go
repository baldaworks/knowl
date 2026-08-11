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
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/normahq/runtime/v2/agentfactory"
	adkagent "google.golang.org/adk/v2/agent"
)

const hostSourceRef = "inline:source-1@1"
const hostCompletedStatus = "completed"
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
	if ingested.Status != hostCompletedStatus {
		t.Fatalf("ingest status = %q, want completed", ingested.Status)
	}
	if _, err := os.Stat(pagePath); err != nil {
		t.Fatalf("committed page missing: %v", err)
	}

	operationPath := "/v1/operations/" + url.PathEscape(ingested.OperationID)
	body, status, err = doHostRequest(t, host, http.MethodGet, operationPath, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("operation status request = %d, %v, body %s", status, err, body)
	}
	var operation struct {
		ID        string    `json:"id"`
		Status    string    `json:"status"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(body, &operation); err != nil {
		t.Fatalf("decode operation: %v", err)
	}
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
	body, status, err = doHostRequest(t, host, http.MethodGet, "/v1/retrieve?query=One", nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("retrieve request = %d, %v, body %s", status, err, body)
	}
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
	if mcpIngest.Status != hostCompletedStatus {
		t.Fatalf("mcp ingest status = %q, want %q", mcpIngest.Status, hostCompletedStatus)
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
	if httpIngest.Status != hostCompletedStatus {
		t.Fatalf("http ingest status = %q, want %q", httpIngest.Status, hostCompletedStatus)
	}
	if httpIngest.OperationID != string(mcpIngest.OperationID) {
		t.Fatalf("operation IDs differ: http=%q mcp=%q", httpIngest.OperationID, mcpIngest.OperationID)
	}

	mcpRetrieveValue, err := host.MCP().Call(ctx, hostRetrieveToolName, map[string]any{hostQueryKey: hostQuery})
	if err != nil {
		t.Fatalf("mcp retrieve: %v", err)
	}
	mcpRetrieve, ok := mcpRetrieveValue.(mcp.RetrieveResult)
	if !ok {
		t.Fatalf("mcp retrieve type = %T, want mcp.RetrieveResult", mcpRetrieveValue)
	}
	body, status, err = doHostRequest(t, host, http.MethodGet, "/v1/retrieve?query=One", nil)
	if err != nil {
		t.Fatalf("http retrieve: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("http retrieve status = %d, body %s", status, body)
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

func TestNewRejectsNilMaintainerBeforeOpeningStore(t *testing.T) {
	workspace := t.TempDir()
	config := knowl.DefaultConfig()
	config.Workspace = workspace
	config.StorePath = filepath.Join(workspace, ".knowl", "state.db")

	_, err := knowl.New(context.Background(), knowl.Options{Config: config})
	if err == nil || !strings.Contains(err.Error(), "maintainer") {
		t.Fatalf("New() error = %v, want required maintainer", err)
	}
	if _, statErr := os.Stat(config.StorePath); !os.IsNotExist(statErr) {
		t.Fatalf("store stat error = %v, want store unopened", statErr)
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
	factory := &validatingRuntimeFactory{providerID: "provider"}

	host, err := knowl.New(context.Background(), knowl.Options{
		Config:         config,
		RuntimeFactory: factory,
		ProviderID:     "provider",
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
