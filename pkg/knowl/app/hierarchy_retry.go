package app

import (
	"context"
	"errors"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var ErrHierarchyRetryConflict = errors.New("hierarchy retry conflicts with current operation or snapshot")

// HierarchyRetryRequest binds a retry to one immutable descriptor and attempt.
type HierarchyRetryRequest struct {
	Descriptor  knowl.ExecutionDescriptor
	WorkAttempt int
	DryRun      bool
}

type HierarchyRetryResult struct {
	OperationID      knowl.OperationID             `json:"operation_id"`
	DryRun           bool                          `json:"dry_run"`
	Requeued         bool                          `json:"requeued"`
	WorkAttempt      int                           `json:"work_attempt"`
	ManualRetryCount int                           `json:"manual_retry_count"`
	PreviousFailure  *knowl.Failure                `json:"previous_failure,omitempty"`
	Diagnostics      []knowl.MaintenanceDiagnostic `json:"diagnostics,omitempty"`
}

// HierarchyRetryStore is an optional, targeted recovery capability.
type HierarchyRetryStore interface {
	RetryHierarchy(ctx context.Context, request HierarchyRetryRequest) (HierarchyRetryResult, error)
}

func (service *HierarchyService) Retry(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, dryRun bool) (HierarchyRetryResult, error) {
	ctx = nonNilContext(ctx)
	result := HierarchyRetryResult{OperationID: id, DryRun: dryRun}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if service.applyCoordinator != nil {
		release, err := service.applyCoordinator.Acquire(ctx)
		if err != nil {
			return result, err
		}
		defer release()
	}
	retryer, ok := service.operations.(HierarchyRetryStore)
	if !ok {
		return result, ErrExecutionDescriptorUnavailable
	}
	operation, err := service.operations.Operation(ctx, scope, id)
	if err != nil {
		return result, err
	}
	if operation.Kind != knowl.WorkHierarchy || operation.Status != knowl.StatusFailed || operation.WorkAttempt < 1 || operation.Plan != nil {
		return result, ErrHierarchyRetryConflict
	}
	descriptor, err := service.operations.Execution(ctx, scope, id)
	if err != nil {
		return result, err
	}
	if descriptor.Kind != knowl.WorkHierarchy || descriptor.Hierarchy == nil || descriptor.Hierarchy.PlannerVersion != service.planner || ValidateGenericExecutionDescriptor(scope, descriptor) != nil {
		return result, ErrHierarchyRetryConflict
	}
	schema, err := service.content.Schema(ctx, scope)
	if err != nil {
		return result, err
	}
	snapshot, err := service.content.HierarchySnapshotDigest(ctx, scope)
	if err != nil {
		return result, err
	}
	if schema.Digest != descriptor.Schema.Digest || snapshot != descriptor.Hierarchy.SnapshotDigest {
		return result, ErrHierarchyRetryConflict
	}
	if _, err := service.content.LoadHierarchyStage(ctx, scope, id); !errors.Is(err, ErrStageNotFound) {
		if err != nil {
			return result, err
		}
		return result, ErrHierarchyRetryConflict
	}
	result, err = retryer.RetryHierarchy(ctx, HierarchyRetryRequest{Descriptor: descriptor, WorkAttempt: operation.WorkAttempt, DryRun: dryRun})
	result.PreviousFailure = operation.Failure
	result.Diagnostics = operation.Diagnostics
	return result, err
}

// HierarchyRetryAttempt is the bounded failure snapshot retained before requeue.
// Current operation counters remain cumulative across these attempts.
type HierarchyRetryAttempt struct {
	WorkAttempt      int                              `json:"work_attempt"`
	ManualRetryCount int                              `json:"manual_retry_count"`
	Failure          knowl.Failure                    `json:"failure"`
	Correction       *knowl.OperationCorrectionReport `json:"correction,omitempty"`
	Diagnostics      []knowl.MaintenanceDiagnostic    `json:"diagnostics,omitempty"`
	FailedAt         time.Time                        `json:"failed_at"`
	RetriedAt        time.Time                        `json:"retried_at"`
}
