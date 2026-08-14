package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
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
	schema, err := workspace.Schema(ctx, "local")
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
	handler := NewHandler(Dependencies{Scope: "local", Ingest: ingest, Query: query, Waker: waker})
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

	claim, err := store.ClaimReady(ctx, "local", domain.WorkLease{Token: "test-worker", ExpiresAt: time.Now().Add(time.Minute)})
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
