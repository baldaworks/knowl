package knowl_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/internal/knowledgetest"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type goldenDriver interface {
	Ingest(t *testing.T, source knowledgetest.SourceFixture) goldenIngest
	Operation(t *testing.T, id domain.OperationID) goldenOperation
	Retrieve(ctx context.Context, query string, limits domain.ReadLimits) ([]domain.PageReference, error)
}

type goldenIngest struct {
	ID     domain.OperationID
	Status string
}

type goldenOperation struct {
	ID     domain.OperationID
	Status string
}

type goldenOutcome struct {
	OperationIDs []domain.OperationID
	Metrics      knowledgetest.Metrics
	PageDigests  map[string]string
	PageRefs     map[domain.PageID][]string
}

func TestHostGoldenKnowledgeLoopThroughMCPAndHTTP(t *testing.T) {
	var outcomes []goldenOutcome
	for _, transport := range []string{"mcp", "http"} {
		t.Run(transport, func(t *testing.T) {
			outcomes = append(outcomes, runGoldenLoop(t, transport))
		})
	}
	if len(outcomes) != 2 {
		t.Fatalf("golden outcomes = %d, want 2", len(outcomes))
	}
	if !reflect.DeepEqual(outcomes[0], outcomes[1]) {
		t.Fatalf("public golden outcomes differ:\nMCP:  %#v\nHTTP: %#v", outcomes[0], outcomes[1])
	}
}

func TestHostGoldenAcceptedOperationResumesAcrossRestart(t *testing.T) {
	ctx := context.Background()
	workspace, config := newGoldenWorkspace(t)
	maintainer := &knowledgetest.Maintainer{}
	first, err := knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("NewHost() before restart: %v", err)
	}
	source := knowledgetest.Sources()[0]
	accepted := (&goldenMCPDriver{host: first}).Ingest(t, source)
	if accepted.ID == "" || accepted.Status != hostQueuedStatus {
		t.Fatalf("accepted operation = %#v, want queued", accepted)
	}
	shutdownHost(t, first)

	restarted, err := knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("NewHost() after restart: %v", err)
	}
	defer shutdownHost(t, restarted)
	if err := restarted.Start(ctx); err != nil {
		t.Fatalf("Start() restarted host: %v", err)
	}
	mcpDriver := &goldenMCPDriver{host: restarted}
	httpDriver := &goldenHTTPDriver{host: restarted}
	operation := waitForGoldenOperation(t, mcpDriver, accepted.ID)
	if operation.Status != hostCompletedStatus {
		t.Fatalf("restarted operation = %#v", operation)
	}
	httpOperation := httpDriver.Operation(t, accepted.ID)
	if httpOperation != operation {
		t.Fatalf("restart operation mismatch: MCP=%#v HTTP=%#v", operation, httpOperation)
	}
	replayed := httpDriver.Ingest(t, source)
	if replayed.ID != accepted.ID || replayed.Status != hostCompletedStatus {
		t.Fatalf("restart replay = %#v, want original completed operation", replayed)
	}
	logContent, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("ReadFile(log): %v", err)
	}
	if count := bytes.Count(logContent, []byte(accepted.ID)); count != 1 {
		t.Fatalf("restart operation log count = %d, want 1", count)
	}
}

