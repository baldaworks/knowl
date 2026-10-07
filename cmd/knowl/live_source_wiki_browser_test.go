//go:build browser

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const liveBrowserToken = "browser-live-secret"

type countingBrowserMaintainer struct {
	provider.Fixture
	plans atomic.Int64
}

func (maintainer *countingBrowserMaintainer) Plan(ctx context.Context, input domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	maintainer.plans.Add(1)
	return maintainer.Fixture.Plan(ctx, input)
}

func TestBrowserLiveSourceWiki(t *testing.T) {
	fixture := newCommandWorkflowFixture(t, true)
	fixture.config.Web.Enabled = true
	fixture.config.OperatorToken = liveBrowserToken
	maintainer := &countingBrowserMaintainer{Fixture: fixture.maintainer}
	host, err := knowl.NewHost(t.Context(), fixture.config, maintainer)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer shutdownSmokeHost(t, host)

	baseURL := "http://" + host.Addr()
	client := &http.Client{Timeout: 5 * time.Second}
	waitForHTTPStatus(t, client, baseURL, "/readyz", http.StatusOK)
	requestBody, err := json.Marshal(knowlapi.IngestRequest{
		Content: pointerTo(smokeSourceText), Origin: pointerTo(smokeSourceID), IdempotencyKey: pointerTo(smokeSourceVersion),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, status := liveBrowserRequest(t, client, baseURL, http.MethodPost, publicIngestPath, requestBody)
	if status != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %s", status, body)
	}
	var ingested knowlapi.IngestResult
	if err := json.Unmarshal(body, &ingested); err != nil {
		t.Fatal(err)
	}
	if ingested.Status != knowlapi.IngestResultStatusQueued || ingested.OperationId == "" {
		t.Fatalf("ingest result = %#v", ingested)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		body, status = liveBrowserRequest(t, client, baseURL, http.MethodGet, "/v1/operations/"+url.PathEscape(ingested.OperationId), nil)
		if status != http.StatusOK {
			t.Fatalf("operation status = %d, body = %s", status, body)
		}
		var operation knowlapi.OperationResult
		if err := json.Unmarshal(body, &operation); err != nil {
			t.Fatal(err)
		}
		if operation.Status == knowlapi.OperationResultStatusCompleted {
			break
		}
		if operation.Status == knowlapi.OperationResultStatusFailed || time.Now().After(deadline) {
			t.Fatalf("operation %s stopped at %s", ingested.OperationId, operation.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if maintainer.plans.Load() != 1 {
		t.Fatalf("ingest plans = %d, want 1", maintainer.plans.Load())
	}
	if _, err := os.Stat(fixture.pagePath(smokePagePath)); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(testRepoRoot(t), "tools", "webui-browser", "live_source_wiki.cjs")
	command := exec.CommandContext(t.Context(), "node", script)
	command.Env = append(os.Environ(), "KNOWL_BROWSER_URL="+baseURL, "KNOWL_BROWSER_TOKEN="+liveBrowserToken, "KNOWL_BROWSER_OPERATION_ID="+ingested.OperationId)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	t.Log(string(output))
	if maintainer.plans.Load() != 1 {
		t.Fatalf("browsing invoked inference: plans = %d", maintainer.plans.Load())
	}
}

func liveBrowserRequest(t *testing.T, client *http.Client, baseURL, method, path string, body []byte) ([]byte, int) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+liveBrowserToken)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return content, response.StatusCode
}
