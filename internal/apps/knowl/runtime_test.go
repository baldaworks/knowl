package knowl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	domain "github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	"github.com/normahq/runtime/v2/agentfactory"
	"go.uber.org/fx"
	adkagent "google.golang.org/adk/v2/agent"
)

const hostSourceRef = "fixture:source-1@1"

func TestHostPreflightsHTTPPreviewApplyAndRestart(t *testing.T) {
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
	config := DefaultConfig()
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
	host, err := NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("compose host: %v", err)
	}
	preStart := httptest.NewRecorder()
	preStartRequest := httptest.NewRequest(http.MethodGet, "http://knowl/readyz", nil)
	host.Handler().ServeHTTP(preStart, preStartRequest)
	if preStart.Code != http.StatusServiceUnavailable {
		t.Fatalf("pre-start readiness status = %d, want %d", preStart.Code, http.StatusServiceUnavailable)
	}
	if err := host.worker.Start(ctx); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	host.ready.Store(true)
	shutdownHost(t, host)

	host, err = NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("recompose host: %v", err)
	}
	defer shutdownHost(t, host)
	if err := host.worker.Start(ctx); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	host.ready.Store(true)
	if _, status, _ := doHostRequest(t, host, http.MethodGet, "/healthz", nil, ""); status != http.StatusOK {
		t.Fatalf("health status = %d, want %d", status, http.StatusOK)
	}
	if _, status, _ := doHostRequest(t, host, http.MethodGet, "/readyz", nil, ""); status != http.StatusOK {
		t.Fatalf("readiness status = %d, want %d", status, http.StatusOK)
	}

	envelope := domain.SourceEnvelope{
		Scope:   "local",
		Source:  domain.SourceRef{Adapter: "fixture", ID: "source-1"},
		Version: domain.SourceVersion{Version: "1", Digest: hostDigest([]byte("source text"))},
		Content: []byte("source text"),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	if _, status, _ := doHostRequest(t, host, http.MethodPost, "/v1/ingest/preview", encoded, ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthorized preview status = %d, want %d", status, http.StatusUnauthorized)
	}
	body, status, err := doHostRequest(t, host, http.MethodPost, "/v1/ingest/preview", encoded, config.OperatorToken)
	if err != nil {
		t.Fatalf("preview request: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("preview status = %d, body %s", status, body)
	}
	var planned app.IngestResult
	if err := json.Unmarshal(body, &planned); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if planned.Operation.Status != domain.StatusAwaitingReview {
		t.Fatalf("preview operation status = %q, want awaiting_review", planned.Operation.Status)
	}
	pagePath := filepath.Join(workspace.Root(), "wiki", "entities", "one.md")
	if _, err := os.Stat(pagePath); !os.IsNotExist(err) {
		t.Fatalf("preview page stat = %v, want absent", err)
	}

	operationPath := "/v1/operations/" + url.PathEscape(string(planned.Operation.ID))
	body, status, err = doHostRequest(t, host, http.MethodPost, operationPath+"/apply", nil, config.OperatorToken)
	if err != nil {
		t.Fatalf("apply request: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("apply status = %d, body %s", status, body)
	}
	var applied app.ApplyResult
	if err := json.Unmarshal(body, &applied); err != nil {
		t.Fatalf("decode apply: %v", err)
	}
	if applied.Operation.Status != domain.StatusCommitted {
		t.Fatalf("apply operation status = %q, want committed", applied.Operation.Status)
	}
	if _, err := os.Stat(pagePath); err != nil {
		t.Fatalf("committed page missing: %v", err)
	}

	body, status, err = doHostRequest(t, host, http.MethodGet, operationPath, nil, "")
	if err != nil || status != http.StatusOK {
		t.Fatalf("operation status request = %d, %v, body %s", status, err, body)
	}
	var operation domain.Operation
	if err := json.Unmarshal(body, &operation); err != nil {
		t.Fatalf("decode operation: %v", err)
	}
	if operation.Status != domain.StatusCommitted {
		t.Fatalf("operation status after commit = %q", operation.Status)
	}
	body, status, err = doHostRequest(t, host, http.MethodGet, "/v1/pages/entities/one", nil, "")
	if err != nil || status != http.StatusOK {
		t.Fatalf("page request = %d, %v, body %s", status, err, body)
	}
	var page domain.PageSnapshot
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if !page.Untrusted || page.ID != "entities/one" {
		t.Fatalf("page = %#v", page)
	}

	shutdownHost(t, host)
	host, err = NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("reopen committed host: %v", err)
	}
	defer shutdownHost(t, host)
	if err := host.worker.Start(ctx); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	host.ready.Store(true)
	body, status, err = doHostRequest(t, host, http.MethodGet, operationPath, nil, "")
	if err != nil || status != http.StatusOK {
		t.Fatalf("reopened operation request = %d, %v, body %s", status, err, body)
	}
	if err := json.Unmarshal(body, &operation); err != nil {
		t.Fatalf("decode reopened operation: %v", err)
	}
	if operation.Status != domain.StatusCommitted {
		t.Fatalf("reopened operation status = %q, want committed", operation.Status)
	}
}

func TestNewRejectsNilMaintainerBeforeOpeningStore(t *testing.T) {
	workspace := t.TempDir()
	config := DefaultConfig()
	config.Workspace = workspace
	config.StorePath = filepath.Join(workspace, ".knowl", "state.db")

	_, err := New(context.Background(), Options{Config: config})
	if err == nil || !strings.Contains(err.Error(), "maintainer") {
		t.Fatalf("New() error = %v, want required maintainer", err)
	}
	if _, statErr := os.Stat(config.StorePath); !os.IsNotExist(statErr) {
		t.Fatalf("store stat error = %v, want store unopened", statErr)
	}
}

func TestNewAppBuildsRuntimeMaintainerThroughFx(t *testing.T) {
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	config := DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.ListenAddr = "127.0.0.1:0"
	factory := &validatingRuntimeFactory{providerID: "provider"}
	var host *Host
	application := NewApp(context.Background(), Options{
		Config:         config,
		RuntimeFactory: factory,
		ProviderID:     "provider",
	}, fx.Populate(&host))
	if err := application.Err(); err != nil {
		t.Fatalf("Fx composition error: %v", err)
	}
	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := application.Start(startCtx); err != nil {
		t.Fatalf("Fx start error: %v", err)
	}
	if host == nil || !host.Ready() {
		t.Fatalf("host = %#v, want ready host", host)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := application.Stop(stopCtx); err != nil {
		t.Fatalf("Fx stop error: %v", err)
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

func doHostRequest(t *testing.T, host *Host, method, path string, body []byte, token string) ([]byte, int, error) {
	t.Helper()
	request := httptest.NewRequest(method, "http://knowl"+path, bytes.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	return response.Body.Bytes(), response.Code, nil
}

func shutdownHost(t *testing.T, host *Host) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := host.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown host: %v", err)
	}
}

func hostDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
