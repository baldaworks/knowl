package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	httpTestScope       = "local"
	httpTestEngineering = "engineering"
	httpTestOperations  = "operations"
	httpTransportQuery  = "transportbeacon"
)

func TestHTTPIngestWakesNewAndNonTerminalReplayOnly(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(workspace.Root(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	schema, err := workspace.Schema(ctx, httpTestScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	maintainer := &httpCountingMaintainer{plan: domain.ModelEditPlan{
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{"inline:source-1@1"},
		Edits: []domain.FileEdit{{
			Path:    "wiki/entities/one.md",
			Content: []byte("---\nid: entities/one\ntitle: One\ntype: entity\nsource_refs:\n  - inline:source-1@1\n---\n# One\n"),
		}},
	}}
	ingest, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}
	query, err := app.NewQueryService(workspace, store, store, ingest, app.QueryOptions{})
	if err != nil {
		t.Fatalf("new query service: %v", err)
	}
	waker := &httpRecordingWaker{}
	handler := NewHandler(Dependencies{Scope: httpTestScope, Ingest: ingest, Query: query, Waker: waker})
	body := []byte(`{"content":"source","origin":"source-1","idempotency_key":"1"}`)

	first := postIngest(t, handler, body)
	second := postIngest(t, handler, body)
	if first.OperationID == "" || second.OperationID != first.OperationID || first.Status != httpQueuedStatus || second.Status != httpQueuedStatus {
		t.Fatalf("ingest results = %#v / %#v, want same queued operation", first, second)
	}
	if ids := waker.IDs(); len(ids) != 2 || ids[0] != first.OperationID || ids[1] != first.OperationID {
		t.Fatalf("wake IDs = %#v, want new and non-terminal replay", ids)
	}
	if maintainer.Calls() != 0 {
		t.Fatalf("HTTP transport invoked maintainer %d times", maintainer.Calls())
	}

	claim, err := store.ClaimReady(ctx, httpTestScope, domain.WorkLease{Token: "test-worker", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	if result, err := ingest.RunToTerminal(ctx, claim); err != nil || result.Operation.Status != domain.StatusCommitted {
		t.Fatalf("run operation = %#v, err = %v", result, err)
	}
	terminal := postIngest(t, handler, body)
	if terminal.Status != "completed" || terminal.OperationID != first.OperationID {
		t.Fatalf("terminal replay = %#v, want completed operation", terminal)
	}
	if ids := waker.IDs(); len(ids) != 2 {
		t.Fatalf("terminal replay emitted wake: %#v", ids)
	}
}

func TestHTTPRetrieveBindsRepeatableSourceFilterAndMapsEvidence(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(workspace.Root(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Rebuild(ctx, httpTransportSearchSnapshot()); err != nil {
		t.Fatal(err)
	}
	ingest, err := app.NewIngestService(workspace, store, store, &httpCountingMaintainer{}, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	query, err := app.NewQueryService(workspace, store, store, nil, app.QueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Dependencies{Scope: httpTestScope, Ingest: ingest, Query: query, Waker: &httpRecordingWaker{}})

	request := httptest.NewRequest(http.MethodGet, "/v1/retrieve?query=transportbeacon&source=operations&source=engineering", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("retrieve status = %d, body = %s", response.Code, response.Body.String())
	}
	var result knowlapi.RetrieveResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 2 {
		t.Fatalf("filtered HTTP evidence = %#v", result.Evidence)
	}
	bySource := make(map[string]knowlapi.EvidenceItem, len(result.Evidence))
	for _, evidence := range result.Evidence {
		if evidence.SourceId != nil {
			bySource[*evidence.SourceId] = evidence
		}
	}
	engineering := bySource[httpTestEngineering]
	if len(bySource) != 2 || bySource[httpTestOperations].SourceId == nil || engineering.DocumentId == nil || *engineering.DocumentId != "shared.md" || engineering.Revision == nil || *engineering.Revision != "revision-1" || engineering.Uri == nil || *engineering.Uri == "" {
		t.Fatalf("filtered HTTP evidence = %#v", result.Evidence)
	}
	metadata := engineering.Okf
	if metadata == nil || metadata.Type != "Reference" || metadata.Executor == nil || metadata.Attester == nil || metadata.Extensions == nil || (*metadata.Extensions)["unknown_nested"] == nil || !engineering.Untrusted {
		t.Fatalf("HTTP OKF evidence = %#v", engineering)
	}

	invalid := httptest.NewRequest(http.MethodGet, "/v1/retrieve?query=transportbeacon&source=Engineering", nil)
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest || !strings.Contains(invalidResponse.Body.String(), `"error":"invalid_source_filter"`) {
		t.Fatalf("invalid filter response = %d, %s", invalidResponse.Code, invalidResponse.Body.String())
	}
}

func httpTransportSearchSnapshot() domain.WorkspaceSnapshot {
	document := func(sourceID domain.SourceID) *domain.SourceDocument {
		return &domain.SourceDocument{SourceID: sourceID, DocumentID: "shared.md", Revision: "revision-1", URI: "file:///" + string(sourceID) + "/shared.md"}
	}
	body := httpTransportQuery + " user body"
	metadata := &okf.Metadata{
		Type: "Reference", Title: "Transportbeacon Engineering", Runtime: "python",
		Computation: "https://127.0.0.1:1/inert-computation",
		Executor:    &okf.Executor{Resource: "https://127.0.0.1:1/inert-executor", Receipt: []string{"sha256"}},
		Attester:    &okf.Attester{Resource: "https://127.0.0.1:1/inert-attester"},
		Extensions:  map[string]any{"unknown_nested": map[string]any{"enabled": true}},
	}
	return domain.WorkspaceSnapshot{Scope: httpTestScope, Pages: []domain.PageSnapshot{
		{ID: "curated", Path: "wiki/curated.md", Title: "Transportbeacon Curated", Content: httpTransportQuery, Digest: "curated"},
		{ID: httpTestEngineering, Path: "wiki/sources/engineering/shared.md", Title: "Transportbeacon Engineering", Content: body, Body: body, OKF: metadata, Digest: httpTestEngineering, SourceDocument: document(httpTestEngineering)},
		{ID: httpTestOperations, Path: "wiki/sources/operations/shared.md", Title: "Transportbeacon Operations", Content: httpTransportQuery, Digest: httpTestOperations, SourceDocument: document(httpTestOperations)},
	}}
}

func postIngest(t *testing.T, handler http.Handler, body []byte) httpIngestResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, body = %s", response.Code, response.Body.String())
	}
	var result httpIngestResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode ingest response: %v", err)
	}
	return result
}

type httpRecordingWaker struct {
	mu  sync.Mutex
	ids []domain.OperationID
}

func (waker *httpRecordingWaker) Wake(id domain.OperationID) {
	waker.mu.Lock()
	defer waker.mu.Unlock()
	waker.ids = append(waker.ids, id)
}

func (waker *httpRecordingWaker) IDs() []domain.OperationID {
	waker.mu.Lock()
	defer waker.mu.Unlock()
	return append([]domain.OperationID(nil), waker.ids...)
}

type httpCountingMaintainer struct {
	mu      sync.Mutex
	plan    domain.ModelEditPlan
	counter int
}

func (maintainer *httpCountingMaintainer) Plan(context.Context, domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	maintainer.counter++
	return maintainer.plan, nil
}

func (maintainer *httpCountingMaintainer) Calls() int {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	return maintainer.counter
}
