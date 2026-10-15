package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type applyTailStage string

const (
	applyTailCoordinator applyTailStage = "coordinator"
	applyTailOperation   applyTailStage = "operation"
	applyTailCommit      applyTailStage = "commit"
	applyTailProjection  applyTailStage = "projection"
	applyTailOutcome     applyTailStage = "outcome"
)

type applyTailRequest struct {
	coordinator   ApplyCoordinator
	operations    OperationStore
	scope         knowl.ScopeRef
	id            knowl.OperationID
	leaseDuration time.Duration
	reuseApplying bool
	commit        func(context.Context) (knowl.ContentCommit, error)
	session       applyTailSession
	snapshot      func(context.Context, knowl.ScopeRef) (knowl.WorkspaceSnapshot, error)
	project       func(context.Context, knowl.Operation, knowl.ContentCommit) error
}

type applyTailSession func(
	context.Context,
	func(context.Context) (bool, error),
	func(context.Context, knowl.ContentCommit) error,
) (knowl.ContentCommit, error)

type applyTailResult struct {
	operation knowl.Operation
	commit    *knowl.ContentCommit
	stage     applyTailStage
}

type maintenanceApplySession interface {
	MaintenanceAuthorityBound() bool
	RunMaintenanceApply(ctx context.Context, staged knowl.StagedChange, hierarchy bool, prepare func(context.Context) (bool, error), complete func(context.Context, knowl.ContentCommit) error) (knowl.ContentCommit, error)
}

func boundMaintenanceSession(content any, staged knowl.StagedChange, hierarchy bool) applyTailSession {
	bound, ok := content.(maintenanceApplySession)
	if !ok || !bound.MaintenanceAuthorityBound() {
		return nil
	}
	return func(ctx context.Context, prepare func(context.Context) (bool, error), complete func(context.Context, knowl.ContentCommit) error) (knowl.ContentCommit, error) {
		return bound.RunMaintenanceApply(ctx, staged, hierarchy, prepare, complete)
	}
}

func saveApplyFailureDiagnostics(ctx context.Context, operations OperationStore, operation knowl.Operation, cause error, planDiagnostics []knowl.MaintenanceDiagnostic) error {
	var failure interface {
		MaintenanceDiagnostics() []knowl.MaintenanceDiagnostic
	}
	if !errors.As(cause, &failure) {
		return nil
	}
	return saveFailureDiagnostics(ctx, operations, operation, planDiagnostics, nil, cause)
}

// saveFailureDiagnostics saves only current failure evidence. Plan warnings
// already have their own durable column and must not be copied into it.
func saveFailureDiagnostics(ctx context.Context, operations OperationStore, operation knowl.Operation, planDiagnostics, diagnostics []knowl.MaintenanceDiagnostic, cause error) error {
	store, ok := operations.(OperationDiagnosticStore)
	if !ok || operation.WorkAttempt <= 0 {
		return nil
	}
	diagnostics, err := NormalizeMaintenanceDiagnostics(diagnostics)
	if err != nil {
		return err
	}
	var failure interface {
		MaintenanceDiagnostics() []knowl.MaintenanceDiagnostic
	}
	if errors.As(cause, &failure) {
		additional, normalizeErr := NormalizeMaintenanceDiagnostics(failure.MaintenanceDiagnostics())
		if normalizeErr != nil {
			return normalizeErr
		}
		for _, diagnostic := range additional {
			if !slices.Contains(diagnostics, diagnostic) {
				diagnostics = append(diagnostics, diagnostic)
			}
		}
		diagnostics, err = NormalizeMaintenanceDiagnostics(diagnostics)
		if err != nil {
			return err
		}
	}
	planDiagnostics, err = NormalizeMaintenanceDiagnostics(planDiagnostics)
	if err != nil {
		return err
	}
	diagnostics = slices.DeleteFunc(diagnostics, func(diagnostic knowl.MaintenanceDiagnostic) bool {
		return slices.Contains(planDiagnostics, diagnostic)
	})
	if _, err := NormalizeMaintenanceDiagnostics(append(slices.Clone(planDiagnostics), diagnostics...)); err != nil {
		return err
	}
	return store.SaveOperationDiagnostics(ctx, operation.Key.Scope, operation.ID, operation.WorkAttempt, diagnostics)
}

// runApplyTail is the single operation-state tail for authenticated staged
// changes. The caller owns planning, stage authentication, and stage-specific
// error policy; this path owns applying, canonical commit, projection, and the
// terminal outcome.
func runApplyTail(ctx context.Context, request applyTailRequest) (applyTailResult, error) {
	result := applyTailResult{stage: applyTailCoordinator}
	var operation knowl.Operation
	prepare := func(sessionCtx context.Context) (bool, error) {
		result.stage = applyTailOperation
		current, err := request.operations.Operation(sessionCtx, request.scope, request.id)
		if err != nil {
			return false, err
		}
		operation, result.operation = current, current
		if operation.Status == knowl.StatusCommitted {
			result.stage = ""
			return false, nil
		}
		if operation.Status == knowl.StatusFailed {
			return false, ErrOperationNotApplyable
		}
		stateCtx := durableContext(sessionCtx)
		if operation.Status != knowl.StatusApplying || !request.reuseApplying {
			lease, leaseErr := newLease(time.Now().UTC(), request.leaseDuration)
			if leaseErr != nil {
				return false, leaseErr
			}
			if markErr := request.operations.MarkApplying(stateCtx, request.id, lease); markErr != nil {
				return false, markErr
			}
			if current, readErr := request.operations.Operation(stateCtx, request.scope, request.id); readErr == nil {
				operation, result.operation = current, current
			}
		}
		result.stage = applyTailCommit
		return true, nil
	}
	complete := func(sessionCtx context.Context, commit knowl.ContentCommit) error {
		result.commit = &commit
		result.stage = applyTailProjection
		snapshot, err := request.snapshot(sessionCtx, request.scope)
		if err != nil {
			return err
		}
		commit.Snapshot = snapshot
		result.commit = &commit
		if err := request.project(sessionCtx, operation, commit); err != nil {
			return err
		}
		result.stage = applyTailOutcome
		stateCtx := durableContext(sessionCtx)
		if err := request.operations.CommitOutcome(stateCtx, request.id, commit); err != nil {
			if current, readErr := request.operations.Operation(stateCtx, request.scope, request.id); readErr == nil {
				result.operation = current
			}
			return err
		}
		result.operation, err = request.operations.Operation(stateCtx, request.scope, request.id)
		if err != nil {
			return fmt.Errorf("read committed operation: %w", err)
		}
		result.stage = ""
		return nil
	}
	session := request.session
	if session == nil {
		session = func(sessionCtx context.Context, prepare func(context.Context) (bool, error), complete func(context.Context, knowl.ContentCommit) error) (knowl.ContentCommit, error) {
			release, err := acquireApply(sessionCtx, request.coordinator)
			if err != nil {
				return knowl.ContentCommit{}, err
			}
			defer release()
			proceed, err := prepare(sessionCtx)
			if err != nil || !proceed {
				return knowl.ContentCommit{}, err
			}
			commit, err := request.commit(sessionCtx)
			if err != nil {
				return knowl.ContentCommit{}, err
			}
			return commit, complete(sessionCtx, commit)
		}
	}
	_, err := session(ctx, prepare, complete)
	return result, err
}
