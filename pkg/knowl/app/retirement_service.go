package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	RetirementProjectionFailedCode = "retirement.projection_failed"
	RetirementCommitFailedCode     = "retirement.commit_failed"
	RetirementStagingFailedCode    = "retirement.staging_failed"
)

// RetirementPageOptions contains the established bounded planner and apply
// settings for one durable page operation.
type RetirementPageOptions struct {
	Planner       RetirementPlannerOptions
	LeaseDuration time.Duration
}

// RetirementOperationContent is the narrow canonical capability used by the
// durable page runner. It intentionally omits ordinary source acceptance and
// unrestricted commit authority.
type RetirementOperationContent interface {
	RetirementContentReader
	Schema(ctx context.Context, scope knowl.ScopeRef) (knowl.SchemaDocument, error)
	Inspect(ctx context.Context, scope knowl.ScopeRef) (knowl.WorkspaceInspection, error)
	StagePlan(ctx context.Context, plan knowl.ValidatedEditPlan) (knowl.StagedChange, error)
	LoadStage(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.StagedChange, error)
	Snapshot(ctx context.Context, scope knowl.ScopeRef) (knowl.WorkspaceSnapshot, error)
}

// RetirementPageService reserves and executes one idempotent page-scoped
// retirement unit through the existing generic operation lifecycle.
type RetirementPageService struct {
	content       RetirementOperationContent
	operations    OperationStore
	index         SearchIndex
	lifecycles    SourceLifecycleReader
	committer     RetirementCommitter
	planner       *RetirementPlanner
	catalogLimits knowl.CatalogLimits
	leaseDuration time.Duration
}

// NewRetirementPageService constructs the durable page runner.
func NewRetirementPageService(
	content RetirementOperationContent,
	operations OperationStore,
	index SearchIndex,
	lifecycles SourceLifecycleReader,
	committer RetirementCommitter,
	maintainer Maintainer,
	options RetirementPageOptions,
) (*RetirementPageService, error) {
	if content == nil || operations == nil || index == nil || lifecycles == nil || committer == nil {
		return nil, fmt.Errorf("retirement page dependencies are required")
	}
	planner, err := NewRetirementPlanner(content, lifecycles, maintainer, options.Planner)
	if err != nil {
		return nil, err
	}
	catalogLimits, err := NormalizeCatalogLimits(options.Planner.CatalogLimits)
	if err != nil {
		return nil, err
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = defaultLeaseDuration
	}
	return &RetirementPageService{
		content: content, operations: operations, index: index, lifecycles: lifecycles,
		committer: committer, planner: planner, catalogLimits: catalogLimits, leaseDuration: options.LeaseDuration,
	}, nil
}

// Reserve persists one immutable page identity before discovery may advance.
func (service *RetirementPageService) Reserve(
	ctx context.Context,
	scope knowl.ScopeRef,
	owner knowl.SourceOwner,
	retirementGeneration string,
	pageID knowl.PageID,
) (OperationReservation, error) {
	ctx = nonNilContext(ctx)
	if err := contextErr(ctx); err != nil {
		return OperationReservation{}, err
	}
	state, err := service.lifecycles.SourceLifecycle(ctx, scope, owner)
	if err != nil || ValidateSourceLifecycle(state) != nil || state.Scope != scope || state.Owner != owner ||
		state.State != knowl.SourceLifecycleRetiring || state.RetirementGeneration != retirementGeneration ||
		state.ReplacementPending != 0 || state.BlockerCode != "" {
		return OperationReservation{}, errors.Join(ErrRetirementLifecycleConflict, err)
	}
	schema, err := service.content.Schema(ctx, scope)
	if err != nil {
		return OperationReservation{}, fmt.Errorf("read retirement schema: %w", err)
	}
	identity := RetirementPageOperationIdentity(scope, retirementGeneration, pageID)
	id, err := OperationIDForIdentity(identity)
	if err != nil {
		return OperationReservation{}, err
	}
	descriptor := knowl.ExecutionDescriptor{
		OperationID: id, Kind: knowl.WorkSourceRetirementPage, Schema: schema,
		RetirementPage: &knowl.RetirementPageExecutionDescriptor{
			PageID: pageID, RetirementGeneration: retirementGeneration, Owner: owner,
		},
	}
	reservation, err := service.operations.ReserveOperation(ctx, identity, descriptor)
	if err != nil {
		replayed, replayErr := service.sharedPageReservation(ctx, scope, identity, descriptor)
		if replayErr == nil {
			return service.associateRetirementOperation(ctx, scope, replayed, owner, retirementGeneration, pageID)
		}
		return OperationReservation{}, fmt.Errorf("reserve retirement page: %w", err)
	}
	return service.associateRetirementOperation(ctx, scope, reservation, owner, retirementGeneration, pageID)
}

