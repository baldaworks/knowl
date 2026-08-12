package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

var (
	ErrOperationNotApplyable = errors.New("operation is not ready to apply")
	ErrProjection            = errors.New("canonical content committed but projection failed")
)

const (
	defaultReadPages      = 20
	defaultReadBytes      = 4 << 20
	defaultReadCharacters = 32 << 10
	defaultReadDepth      = 8
	defaultLeaseDuration  = 5 * time.Minute
)

// IngestOptions configures bounded planning and the review gate.
type IngestOptions struct {
	PlanLimits    PlanLimits
	ReadLimits    knowl.ReadLimits
	LeaseDuration time.Duration
	AutoApply     bool
}

// DefaultReadLimits returns the local bounded context defaults.
func DefaultReadLimits() knowl.ReadLimits {
	return knowl.ReadLimits{
		Pages:      defaultReadPages,
		Bytes:      defaultReadBytes,
		Characters: defaultReadCharacters,
		Depth:      defaultReadDepth,
		Deadline:   30 * time.Second,
	}
}

// IngestResult contains the durable operation and any plan or commit produced by this call.
type IngestResult struct {
	Operation knowl.Operation
	Plan      knowl.ValidatedEditPlan
	Staged    knowl.StagedChange
	Commit    *knowl.ContentCommit
}

// IngestSubmission is the durable handoff from request-time source acceptance
// to host-owned maintenance execution.
type IngestSubmission struct {
	Operation knowl.Operation
	accepted  knowl.AcceptedSource
	schema    knowl.SchemaDocument
	new       bool
}

// NeedsExecution reports whether this submission created work for the host queue.
func (submission IngestSubmission) NeedsExecution() bool { return submission.new }

// ApplyResult contains the durable operation and the canonical commit, when one occurred.
type ApplyResult struct {
	Operation knowl.Operation
	Commit    *knowl.ContentCommit
}

// IngestService coordinates source acceptance, planning, review, commit, and projection.
type IngestService struct {
	content       ContentStore
	operations    OperationStore
	index         SearchIndex
	maintainer    Maintainer
	planLimits    PlanLimits
	readLimits    knowl.ReadLimits
	leaseDuration time.Duration
	autoApply     bool
}

