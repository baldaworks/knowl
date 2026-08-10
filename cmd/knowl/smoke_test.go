package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	smokeOperatorToken = "test-token"
	smokeSourceAdapter = "fixture"
	smokeSourceID      = "source-1"
	smokeSourceRef     = smokeSourceAdapter + ":" + smokeSourceID + "@1"
	smokeSourceText    = "source text"
	smokeQueryText     = "One"
	smokePageID        = "entities/one"
	smokePagePath      = "wiki/entities/one.md"
	smokePageContent   = "---\nid: " + smokePageID + "\ntitle: " + smokeQueryText + "\ntype: entity\nsource_refs:\n  - " + smokeSourceRef + "\n---\n# " + smokeQueryText + "\n"
)

func TestSupportedLocalWorkflowSmoke(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	clearKnowlEnv(t)

	if err := executeRootCommand(initCommandName); err != nil {
		t.Fatalf("run knowl init: %v", err)
	}
	t.Setenv("KNOWL_SERVER_LISTEN_ADDR", loopbackListenAddr)
	t.Setenv("KNOWL_OPERATOR_TOKEN", smokeOperatorToken)
	if err := executeRootCommand("validate"); err != nil {
		t.Fatalf("run knowl validate: %v", err)
	}

	ctx, err := loadConfig(context.Background(), "", "")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatalf("derive host config: %v", err)
	}
	if config.Workspace != workspace {
		t.Fatalf("host config workspace = %q, want %q", config.Workspace, workspace)
	}
	if config.ListenAddr != loopbackListenAddr {
		t.Fatalf("host config listen addr = %q, want %s", config.ListenAddr, loopbackListenAddr)
	}
	if config.OperatorToken != smokeOperatorToken {
		t.Fatalf("host config operator token = %q, want %q", config.OperatorToken, smokeOperatorToken)
	}
	if config.StoreDriver != knowl.StoreSQLite {
		t.Fatalf("host config store driver = %q, want %q", config.StoreDriver, knowl.StoreSQLite)
	}
	wantStorePath := filepath.Join(workspace, ".knowl", "knowl.sqlite")
	if config.StorePath != wantStorePath {
		t.Fatalf("host config store path = %q, want %q", config.StorePath, wantStorePath)
	}

	content, err := contentfs.New(config.Workspace)
	if err != nil {
		t.Fatalf("open workspace content: %v", err)
	}
	schema, err := content.Schema(ctx, config.Scope)
	if err != nil {
		t.Fatalf("read workspace schema: %v", err)
	}
	maintainer := provider.Fixture{Result: domain.ModelEditPlan{
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{smokeSourceRef},
		Edits:        []domain.FileEdit{{Path: smokePagePath, Content: []byte(smokePageContent)}},
	}}

	host, err := knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("compose host: %v", err)
	}
	if err := host.Start(ctx); err != nil {
		t.Fatalf("start host: %v", err)
	}
	defer shutdownSmokeHost(t, host)

	baseURL := "http://" + host.Addr()
	client := &http.Client{Timeout: 5 * time.Second}
	waitForHTTPStatus(t, client, baseURL, "/healthz", http.StatusOK)
	waitForHTTPStatus(t, client, baseURL, "/readyz", http.StatusOK)

	envelope := domain.SourceEnvelope{
		Scope:   config.Scope,
		Source:  domain.SourceRef{Adapter: smokeSourceAdapter, ID: smokeSourceID},
		Version: domain.SourceVersion{Version: "1", Digest: smokeDigest([]byte(smokeSourceText))},
		Content: []byte(smokeSourceText),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("encode source envelope: %v", err)
	}

	if _, status, err := doLiveHostRequest(client, baseURL, http.MethodPost, "/v1/ingest/preview", encoded, ""); err != nil || status != http.StatusUnauthorized {
		t.Fatalf("unauthorized preview = (%d, %v), want (%d, nil)", status, err, http.StatusUnauthorized)
	}

	body, status, err := doLiveHostRequest(client, baseURL, http.MethodPost, "/v1/ingest/preview", encoded, config.OperatorToken)
	if err != nil {
		t.Fatalf("preview request: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("preview status = %d, body %s", status, body)
	}
	var preview app.IngestResult
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if preview.Operation.Status != domain.StatusAwaitingReview {
		t.Fatalf("preview operation status = %q, want %q", preview.Operation.Status, domain.StatusAwaitingReview)
	}

	pagePath := filepath.Join(config.Workspace, workspaceWikiDir, "entities", "one.md")
	if _, err := os.Stat(pagePath); !os.IsNotExist(err) {
		t.Fatalf("preview page stat = %v, want absent", err)
	}

	operationPath := "/v1/operations/" + url.PathEscape(string(preview.Operation.ID))
	body, status, err = doLiveHostRequest(client, baseURL, http.MethodPost, operationPath+"/apply", nil, config.OperatorToken)
	if err != nil {
		t.Fatalf("apply request: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("apply status = %d, body %s", status, body)
	}
	var applied app.ApplyResult
	if err := json.Unmarshal(body, &applied); err != nil {
		t.Fatalf("decode apply response: %v", err)
	}
	if applied.Operation.Status != domain.StatusCommitted {
		t.Fatalf("apply operation status = %q, want %q", applied.Operation.Status, domain.StatusCommitted)
	}
	if _, err := os.Stat(pagePath); err != nil {
		t.Fatalf("committed page missing: %v", err)
	}

	body, status, err = doLiveHostRequest(client, baseURL, http.MethodGet, "/v1/pages/entities/one", nil, "")
	if err != nil {
		t.Fatalf("page request: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("page status = %d, body %s", status, body)
	}
	var page domain.PageSnapshot
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode page response: %v", err)
	}
	if page.ID != smokePageID || !page.Untrusted {
		t.Fatalf("page snapshot = %#v, want entities/one untrusted page", page)
	}
}

func executeRootCommand(args ...string) error {
	root := newRootCommand()
	root.SetArgs(args)
	root.SetIn(bytes.NewReader(nil))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetContext(context.Background())
	return root.Execute()
}

func waitForHTTPStatus(t *testing.T, client *http.Client, baseURL, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastStatus int
	var lastErr error
	for time.Now().Before(deadline) {
		_, status, err := doLiveHostRequest(client, baseURL, http.MethodGet, path, nil, "")
		if err == nil && status == want {
			return
		}
		lastStatus = status
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s status = (%d, %v), want (%d, nil)", path, lastStatus, lastErr, want)
}

func doLiveHostRequest(client *http.Client, baseURL, method, path string, body []byte, token string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(context.Background(), method, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = response.Body.Close() }()
	content, err := io.ReadAll(response.Body)
	return content, response.StatusCode, err
}

func shutdownSmokeHost(t *testing.T, host *knowl.Host) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := host.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown host: %v", err)
	}
}

func smokeDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