func (service *RetirementPageService) associateRetirementOperation(ctx context.Context, scope knowl.ScopeRef, reservation OperationReservation, owner knowl.SourceOwner, generation string, pageID knowl.PageID) (OperationReservation, error) {
	associator, ok := service.operations.(RetirementOperationAssociator)
	if !ok {
		return reservation, nil
	}
	if err := associator.AssociateRetirementOperation(ctx, scope, reservation.ID, owner, generation, pageID); err != nil {
		return OperationReservation{}, fmt.Errorf("associate retirement page: %w", err)
	}
	return reservation, nil
}

func (service *RetirementPageService) sharedPageReservation(
	ctx context.Context,
	scope knowl.ScopeRef,
	identity knowl.OperationIdentity,
	want knowl.ExecutionDescriptor,
) (OperationReservation, error) {
	id, err := OperationIDForIdentity(identity)
	if err != nil {
		return OperationReservation{}, err
	}
	operation, err := service.operations.Operation(ctx, scope, id)
	if err != nil {
		return OperationReservation{}, err
	}
	descriptor, err := service.operations.Execution(ctx, scope, id)
	if err != nil || descriptor.RetirementPage == nil || want.RetirementPage == nil ||
		descriptor.Kind != knowl.WorkSourceRetirementPage || descriptor.OperationID != id ||
		ValidateOperationDescriptor(identity, descriptor) != nil || descriptor.RetirementPage.PageID != want.RetirementPage.PageID ||
		descriptor.RetirementPage.RetirementGeneration != want.RetirementPage.RetirementGeneration {
		return OperationReservation{}, errors.Join(ErrExecutionDescriptorUnavailable, err)
	}
	state, err := service.lifecycles.SourceLifecycle(ctx, scope, descriptor.RetirementPage.Owner)
	if err != nil || ValidateSourceLifecycle(state) != nil || state.State != knowl.SourceLifecycleRetiring ||
		state.RetirementGeneration != descriptor.RetirementPage.RetirementGeneration {
		return OperationReservation{}, errors.Join(ErrRetirementLifecycleConflict, err)
	}
	return OperationReservation{Operation: operation, Descriptor: descriptor}, nil
}

// RunToTerminal executes one exclusively claimed retirement-page operation.
func (service *RetirementPageService) RunToTerminal(ctx context.Context, claim knowl.WorkClaim) (IngestResult, error) {
	ctx = nonNilContext(ctx)
	descriptor := claim.Descriptor
	if descriptor.Kind != knowl.WorkSourceRetirementPage || descriptor.RetirementPage == nil ||
		claim.Operation.ID != descriptor.OperationID || strings.TrimSpace(claim.Lease.Token) == "" ||
		ValidateGenericExecutionDescriptor(descriptor.Schema.Scope, descriptor) != nil {
		return IngestResult{}, ErrExecutionDescriptorUnavailable
	}
	scope, id := descriptor.Schema.Scope, descriptor.OperationID
	operation, err := service.operations.Operation(ctx, scope, id)
	if err != nil {
		return IngestResult{}, fmt.Errorf("read retirement operation: %w", err)
	}
	result := IngestResult{Operation: operation}
	if terminalOperation(operation.Status) {
		return result, nil
	}

	staged, loadErr := service.content.LoadStage(ctx, scope, id)
	switch {
	case loadErr == nil:
		result.Staged = staged
		if operation.Status == knowl.StatusReceived {
			if err := service.savePlan(ctx, operation.ID, staged); err != nil {
				return result, err
			}
		}
		return service.apply(ctx, result, descriptor, staged)
	case !errors.Is(loadErr, ErrStageNotFound):
		return service.fail(ctx, result, "staging", descriptor, nil, loadErr)
	case operation.Status == knowl.StatusApplying && operation.Plan != nil && operation.Plan.FileCount != nil && *operation.Plan.FileCount == 0:
		return service.commitNoOp(ctx, result, descriptor)
	}

	request, noOp, diagnostics, err := service.prepareRequest(ctx, descriptor)
	if err != nil {
		return service.fail(ctx, result, "planning", descriptor, diagnostics, err)
	}
	if noOp {
		return service.commitNoOp(ctx, result, descriptor)
	}
	planned, err := service.planner.PlanRetirement(ctx, request)
	if err != nil {
		return service.fail(ctx, result, "planning", descriptor, planned.Diagnostics, err)
	}
	validated, err := service.planner.BuildRetirementEditPlan(ctx, string(id), request, planned)
	if err != nil {
		return service.fail(ctx, result, "planning", descriptor, retirementErrorDiagnostics(err), err)
	}
	staged, err = service.content.StagePlan(ctx, validated)
	if err != nil {
		return service.fail(ctx, result, "staging", descriptor, staged.Diagnostics, err)
	}
	result.Plan, result.Staged = validated, staged
	if err := service.savePlan(ctx, id, staged); err != nil {
		return result, err
	}
	return service.apply(ctx, result, descriptor, staged)
}