var _ interface {
	Submit(ctx context.Context, envelope knowl.SourceEnvelope) (IngestSubmission, error)
	Execute(ctx context.Context, submission IngestSubmission) (IngestResult, error)
	Ingest(ctx context.Context, envelope knowl.SourceEnvelope) (IngestResult, error)
	Apply(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (ApplyResult, error)
	Recover(ctx context.Context) ([]knowl.RecoveryResult, error)
} = (*IngestService)(nil)

// NewIngestService constructs the application workflow with conservative defaults.
func NewIngestService(content ContentStore, operations OperationStore, index SearchIndex, maintainer Maintainer, options IngestOptions) (*IngestService, error) {
	if content == nil || operations == nil || index == nil || maintainer == nil {
		return nil, fmt.Errorf("ingest dependencies are required")
	}
	if options.PlanLimits == (PlanLimits{}) {
		options.PlanLimits = DefaultPlanLimits()
	}
	if options.PlanLimits.MaxFiles <= 0 || options.PlanLimits.MaxFileBytes <= 0 || options.PlanLimits.MaxSourceRefs <= 0 || options.PlanLimits.MaxRationaleSize <= 0 {
		return nil, fmt.Errorf("invalid ingest plan limits: %w", ErrPlanInvalid)
	}
	if options.ReadLimits == (knowl.ReadLimits{}) {
		options.ReadLimits = DefaultReadLimits()
	}
	if options.ReadLimits.Pages <= 0 || options.ReadLimits.Bytes <= 0 || options.ReadLimits.Characters <= 0 || options.ReadLimits.Depth <= 0 {
		return nil, fmt.Errorf("invalid ingest read limits")
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = defaultLeaseDuration
	}
	return &IngestService{
		content:       content,
		operations:    operations,
		index:         index,
		maintainer:    maintainer,
		planLimits:    options.PlanLimits,
		readLimits:    options.ReadLimits,
		leaseDuration: options.LeaseDuration,
		autoApply:     options.AutoApply,
	}, nil
}

// Submit accepts one immutable source revision and reserves its durable operation.
// It does not invoke the maintainer or make canonical workspace changes.
func (service *IngestService) Submit(ctx context.Context, envelope knowl.SourceEnvelope) (IngestSubmission, error) {
	ctx = nonNilContext(ctx)
	if err := contextErr(ctx); err != nil {
		return IngestSubmission{}, err
	}
	accepted, err := service.content.AcceptSource(ctx, envelope)
	if err != nil {
		return IngestSubmission{}, fmt.Errorf("accept source: %w", err)
	}
	readCtx, cancel := service.boundedContext(ctx)
	defer cancel()
	schema, err := service.content.Schema(readCtx, accepted.Scope)
	if err != nil {
		return IngestSubmission{}, fmt.Errorf("read schema: %w", err)
	}
	key := knowl.OperationKey{Scope: accepted.Scope, Source: accepted.Source, Version: accepted.Version}
	reservation, err := service.operations.Reserve(readCtx, key, knowl.OperationMeta{Key: key, SchemaDigest: schema.Digest, CreatedAt: time.Now().UTC()})
	if err != nil {
		return IngestSubmission{}, fmt.Errorf("reserve operation: %w", err)
	}
	return IngestSubmission{
		Operation: reservation.Operation,
		accepted:  accepted,
		schema:    schema,
		new:       reservation.New,
	}, nil
}

// Execute runs host-owned maintenance for a previously accepted source revision.
func (service *IngestService) Execute(ctx context.Context, submission IngestSubmission) (IngestResult, error) {
	return service.execute(ctx, submission, nil, false)
}

// FailSubmission records a failure after durable submission could not be scheduled.
func (service *IngestService) FailSubmission(ctx context.Context, submission IngestSubmission, class string) error {
	if err := service.operations.Fail(durableContext(ctx), submission.Operation.ID, knowl.Failure{
		Class:       class,
		OperationID: string(submission.Operation.ID),
	}); err != nil {
		return fmt.Errorf("record %s failure: %w", class, err)
	}
	return nil
}

// Ingest is a synchronous application convenience that submits then executes one source revision.
func (service *IngestService) Ingest(ctx context.Context, envelope knowl.SourceEnvelope) (IngestResult, error) {
	return service.ingest(ctx, envelope, nil, false)
}

// Preview accepts and stages one source revision without applying it, even when auto-apply is enabled.
func (service *IngestService) Preview(ctx context.Context, envelope knowl.SourceEnvelope) (IngestResult, error) {
	return service.ingest(ctx, envelope, nil, true)
}

// FilePlan files an explicitly supplied query or maintenance plan through the same ingest gate.
func (service *IngestService) FilePlan(ctx context.Context, envelope knowl.SourceEnvelope, plan knowl.ModelEditPlan) (IngestResult, error) {
	return service.ingest(ctx, envelope, &plan, false)
}

func (service *IngestService) ingest(ctx context.Context, envelope knowl.SourceEnvelope, suppliedPlan *knowl.ModelEditPlan, forceReview bool) (IngestResult, error) {
	submission, err := service.Submit(ctx, envelope)
	if err != nil {
		return IngestResult{}, err
	}
	return service.execute(ctx, submission, suppliedPlan, forceReview)
}

func (service *IngestService) execute(ctx context.Context, submission IngestSubmission, suppliedPlan *knowl.ModelEditPlan, forceReview bool) (IngestResult, error) {
	ctx = nonNilContext(ctx)
	if err := contextErr(ctx); err != nil {
		return IngestResult{}, err
	}
	operation := submission.Operation
	result := IngestResult{Operation: operation}
	switch operation.Status {
	case knowl.StatusCommitted, knowl.StatusAwaitingReview, knowl.StatusApplying, knowl.StatusFailed:
		return result, nil
	}

	readCtx, cancel := service.boundedContext(ctx)
	sourceText, err := service.content.ReadSource(readCtx, submission.accepted, service.readLimits)
	if err != nil {
		cancel()
		return service.failIngest(ctx, result, "source", err)
	}
	pageIDs, err := service.index.SelectContext(readCtx, submission.accepted.Scope, knowl.SourceSummary{Source: submission.accepted.Source, Version: submission.accepted.Version}, service.readLimits)
	if err != nil {
		cancel()
		return service.failIngest(ctx, result, "context", err)
	}
	pages, err := service.content.ReadPages(readCtx, submission.accepted.Scope, pageIDs, service.readLimits)
	cancel()
	if err != nil {
		return service.failIngest(ctx, result, "content", err)
	}
	var modelPlan knowl.ModelEditPlan
	if suppliedPlan != nil {
		modelPlan = *suppliedPlan
	} else {
		modelPlan, err = service.maintainer.Plan(ctx, knowl.MaintenanceInput{
			Scope:      submission.accepted.Scope,
			Schema:     submission.schema,
			Source:     submission.accepted,
			SourceText: string(sourceText),
			Pages:      pages,
			Limits:     service.readLimits,
		})
		if err != nil {
			return service.failIngest(ctx, result, "provider", err)
		}
	}
	validated, err := ValidatePlan(ctx, knowl.MaintenanceInput{
		Scope:      submission.accepted.Scope,
		Schema:     submission.schema,
		Source:     submission.accepted,
		SourceText: string(sourceText),
		Pages:      pages,
		Limits:     service.readLimits,
	}, modelPlan, service.planLimits)
	if err != nil {
		return service.failIngest(ctx, result, "plan_validation", err)
	}
	validated.OperationID = string(operation.ID)
	planDigest, err := PlanDigest(validated)
	if err != nil {
		return service.failIngest(ctx, result, "plan", err)
	}
	if err := service.operations.SavePlan(ctx, operation.ID, knowl.PlanSummary{OperationID: string(operation.ID), Digest: planDigest, FileCount: len(validated.Edits), CreatedAt: time.Now().UTC()}); err != nil {
		if advanced, current, readErr := service.advancedIngest(ctx, submission.accepted.Scope, operation.ID); readErr == nil {
			if advanced {
				return current, nil
			}
			return current, fmt.Errorf("concurrent plan did not win: %w", err)
		}
		return service.failIngest(ctx, result, "operation", err)
	}
	staged, err := service.content.StagePlan(ctx, validated)
	if err != nil {
		return service.failIngest(ctx, result, "staging", err)
	}
	result.Plan = validated
	result.Staged = staged
	if forceReview || !service.autoApply {
		if err := service.operations.MarkAwaitingReview(ctx, operation.ID); err != nil {
			if advanced, current, readErr := service.advancedIngest(ctx, submission.accepted.Scope, operation.ID); readErr == nil {
				if advanced {
					return current, nil
				}
				return current, fmt.Errorf("concurrent review transition did not win: %w", err)
			}
			return service.failIngest(ctx, result, "operation", err)
		}
		result.Operation, err = service.operations.Operation(ctx, submission.accepted.Scope, operation.ID)
		if err != nil {
			return result, fmt.Errorf("read planned operation: %w", err)
		}
		return result, nil
	}

	applied, err := service.apply(ctx, submission.accepted.Scope, operation.ID, staged)
	result.Operation = applied.Operation
	result.Commit = applied.Commit
	return result, err
}

// Apply explicitly applies a planned operation after review.
func (service *IngestService) Apply(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (ApplyResult, error) {
	ctx = nonNilContext(ctx)
	if err := contextErr(ctx); err != nil {
		return ApplyResult{}, err
	}
	operation, err := service.operations.Operation(ctx, scope, id)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("read operation: %w", err)
	}
	if operation.Status == knowl.StatusCommitted {
		return ApplyResult{Operation: operation}, nil
	}
	if operation.Status == knowl.StatusFailed {
		return ApplyResult{Operation: operation}, fmt.Errorf("operation %q failed: %w", id, ErrOperationNotApplyable)
	}
	return service.apply(ctx, scope, id, knowl.StagedChange{OperationID: string(id)})
}

// Recover rolls back prepared canonical commits before the caller announces readiness.
func (service *IngestService) Recover(ctx context.Context) ([]knowl.RecoveryResult, error) {
	ctx = nonNilContext(ctx)
	results, err := service.content.Recover(ctx)
	if err != nil {
		return nil, err
	}
	stateCtx := durableContext(ctx)
	for _, result := range results {
		if result.Action != "rolled_back" || result.OperationID == "" {
			continue
		}
		if err := service.operations.Fail(stateCtx, knowl.OperationID(result.OperationID), knowl.Failure{Class: "recovery", OperationID: result.OperationID}); err != nil {
			return nil, fmt.Errorf("record recovered operation %q: %w", result.OperationID, err)
		}
	}
	return results, nil
}

func (service *IngestService) apply(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, staged knowl.StagedChange) (ApplyResult, error) {
	operation, err := service.operations.Operation(ctx, scope, id)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("read operation before apply: %w", err)
	}
	if operation.Status == knowl.StatusCommitted {
		return ApplyResult{Operation: operation}, nil
	}
	if operation.Status == knowl.StatusFailed {
		return ApplyResult{Operation: operation}, fmt.Errorf("operation %q failed: %w", id, ErrOperationNotApplyable)
	}
	lease, err := newLease(time.Now().UTC(), service.leaseDuration)
	if err != nil {
		return ApplyResult{Operation: operation}, err
	}
	stateCtx := durableContext(ctx)
	if err := service.operations.MarkApplying(stateCtx, id, lease); err != nil {
		return ApplyResult{Operation: operation}, fmt.Errorf("claim operation: %w", err)
	}
	commit, err := service.content.Commit(ctx, staged)
	if err != nil {
		return service.failApply(stateCtx, scope, id, operation, "commit", err)
	}
	snapshot, err := service.content.Snapshot(ctx, scope)
	if err != nil {
		return service.failAfterCommit(stateCtx, scope, id, operation, commit, err)
	}
	commit.Snapshot = snapshot
	if err := service.index.Project(ctx, commit); err != nil {
		return service.failAfterCommit(stateCtx, scope, id, operation, commit, err)
	}
	if err := service.operations.CommitOutcome(stateCtx, id, commit); err != nil {
		current, readErr := service.operations.Operation(stateCtx, scope, id)
		if readErr == nil {
			operation = current
		}
		return ApplyResult{Operation: operation, Commit: &commit}, fmt.Errorf("record commit outcome: %w", err)
	}
	operation, err = service.operations.Operation(stateCtx, scope, id)
	if err != nil {
		return ApplyResult{Commit: &commit}, fmt.Errorf("read committed operation: %w", err)
	}
	return ApplyResult{Operation: operation, Commit: &commit}, nil
}

