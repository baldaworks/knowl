// Package app owns Knowl application policy and its consuming ports.
package app

import (
	"context"

	"github.com/baldaworks/knowl/pkg/knowl"
)

// ContentStore owns canonical workspace content and recovery.
type ContentStore interface {
	AcceptSource(ctx context.Context, envelope knowl.SourceEnvelope) (knowl.AcceptedSource, error)
	ReadSource(ctx context.Context, source knowl.AcceptedSource, limits knowl.ReadLimits) ([]byte, error)
	Schema(ctx context.Context, scope knowl.ScopeRef) (knowl.SchemaDocument, error)
	ReadPages(ctx context.Context, scope knowl.ScopeRef, ids []knowl.PageID, limits knowl.ReadLimits) ([]knowl.PageSnapshot, error)
	StagePlan(ctx context.Context, plan knowl.ValidatedEditPlan) (knowl.StagedChange, error)
	Commit(ctx context.Context, staged knowl.StagedChange) (knowl.ContentCommit, error)
	Recover(ctx context.Context) ([]knowl.RecoveryResult, error)
	Snapshot(ctx context.Context, scope knowl.ScopeRef) (knowl.WorkspaceSnapshot, error)
	Inspect(ctx context.Context, scope knowl.ScopeRef) (knowl.WorkspaceInspection, error)
}

// OperationStore owns durable operation state, leases, and redacted outcomes.
type OperationStore interface {
	Reserve(ctx context.Context, key knowl.OperationKey, meta knowl.OperationMeta) (knowl.Operation, error)
	SavePlan(ctx context.Context, id knowl.OperationID, summary knowl.PlanSummary) error
	MarkAwaitingReview(ctx context.Context, id knowl.OperationID) error
	MarkApplying(ctx context.Context, id knowl.OperationID, lease knowl.Lease) error
	CommitOutcome(ctx context.Context, id knowl.OperationID, commit knowl.ContentCommit) error
	Fail(ctx context.Context, id knowl.OperationID, failure knowl.Failure) error
	Operation(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.Operation, error)
}

// SearchIndex owns rebuildable context, text, and link projections.
type SearchIndex interface {
	SelectContext(ctx context.Context, scope knowl.ScopeRef, source knowl.SourceSummary, limits knowl.ReadLimits) ([]knowl.PageID, error)
	Search(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits) ([]knowl.PageReference, error)
	Links(ctx context.Context, scope knowl.ScopeRef, page knowl.PageID, limits knowl.ReadLimits) ([]knowl.LinkReference, error)
	Project(ctx context.Context, commit knowl.ContentCommit) error
	Rebuild(ctx context.Context, snapshot knowl.WorkspaceSnapshot) error
}

// Maintainer produces structured data-only edit plans.
type Maintainer interface {
	Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error)
}

// ScopeAuthorizer enforces the host's trusted scope boundary.
type ScopeAuthorizer interface {
	Authorize(ctx context.Context, scope knowl.ScopeRef, operation OperationKind) error
}

// OperationKind identifies the policy operation being authorized.
type OperationKind string

const (
	OperationRead   OperationKind = "read"
	OperationWrite  OperationKind = "write"
	OperationReview OperationKind = "review"
)
