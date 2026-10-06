//go:build browser

package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestBrowserOperationsSources(t *testing.T) {
	var operationStatus atomic.Value
	operationStatus.Store(knowlapi.OperationResultStatusRunning)
	reader := &responsiveActivityReader{&activityReader{manyOperations: true}}
	h := activityHandler(t, reader.activityReader)
	operator, err := app.NewOperatorService("trusted", app.OperatorReaders{Operations: reader, Sources: reader, Documents: reader}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h.dependencies.Operator = operator
	h.dependencies.Operation = func(_ context.Context, id string) (knowlapi.OperationResult, error) {
		if id == "op-10" {
			return knowlapi.OperationResult{}, app.ErrOperationNotFound
		}
		return knowlapi.OperationResult{Id: id, Status: operationStatus.Load().(knowlapi.OperationResultStatus), UpdatedAt: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC), Details: &knowlapi.OperationDetails{Retrieval: &knowlapi.RetrievalReport{Effective: "lexical"}, Execution: &knowlapi.OperationExecutionDetails{WorkAttempt: 2, RetryAttempt: 1}, Context: &knowlapi.OperationContextReport{Version: 1, WorkAttempt: 1, Outcome: knowlapi.Assembled, EntriesOmitted: 4, Budget: &knowlapi.ContextBudget{MaxBytes: 4096, IncludedCount: 3, OmittedCount: 2}}, Correction: &knowlapi.OperationCorrectionReport{WorkAttempt: 1, Outcome: "accepted", MaxCorrections: 2}, Plan: &knowlapi.OperationPlanSummary{Digest: screenSnapshot}}}, nil
	}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture/status" {
			operationStatus.Store(knowlapi.OperationResultStatus(r.URL.Query().Get("value")))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/ui/fragments/") {
			if r.Header.Get("Authorization") != "Bearer browser-secret-token" {
				h.Error(w, 401, "unauthorized")
				return
			}
			h.Fragments(w, r)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer host.Close()
	script, e := filepath.Abs("../../tools/webui-browser/activity.cjs")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(t.Context(), "node", script)
	cmd.Env = append(os.Environ(), "KNOWL_BROWSER_URL="+host.URL)
	output, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("browser: %v\n%s", e, output)
	}
	t.Log(string(output))
}

type responsiveActivityReader struct{ *activityReader }

func (*responsiveActivityReader) ListSources(context.Context, domain.ScopeRef, app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorSourceSummary], error) {
	second := activitySource()
	second.ID = "other-docs"
	items := []domain.OperatorSourceSummary{activitySource()}
	for _, id := range []domain.SourceID{"docs-2", "docs-3", "docs-4", "docs-5", "docs-6", "docs-7"} {
		source := activitySource()
		source.ID = id
		items = append(items, source)
	}
	return app.OperatorReadPage[domain.OperatorSourceSummary]{Items: append(items, second)}, nil
}

func (f *responsiveActivityReader) ListOperations(ctx context.Context, scope domain.ScopeRef, o app.OperatorOperationReadOptions) (app.OperatorReadPage[domain.OperatorOperationSummary], error) {
	result, err := f.activityReader.ListOperations(ctx, scope, o)
	for _, id := range []domain.OperationID{"op-3", "op-4", "op-5", "op-6", "op-7", "op-8", "op-9", "op-10"} {
		if len(result.Items) >= o.Limit {
			break
		}
		result.Items = append(result.Items, domain.OperatorOperationSummary{ID: id, Kind: "maintenance", Status: domain.StatusApplying, SourceID: activitySourceID})
	}
	return result, err
}
func (*responsiveActivityReader) Source(_ context.Context, _ domain.ScopeRef, id domain.SourceID) (domain.OperatorSourceSummary, error) {
	if id == "docs-7" {
		return domain.OperatorSourceSummary{}, app.ErrSourceNotFound
	}
	source := activitySource()
	source.ID = id
	return source, nil
}
