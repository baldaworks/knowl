package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const retrievalTestSpaceHash = "0123456789abcdef"

type reportedContextIndex struct {
	app.SearchIndex
	report knowl.RetrievalReport
	err    error
}

func (index reportedContextIndex) SearchWithReport(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, knowl.RetrievalReport, error) {
	refs, err := index.Search(ctx, scope, query, limits, sources)
	return refs, index.report, err
}

func (index reportedContextIndex) SelectContextWithReport(ctx context.Context, scope knowl.ScopeRef, source knowl.SourceSummary, limits knowl.ReadLimits) ([]knowl.PageID, knowl.RetrievalReport, error) {
	if index.err != nil {
		return nil, index.report, index.err
	}
	ids, err := index.SelectContext(ctx, scope, source, limits)
	return ids, index.report, err
}

type checkingReportMaintainer struct {
	app.Maintainer
	before func()
}

func (maintainer checkingReportMaintainer) Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	maintainer.before()
	return maintainer.Maintainer.Plan(ctx, input)
}

func TestIngestPersistsRetrievalBeforePlanningAndOnSelectionFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "planned", true: "selection failed"}[failed], func(t *testing.T) {
			ctx := t.Context()
			workspace, store, _, maintainer := newWorkflow(t, false, nil)
			report := knowl.RetrievalReport{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalDegraded, Reason: knowl.RetrievalUnavailable, ModelSpace: retrievalTestSpaceHash}
			index := reportedContextIndex{SearchIndex: store, report: report}
			if failed {
				index.report.Effective = knowl.RetrievalFailed
				index.err = &app.EmbeddingError{Code: knowl.RetrievalUnavailable}
			}
			var operationID knowl.OperationID
			check := func() {
				operation, err := store.Operation(ctx, testSourceScope, operationID)
				if err != nil || !reflect.DeepEqual(operation.Retrieval, &index.report) {
					t.Fatalf("before inference: operation=%#v err=%v", operation, err)
				}
			}
			service, err := app.NewIngestService(workspace, store, index, checkingReportMaintainer{Maintainer: maintainer, before: check}, app.IngestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			submission, err := service.Submit(ctx, sourceEnvelope([]byte("source text")))
			if err != nil {
				t.Fatal(err)
			}
			operationID = submission.Operation.ID
			result, err := service.Execute(ctx, submission)
			if failed && !errors.Is(err, app.ErrEmbedding) || !failed && err != nil {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			check()
			if !reflect.DeepEqual(result.Operation.Retrieval, &index.report) {
				t.Fatalf("result report=%#v", result.Operation.Retrieval)
			}
			if failed && maintainer.calls() != 0 {
				t.Fatal("failed selection invoked maintainer")
			}
		})
	}
}

type reportlessOperations struct{ app.OperationStore }
type refusingReportOperations struct {
	app.OperationStore
	failure error
}

func (store refusingReportOperations) SaveRetrievalReport(context.Context, knowl.ScopeRef, knowl.OperationID, int, knowl.RetrievalReport) error {
	return store.failure
}

