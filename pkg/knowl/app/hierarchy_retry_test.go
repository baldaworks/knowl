package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type retryHierarchyFixture struct {
	hierarchyMaintainer
	invalid bool
}

func (m *retryHierarchyFixture) PlanHierarchyValidated(ctx context.Context, input knowl.HierarchyInput, limits knowl.OutputCorrectionLimits, validate func(knowl.HierarchyModelPlan) error) (knowl.HierarchyModelPlan, knowl.OperationCorrectionReport, error) {
	plan, err := m.PlanHierarchy(ctx, input)
	if err != nil {
		return plan, knowl.OperationCorrectionReport{}, err
	}
	if m.invalid {
		plan.Catalogs[0].Children = []string{"wiki/concepts/missing.md"}
	}
	if err := validate(plan); err != nil {
		return plan, hierarchyFixtureReport(limits, 1, knowl.CorrectionExhausted, knowl.HierarchyPlanInvalid, plan), app.ErrOutputCorrectionExhausted
	}
	return plan, hierarchyFixtureReport(limits, 1, knowl.CorrectionAccepted, "", plan), nil
}

func TestHierarchyRetryReplansSameSnapshotAndPreservesAttempts(t *testing.T) {
	workspace, store := hierarchyWorkflow(t)
	m := &retryHierarchyFixture{invalid: true}
	zero := 0
	service, err := app.NewHierarchyService(workspace, store, store, m, app.HierarchyOptions{Output: knowl.OutputSettings{MaxCorrections: &zero}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := workspace.HierarchySnapshotDigest(t.Context(), testSourceScope)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := service.Reconcile(t.Context(), testSourceScope)
	if !errors.Is(err, app.ErrOutputCorrectionExhausted) || failed.Operation.Status != knowl.StatusFailed {
		t.Fatalf("first failure: %+v %v", failed, err)
	}
	preview, err := service.Retry(t.Context(), testSourceScope, failed.Operation.ID, true)
	if err != nil || preview.Requeued || !preview.DryRun {
		t.Fatalf("retry preview: %+v %v", preview, err)
	}
	unchanged, err := store.Operation(t.Context(), testSourceScope, failed.Operation.ID)
	if err != nil || unchanged.Status != knowl.StatusFailed || unchanged.WorkAttempt != 1 || unchanged.ManualRetryCount != 0 {
		t.Fatalf("preview changed operation: %+v %v", unchanged, err)
	}
	if len(unchanged.Diagnostics) != 1 || unchanged.Diagnostics[0].Code != "hierarchy.missing_child" || unchanged.Diagnostics[0].Target != "wiki/concepts/missing.md" {
		t.Fatalf("missing bounded graph diagnostic: %+v", unchanged.Diagnostics)
	}
	after, err := workspace.HierarchySnapshotDigest(t.Context(), testSourceScope)
	if err != nil || after != before || m.calls() != 1 {
		t.Fatalf("preview changed input/called provider: %v", err)
	}
	m.invalid = false
	queued, err := service.Retry(t.Context(), testSourceScope, failed.Operation.ID, false)
	if err != nil || !queued.Requeued || queued.ManualRetryCount != 1 {
		t.Fatalf("retry: %+v %v", queued, err)
	}
	if _, err := service.Retry(t.Context(), testSourceScope, failed.Operation.ID, false); !errors.Is(err, app.ErrHierarchyRetryConflict) {
		t.Fatalf("active retry accepted: %v", err)
	}
	result, err := service.Reconcile(t.Context(), testSourceScope)
	if err != nil || result.Operation.Status != knowl.StatusCommitted || result.Operation.ID != failed.Operation.ID || result.Operation.WorkAttempt != 2 || result.Operation.ManualRetryCount != 1 || m.calls() != 2 {
		t.Fatalf("retry result: %+v %v calls=%d", result, err, m.calls())
	}
	if _, err := service.Retry(t.Context(), testSourceScope, result.Operation.ID, true); !errors.Is(err, app.ErrHierarchyRetryConflict) {
		t.Fatalf("committed retry accepted: %v", err)
	}
	history, err := store.HierarchyRetryHistory(t.Context(), testSourceScope, result.Operation.ID, 10)
	if err != nil || len(history) != 1 || history[0].WorkAttempt != 1 || history[0].Failure.Reason != "provider_output_exhausted" || history[0].Correction == nil || history[0].Correction.Outcome != knowl.CorrectionExhausted {
		t.Fatalf("original failed attempt not retained after success: %+v %v", history, err)
	}
}

func TestHierarchyRetryRejectsChangedInputs(t *testing.T) {
	for _, path := range []string{"schema.md", testPagePath} {
		t.Run(path, func(t *testing.T) {
			workspace, store := hierarchyWorkflow(t)
			m := &retryHierarchyFixture{invalid: true}
			service := newHierarchyService(t, workspace, store, store, m, 0)
			failed, err := service.Reconcile(t.Context(), testSourceScope)
			if !errors.Is(err, app.ErrOutputCorrectionExhausted) {
				t.Fatal(err)
			}
			file := filepath.Join(workspace.Root(), filepath.FromSlash(path))
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, append(before, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, preview := range []bool{true, false} {
				if _, err := service.Retry(t.Context(), testSourceScope, failed.Operation.ID, preview); !errors.Is(err, app.ErrHierarchyRetryConflict) {
					t.Fatalf("changed %s accepted: %v", path, err)
				}
			}
			unchanged, err := store.Operation(t.Context(), testSourceScope, failed.Operation.ID)
			if err != nil || unchanged.Status != knowl.StatusFailed || unchanged.ManualRetryCount != 0 || m.calls() != 1 {
				t.Fatalf("stale retry changed state: %+v %v", unchanged, err)
			}
			page, err := store.ListOperations(t.Context(), testSourceScope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 100}})
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range page.Items {
				if op.Kind == knowl.WorkHierarchy {
					continue
				}
				if _, err := service.Retry(t.Context(), testSourceScope, op.ID, false); !errors.Is(err, app.ErrHierarchyRetryConflict) {
					t.Fatalf("wrong kind accepted: %v", err)
				}
			}
		})
	}
}