func (service *RetirementPageService) prepareRequest(ctx context.Context, descriptor knowl.ExecutionDescriptor) (RetirementPlanningRequest, bool, []knowl.MaintenanceDiagnostic, error) {
	payload := descriptor.RetirementPage
	scope := descriptor.Schema.Scope
	currentSchema, err := service.content.Schema(ctx, scope)
	if err != nil || currentSchema.Digest != descriptor.Schema.Digest {
		return RetirementPlanningRequest{}, false, nil, errors.Join(ErrRetirementPageStale, err)
	}
	pages, err := service.content.ReadPages(ctx, scope, []knowl.PageID{payload.PageID}, service.planner.readLimits)
	if err != nil {
		return RetirementPlanningRequest{}, false, nil, err
	}
	if len(pages) == 0 {
		return RetirementPlanningRequest{}, true, nil, nil
	}
	if len(pages) != 1 {
		return RetirementPlanningRequest{}, false, nil, ErrRetirementPlanInvalid
	}
	page := pages[0]
	sources, err := service.content.ResolveAcceptedSources(ctx, scope, page.SourceRefs)
	if err != nil {
		return RetirementPlanningRequest{}, false, []knowl.MaintenanceDiagnostic{{Code: RetirementEvidenceUnavailableCode, Path: page.Path}}, err
	}
	retiring := make([]knowl.SourceLifecycle, 0)
	triggerFound := false
	for _, ref := range page.SourceRefs {
		source, ok := sources[ref]
		if !ok || source.SourceDocument.SourceID == "" {
			return RetirementPlanningRequest{}, false, []knowl.MaintenanceDiagnostic{{Code: RetirementEvidenceUnavailableCode, Path: page.Path, Target: ref}}, ErrRetirementEvidenceUnavailable
		}
		owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterID(source.Source.Adapter), SourceID: source.SourceDocument.SourceID}
		if owner == payload.Owner {
			triggerFound = true
		}
		state, stateErr := service.lifecycles.SourceLifecycle(ctx, scope, owner)
		if stateErr != nil {
			return RetirementPlanningRequest{}, false, []knowl.MaintenanceDiagnostic{{Code: RetirementLifecycleConflictCode, Path: page.Path, Target: ref}}, stateErr
		}
		if state.State == knowl.SourceLifecycleRetiring {
			retiring = append(retiring, state)
		}
	}
	if !triggerFound {
		return RetirementPlanningRequest{}, true, nil, nil
	}
	trigger, err := service.lifecycles.SourceLifecycle(ctx, scope, payload.Owner)
	if err != nil || trigger.State != knowl.SourceLifecycleRetiring || trigger.RetirementGeneration != payload.RetirementGeneration ||
		trigger.ReplacementPending != 0 || trigger.BlockerCode != "" {
		return RetirementPlanningRequest{}, false, []knowl.MaintenanceDiagnostic{{Code: RetirementLifecycleConflictCode, Path: page.Path}}, errors.Join(ErrRetirementLifecycleConflict, err)
	}
	authority, err := service.lifecycles.SourceLifecycleAuthority(ctx, scope)
	if err != nil {
		return RetirementPlanningRequest{}, false, []knowl.MaintenanceDiagnostic{{Code: RetirementLifecycleConflictCode, Path: page.Path}}, err
	}
	inspection, err := service.content.Inspect(ctx, scope)
	if err != nil {
		return RetirementPlanningRequest{}, false, nil, err
	}
	catalogs, err := catalogGraph(inspection.Catalogs, service.catalogLimits)
	if err != nil {
		return RetirementPlanningRequest{}, false, nil, err
	}
	sort.Slice(retiring, func(left, right int) bool { return sourceOwnerLess(retiring[left].Owner, retiring[right].Owner) })
	return RetirementPlanningRequest{
		Scope:      scope,
		Page:       CanonicalPageProvenance{ID: page.ID, Path: page.Path, Digest: page.Digest, SourceRefs: slices.Clone(page.SourceRefs)},
		SourceRefs: slices.Clone(page.SourceRefs), Retiring: retiring, Authority: authority,
		Schema: descriptor.Schema, Catalogs: catalogs,
	}, false, nil, nil
}