func runGoldenLoop(t *testing.T, transport string) goldenOutcome {
	t.Helper()
	ctx := context.Background()
	workspace, config := newGoldenWorkspace(t)
	maintainer := &knowledgetest.Maintainer{}
	host, err := knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("NewHost(): %v", err)
	}
	defer shutdownHost(t, host)
	if err := host.Start(ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	var driver goldenDriver
	switch transport {
	case "mcp":
		driver = &goldenMCPDriver{host: host}
	case "http":
		driver = &goldenHTTPDriver{host: host}
	default:
		t.Fatalf("unknown golden transport %q", transport)
	}

	sources := knowledgetest.Sources()
	operationIDs := make([]domain.OperationID, len(sources))
	for index, source := range sources {
		ingested := driver.Ingest(t, source)
		if ingested.ID == "" || (ingested.Status != hostQueuedStatus && ingested.Status != hostCompletedStatus) {
			t.Fatalf("Ingest(%q) = %#v", source.ExpectedRef, ingested)
		}
		operationIDs[index] = ingested.ID
		if operation := waitForGoldenOperation(t, driver, ingested.ID); operation.Status != hostCompletedStatus {
			t.Fatalf("Operation(%q) = %#v", ingested.ID, operation)
		}
	}

	metrics, err := knowledgetest.Evaluate(ctx, driver.Retrieve)
	if err != nil || !metrics.Passed() {
		t.Fatalf("golden evaluation = %#v, err = %v", metrics, err)
	}
	snapshot, err := workspace.Snapshot(ctx, knowl.DefaultScope)
	if err != nil {
		t.Fatalf("Snapshot(): %v", err)
	}
	if err := knowledgetest.ValidateFinalSnapshot(snapshot); err != nil {
		t.Fatalf("ValidateFinalSnapshot(): %v", err)
	}
	inspection, err := workspace.Inspect(ctx, knowl.DefaultScope)
	if err != nil {
		t.Fatalf("Inspect(): %v", err)
	}
	if len(inspection.RawSources) != len(sources) {
		t.Fatalf("raw source count = %d, want %d", len(inspection.RawSources), len(sources))
	}
	for _, raw := range inspection.RawSources {
		if !raw.Valid {
			t.Fatalf("invalid raw source record: %#v", raw)
		}
	}

	callsBeforeReplay := maintainer.Calls()
	logBefore, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("ReadFile(log before replay): %v", err)
	}
	for index, source := range sources {
		replayed := driver.Ingest(t, source)
		if replayed.ID != operationIDs[index] || replayed.Status != hostCompletedStatus {
			t.Fatalf("replay %q = %#v, want %q completed", source.ExpectedRef, replayed, operationIDs[index])
		}
	}
	if maintainer.Calls() != callsBeforeReplay || callsBeforeReplay != len(sources) {
		t.Fatalf("maintainer calls after replay = %d, before = %d", maintainer.Calls(), callsBeforeReplay)
	}
	logAfter, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("ReadFile(log after replay): %v", err)
	}
	if !bytes.Equal(logBefore, logAfter) {
		t.Fatal("terminal replays changed the canonical operation log")
	}
	afterReplay, err := workspace.Snapshot(ctx, knowl.DefaultScope)
	if err != nil {
		t.Fatalf("Snapshot() after replay: %v", err)
	}
	if !reflect.DeepEqual(snapshot.PageDigests, afterReplay.PageDigests) {
		t.Fatalf("page digests changed after replay: before=%q after=%q", snapshot.PageDigests, afterReplay.PageDigests)
	}
	replayedMetrics, err := knowledgetest.Evaluate(ctx, driver.Retrieve)
	if err != nil || !replayedMetrics.Passed() || replayedMetrics.Hits != metrics.Hits {
		t.Fatalf("replayed evaluation = %#v, err = %v", replayedMetrics, err)
	}

	pageRefs := make(map[domain.PageID][]string, len(snapshot.Pages))
	for _, page := range snapshot.Pages {
		pageRefs[page.ID] = append([]string(nil), page.SourceRefs...)
	}
	return goldenOutcome{
		OperationIDs: operationIDs, Metrics: metrics,
		PageDigests: cloneStringMap(snapshot.PageDigests), PageRefs: pageRefs,
	}
}

