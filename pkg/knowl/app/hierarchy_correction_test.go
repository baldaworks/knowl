package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	hierarchyCorrectionCanceled = "canceled"
	hierarchyCorrectionChanged  = "changed"
	hierarchyCorrectionDisabled = "disabled"
	hierarchyCorrectionBypass   = "bypass"
	hierarchyCorrectionWrite    = "write failed"
)

type validatingHierarchyFixture struct {
	hierarchyMaintainer
	beforeReplacement func()
	cancel            context.CancelFunc
	skip, change      bool
	turns             int
}

func (m *validatingHierarchyFixture) PlanHierarchyValidated(ctx context.Context, input knowl.HierarchyInput, limits knowl.OutputCorrectionLimits, validate func(knowl.HierarchyModelPlan) error) (knowl.HierarchyModelPlan, knowl.OperationCorrectionReport, error) {
	good, err := m.PlanHierarchy(ctx, input)
	if err != nil {
		return good, knowl.OperationCorrectionReport{}, err
	}
	bad := good
	bad.Catalogs = append([]knowl.HierarchyCatalogSpec(nil), good.Catalogs...)
	bad.Catalogs[0].Title = "source-1"
	m.turns = 1
	code := knowl.OutputValidationCode("")
	if !m.skip {
		if err := validate(bad); !errors.Is(err, app.ErrHierarchyForbiddenPath) {
			return bad, knowl.OperationCorrectionReport{}, errors.New("full validator did not reject source identity in catalog")
		}
		code = knowl.HierarchyPlanInvalid
		if m.beforeReplacement != nil {
			m.beforeReplacement()
		}
		if limits.MaxCorrections == 0 {
			return bad, hierarchyFixtureReport(limits, 1, knowl.CorrectionExhausted, code, bad), app.ErrOutputCorrectionExhausted
		}
		m.turns++
		if err := validate(good); err != nil {
			return good, knowl.OperationCorrectionReport{}, err
		}
	}
	report := hierarchyFixtureReport(limits, m.turns, knowl.CorrectionAccepted, code, bad, good)
	if m.cancel != nil {
		m.cancel()
	}
	if m.change {
		return bad, report, nil
	}
	return good, report, nil
}

func hierarchyFixtureReport(limits knowl.OutputCorrectionLimits, turns int, outcome knowl.CorrectionOutcome, code knowl.OutputValidationCode, plans ...knowl.HierarchyModelPlan) knowl.OperationCorrectionReport {
	used := 0
	for _, plan := range plans[:turns] {
		encoded, _ := json.Marshal(plan)
		used += len(encoded)
	}
	corrections := turns - 1
	return knowl.OperationCorrectionReport{Version: 1, MaxCorrections: limits.MaxCorrections, MaxOutputBytes: limits.MaxOutputBytes, DeadlineNanos: limits.DeadlineNanos, Outcome: outcome, Turns: &turns, Corrections: &corrections, OutputBytes: &used, ValidationCode: code}
}