func (service *IngestService) failAfterCommit(
	ctx context.Context,
	scope knowl.ScopeRef,
	id knowl.OperationID,
	operation knowl.Operation,
	commit knowl.ContentCommit,
	cause error,
) (ApplyResult, error) {
	err := fmt.Errorf("%w: %w", ErrProjection, cause)
	if failErr := service.operations.Fail(ctx, id, knowl.Failure{Class: "projection", OperationID: string(id)}); failErr != nil {
		return ApplyResult{Operation: operation, Commit: &commit}, errors.Join(err, fmt.Errorf("record projection failure: %w", failErr))
	}
	if current, readErr := service.operations.Operation(ctx, scope, id); readErr == nil {
		operation = current
	}
	return ApplyResult{Operation: operation, Commit: &commit}, err
}

func (service *IngestService) failIngest(ctx context.Context, result IngestResult, class string, cause error) (IngestResult, error) {
	if err := service.operations.Fail(durableContext(ctx), result.Operation.ID, knowl.Failure{Class: class, OperationID: string(result.Operation.ID)}); err != nil {
		return result, errors.Join(cause, fmt.Errorf("record %s failure: %w", class, err))
	}
	result.Operation.Status = knowl.StatusFailed
	result.Operation.Failure = &knowl.Failure{Class: class, OperationID: string(result.Operation.ID)}
	return result, fmt.Errorf("%s: %w", class, cause)
}