func (service *RetirementPageService) savePlan(ctx context.Context, id knowl.OperationID, staged knowl.StagedChange) error {
	return service.operations.SavePlan(ctx, id, knowl.PlanSummary{
		OperationID: string(id), Digest: staged.Digest, FileCount: len(staged.Files),
		CreatedAt: time.Now().UTC(), Diagnostics: staged.Diagnostics,
	})
}

func (service *RetirementPageService) commitNoOp(ctx context.Context, result IngestResult, descriptor knowl.ExecutionDescriptor) (IngestResult, error) {
	digest := retirementNoOpDigest(descriptor)
	id, scope := descriptor.OperationID, descriptor.Schema.Scope
	if result.Operation.Status == knowl.StatusReceived {
		if err := service.operations.SavePlan(ctx, id, knowl.PlanSummary{OperationID: string(id), Digest: digest, FileCount: 0, CreatedAt: time.Now().UTC()}); err != nil {
			return result, err
		}
		result.Operation.Status = knowl.StatusPlanned
	}
	stateCtx := durableContext(ctx)
	if result.Operation.Status != knowl.StatusApplying {
		lease, err := newLease(time.Now().UTC(), service.leaseDuration)
		if err != nil {
			return result, err
		}
		if err := service.operations.MarkApplying(stateCtx, id, lease); err != nil {
			return result, err
		}
	}
	if err := service.operations.CommitOutcome(stateCtx, id, knowl.ContentCommit{OperationID: string(id), Generation: "noop-" + digest}); err != nil {
		return result, err
	}
	operation, err := service.operations.Operation(stateCtx, scope, id)
	result.Operation = operation
	return result, err
}

func (service *RetirementPageService) apply(ctx context.Context, result IngestResult, descriptor knowl.ExecutionDescriptor, staged knowl.StagedChange) (IngestResult, error) {
	scope, id := descriptor.Schema.Scope, descriptor.OperationID
	tail, err := runApplyTail(ctx, applyTailRequest{
		operations: service.operations, scope: scope, id: id, leaseDuration: service.leaseDuration, reuseApplying: true,
		session: func(
			ctx context.Context,
			prepare func(context.Context) (bool, error),
			complete func(context.Context, knowl.ContentCommit) error,
		) (knowl.ContentCommit, error) {
			return service.committer.RunRetirementApply(ctx, staged, prepare, complete)
		},
		snapshot: service.content.Snapshot,
		project: func(ctx context.Context, _ knowl.Operation, commit knowl.ContentCommit) error {
			return service.index.Project(ctx, commit)
		},
	})
	result.Operation, result.Commit = tail.operation, tail.commit
	if err == nil {
		return result, nil
	}
	switch tail.stage {
	case applyTailCommit:
		return service.fail(ctx, result, "commit", descriptor, staged.Diagnostics, err)
	case applyTailProjection:
		return service.fail(ctx, result, "projection", descriptor, staged.Diagnostics, fmt.Errorf("%w: %w", ErrProjection, err))
	default:
		return result, err
	}
}

