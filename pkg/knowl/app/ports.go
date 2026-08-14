// Package app owns Knowl application policy and its consuming ports.
package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const maxExecutionSchemaBytes = 4 << 20

var (
	// ErrNoReadyOperation reports that durable work inspection found no claimable operation.
	ErrNoReadyOperation = errors.New("no ready Knowl operation")
	// ErrExecutionDescriptorUnavailable reports missing or invalid durable execution inputs.
	ErrExecutionDescriptorUnavailable = errors.New("knowl execution descriptor is unavailable")
	// ErrWorkLeaseConflict reports that another worker owns or replaced a work lease.
	ErrWorkLeaseConflict = errors.New("knowl operation work lease conflicts")
	// ErrStageNotFound reports that no complete staged artifact exists for an operation.
	ErrStageNotFound = errors.New("knowl staged artifact not found")
	// ErrApplyLeaseConflict reports that canonical application is still owned by another attempt.
	ErrApplyLeaseConflict = errors.New("knowl operation application lease conflicts")
)

// ContentStore owns canonical workspace content and recovery.
type ContentStore interface {
	AcceptSource(ctx context.Context, envelope knowl.SourceEnvelope) (knowl.AcceptedSource, error)
	ReadSource(ctx context.Context, source knowl.AcceptedSource, limits knowl.ReadLimits) ([]byte, error)
	Schema(ctx context.Context, scope knowl.ScopeRef) (knowl.SchemaDocument, error)
	ReadPages(ctx context.Context, scope knowl.ScopeRef, ids []knowl.PageID, limits knowl.ReadLimits) ([]knowl.PageSnapshot, error)
	StagePlan(ctx context.Context, plan knowl.ValidatedEditPlan) (knowl.StagedChange, error)
	LoadStage(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.StagedChange, error)
	Commit(ctx context.Context, staged knowl.StagedChange) (knowl.ContentCommit, error)
	Recover(ctx context.Context) ([]knowl.RecoveryResult, error)
	Snapshot(ctx context.Context, scope knowl.ScopeRef) (knowl.WorkspaceSnapshot, error)
	Inspect(ctx context.Context, scope knowl.ScopeRef) (knowl.WorkspaceInspection, error)
}

// OperationStore owns durable operation state, leases, and redacted outcomes.
type OperationStore interface {
	Reserve(ctx context.Context, key knowl.OperationKey, meta knowl.OperationMeta) (OperationReservation, error)
	Execution(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.ExecutionDescriptor, error)
	ResumeReady(ctx context.Context, scope knowl.ScopeRef, limit int) ([]knowl.OperationID, error)
	ClaimReady(ctx context.Context, scope knowl.ScopeRef, lease knowl.WorkLease) (knowl.WorkClaim, error)
	RenewClaim(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, currentToken string, next knowl.WorkLease) error
	ReleaseClaim(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, token string) error
	DescriptorFailures(ctx context.Context, scope knowl.ScopeRef, limit int) ([]knowl.OperationID, error)
	SavePlan(ctx context.Context, id knowl.OperationID, summary knowl.PlanSummary) error
	MarkAwaitingReview(ctx context.Context, id knowl.OperationID) error
	MarkApplying(ctx context.Context, id knowl.OperationID, lease knowl.Lease) error
	CommitOutcome(ctx context.Context, id knowl.OperationID, commit knowl.ContentCommit) error
	Fail(ctx context.Context, id knowl.OperationID, failure knowl.Failure) error
	Operation(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.Operation, error)
}

// OperationReservation is the durable result of reserving one immutable source revision.
// New reports whether this call created the operation rather than replaying it.
type OperationReservation struct {
	knowl.Operation
	Descriptor knowl.ExecutionDescriptor
	New        bool
}

// ExecutionDescriptorFromMeta validates reservation inputs and builds the
// bounded descriptor that operational stores persist atomically.
func ExecutionDescriptorFromMeta(id knowl.OperationID, key knowl.OperationKey, meta knowl.OperationMeta) (knowl.ExecutionDescriptor, error) {
	descriptor := knowl.ExecutionDescriptor{OperationID: id, Source: meta.AcceptedSource, Schema: meta.Schema}
	if err := ValidateExecutionDescriptor(key, descriptor); err != nil {
		return knowl.ExecutionDescriptor{}, err
	}
	if meta.Key != (knowl.OperationKey{}) && meta.Key != key {
		return knowl.ExecutionDescriptor{}, fmt.Errorf("operation metadata key differs: %w", ErrExecutionDescriptorUnavailable)
	}
	if meta.SchemaDigest != "" && meta.SchemaDigest != descriptor.Schema.Digest {
		return knowl.ExecutionDescriptor{}, fmt.Errorf("operation metadata schema digest differs: %w", ErrExecutionDescriptorUnavailable)
	}
	return descriptor, nil
}

// ValidateExecutionDescriptor verifies durable execution identity and bounded
// schema content without exposing descriptor data in errors.
func ValidateExecutionDescriptor(key knowl.OperationKey, descriptor knowl.ExecutionDescriptor) error {
	source := descriptor.Source
	schema := descriptor.Schema
	if strings.TrimSpace(string(key.Scope)) == "" ||
		strings.TrimSpace(key.Source.Adapter) == "" ||
		strings.TrimSpace(key.Source.ID) == "" ||
		strings.TrimSpace(key.Version.Version) == "" ||
		strings.TrimSpace(key.Version.Digest) == "" ||
		source.Scope != key.Scope || source.Source != key.Source || source.Version != key.Version ||
		strings.TrimSpace(source.MediaType) == "" || !validManifestRef(source.ManifestRef) ||
		schema.Scope != key.Scope || strings.TrimSpace(schema.Digest) == "" ||
		len(schema.Content) == 0 || len(schema.Content) > maxExecutionSchemaBytes {
		return ErrExecutionDescriptorUnavailable
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(schema.Content))
	if !strings.EqualFold(schema.Digest, digest) {
		return ErrExecutionDescriptorUnavailable
	}
	return nil
}

func validManifestRef(ref string) bool {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" || strings.Contains(trimmed, `\`) || path.IsAbs(trimmed) {
		return false
	}
	cleaned := path.Clean(trimmed)
	return cleaned == trimmed && cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
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
