package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// An expired apply lease makes recovery deterministic without nanosecond wall-clock timing.
type expiredApplyLeaseOperations struct{ app.OperationStore }

func (s expiredApplyLeaseOperations) MarkApplying(ctx context.Context, id knowl.OperationID, lease knowl.Lease) error {
	lease.ExpiresAt = time.Unix(1, 0).UTC()
	return s.OperationStore.MarkApplying(ctx, id, lease)
}

type correctionPlanFailureOperations struct {
	app.OperationStore
	fail bool
}

func (s *correctionPlanFailureOperations) SavePlan(ctx context.Context, id knowl.OperationID, plan knowl.PlanSummary) error {
	if s.fail {
		s.fail = false
		return classifiedTestError{class: "operation", reason: "fixture_plan_storage", retryable: true}
	}
	return s.OperationStore.SavePlan(ctx, id, plan)
}

func (s *correctionPlanFailureOperations) SaveOperationCorrectionReport(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, attempt int, report knowl.OperationCorrectionReport) error {
	return s.OperationStore.(app.OperationCorrectionReportStore).SaveOperationCorrectionReport(ctx, scope, id, attempt, report)
}

func TestSourceCorrectionPolicyRejectsOldUnplannedAndRecoversOldStage(t *testing.T) {
	for _, staged := range []bool{false, true} {
		workspace, store, _, _ := newBaselineIngest(t)
		m := &validatingFixtureMaintainer{}
		ops := &correctionPlanFailureOperations{OperationStore: store, fail: staged}
		old, err := app.NewIngestService(workspace, ops, store, m, app.IngestOptions{})
		if err != nil {
			t.Fatal(err)
		}
		submission, err := old.Submit(t.Context(), sourceEnvelope([]byte("input evidence")))
		if err != nil {
			t.Fatal(err)
		}
		if staged {
			claim := claimReady(t, store, "local")
			first, err := old.RunToTerminal(t.Context(), claim)
			if err == nil || first.Staged.Digest == "" || first.Operation.Status != knowl.StatusReceived {
				t.Fatalf("old staged fixture: %+v %v", first, err)
			}
			if err := store.ReleaseClaim(t.Context(), "local", submission.Operation.ID, claim.Lease.Token); err != nil {
				t.Fatal(err)
			}
		}
		zero := 0
		current, err := app.NewIngestService(workspace, ops, store, m, app.IngestOptions{Output: knowl.OutputSettings{MaxCorrections: &zero}})
		if err != nil {
			t.Fatal(err)
		}
		_, generation, err := current.CurrentMaintenancePolicy(t.Context(), "local")
		if err != nil || generation == submission.Operation.Key.MaintenanceGeneration {
			t.Fatal("effective correction allowance did not change source generation")
		}
		before, err := store.Operation(t.Context(), "local", submission.Operation.ID)
		if err != nil {
			t.Fatal(err)
		}
		result, err := current.RunToTerminal(t.Context(), claimReady(t, store, "local"))
		if staged {
			if err != nil || result.Operation.Status != knowl.StatusCommitted || m.validations != 2 || !reflect.DeepEqual(result.Operation.Correction, before.Correction) || result.Operation.Correction.WorkAttempt != 1 {
				t.Fatalf("old stage required new inference: %+v %v validations=%d", result.Operation, err, m.validations)
			}
		} else if !errors.Is(err, app.ErrMaintenancePolicyMismatch) || m.validations != 0 || result.Operation.Correction != nil {
			t.Fatalf("old unplanned policy was reinterpreted: %+v %v", result.Operation, err)
		}
	}
}

func TestHierarchyCorrectionPolicyRejectsOldUnplannedAndRecoversOldStage(t *testing.T) {
	for _, staged := range []bool{false, true} {
		workspace, store := hierarchyWorkflow(t)
		m := &hierarchyMaintainer{}
		ops := &correctionPlanFailureOperations{OperationStore: store, fail: staged}
		old, err := app.NewHierarchyService(workspace, ops, store, m, app.HierarchyOptions{PlannerVersion: "semantic-old"})
		if err != nil {
			t.Fatal(err)
		}
		reservation, err := old.Reserve(t.Context(), "local")
		if err != nil {
			t.Fatal(err)
		}
		if staged {
			claim := claimReady(t, store, "local")
			first, err := old.RunToTerminal(t.Context(), claim)
			if err == nil || first.Staged.Digest == "" || first.Operation.Status != knowl.StatusReceived {
				t.Fatalf("old hierarchy stage fixture: %+v %v", first, err)
			}
			if err := store.ReleaseClaim(t.Context(), "local", reservation.ID, claim.Lease.Token); err != nil {
				t.Fatal(err)
			}
		}
		current, err := app.NewHierarchyService(workspace, ops, store, m, app.HierarchyOptions{PlannerVersion: "semantic-current"})
		if err != nil {
			t.Fatal(err)
		}
		before, err := store.Operation(t.Context(), "local", reservation.ID)
		if err != nil {
			t.Fatal(err)
		}
		result, err := current.RunToTerminal(t.Context(), claimReady(t, store, "local"))
		if staged {
			if err != nil || result.Operation.Status != knowl.StatusCommitted || m.calls() != 1 || !reflect.DeepEqual(result.Operation.Correction, before.Correction) || result.Operation.Correction.WorkAttempt != 1 {
				t.Fatalf("old hierarchy stage required inference: %+v %v calls=%d", result.Operation, err, m.calls())
			}
		} else if !errors.Is(err, app.ErrExecutionDescriptorUnavailable) || m.calls() != 0 || result.Operation.Correction != nil {
			t.Fatalf("old unplanned hierarchy policy reinterpreted: %+v %v", result.Operation, err)
		}
	}
}
