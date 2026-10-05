package app_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type contextCheckingMaintainer struct {
	*wrappedBudgetMaintainer
	before  func(knowl.MaintenanceInput)
	failure error
}

func (m contextCheckingMaintainer) Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	m.before(input)
	if m.failure != nil {
		m.calls++
		return knowl.ModelEditPlan{}, m.failure
	}
	return m.wrappedBudgetMaintainer.Plan(ctx, input)
}

type recordingContextOperations struct {
	app.OperationStore
	writes  int
	failure error
}

func (s *recordingContextOperations) SaveOperationContextReport(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, attempt int, report knowl.OperationContextReport) error {
	s.writes++
	if s.failure != nil {
		return s.failure
	}
	return s.OperationStore.(app.OperationContextReportStore).SaveOperationContextReport(ctx, scope, id, attempt, report)
}

func TestIngestContextIsSavedBeforeFailedPlanAndReplayedWithoutInference(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	seedBaselineContext(t, workspace, store)
	ops := &recordingContextOperations{OperationStore: store}
	sentinel := errors.New("provider fixture failure")
	var operationID knowl.OperationID
	maintainer := contextCheckingMaintainer{wrappedBudgetMaintainer: &wrappedBudgetMaintainer{cap: 12000}, failure: sentinel}
	maintainer.before = func(input knowl.MaintenanceInput) {
		operation, err := store.Operation(t.Context(), input.Scope, operationID)
		if err != nil || operation.Context == nil || operation.Context.Outcome != knowl.ContextAssembled || operation.Context.WorkAttempt != operation.WorkAttempt || operation.Plan != nil || ops.writes != 1 {
			t.Fatalf("before inference: %+v %v", operation.Context, err)
		}
		var included []knowl.PageID
		for _, page := range operation.Context.Pages {
			if page.Disposition == knowl.ContextIncluded {
				included = append(included, page.PageID)
			}
		}
		var actual []knowl.PageID
		for _, page := range input.Pages {
			actual = append(actual, page.ID)
		}
		if !reflect.DeepEqual(included, actual) {
			t.Fatalf("reported pages=%v actual=%v", included, actual)
		}
		used, err := maintainer.RequestBytes(t.Context(), input)
		if err != nil || operation.Context.Budget.UsedBytes == nil || *operation.Context.Budget.UsedBytes != used {
			t.Fatal("budget differs from actual request")
		}
	}
	service, err := app.NewIngestService(workspace, ops, store, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	submission, err := service.Submit(t.Context(), sourceEnvelope([]byte("storage evidence")))
	if err != nil {
		t.Fatal(err)
	}
	operationID = submission.Operation.ID
	result, err := service.Execute(t.Context(), submission)
	if !errors.Is(err, sentinel) || result.Operation.Context == nil || ops.writes != 1 || maintainer.calls != 1 || result.Operation.Status != knowl.StatusFailed {
		t.Fatalf("result=%+v writes=%d calls=%d err=%v", result.Operation.Context, ops.writes, maintainer.calls, err)
	}
	replayed, err := service.Execute(t.Context(), submission)
	if err != nil || ops.writes != 1 || maintainer.calls != 1 || !reflect.DeepEqual(replayed.Operation.Context, result.Operation.Context) {
		t.Fatalf("reconstructed terminal report: %+v %v", replayed.Operation.Context, err)
	}
}

func TestIngestContextSaveFailureStopsInferenceAndPreservesAssemblyReason(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(map[bool]string{false: "assembled", true: "required_overflow"}[overflow], func(t *testing.T) {
			workspace, store, _, _ := newBaselineIngest(t)
			sentinel := errors.New("context storage unavailable")
			ops := &recordingContextOperations{OperationStore: store, failure: sentinel}
			requestCap := 12000
			if overflow {
				requestCap = 100
			}
			maintainer := &wrappedBudgetMaintainer{cap: requestCap}
			service, err := app.NewIngestService(workspace, ops, store, maintainer, app.IngestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Ingest(t.Context(), sourceEnvelope([]byte("indispensable source")))
			if !errors.Is(err, sentinel) || ops.writes != 1 || maintainer.calls != 0 || result.Operation.Context != nil {
				t.Fatalf("writes=%d calls=%d context=%+v err=%v", ops.writes, maintainer.calls, result.Operation.Context, err)
			}
			if overflow {
				info, ok := app.ClassifyExecutionFailure(err)
				if !errors.Is(err, app.ErrMaintenanceInputLimit) || !ok || info.Reason != budgetRequiredLimitReason || result.Operation.Failure.Reason != info.Reason {
					t.Fatalf("original assembly reason lost: %+v %v", info, err)
				}
			}
		})
	}
}

// This adapter can measure a request even when it exceeds its declared cap.
type measuringOverflowMaintainer struct{ *wrappedBudgetMaintainer }

func (m measuringOverflowMaintainer) RequestBytes(ctx context.Context, input knowl.MaintenanceInput) (int, error) {
	input.InputLimits.MaxRequestBytes = app.MaxMaintenanceRequestBytes
	return m.wrappedBudgetMaintainer.RequestBytes(ctx, input)
}

func TestIngestContextRetainsKnownRequiredOverflow(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	maintainer := measuringOverflowMaintainer{&wrappedBudgetMaintainer{cap: 100}}
	service, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Ingest(t.Context(), sourceEnvelope([]byte("required source")))
	report := result.Operation.Context
	if !errors.Is(err, app.ErrMaintenanceInputLimit) || maintainer.calls != 0 || report == nil || report.Outcome != knowl.ContextAssemblyFailed || report.Budget == nil || report.Budget.UsedBytes == nil || *report.Budget.UsedBytes <= report.Budget.MaxBytes {
		t.Fatalf("known overflow: %+v %v", report, err)
	}
}

type diagnosticFailureIndex struct{ reportedContextIndex }

func (i diagnosticFailureIndex) SelectContextWithDiagnostics(ctx context.Context, scope knowl.ScopeRef, source knowl.SourceSummary, limits knowl.ReadLimits) ([]knowl.PageID, knowl.RetrievalReport, knowl.ContextSelectionDiagnostics, error) {
	ids, report, err := i.SelectContextWithReport(ctx, scope, source, limits)
	return ids, report, knowl.ContextSelectionDiagnostics{VectorProjection: &knowl.VectorProjectionStatus{State: knowl.VectorInvalid, Reason: knowl.RetrievalProjectionNotReady}}, err
}

func TestIngestContextRetainsStrictProjectionFailureWithoutInventingAssembly(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	maintainer := &wrappedBudgetMaintainer{cap: 12000}
	ops := &recordingContextOperations{OperationStore: store}
	index := diagnosticFailureIndex{reportedContextIndex{SearchIndex: store, report: knowl.RetrievalReport{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalFailed, Reason: knowl.RetrievalProjectionNotReady, ModelSpace: retrievalTestSpaceHash}, err: &app.EmbeddingError{Code: knowl.RetrievalProjectionNotReady}}}
	service, err := app.NewIngestService(workspace, ops, index, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Ingest(t.Context(), sourceEnvelope([]byte(budgetSourceComponent)))
	report := result.Operation.Context
	if !errors.Is(err, app.ErrEmbedding) || maintainer.calls != 0 || ops.writes != 1 || report == nil || report.Outcome != knowl.ContextSelectionFailed || report.CandidateCount != nil || report.CatalogCount != nil || report.Budget != nil || report.VectorProjection == nil || report.VectorProjection.State != knowl.VectorInvalid || report.VectorProjection.Reason != knowl.RetrievalProjectionNotReady {
		t.Fatalf("selection evidence: %+v writes=%d err=%v", report, ops.writes, err)
	}
}

func TestIngestContextWithoutWriterRemainsTransient(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	maintainer := &wrappedBudgetMaintainer{cap: 12000}
	service, err := app.NewIngestService(workspace, reportlessOperations{store}, orderedBudgetIndex{SearchIndex: store}, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Ingest(t.Context(), sourceEnvelope([]byte(budgetSourceComponent)))
	if err != nil || result.Context == nil || result.Context.Outcome != knowl.ContextAssembled || result.Operation.Context != nil || maintainer.calls != 1 {
		t.Fatalf("transient report: %+v err=%v", result.Context, err)
	}
	operation, err := store.Operation(t.Context(), result.Operation.Key.Scope, result.Operation.ID)
	if err != nil || operation.Context != nil {
		t.Fatalf("custom store claimed persistence: %+v %v", operation.Context, err)
	}
}

type failingContextContent struct {
	app.ContentStore
	stage string
	err   error
	reads int
}

func (c *failingContextContent) ReadSource(ctx context.Context, source knowl.AcceptedSource, limits knowl.ReadLimits) ([]byte, error) {
	if c.stage == budgetSourceComponent {
		return nil, c.err
	}
	return c.ContentStore.ReadSource(ctx, source, limits)
}

func (c *failingContextContent) Inspect(ctx context.Context, scope knowl.ScopeRef) (knowl.WorkspaceInspection, error) {
	if c.stage == budgetCatalogComponent {
		return knowl.WorkspaceInspection{}, c.err
	}
	return c.ContentStore.Inspect(ctx, scope)
}

func (c *failingContextContent) ReadPages(ctx context.Context, scope knowl.ScopeRef, ids []knowl.PageID, limits knowl.ReadLimits) ([]knowl.PageSnapshot, error) {
	c.reads++
	if c.stage == "second_page" && c.reads == 2 {
		return nil, c.err
	}
	return c.ContentStore.ReadPages(ctx, scope, ids, limits)
}

type recordingSizerMaintainer struct {
	*wrappedBudgetMaintainer
	lastSize int
}

func (m *recordingSizerMaintainer) RequestBytes(ctx context.Context, input knowl.MaintenanceInput) (int, error) {
	size, err := m.wrappedBudgetMaintainer.RequestBytes(ctx, input)
	if err == nil {
		m.lastSize = size
	}
	return size, err
}

func TestIngestContextKeepsOnlyMeasuredFactsOnEarlyAndPartialFailures(t *testing.T) {
	for _, stage := range []string{budgetSourceComponent, budgetCatalogComponent, "second_page"} {
		t.Run(stage, func(t *testing.T) {
			workspace, store, _, _ := newBaselineIngest(t)
			seedBaselineContext(t, workspace, store)
			sentinel := errors.New("content fixture failure")
			content := &failingContextContent{ContentStore: workspace, stage: stage, err: sentinel}
			ops := &recordingContextOperations{OperationStore: store}
			maintainer := &recordingSizerMaintainer{wrappedBudgetMaintainer: &wrappedBudgetMaintainer{cap: 12000}}
			service, err := app.NewIngestService(content, ops, orderedBudgetIndex{store, []knowl.PageID{budgetStorageID, budgetSmallID}}, maintainer, app.IngestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Ingest(t.Context(), sourceEnvelope([]byte(budgetSourceComponent)))
			if !errors.Is(err, sentinel) || maintainer.calls != 0 {
				t.Fatalf("calls=%d err=%v", maintainer.calls, err)
			}
			report := result.Operation.Context
			if stage == budgetSourceComponent {
				if report != nil || ops.writes != 0 {
					t.Fatal("source failure invented selection")
				}
				return
			}
			if report == nil || ops.writes != 1 || report.Outcome != knowl.ContextAssemblyFailed || report.CandidateCount == nil || *report.CandidateCount != 2 || len(report.Pages) != 2 {
				t.Fatalf("partial report=%+v writes=%d", report, ops.writes)
			}
			if stage == budgetCatalogComponent {
				if report.CatalogCount != nil || report.Budget != nil || report.Pages[0].Disposition != knowl.ContextPending || report.Pages[1].Disposition != knowl.ContextPending {
					t.Fatal("catalog failure invented fitting")
				}
				return
			}
			if report.Budget == nil || report.Budget.UsedBytes == nil || *report.Budget.UsedBytes != maintainer.lastSize || report.Budget.IncludedCount != 1 || report.Budget.OmittedCount != 0 || report.Pages[0].Disposition != knowl.ContextIncluded || report.Pages[1].Disposition != knowl.ContextPending {
				t.Fatalf("last accepted prefix lost: %+v", report)
			}
		})
	}
}

type syntheticPageContent struct{ app.ContentStore }

func (syntheticPageContent) ReadPages(_ context.Context, _ knowl.ScopeRef, ids []knowl.PageID, _ knowl.ReadLimits) ([]knowl.PageSnapshot, error) {
	return []knowl.PageSnapshot{{ID: ids[0], Path: "wiki/" + string(ids[0]) + ".md", Content: "# Fixture\n", Body: "Fixture"}}, nil
}

func TestIngestContextTelemetryOmissionDoesNotRemoveModelPages(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	ids := make([]knowl.PageID, 20)
	for i := range ids {
		ids[i] = knowl.PageID(fmt.Sprintf("entities/%s-%02d", strings.Repeat("a", 1800), i))
	}
	sentinel := errors.New("stop after captured input")
	maintainer := contextCheckingMaintainer{wrappedBudgetMaintainer: &wrappedBudgetMaintainer{cap: app.MaxMaintenanceRequestBytes}, failure: sentinel, before: func(input knowl.MaintenanceInput) {
		if len(input.Pages) != len(ids) {
			t.Fatalf("telemetry bounds changed model page count: %d", len(input.Pages))
		}
		for i, page := range input.Pages {
			if page.ID != ids[i] {
				t.Fatal("telemetry bounds changed model page order")
			}
		}
	}}
	service, err := app.NewIngestService(syntheticPageContent{workspace}, store, orderedBudgetIndex{store, ids}, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Ingest(t.Context(), sourceEnvelope([]byte(budgetSourceComponent)))
	report := result.Operation.Context
	if !errors.Is(err, sentinel) || report == nil || report.CandidateCount == nil || *report.CandidateCount != len(ids) || report.EntriesOmitted == 0 || len(report.Pages)+report.EntriesOmitted != len(ids) || report.Budget.IncludedCount != len(ids) || report.Budget.OmittedCount != 0 {
		t.Fatalf("telemetry omission lost measured counts: %+v %v", report, err)
	}
	for i, page := range report.Pages {
		if page.PageID != ids[i] || page.Disposition != knowl.ContextIncluded {
			t.Fatal("visible telemetry disagrees with model input")
		}
	}
}

// Cancel the caller on its first check after the final successful measurement.
// The derived read context remains usable until the assembly has returned.
type assemblyBoundaryContext struct {
	context.Context
	cancel context.CancelFunc
	armed  atomic.Bool
}

func (c *assemblyBoundaryContext) Err() error {
	if c.armed.Swap(false) {
		c.cancel()
	}
	return c.Context.Err()
}

type assemblyBoundarySizer struct {
	*wrappedBudgetMaintainer
	caller *assemblyBoundaryContext
}

func (m assemblyBoundarySizer) RequestBytes(ctx context.Context, input knowl.MaintenanceInput) (int, error) {
	size, err := m.wrappedBudgetMaintainer.RequestBytes(ctx, input)
	if err == nil && len(input.Pages) == 1 {
		m.caller.armed.Store(true)
	}
	return size, err
}

func TestIngestContextCancellationAfterAssemblyPreservesCompletedEvidence(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	seedBaselineContext(t, workspace, store)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	caller := &assemblyBoundaryContext{Context: ctx, cancel: cancel}
	maintainer := assemblyBoundarySizer{wrappedBudgetMaintainer: &wrappedBudgetMaintainer{cap: 12000}, caller: caller}
	ops := &recordingContextOperations{OperationStore: store}
	service, err := app.NewIngestService(workspace, ops, orderedBudgetIndex{store, []knowl.PageID{budgetStorageID}}, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Submit(caller, sourceEnvelope([]byte(budgetSourceComponent))); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimReady(caller, "local", knowl.WorkLease{Token: "boundary-fixture", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunToTerminal(caller, claim)
	report := result.Operation.Context
	if !errors.Is(err, context.Canceled) || ops.writes != 1 || maintainer.calls != 0 || result.Operation.Status != knowl.StatusReceived || report == nil || report.Outcome != knowl.ContextAssembled || report.Budget == nil || report.Budget.UsedBytes == nil || report.Budget.IncludedCount != 1 || report.Pages[0].Disposition != knowl.ContextIncluded {
		t.Fatalf("completed measurement lost on cancellation: %+v writes=%d calls=%d err=%v", report, ops.writes, maintainer.calls, err)
	}
}
