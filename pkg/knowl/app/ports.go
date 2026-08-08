// Package app owns Knowl application policy and its consuming ports.
package app

import (
	"context"

	"github.com/baldaworks/knowl/pkg/knowl"
)

// ContentStore owns canonical workspace content and recovery.
type ContentStore interface {
	AcceptSource(context.Context, knowl.SourceEnvelope) (knowl.AcceptedSource, error)
	Schema(context.Context, knowl.ScopeRef) (knowl.SchemaDocument, error)
	ReadPages(context.Context, knowl.ScopeRef, []knowl.PageID, knowl.ReadLimits) ([]knowl.PageSnapshot, error)
	StagePlan(context.Context, knowl.ValidatedEditPlan) (knowl.StagedChange, error)
	Commit(context.Context, knowl.StagedChange) (knowl.ContentCommit, error)
	Recover(context.Context) ([]knowl.RecoveryResult, error)
	Snapshot(context.Context, knowl.ScopeRef) (knowl.WorkspaceSnapshot, error)
}

// OperationStore owns durable operation state, leases, and redacted outcomes.
type OperationStore interface {
	Reserve(context.Context, knowl.OperationKey, knowl.OperationMeta) (knowl.Operation, error)
	SavePlan(context.Context, knowl.OperationID, knowl.PlanSummary) error
	MarkAwaitingReview(context.Context, knowl.OperationID) error
	MarkApplying(context.Context, knowl.OperationID, knowl.Lease) error
	CommitOutcome(context.Context, knowl.OperationID, knowl.ContentCommit) error
	Fail(context.Context, knowl.OperationID, knowl.Failure) error
	Operation(context.Context, knowl.ScopeRef, knowl.OperationID) (knowl.Operation, error)
}

// SearchIndex owns rebuildable context, text, and link projections.
type SearchIndex interface {
	SelectContext(context.Context, knowl.ScopeRef, knowl.SourceSummary, knowl.ReadLimits) ([]knowl.PageID, error)
	Search(context.Context, knowl.ScopeRef, string, knowl.ReadLimits) ([]knowl.PageReference, error)
	Links(context.Context, knowl.ScopeRef, knowl.PageID, knowl.ReadLimits) ([]knowl.LinkReference, error)
	Project(context.Context, knowl.ContentCommit) error
	Rebuild(context.Context, knowl.WorkspaceSnapshot) error
}

// Maintainer produces structured data-only edit plans.
type Maintainer interface {
	Plan(context.Context, knowl.MaintenanceInput) (knowl.ModelEditPlan, error)
}

// ScopeAuthorizer enforces the host's trusted scope boundary.
type ScopeAuthorizer interface {
	Authorize(context.Context, knowl.ScopeRef, OperationKind) error
}

// OperationKind identifies the policy operation being authorized.
type OperationKind string

const (
	OperationRead   OperationKind = "read"
	OperationWrite  OperationKind = "write"
	OperationReview OperationKind = "review"
)
