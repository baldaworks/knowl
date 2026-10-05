package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type validatingFixtureMaintainer struct {
	baselineMaintainer
	baseCalls, validations                     int
	beforeReplacement                          func(knowl.MaintenanceInput)
	alwaysInvalid, skipCallback, returnInvalid bool
}

func (m *validatingFixtureMaintainer) Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	m.baseCalls++
	return m.baselineMaintainer.Plan(ctx, input)
}

func (m *validatingFixtureMaintainer) PlanValidated(ctx context.Context, input knowl.MaintenanceInput, limits knowl.OutputCorrectionLimits, validate func(knowl.ModelEditPlan) error) (knowl.ModelEditPlan, knowl.OperationCorrectionReport, error) {
	bad := knowl.ModelEditPlan{SchemaDigest: "incorrect", SourceRefs: []string{app.SourceRefKey(input.Source)}}
	if m.skipCallback {
		good, err := m.baselineMaintainer.Plan(ctx, input)
		return good, fixtureCorrectionReport(limits, 1, knowl.CorrectionAccepted, "", good), err
	}
	m.validations++
	if err := validate(bad); err == nil {
		return bad, knowl.OperationCorrectionReport{}, errors.New("app accepted incorrect schema")
	}
	if m.beforeReplacement != nil {
		m.beforeReplacement(input)
	}
	if m.alwaysInvalid || limits.MaxCorrections == 0 {
		turns := 1
		if limits.MaxCorrections > 0 {
			m.validations++
			_ = validate(bad)
			turns++
		}
		return knowl.ModelEditPlan{}, fixtureCorrectionReport(limits, turns, knowl.CorrectionExhausted, knowl.SourcePlanInvalid, bad), app.ErrOutputCorrectionExhausted
	}
	good, err := m.baselineMaintainer.Plan(ctx, input)
	if err != nil {
		return knowl.ModelEditPlan{}, knowl.OperationCorrectionReport{}, err
	}
	m.validations++
	if err := validate(good); err != nil {
		return knowl.ModelEditPlan{}, knowl.OperationCorrectionReport{}, err
	}
	report := fixtureCorrectionReport(limits, 2, knowl.CorrectionAccepted, knowl.SourcePlanInvalid, bad, good)
	if m.returnInvalid {
		return bad, report, nil
	}
	return good, report, nil
}

func fixtureCorrectionReport(limits knowl.OutputCorrectionLimits, turns int, outcome knowl.CorrectionOutcome, code knowl.OutputValidationCode, plans ...knowl.ModelEditPlan) knowl.OperationCorrectionReport {
	used := 0
	for _, plan := range plans {
		encoded, _ := json.Marshal(plan)
		used += len(encoded)
	}
	if len(plans) == 1 {
		used *= turns
	}
	corrections := turns - 1
	return knowl.OperationCorrectionReport{Version: 1, MaxCorrections: limits.MaxCorrections, MaxOutputBytes: limits.MaxOutputBytes, DeadlineNanos: limits.DeadlineNanos, Outcome: outcome, Turns: &turns, Corrections: &corrections, OutputBytes: &used, ValidationCode: code}
}

type recordingCorrectionOperations struct {
	app.OperationStore
	before  func(knowl.OperationCorrectionReport)
	failure error
	writes  int
}

type cancelAfterCorrectionMaintainer struct {
	*validatingFixtureMaintainer
	cancel context.CancelFunc
}

func (m cancelAfterCorrectionMaintainer) PlanValidated(ctx context.Context, input knowl.MaintenanceInput, limits knowl.OutputCorrectionLimits, validate func(knowl.ModelEditPlan) error) (knowl.ModelEditPlan, knowl.OperationCorrectionReport, error) {
	plan, report, err := m.validatingFixtureMaintainer.PlanValidated(ctx, input, limits, validate)
	m.cancel()
	return plan, report, err
}

