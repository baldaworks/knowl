package webui

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"golang.org/x/net/html"
)

const activityOperationID = "op-1"
const activitySourceID = "docs"
const activityAccepted = "accepted-old"
const activityProcessed = "processed-old"
const activityHead = "head-new"
const activityMaintenanceKind = "maintenance"
const hxGetAttribute = "hx-get"
const htmlClassAttribute = "class"

type activityReader struct {
	manyOperations   bool
	similarOperations bool
	operationOptions []app.OperatorOperationReadOptions
	documentOptions  []app.OperatorReadOptions
}

func (f *activityReader) ListOperations(_ context.Context, scope domain.ScopeRef, o app.OperatorOperationReadOptions) (app.OperatorReadPage[domain.OperatorOperationSummary], error) {
	f.operationOptions = append(f.operationOptions, o)
	if scope != "trusted" {
		return app.OperatorReadPage[domain.OperatorOperationSummary]{}, app.ErrOperatorWorkspaceUnavailable
	}
	next := ""
	if o.Continuation.Key == "" {
		next, _ = app.EncodeOperatorOperationPosition(app.OperatorOperationPosition{CreatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), OperationID: activityOperationID})
	}
	items := []domain.OperatorOperationSummary{{ID: activityOperationID, Kind: activityMaintenanceKind, Status: domain.StatusApplying, SourceID: activitySourceID}}
	if f.similarOperations {
		items = []domain.OperatorOperationSummary{
			{ID: "operation-prefix-aaaaaaaaaaaaaaaa-first-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Kind: activityMaintenanceKind, Status: domain.StatusApplying, SourceID: activitySourceID},
			{ID: "operation-prefix-aaaaaaaaaaaaaaaa-second-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Kind: activityMaintenanceKind, Status: domain.StatusApplying, SourceID: activitySourceID},
		}
	}
	if f.manyOperations {
		items = append(items, domain.OperatorOperationSummary{ID: "op-new", Kind: activityMaintenanceKind, Status: domain.StatusApplying})
	}
	return app.OperatorReadPage[domain.OperatorOperationSummary]{Items: items, NextKey: next}, nil
}
func (*activityReader) ListSources(context.Context, domain.ScopeRef, app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorSourceSummary], error) {
	return app.OperatorReadPage[domain.OperatorSourceSummary]{Items: []domain.OperatorSourceSummary{activitySource()}}, nil
}
func (*activityReader) Source(context.Context, domain.ScopeRef, domain.SourceID) (domain.OperatorSourceSummary, error) {
	return activitySource(), nil
}
func activitySource() domain.OperatorSourceSummary {
	return domain.OperatorSourceSummary{ID: activitySourceID, Type: domain.SourceTypeGit, Enabled: false, Status: &domain.OperatorSourceStatus{Status: domain.SyncStatusFailed, LastAttemptAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), LastSuccessfulAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), MaintenanceCounts: domain.MaintenanceCounts{Queued: 1, Committed: 2, Failed: 1}}}
}
func (f *activityReader) ListSourceDocuments(_ context.Context, _ domain.ScopeRef, _ domain.SourceID, o app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorDocumentSummary], error) {
	f.documentOptions = append(f.documentOptions, o)
	if o.Continuation.Key != "" {
		return app.OperatorReadPage[domain.OperatorDocumentSummary]{Items: []domain.OperatorDocumentSummary{{ID: "deleted.md", Deleted: true, Revision: "head-old", AcceptedRevision: activityAccepted, MaintenanceRevision: activityProcessed, MaintenanceOperationID: "op-2", MaintenanceStatus: domain.StatusCommitted}}}, nil
	}
	return app.OperatorReadPage[domain.OperatorDocumentSummary]{Items: []domain.OperatorDocumentSummary{{ID: "guide.md", Revision: activityHead, AcceptedRevision: activityAccepted, MaintenanceRevision: activityProcessed, MaintenanceOperationID: activityOperationID, MaintenanceStatus: domain.StatusApplying}}, NextKey: "guide.md"}, nil
}
func activityHandler(t *testing.T, f *activityReader) *Handler {
	t.Helper()
	s, e := app.NewOperatorService("trusted", app.OperatorReaders{Operations: f, Sources: f, Documents: f}, app.OperatorOptions{})
	if e != nil {
		t.Fatal(e)
	}
	h, e := New(Dependencies{Operator: s, OperationStatus: func(status domain.OperationStatus) string {
		switch status {
		case domain.StatusApplying:
			return "running"
		case domain.StatusCommitted:
			return "completed"
		default:
			return "unavailable"
		}
	}, Operation: func(context.Context, string) (knowlapi.OperationResult, error) {
		return knowlapi.OperationResult{Id: activityOperationID, Status: "running", Details: &knowlapi.OperationDetails{Context: &knowlapi.OperationContextReport{WorkAttempt: 2, Outcome: knowlapi.Assembled, EntriesOmitted: 7, Budget: &knowlapi.ContextBudget{OmittedCount: 3}}, Execution: &knowlapi.OperationExecutionDetails{WorkAttempt: 3, RetryAttempt: 2, ApplyAttempt: 1, ManualRetryCount: 4}, Plan: &knowlapi.OperationPlanSummary{Digest: screenSnapshot}}}, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func markedText(doc *html.Node, attribute string) map[string]string {
	m := map[string]string{}
	walk(doc, func(n *html.Node) {
		for _, a := range n.Attr {
			if a.Key == attribute {
				m[a.Val] = nodeText(n)
			}
		}
	})
	return m
}
func continuation(doc *html.Node) string {
	var next string
	walk(doc, func(n *html.Node) {
		for _, a := range n.Attr {
			if a.Key == "data-next" {
				for _, b := range n.Attr {
					if b.Key == hxGetAttribute {
						next = b.Val
					}
				}
			}
		}
	})
	return next
}
func TestOperationFactsAndUnavailableReports(t *testing.T) {
	h := activityHandler(t, &activityReader{})
	r, doc := fragmentDocument(t, h, "/ui/fragments/operation?operation_id=op-1")
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	got := markedText(doc, "data-fact")
	for k, want := range map[string]string{"work-attempt": "3", "retry-attempt": "2", "apply-attempt": "1", "manual-retries": "4", "context-attempt": "2", "budget-omitted": "3", "report-omitted": "7", "plan-count": unavailableLabel, "correction": unavailableLabel, "retrieval": unavailableLabel} {
		if got[k] != want {
			t.Errorf("%s=%q want %q", k, got[k], want)
		}
	}
}

func TestOperationListDistinguishesOpaqueIDsWithSameEdges(t *testing.T) {
	h := activityHandler(t, &activityReader{similarOperations: true})
	r, doc := fragmentDocument(t, h, "/ui/fragments/operations")
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	var labels []string
	walk(doc, func(n *html.Node) {
		if n.Data != "button" {
			return
		}
		for _, a := range n.Attr {
			if a.Key == htmlClassAttribute && a.Val == "table-name operation-select" {
				labels = append(labels, nodeText(n))
			}
		}
	})
	if len(labels) != 2 || !strings.Contains(labels[0], "first") || !strings.Contains(labels[1], "second") {
		t.Fatalf("operation labels do not distinguish opaque IDs: %q", labels)
	}
}
func TestOperationContinuationPreservesFilters(t *testing.T) {
	f := &activityReader{}
	h := activityHandler(t, f)
	r, doc := fragmentDocument(t, h, "/ui/fragments/operations?status=applying&source_id=docs&limit=1")
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	next := continuation(doc)
	u, e := url.Parse(next)
	if e != nil || u.Query().Get("status") != "applying" || u.Query().Get("source_id") != activitySourceID || u.Query().Get("limit") != "1" {
		t.Fatalf("continuation=%q %v", next, e)
	}
	r, _ = fragmentDocument(t, h, next)
	if r.Code != 200 || len(f.operationOptions) != 2 || f.operationOptions[1].Continuation.Key == "" {
		t.Fatalf("continuation status=%d reads=%+v", r.Code, f.operationOptions)
	}
}

func TestOperationRefreshPreservesFiltersAndRestartsContinuation(t *testing.T) {
	f := &activityReader{}
	h := activityHandler(t, f)
	r, doc := fragmentDocument(t, h, operationsFragment+"?status=applying&source_id=docs&limit=1")
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	r, doc = fragmentDocument(t, h, continuation(doc))
	if r.Code != 200 {
		t.Fatalf("continuation status=%d", r.Code)
	}
	var refresh string
	walk(doc, func(n *html.Node) {
		if n.Data == "button" && nodeText(n) == "Refresh" {
			for _, a := range n.Attr {
				if a.Key == hxGetAttribute {
					refresh = a.Val
				}
			}
		}
	})
	u, err := url.Parse(refresh)
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{"status": {"applying"}, sourceIDParameter: {activitySourceID}, limitParameter: {"1"}}
	if u.Path != operationsFragment || !reflect.DeepEqual(u.Query(), want) {
		t.Fatalf("refresh query=%v want=%v", u.Query(), want)
	}
	r, _ = fragmentDocument(t, h, refresh)
	last := f.operationOptions[len(f.operationOptions)-1]
	if r.Code != 200 || last.Status != domain.StatusApplying || last.SourceID != activitySourceID || last.Limit != 1 || last.Continuation.Key != "" {
		t.Fatalf("refresh status=%d options=%+v", r.Code, last)
	}
}
func TestSourceSavedLifecycleAndDocumentContinuation(t *testing.T) {
	f := &activityReader{}
	h := activityHandler(t, f)
	r, doc := fragmentDocument(t, h, "/ui/fragments/source?source_id=docs&limit=1")
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	got := markedText(doc, "data-fact")
	for k, want := range map[string]string{"last-success": "2026-01-02 00:00 UTC", "head-revision": activityHead, "accepted-revision": activityAccepted, "maintenance-revision": activityProcessed, "upstream-state": "Present"} {
		if got[k] != want {
			t.Errorf("%s=%q want %q", k, got[k], want)
		}
	}
	next := continuation(doc)
	r, doc = fragmentDocument(t, h, next)
	if r.Code != 200 || len(f.documentOptions) != 2 {
		t.Fatalf("continuation=%q status=%d", next, r.Code)
	}
	if got := markedText(doc, "data-fact")["upstream-state"]; got != "Confirmed deletion" {
		t.Errorf("state=%q", got)
	}
}

func TestSourceSelectionLeadsWithDocumentsWithoutRepeatingListFacts(t *testing.T) {
	h := activityHandler(t, &activityReader{})
	r, list := fragmentDocument(t, h, "/ui/fragments/sources")
	if r.Code != 200 {
		t.Fatalf("list status=%d", r.Code)
	}
	if got := markedText(list, "data-fact")["sync-status"]; got != "failed" {
		t.Fatalf("listed sync status=%q", got)
	}
	r, detail := fragmentDocument(t, h, "/ui/fragments/source?source_id=docs")
	if r.Code != 200 {
		t.Fatalf("detail status=%d", r.Code)
	}
	if got := markedText(detail, "data-fact")["sync-status"]; got != "" {
		t.Errorf("selected detail repeated listed sync status=%q", got)
	}
	var documentTop, factsTop, selectedID, factsBeforeDocuments bool
	walk(detail, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		for _, a := range n.Attr {
			if a.Key == htmlClassAttribute && a.Val == "activity-table" {
				documentTop = true
			}
			if a.Key == htmlClassAttribute && a.Val == "source-card-facts" {
				factsTop = true
				factsBeforeDocuments = factsBeforeDocuments || !documentTop
			}
			if a.Key == htmlClassAttribute && a.Val == "card-title identity" && nodeText(n) == activitySourceID {
				selectedID = true
			}
		}
	})
	if factsBeforeDocuments {
		t.Error("source summary appears before saved documents")
	}
	if !documentTop || !factsTop || !selectedID {
		t.Fatalf("documents=%t facts=%t selected identity=%t", documentTop, factsTop, selectedID)
	}
	got := markedText(detail, "data-fact")
	if got["last-success"] != "2026-01-02 00:00 UTC" || got["head-revision"] != activityHead || got["accepted-revision"] != activityAccepted || got["maintenance-revision"] != activityProcessed {
		t.Errorf("saved and sync facts=%v", got)
	}
}
func TestActivityInputsRejectBeforeRead(t *testing.T) {
	for _, path := range []string{"operations?status=madeup", "operations?limit=101", "operations?source_id=../secret", "sources?cursor=forged", "source?source_id=../secret", "operation?operation_id=", "operation?operation_id=x&operation_id=y", "operation?operation_id=x&scope=other"} {
		t.Run(path, func(t *testing.T) {
			f := &activityReader{}
			h := activityHandler(t, f)
			r, _ := fragmentDocument(t, h, "/ui/fragments/"+path)
			if r.Code != 400 {
				t.Fatalf("status=%d", r.Code)
			}
			if !reflect.DeepEqual(f.operationOptions, []app.OperatorOperationReadOptions(nil)) || len(f.documentOptions) != 0 {
				t.Fatal("invalid request reached read port")
			}
		})
	}
}

func TestOperationZeroCountersRemainDistinctFromMissingReports(t *testing.T) {
	h := activityHandler(t, &activityReader{})
	zero := 0
	h.dependencies.Operation = func(context.Context, string) (knowlapi.OperationResult, error) {
		return knowlapi.OperationResult{Id: activityOperationID, Status: "completed", Details: &knowlapi.OperationDetails{Plan: &knowlapi.OperationPlanSummary{Digest: screenSnapshot, FileCount: &zero}, Correction: &knowlapi.OperationCorrectionReport{Corrections: &zero}}}, nil
	}
	r, doc := fragmentDocument(t, h, "/ui/fragments/operation?operation_id=op-1")
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	facts := markedText(doc, "data-fact")
	if facts["plan-count"] != "0" || facts["correction"] != "0" || facts["retrieval"] != unavailableLabel {
		t.Fatalf("facts=%v", facts)
	}
	h.dependencies.Operation = func(context.Context, string) (knowlapi.OperationResult, error) {
		return knowlapi.OperationResult{}, app.ErrOperationNotFound
	}
	r, _ = fragmentDocument(t, h, "/ui/fragments/operation?operation_id=op-1")
	if r.Code != 404 || r.Header().Get("X-Knowl-Error") != "operation_not_found" {
		t.Fatalf("status=%d headers=%v", r.Code, r.Header())
	}
}