func (service *RetirementPageService) fail(
	ctx context.Context,
	result IngestResult,
	stage string,
	descriptor knowl.ExecutionDescriptor,
	diagnostics []knowl.MaintenanceDiagnostic,
	cause error,
) (IngestResult, error) {
	failure := service.pageFailure(stage, descriptor, diagnostics, cause)
	diagnosticStore, ok := service.operations.(OperationDiagnosticStore)
	if !ok || result.Operation.WorkAttempt <= 0 {
		return result, failure
	}
	stateCtx := durableContext(ctx)
	if err := diagnosticStore.SaveOperationDiagnostics(
		stateCtx,
		descriptor.Schema.Scope,
		descriptor.OperationID,
		result.Operation.WorkAttempt,
		failure.Diagnostics,
	); err != nil {
		return result, errors.Join(failure, err)
	}
	operation, err := service.operations.Operation(stateCtx, descriptor.Schema.Scope, descriptor.OperationID)
	if err != nil {
		return result, errors.Join(failure, err)
	}
	result.Operation = operation
	return result, failure
}

func (service *RetirementPageService) pageFailure(stage string, descriptor knowl.ExecutionDescriptor, diagnostics []knowl.MaintenanceDiagnostic, cause error) *RetirementOperationError {
	var authorityFailure interface {
		MaintenanceDiagnostics() []knowl.MaintenanceDiagnostic
	}
	if errors.As(cause, &authorityFailure) {
		diagnostics = append(append([]knowl.MaintenanceDiagnostic(nil), diagnostics...), authorityFailure.MaintenanceDiagnostics()...)
	}
	code := retirementFailureCode(stage, cause)
	path := ""
	if descriptor.RetirementPage != nil {
		path = "wiki/" + string(descriptor.RetirementPage.PageID) + ".md"
	}
	if len(diagnostics) == 0 && path != "" {
		diagnostics = []knowl.MaintenanceDiagnostic{{Code: code, Path: path}}
	}
	normalized, err := NormalizeMaintenanceDiagnostics(diagnostics)
	if err != nil {
		normalized = []knowl.MaintenanceDiagnostic{{Code: code, Path: path}}
	}
	return &RetirementOperationError{Stage: stage, Code: code, Diagnostics: normalized, Cause: cause}
}

// RetirementOperationError carries retry policy and bounded page diagnostics
// without exposing provider or storage error text as durable metadata.
type RetirementOperationError struct {
	Stage       string
	Code        string
	Diagnostics []knowl.MaintenanceDiagnostic
	Cause       error
}

func (failure *RetirementOperationError) Error() string         { return failure.Code }
func (failure *RetirementOperationError) Unwrap() error         { return failure.Cause }
func (failure *RetirementOperationError) FailureClass() string  { return "retirement_" + failure.Stage }
func (failure *RetirementOperationError) FailureReason() string { return failure.Code }
func (*RetirementOperationError) Retryable() bool               { return true }

func retirementFailureCode(stage string, cause error) string {
	var unit *RetirementUnitError
	if errors.As(cause, &unit) && unit.Code != "" {
		return unit.Code
	}
	for _, candidate := range []struct {
		err  error
		code string
	}{
		{ErrRetirementLifecycleConflict, RetirementLifecycleConflictCode},
		{ErrRetirementPageStale, RetirementPageStaleCode},
		{ErrRetirementCatalogClosureLimit, RetirementCatalogClosureLimitCode},
		{ErrRetirementMaintainerUnavailable, RetirementMaintainerUnavailableCode},
		{ErrRetirementEvidenceLimit, RetirementEvidenceLimitCode},
		{ErrRetirementEvidenceUnavailable, RetirementEvidenceUnavailableCode},
		{ErrProjection, RetirementProjectionFailedCode},
	} {
		if errors.Is(cause, candidate.err) {
			return candidate.code
		}
	}
	switch stage {
	case "commit":
		return RetirementCommitFailedCode
	case "staging":
		return RetirementStagingFailedCode
	default:
		return RetirementPlanInvalidCode
	}
}

func retirementErrorDiagnostics(err error) []knowl.MaintenanceDiagnostic {
	var unit *RetirementUnitError
	if errors.As(err, &unit) {
		return slices.Clone(unit.Diagnostics)
	}
	return nil
}

func retirementNoOpDigest(descriptor knowl.ExecutionDescriptor) string {
	payload := descriptor.RetirementPage
	value := string(descriptor.Schema.Scope) + "\x00" + string(payload.PageID) + "\x00" + payload.RetirementGeneration
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
