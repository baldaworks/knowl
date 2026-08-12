package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	smokeSourceID      = "source-1"
	smokeSourceVersion = "1"
	smokeSourceRef     = "inline:" + smokeSourceID + "@" + smokeSourceVersion
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

	ingestRequest := map[string]string{
		"content":         smokeSourceText,
		"origin":          smokeSourceID,
		"idempotency_key": smokeSourceVersion,
	}
	encoded, err := json.Marshal(ingestRequest)
	if err != nil {
		t.Fatalf("encode ingest request: %v", err)
	}

	body, status, err := doLiveHostRequest(client, baseURL, http.MethodPost, publicIngestPath, encoded)
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
		t.Fatalf("decode ingest response: %v", err)
	}
	if ingested.Status != "queued" {
		t.Fatalf("ingest status = %q, want queued", ingested.Status)
	}
	waitForSmokeOperation(t, client, baseURL, ingested.OperationID)

	pagePath := filepath.Join(config.Workspace, workspaceWikiDir, "entities", "one.md")
	if _, err := os.Stat(pagePath); err != nil {
		t.Fatalf("committed page missing: %v", err)
	}

	body, status, err = doLiveHostRequest(client, baseURL, http.MethodGet, "/v1/retrieve?query="+smokeQueryText, nil)
	if err != nil {
		t.Fatalf("retrieve request: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("retrieve status = %d, body %s", status, body)
	}
	var result struct {
		Query    string `json:"query"`
		Evidence []struct {
			PageID string `json:"page_id"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode retrieve response: %v", err)
	}
	if result.Query != smokeQueryText || len(result.Evidence) == 0 || result.Evidence[0].PageID != smokePageID {
		t.Fatalf("retrieve result = %#v, want evidence for %s", result, smokePageID)
	}
}

func waitForSmokeOperation(t *testing.T, client *http.Client, baseURL, operationID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	path := "/v1/operations/" + url.PathEscape(operationID)
	for time.Now().Before(deadline) {
		body, status, err := doLiveHostRequest(client, baseURL, http.MethodGet, path, nil)
		if err != nil || status != http.StatusOK {
			t.Fatalf("operation request = %d, %v, body %s", status, err, body)
		}
		var operation struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(body, &operation); err != nil {
			t.Fatalf("decode operation response: %v", err)
		}
		switch operation.Status {
		case "completed":
			return
		case "failed":
			t.Fatalf("operation %q failed", operationID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("operation %q did not complete", operationID)
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
		_, status, err := doLiveHostRequest(client, baseURL, http.MethodGet, path, nil)
		if err == nil && status == want {
			return
		}
		lastStatus = status
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s status = (%d, %v), want (%d, nil)", path, lastStatus, lastErr, want)
}

func doLiveHostRequest(client *http.Client, baseURL, method, path string, body []byte) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(context.Background(), method, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
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