func TestIngestCorrectionCallerCancellationAfterProviderAcceptance(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m := cancelAfterCorrectionMaintainer{validatingFixtureMaintainer: &validatingFixtureMaintainer{}, cancel: cancel}
	service, err := app.NewIngestService(workspace, store, store, m, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Ingest(ctx, sourceEnvelope([]byte("input evidence")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation lost: %v", err)
	}
	operation, err := store.Operation(t.Context(), "local", result.Operation.ID)
	if err != nil || operation.Correction == nil || operation.Correction.Outcome != knowl.CorrectionCanceled || operation.Status != knowl.StatusReceived || operation.Failure != nil || operation.Plan != nil {
		t.Fatalf("canceled report attribution: %+v %v", operation, err)
	}
}

func (s *recordingCorrectionOperations) SaveOperationCorrectionReport(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, attempt int, report knowl.OperationCorrectionReport) error {
	s.writes++
	if s.before != nil {
		s.before(report)
	}
	if s.failure != nil {
		return s.failure
	}
	return s.OperationStore.(app.OperationCorrectionReportStore).SaveOperationCorrectionReport(ctx, scope, id, attempt, report)
}

func TestIngestCorrectionReportWritePrecedesStageAndPreservesOriginalFailure(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		workspace, store, _, _ := newBaselineIngest(t)
		writeErr := errors.New("report storage unavailable")
		ops := &recordingCorrectionOperations{OperationStore: store, failure: writeErr}
		m := &validatingFixtureMaintainer{alwaysInvalid: exhausted}
		service, err := app.NewIngestService(workspace, ops, store, m, app.IngestOptions{})
		if err != nil {
			t.Fatal(err)
		}
		submission, err := service.Submit(t.Context(), sourceEnvelope([]byte("input evidence")))
		if err != nil {
			t.Fatal(err)
		}
		ops.before = func(report knowl.OperationCorrectionReport) {
			if _, err := workspace.LoadStage(t.Context(), "local", submission.Operation.ID); !errors.Is(err, app.ErrStageNotFound) {
				t.Fatalf("stage before report: %v", err)
			}
			operation, err := store.Operation(t.Context(), "local", submission.Operation.ID)
			if err != nil || operation.Plan != nil {
				t.Fatalf("plan summary before report: %+v %v", operation.Plan, err)
			}
		}
		result, err := service.Execute(t.Context(), submission)
		if exhausted {
			if !errors.Is(err, app.ErrOutputCorrectionExhausted) || errors.Is(err, writeErr) {
				t.Fatalf("original classified failure lost: %v", err)
			}
		} else if !errors.Is(err, writeErr) {
			t.Fatalf("accepted write failure ignored: %v", err)
		}
		if ops.writes != 1 || result.Operation.Correction != nil || result.Operation.Plan != nil {
			t.Fatalf("report write failure persisted invented facts: %+v writes=%d", result.Operation, ops.writes)
		}
	}
}

func TestIngestDefensivelyRejectsUnvalidatedOrChangedCandidate(t *testing.T) {
	for _, bypass := range []bool{false, true} {
		workspace, store, _, _ := newBaselineIngest(t)
		m := &validatingFixtureMaintainer{skipCallback: bypass, returnInvalid: !bypass}
		service, err := app.NewIngestService(workspace, store, store, m, app.IngestOptions{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Ingest(t.Context(), sourceEnvelope([]byte("input evidence")))
		if err == nil || result.Operation.Plan != nil || result.Operation.Status != knowl.StatusFailed {
			t.Fatalf("invalid returned candidate accepted: %+v %v", result.Operation, err)
		}
		if _, err := workspace.LoadStage(t.Context(), "local", result.Operation.ID); !errors.Is(err, app.ErrStageNotFound) {
			t.Fatalf("unsafe candidate staged: %v", err)
		}
		if result.Operation.Correction != nil && result.Operation.Correction.Outcome == knowl.CorrectionAccepted {
			t.Fatal("failed defensive validation reported acceptance")
		}
	}
}

func TestIngestOutputPolicyDisableAndTerminalReplay(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	zero := 0
	m := &validatingFixtureMaintainer{}
	service, err := app.NewIngestService(workspace, store, store, m, app.IngestOptions{Output: knowl.OutputSettings{MaxCorrections: &zero}})
	if err != nil {
		t.Fatal(err)
	}
	submission, err := service.Submit(t.Context(), sourceEnvelope([]byte("input evidence")))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(t.Context(), submission)
	if !errors.Is(err, app.ErrOutputCorrectionExhausted) || m.validations != 1 || result.Operation.Correction == nil || *result.Operation.Correction.Turns != 1 || result.Operation.WorkAttempt != 1 || result.Operation.RetryAttempt != 1 {
		t.Fatalf("disabled exact counts: %+v %v", result.Operation, err)
	}
	replayed, err := service.Execute(t.Context(), submission)
	if err != nil || m.validations != 1 || !reflect.DeepEqual(replayed.Operation.Correction, result.Operation.Correction) {
		t.Fatalf("terminal replay inferred again: %+v %v", replayed.Operation.Correction, err)
	}
}

func TestFilePlanDoesNotRequestCorrection(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	m := &validatingFixtureMaintainer{}
	service, err := app.NewIngestService(workspace, store, store, m, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.FilePlan(t.Context(), sourceEnvelope([]byte("input evidence")), knowl.ModelEditPlan{SchemaDigest: "incorrect"})
	if !errors.Is(err, app.ErrSchemaMismatch) || m.baseCalls != 0 || m.validations != 0 || result.Operation.Correction != nil {
		t.Fatalf("supplied plan corrected: %+v %v", result.Operation, err)
	}
}

func TestIngestCorrectionUsesEarlierCallerDeadline(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	m := &deadlineMaintainer{}
	service, err := app.NewIngestService(workspace, store, store, m, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if _, err := service.Ingest(ctx, sourceEnvelope([]byte("input evidence"))); err != nil {
		t.Fatal(err)
	}
	if !m.deadline.Equal(deadline) {
		t.Fatalf("caller deadline extended: %v want%v", m.deadline, deadline)
	}
}

func TestIngestUsesFullCorrectionValidatorBeforeAnyStage(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	before, err := workspace.Snapshot(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	m := &validatingFixtureMaintainer{}
	service, err := app.NewIngestService(workspace, store, store, m, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	submission, err := service.Submit(t.Context(), sourceEnvelope([]byte("input evidence")))
	if err != nil {
		t.Fatal(err)
	}
	m.beforeReplacement = func(input knowl.MaintenanceInput) {
		operation, err := store.Operation(t.Context(), "local", submission.Operation.ID)
		if err != nil || operation.Plan != nil || operation.Correction != nil {
			t.Fatalf("invalid intermediate evidence persisted: %+v %v", operation, err)
		}
		if _, err := workspace.LoadStage(t.Context(), "local", submission.Operation.ID); !errors.Is(err, app.ErrStageNotFound) {
			t.Fatalf("invalid candidate reached stage: %v", err)
		}
		after, err := workspace.Snapshot(t.Context(), "local")
		if err != nil || !reflect.DeepEqual(before.PageDigests, after.PageDigests) {
			t.Fatal("invalid candidate changed canonical pages")
		}
	}
	result, err := service.Execute(t.Context(), submission)
	if err != nil || m.baseCalls != 0 || m.validations != 2 || result.Operation.Correction == nil || result.Operation.Correction.Outcome != knowl.CorrectionAccepted || result.Operation.Correction.WorkAttempt != result.Operation.WorkAttempt {
		t.Fatalf("corrected app workflow: %+v %v base=%d validations=%d", result.Operation.Correction, err, m.baseCalls, m.validations)
	}
}

func TestIngestCustomMaintainerCorrectionEvidenceIsUnavailable(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	m := &wrappedBudgetMaintainer{cap: 12000}
	service, err := app.NewIngestService(workspace, store, store, m, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Ingest(t.Context(), sourceEnvelope([]byte("input evidence")))
	report := result.Operation.Correction
	if err != nil || m.calls != 1 || report == nil || report.Outcome != knowl.CorrectionUnavailable || report.Turns != nil || report.Corrections != nil || report.OutputBytes != nil || report.MaxCorrections != 0 {
		t.Fatalf("fallback physical evidence: %+v %v calls=%d", report, err, m.calls)
	}
}