func (service *IngestService) failApply(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, operation knowl.Operation, class string, cause error) (ApplyResult, error) {
	stateCtx := durableContext(ctx)
	if err := service.operations.Fail(stateCtx, id, knowl.Failure{Class: class, OperationID: string(id)}); err != nil {
		return ApplyResult{Operation: operation}, errors.Join(cause, fmt.Errorf("record %s failure: %w", class, err))
	}
	operation, readErr := service.operations.Operation(stateCtx, scope, id)
	if readErr != nil {
		return ApplyResult{Operation: operation}, errors.Join(cause, fmt.Errorf("read failed operation: %w", readErr))
	}
	return ApplyResult{Operation: operation}, fmt.Errorf("%s: %w", class, cause)
}

func (service *IngestService) advancedIngest(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (bool, IngestResult, error) {
	operation, err := service.operations.Operation(ctx, scope, id)
	if err != nil {
		return false, IngestResult{}, err
	}
	if operation.Status == knowl.StatusReceived || operation.Status == knowl.StatusPlanned {
		return false, IngestResult{}, nil
	}
	return true, IngestResult{Operation: operation}, nil
}

func (service *IngestService) boundedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if service.readLimits.Deadline <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, service.readLimits.Deadline)
}

func durableContext(ctx context.Context) context.Context {
	return context.WithoutCancel(nonNilContext(ctx))
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func newLease(now time.Time, duration time.Duration) (knowl.Lease, error) {
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return knowl.Lease{}, fmt.Errorf("generate operation lease: %w", err)
	}
	return knowl.Lease{Token: hex.EncodeToString(tokenBytes[:]), ExpiresAt: now.Add(duration)}, nil
}