func TestIngestReportPersistenceFailureStopsInference(t *testing.T) {
	workspace, store, _, maintainer := newWorkflow(t, false, nil)
	refusal := errors.New("report storage unavailable")
	report := knowl.RetrievalReport{Requested: knowl.RetrievalLexical, Effective: knowl.RetrievalLexical}
	index := reportedContextIndex{SearchIndex: store, report: report}
	service, err := app.NewIngestService(workspace, refusingReportOperations{OperationStore: store, failure: refusal}, index, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Ingest(t.Context(), sourceEnvelope([]byte("source text")))
	if !errors.Is(err, refusal) || maintainer.calls() != 0 || result.Operation.Retrieval != nil || !reflect.DeepEqual(result.Retrieval, &report) {
		t.Fatalf("result=%+v calls=%d err=%v", result, maintainer.calls(), err)
	}
}

func TestIngestCustomStoreRetainsTransientReport(t *testing.T) {
	workspace, store, _, maintainer := newWorkflow(t, false, nil)
	report := knowl.RetrievalReport{Requested: knowl.RetrievalLexical, Effective: knowl.RetrievalLexical}
	service, err := app.NewIngestService(workspace, reportlessOperations{store}, reportedContextIndex{SearchIndex: store, report: report}, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Ingest(t.Context(), sourceEnvelope([]byte("source text")))
	if err != nil || result.Operation.Retrieval != nil || !reflect.DeepEqual(result.Retrieval, &report) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestIngestCancelledSelectionStillPersistsAttemptWithoutSuccess(t *testing.T) {
	workspace, store, _, maintainer := newWorkflow(t, false, nil)
	ctx, cancel := context.WithCancel(t.Context())
	report := knowl.RetrievalReport{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalFailed, Reason: knowl.RetrievalDeadline, ModelSpace: retrievalTestSpaceHash}
	index := cancelledReportIndex{SearchIndex: store, report: report, cancel: cancel}
	service, err := app.NewIngestService(workspace, store, index, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Ingest(ctx, sourceEnvelope([]byte("source text")))
	if !errors.Is(err, context.Canceled) || maintainer.calls() != 0 || result.Operation.Status != knowl.StatusReceived {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	operation, err := store.Operation(t.Context(), testSourceScope, result.Operation.ID)
	if err != nil || !reflect.DeepEqual(operation.Retrieval, &report) {
		t.Fatalf("operation=%+v err=%v", operation, err)
	}
}

type cancelledReportIndex struct {
	app.SearchIndex
	report knowl.RetrievalReport
	cancel context.CancelFunc
}

func (index cancelledReportIndex) SearchWithReport(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, knowl.RetrievalReport, error) {
	refs, err := index.Search(ctx, scope, query, limits, sources)
	return refs, index.report, err
}
func (index cancelledReportIndex) SelectContextWithReport(context.Context, knowl.ScopeRef, knowl.SourceSummary, knowl.ReadLimits) ([]knowl.PageID, knowl.RetrievalReport, error) {
	index.cancel()
	return nil, index.report, context.Canceled
}

type queryTestEmbeddings struct{}

func (queryTestEmbeddings) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestQueryServiceReturnsOriginalSemanticEvidenceAndReport(t *testing.T) {
	workspace, operations, _, _ := newWorkflow(t, false, nil)
	index, err := sqlite.Open(t.Context(), t.TempDir()+"/hybrid.sqlite", app.EmbeddingOptions{Provider: queryTestEmbeddings{}, Space: app.EmbeddingSpace{Model: "query-fixture", Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	snapshot := knowl.WorkspaceSnapshot{Scope: testSourceScope, SchemaDigest: "semantic-query-schema", Pages: []knowl.PageSnapshot{{ID: "semantic", Path: "wiki/semantic.md", Title: "Original canonical title", Body: "Canonical evidence", Digest: "page", SourceRefs: []string{testSourceRef}}}}
	if err := index.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	query, err := app.NewQueryService(workspace, operations, index, nil, app.QueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := query.Query(t.Context(), testSourceScope, "unrelatedvocabulary", knowl.ReadLimits{Pages: 1}, nil)
	if err != nil || result.Retrieval == nil || result.Retrieval.Effective != knowl.RetrievalHybrid || len(result.Pages) != 1 || result.Pages[0].ID != "semantic" || len(result.Citations) != 2 || result.Citations[0].SourceRef != testSourceRef {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

type cancelOnceReportMaintainer struct {
	app.Maintainer
	first bool
}

func (m *cancelOnceReportMaintainer) Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	if !m.first {
		m.first = true
		return knowl.ModelEditPlan{}, context.Canceled
	}
	return m.Maintainer.Plan(ctx, input)
}
func TestSynchronousCancelledAttemptRetriesWithNewSelection(t *testing.T) {
	workspace, store, _, maintainer := newWorkflow(t, false, nil)
	index := &reportedContextIndex{SearchIndex: store, report: knowl.RetrievalReport{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalDegraded, Reason: knowl.RetrievalUnavailable, ModelSpace: retrievalTestSpaceHash}}
	service, err := app.NewIngestService(workspace, store, index, &cancelOnceReportMaintainer{Maintainer: maintainer}, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := service.Submit(t.Context(), sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Execute(t.Context(), sub)
	if !errors.Is(err, context.Canceled) || first.Operation.Status != knowl.StatusReceived {
		t.Fatalf("first=%+v err=%v", first.Operation, err)
	}
	index.report.Effective = knowl.RetrievalHybrid
	index.report.Reason = ""
	second, err := service.Execute(t.Context(), sub)
	if first.Operation.WorkAttempt != 1 || second.Operation.WorkAttempt != 2 || second.Operation.RetrievalAttempt != 2 {
		t.Fatalf("attempts first=%+v second=%+v", first.Operation, second.Operation)
	}
	if err != nil {
		t.Fatalf("recoverable synchronous cancellation could not retry selection: status=%s err=%v", second.Operation.Status, err)
	}
}