func TestHierarchyCorrectionValidatesBeforeStageAndPersistsBeforeSummary(t *testing.T) {
	workspace, store := hierarchyWorkflow(t)
	before := hierarchyProtectedFiles(t, workspace)
	m := &validatingHierarchyFixture{}
	ops := &recordingCorrectionOperations{OperationStore: store}
	service, err := app.NewHierarchyService(workspace, ops, store, m, app.HierarchyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := service.Reserve(t.Context(), testSourceScope)
	if err != nil {
		t.Fatal(err)
	}
	assertNoArtifacts := func() {
		t.Helper()
		if _, err := workspace.LoadHierarchyStage(t.Context(), testSourceScope, reservation.ID); !errors.Is(err, app.ErrStageNotFound) {
			t.Fatalf("invalid candidate staged: %v", err)
		}
		op, err := store.Operation(t.Context(), testSourceScope, reservation.ID)
		if err != nil || op.Plan != nil || op.Correction != nil {
			t.Fatalf("intermediate artifacts: %+v %v", op, err)
		}
		assertHierarchyProtectedFiles(t, workspace, before)
	}
	m.beforeReplacement = assertNoArtifacts
	ops.before = func(knowl.OperationCorrectionReport) { assertNoArtifacts() }
	claim := claimReady(t, store, testSourceScope)
	result, err := service.RunToTerminal(t.Context(), claim)
	if err != nil || result.Operation.Status != knowl.StatusCommitted || ops.writes != 1 || result.Operation.Correction == nil || result.Operation.Correction.Outcome != knowl.CorrectionAccepted || m.turns != 2 {
		t.Fatalf("corrected hierarchy: %+v %v writes=%d turns=%d", result, err, ops.writes, m.turns)
	}
	replay, err := service.RunToTerminal(t.Context(), claim)
	if err != nil || replay.Operation.Correction == nil || m.turns != 2 || m.calls() != 1 || ops.writes != 1 {
		t.Fatalf("terminal replay invoked planning: %+v %v", replay, err)
	}
}

func TestHierarchyCorrectionRejectsBypassCancellationAndReportWriteFailure(t *testing.T) {
	for _, mode := range []string{hierarchyCorrectionDisabled, hierarchyCorrectionBypass, hierarchyCorrectionChanged, hierarchyCorrectionCanceled, hierarchyCorrectionWrite} {
		t.Run(mode, func(t *testing.T) {
			workspace, store := hierarchyWorkflow(t)
			before := hierarchyProtectedFiles(t, workspace)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m := &validatingHierarchyFixture{skip: mode == hierarchyCorrectionBypass, change: mode == hierarchyCorrectionChanged}
			options := app.HierarchyOptions{}
			wantErr := app.ErrOperationCorrectionReportInvalid
			if mode == hierarchyCorrectionDisabled {
				zero := 0
				options.Output.MaxCorrections = &zero
				wantErr = app.ErrOutputCorrectionExhausted
			}
			if mode == hierarchyCorrectionChanged {
				wantErr = app.ErrHierarchyForbiddenPath
			}
			if mode == hierarchyCorrectionCanceled {
				m.cancel, wantErr = cancel, context.Canceled
			}
			ops := &recordingCorrectionOperations{OperationStore: store}
			if mode == hierarchyCorrectionWrite {
				wantErr = errors.New("report write failure")
				ops.failure = wantErr
			}
			service, err := app.NewHierarchyService(workspace, ops, store, m, options)
			if err != nil {
				t.Fatal(err)
			}
			reservation, err := service.Reserve(ctx, testSourceScope)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.RunToTerminal(ctx, claimReady(t, store, testSourceScope))
			if !errors.Is(err, wantErr) {
				t.Fatalf("rejection cause: %v want %v", err, wantErr)
			}
			op, err := store.Operation(t.Context(), testSourceScope, reservation.ID)
			if err != nil || op.Plan != nil || result.Operation.Plan != nil {
				t.Fatalf("invalid summary: %+v %v", op, err)
			}
			if _, err := workspace.LoadHierarchyStage(t.Context(), testSourceScope, reservation.ID); !errors.Is(err, app.ErrStageNotFound) {
				t.Fatalf("invalid stage: %v", err)
			}
			if mode == hierarchyCorrectionCanceled && (op.Correction == nil || op.Correction.Outcome != knowl.CorrectionCanceled || op.Correction.ValidationCode != knowl.HierarchyPlanInvalid || op.Status != knowl.StatusReceived || op.Failure != nil) {
				t.Fatalf("cancellation attribution: %+v", op)
			}
			if mode == hierarchyCorrectionWrite && (op.Correction != nil || result.Operation.Correction != nil) {
				t.Fatal("failed report write claimed persistence")
			}
			assertHierarchyProtectedFiles(t, workspace, before)
		})
	}
}