func newGoldenWorkspace(t *testing.T) (*contentfs.Workspace, knowl.Config) {
	t.Helper()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("New workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("Init workspace: %v", err)
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.ListenAddr = hostListenAddr
	return workspace, config
}

func waitForGoldenOperation(t *testing.T, driver goldenDriver, id domain.OperationID) goldenOperation {
	t.Helper()
	deadline := time.Now().Add(knowledgetest.PublicPollDeadline)
	for time.Now().Before(deadline) {
		operation := driver.Operation(t, id)
		switch operation.Status {
		case hostCompletedStatus:
			return operation
		case hostFailedStatus:
			t.Fatalf("operation %q failed", id)
		}
		time.Sleep(knowledgetest.PublicPollInterval)
	}
	t.Fatalf("operation %q did not become terminal", id)
	return goldenOperation{}
}

type goldenMCPDriver struct{ host *knowl.Host }

func (driver *goldenMCPDriver) Ingest(t *testing.T, source knowledgetest.SourceFixture) goldenIngest {
	t.Helper()
	value, err := driver.host.MCP().Call(context.Background(), hostIngestToolName, map[string]any{
		hostSourceContentKey: source.Content, hostSourceOriginKey: source.Origin,
		hostSourceIdempotencyKeyName: source.Revision, "media_type": source.MediaType,
	})
	if err != nil {
		t.Fatalf("MCP ingest %q: %v", source.ExpectedRef, err)
	}
	result, ok := value.(mcp.IngestResult)
	if !ok {
		t.Fatalf("MCP ingest type = %T", value)
	}
	return goldenIngest{ID: result.OperationID, Status: result.Status}
}

func (driver *goldenMCPDriver) Operation(t *testing.T, id domain.OperationID) goldenOperation {
	t.Helper()
	value, err := driver.host.MCP().Call(context.Background(), hostOperationToolName, map[string]any{"id": string(id)})
	if err != nil {
		t.Fatalf("MCP operation %q: %v", id, err)
	}
	result, ok := value.(mcp.OperationResult)
	if !ok {
		t.Fatalf("MCP operation type = %T", value)
	}
	return goldenOperation{ID: result.ID, Status: result.Status}
}

func (driver *goldenMCPDriver) Retrieve(ctx context.Context, query string, _ domain.ReadLimits) ([]domain.PageReference, error) {
	value, err := driver.host.MCP().Call(ctx, hostRetrieveToolName, map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	result, ok := value.(mcp.RetrieveResult)
	if !ok {
		return nil, fmt.Errorf("MCP retrieve type = %T", value)
	}
	references := make([]domain.PageReference, len(result.Evidence))
	for index, evidence := range result.Evidence {
		references[index] = domain.PageReference{
			ID: evidence.PageID, Title: evidence.Title, Snippet: evidence.Snippet,
			SourceRefs: append([]string(nil), evidence.SourceRefs...), Untrusted: evidence.Untrusted,
		}
	}
	return references, nil
}

type goldenHTTPDriver struct{ host *knowl.Host }

func (driver *goldenHTTPDriver) Ingest(t *testing.T, source knowledgetest.SourceFixture) goldenIngest {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		hostSourceContentKey: source.Content, hostSourceOriginKey: source.Origin,
		hostSourceIdempotencyKeyName: source.Revision, "media_type": source.MediaType,
	})
	if err != nil {
		t.Fatalf("Marshal HTTP ingest: %v", err)
	}
	body, status, err := doHostRequest(t, driver.host, http.MethodPost, "/v1/ingest", payload)
	if err != nil || status != http.StatusOK {
		t.Fatalf("HTTP ingest %q = status %d, err %v, body %s", source.ExpectedRef, status, err, body)
	}
	var result struct {
		OperationID domain.OperationID `json:"operation_id"`
		Status      string             `json:"status"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Unmarshal HTTP ingest: %v", err)
	}
	return goldenIngest{ID: result.OperationID, Status: result.Status}
}

func (driver *goldenHTTPDriver) Operation(t *testing.T, id domain.OperationID) goldenOperation {
	t.Helper()
	body, status, err := doHostRequest(t, driver.host, http.MethodGet, "/v1/operations/"+url.PathEscape(string(id)), nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("HTTP operation %q = status %d, err %v, body %s", id, status, err, body)
	}
	var result struct {
		ID     domain.OperationID `json:"id"`
		Status string             `json:"status"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Unmarshal HTTP operation: %v", err)
	}
	return goldenOperation{ID: result.ID, Status: result.Status}
}

func (driver *goldenHTTPDriver) Retrieve(_ context.Context, query string, _ domain.ReadLimits) ([]domain.PageReference, error) {
	body, status, err := doGoldenHTTPRequest(driver.host, http.MethodGet, "/v1/retrieve?query="+url.QueryEscape(query), nil)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("HTTP retrieve %q = status %d, err %v, body %s", query, status, err, body)
	}
	var result struct {
		Evidence []struct {
			PageID     domain.PageID `json:"page_id"`
			Title      string        `json:"title"`
			Snippet    string        `json:"snippet"`
			SourceRefs []string      `json:"source_refs"`
			Untrusted  bool          `json:"untrusted"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal HTTP retrieve: %w", err)
	}
	references := make([]domain.PageReference, len(result.Evidence))
	for index, evidence := range result.Evidence {
		references[index] = domain.PageReference{
			ID: evidence.PageID, Title: evidence.Title, Snippet: evidence.Snippet,
			SourceRefs: append([]string(nil), evidence.SourceRefs...), Untrusted: evidence.Untrusted,
		}
	}
	return references, nil
}

func doGoldenHTTPRequest(host *knowl.Host, method, path string, body []byte) ([]byte, int, error) {
	request := httptest.NewRequest(method, "http://knowl"+path, bytes.NewReader(body))
	response := httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	return response.Body.Bytes(), response.Code, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
