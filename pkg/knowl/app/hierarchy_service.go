package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const DefaultHierarchyPlannerVersion = "hierarchy-v3"

// HierarchyOptions configures explicit hierarchy reconciliation.
type HierarchyOptions struct {
	Limits         knowl.HierarchyLimits
	PlannerVersion string
	LeaseDuration  time.Duration
}

// HierarchyService coordinates durable, explicit catalog-only reconciliation.
type HierarchyService struct {
	content       ContentStore
	operations    OperationStore
	index         SearchIndex
	maintainer    HierarchyMaintainer
	limits        knowl.HierarchyLimits
	planner       string
	leaseDuration time.Duration
}

func NewHierarchyService(content ContentStore, operations OperationStore, index SearchIndex, maintainer HierarchyMaintainer, options HierarchyOptions) (*HierarchyService, error) {
	if content == nil || operations == nil || index == nil || maintainer == nil {
		return nil, fmt.Errorf("hierarchy dependencies are required")
	}
	if options.Limits == (knowl.HierarchyLimits{}) {
		options.Limits = DefaultHierarchyLimits()
	}
	if err := validateHierarchyLimits(options.Limits); err != nil {
		return nil, err
	}
	if options.PlannerVersion == "" {
		options.PlannerVersion = DefaultHierarchyPlannerVersion
	}
	if !validStoredText(options.PlannerVersion, maxPlannerVersionBytes, false) {
		return nil, ErrExecutionDescriptorUnavailable
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = defaultLeaseDuration
	}
	return &HierarchyService{
		content: content, operations: operations, index: index, maintainer: maintainer,
		limits: options.Limits, planner: options.PlannerVersion, leaseDuration: options.LeaseDuration,
	}, nil
}

// Reconcile reserves, exclusively claims, and synchronously executes the
// hierarchy operation for an explicit trusted mutation workflow.
func (service *HierarchyService) Reconcile(ctx context.Context, scope knowl.ScopeRef) (IngestResult, error) {
	ctx = nonNilContext(ctx)
	reservation, err := service.Reserve(ctx, scope)
	if err != nil {
		return IngestResult{}, err
	}
	result := IngestResult{Operation: reservation.Operation}
	if terminalOperation(reservation.Status) {
		if reservation.Status == knowl.StatusFailed {
			class := "operation"
			if reservation.Failure != nil && reservation.Failure.Class != "" {
				class = reservation.Failure.Class
			}
			return result, fmt.Errorf("hierarchy operation failed with class %q", class)
		}
		return result, nil
	}
	lease, err := newLease(time.Now().UTC(), service.leaseDuration)
	if err != nil {
		return result, err
	}
	claim, err := service.operations.ClaimOperation(ctx, scope, reservation.ID, knowl.WorkLease(lease))
	if err != nil {
		return result, fmt.Errorf("claim hierarchy operation: %w", err)
	}
	return service.runClaim(ctx, claim)
}

func (service *HierarchyService) runClaim(ctx context.Context, claim knowl.WorkClaim) (IngestResult, error) {
	executionCtx, cancelExecution := context.WithCancel(ctx)
	renewCtx, stopRenewal := context.WithCancel(ctx)
	renewalDone := make(chan struct{})
	go func() {
		defer close(renewalDone)
		service.renewClaim(renewCtx, cancelExecution, claim)
	}()
	result, err := service.RunToTerminal(executionCtx, claim)
	stopRenewal()
	<-renewalDone
	cancelExecution()
	return result, err
}

func (service *HierarchyService) renewClaim(ctx context.Context, cancelExecution context.CancelFunc, claim knowl.WorkClaim) {
	interval := service.leaseDuration / 3
	if interval <= 0 {
		interval = time.Nanosecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	currentToken := claim.Lease.Token
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lease, err := newLease(time.Now().UTC(), service.leaseDuration)
			if err != nil {
				cancelExecution()
				return
			}
			next := knowl.WorkLease(lease)
			if err := service.operations.RenewClaim(ctx, claim.Descriptor.Schema.Scope, claim.Operation.ID, currentToken, next); err != nil {
				cancelExecution()
				return
			}
			currentToken = next.Token
		}
	}
}

// Reserve captures immutable execution identity without invoking the provider.
func (service *HierarchyService) Reserve(ctx context.Context, scope knowl.ScopeRef) (OperationReservation, error) {
	ctx = nonNilContext(ctx)
	if err := contextErr(ctx); err != nil {
		return OperationReservation{}, err
	}
	schema, err := service.content.Schema(ctx, scope)
	if err != nil {
		return OperationReservation{}, fmt.Errorf("read hierarchy schema: %w", err)
	}
	snapshotDigest, err := service.content.HierarchySnapshotDigest(ctx, scope)
	if err != nil {
		return OperationReservation{}, fmt.Errorf("inspect hierarchy snapshot: %w", err)
	}
	identity := knowl.OperationIdentity{
		Scope: scope, Kind: knowl.WorkHierarchy, Subject: service.planner,
		Revision: schema.Digest, Digest: snapshotDigest,
	}
	id, err := OperationIDForIdentity(identity)
	if err != nil {
		return OperationReservation{}, err
	}
	descriptor := knowl.ExecutionDescriptor{
		OperationID: id, Kind: knowl.WorkHierarchy,
		Hierarchy: &knowl.HierarchyExecutionDescriptor{SnapshotDigest: snapshotDigest, PlannerVersion: service.planner},
		Schema:    schema,
	}
	reservation, err := service.operations.ReserveOperation(ctx, identity, descriptor)
	if err != nil {
		return OperationReservation{}, fmt.Errorf("reserve hierarchy operation: %w", err)
	}
	return reservation, nil
}

// RunToTerminal executes one exclusively claimed hierarchy operation.
func (service *HierarchyService) RunToTerminal(ctx context.Context, claim knowl.WorkClaim) (IngestResult, error) {
	ctx = nonNilContext(ctx)
	descriptor := claim.Descriptor
	if descriptor.Kind != knowl.WorkHierarchy || descriptor.Hierarchy == nil || claim.Operation.ID != descriptor.OperationID ||
		strings.TrimSpace(claim.Lease.Token) == "" || ValidateGenericExecutionDescriptor(descriptor.Schema.Scope, descriptor) != nil {
		return IngestResult{}, ErrExecutionDescriptorUnavailable
	}
	scope, id := descriptor.Schema.Scope, descriptor.OperationID
	operation, err := service.operations.Operation(ctx, scope, id)
	if err != nil {
		return IngestResult{}, fmt.Errorf("read hierarchy operation: %w", err)
	}
	result := IngestResult{Operation: operation}
	if terminalOperation(operation.Status) {
		return result, nil
	}
	if descriptor.Hierarchy.PlannerVersion != service.planner {
		return service.fail(ctx, result, "descriptor", ErrExecutionDescriptorUnavailable)
	}
	schema, err := service.content.Schema(ctx, scope)
	if err != nil || schema.Digest != descriptor.Schema.Digest {
		return service.fail(ctx, result, "precondition", errors.Join(ErrHierarchyDigestMismatch, err))
	}
	staged, loadErr := service.content.LoadHierarchyStage(ctx, scope, id)
	switch {
	case loadErr == nil:
		result.Staged = staged
		if operation.Status == knowl.StatusReceived {
			if err := service.savePlan(ctx, id, staged.Digest, len(staged.Files)); err != nil {
				return service.fail(ctx, result, "operation", err)
			}
		}
	case !errors.Is(loadErr, ErrStageNotFound):
		return service.fail(ctx, result, "staging", loadErr)
	default:
		snapshotDigest, snapshotErr := service.content.HierarchySnapshotDigest(ctx, scope)
		if snapshotErr != nil || snapshotDigest != descriptor.Hierarchy.SnapshotDigest {
			return service.fail(ctx, result, "precondition", errors.Join(ErrHierarchyDigestMismatch, snapshotErr))
		}
		validated, planErr := service.plan(ctx, scope, descriptor)
		if planErr != nil {
			return service.fail(ctx, result, failureClass(planErr), planErr)
		}
		planDigest, digestErr := HierarchyPlanDigest(validated)
		if digestErr != nil {
			return service.fail(ctx, result, "plan", digestErr)
		}
		if len(validated.Mutations) == 0 {
			return service.commitNoOp(ctx, result, scope, planDigest)
		}
		staged, stageErr := service.content.StageHierarchyPlan(ctx, id, validated)
		if stageErr != nil {
			return service.fail(ctx, result, "staging", stageErr)
		}
		result.Staged = staged
		if err := service.savePlan(ctx, id, staged.Digest, len(staged.Files)); err != nil {
			return service.fail(ctx, result, "operation", err)
		}
	}
	return service.apply(ctx, result, scope, id)
}

func (service *HierarchyService) plan(ctx context.Context, scope knowl.ScopeRef, descriptor knowl.ExecutionDescriptor) (knowl.ValidatedHierarchyPlan, error) {
	inspection, err := service.content.Inspect(ctx, scope)
	if err != nil {
		return knowl.ValidatedHierarchyPlan{}, fmt.Errorf("inspection: %w", err)
	}
	input, forbidden, err := hierarchyInputFromInspection(inspection, descriptor.Schema, descriptor.Hierarchy.SnapshotDigest, service.limits)
	if err != nil {
		return knowl.ValidatedHierarchyPlan{}, fmt.Errorf("descriptor: %w", err)
	}
	input, err = NormalizeHierarchyInput(input)
	if err != nil {
		return knowl.ValidatedHierarchyPlan{}, fmt.Errorf("descriptor: %w", err)
	}
	model, err := service.maintainer.PlanHierarchy(ctx, input)
	if err != nil {
		return knowl.ValidatedHierarchyPlan{}, fmt.Errorf("provider: %w", err)
	}
	validated, err := ValidateHierarchyPlan(ctx, input, model, HierarchyValidationOptions{ForbiddenCatalogTerms: forbidden})
	if err != nil {
		return knowl.ValidatedHierarchyPlan{}, fmt.Errorf("plan_validation: %w", err)
	}
	return validated, nil
}

func (service *HierarchyService) savePlan(ctx context.Context, id knowl.OperationID, digest string, files int) error {
	return service.operations.SavePlan(ctx, id, knowl.PlanSummary{OperationID: string(id), Digest: digest, FileCount: files, CreatedAt: time.Now().UTC()})
}

func (service *HierarchyService) commitNoOp(ctx context.Context, result IngestResult, scope knowl.ScopeRef, digest string) (IngestResult, error) {
	id := result.Operation.ID
	if result.Operation.Status == knowl.StatusReceived {
		if err := service.savePlan(ctx, id, digest, 0); err != nil {
			return service.fail(ctx, result, "operation", err)
		}
	}
	lease, err := newLease(time.Now().UTC(), service.leaseDuration)
	if err != nil {
		return result, err
	}
	stateCtx := durableContext(ctx)
	if err := service.operations.MarkApplying(stateCtx, id, lease); err != nil {
		return result, err
	}
	if err := service.operations.CommitOutcome(stateCtx, id, knowl.ContentCommit{OperationID: string(id), Generation: "noop-" + digest}); err != nil {
		return result, err
	}
	result.Operation, err = service.operations.Operation(stateCtx, scope, id)
	return result, err
}

func (service *HierarchyService) apply(ctx context.Context, result IngestResult, scope knowl.ScopeRef, id knowl.OperationID) (IngestResult, error) {
	lease, err := newLease(time.Now().UTC(), service.leaseDuration)
	if err != nil {
		return result, err
	}
	stateCtx := durableContext(ctx)
	if err := service.operations.MarkApplying(stateCtx, id, lease); err != nil {
		return result, err
	}
	commit, err := service.content.CommitHierarchy(ctx, result.Staged)
	if err != nil {
		return service.fail(ctx, result, "commit", err)
	}
	snapshot, err := service.content.Snapshot(ctx, scope)
	if err != nil {
		return result, fmt.Errorf("%w: %w", ErrProjection, err)
	}
	commit.Snapshot = snapshot
	if err := service.index.Project(ctx, commit); err != nil {
		result.Commit = &commit
		return result, fmt.Errorf("%w: %w", ErrProjection, err)
	}
	if err := service.operations.CommitOutcome(stateCtx, id, commit); err != nil {
		result.Commit = &commit
		return result, err
	}
	result.Commit = &commit
	result.Operation, err = service.operations.Operation(stateCtx, scope, id)
	return result, err
}

func (service *HierarchyService) fail(ctx context.Context, result IngestResult, class string, cause error) (IngestResult, error) {
	if retryableExecutionError(cause) {
		return result, cause
	}
	if err := service.operations.Fail(durableContext(ctx), result.Operation.ID, knowl.Failure{Class: class, OperationID: string(result.Operation.ID)}); err != nil {
		return result, errors.Join(cause, err)
	}
	result.Operation.Status = knowl.StatusFailed
	result.Operation.Failure = &knowl.Failure{Class: class, OperationID: string(result.Operation.ID)}
	return result, fmt.Errorf("%s: %w", class, cause)
}

func hierarchyInputFromInspection(inspection knowl.WorkspaceInspection, schema knowl.SchemaDocument, snapshotDigest string, limits knowl.HierarchyLimits) (knowl.HierarchyInput, []string, error) {
	input := knowl.HierarchyInput{Scope: inspection.Scope, SchemaDigest: schema.Digest, SnapshotDigest: snapshotDigest, Limits: limits}
	memberships := make(map[string][]string)
	for _, catalog := range inspection.Catalogs {
		relative := strings.TrimPrefix(catalog.Path, "wiki/")
		destinations, malformed := knowlwiki.IndexDestinations(catalog.Content, limits.MaxEdges)
		if malformed {
			return knowl.HierarchyInput{}, nil, ErrHierarchyPlanInvalid
		}
		children := make([]string, 0, len(destinations))
		for _, destination := range destinations {
			target, external, valid := knowlwiki.ResolveIndexDestination(relative, destination)
			if !valid || external {
				continue
			}
			path := "wiki/" + target
			children = append(children, path)
			memberships[path] = append(memberships[path], catalog.Path)
		}
		input.Catalogs = append(input.Catalogs, knowl.HierarchyCatalog{Path: catalog.Path, Digest: catalog.Digest, Title: catalog.Title, Children: children})
	}
	types := make(map[string]struct{})
	for _, page := range inspection.Snapshot.Pages {
		if page.OKF == nil {
			return knowl.HierarchyInput{}, nil, ErrHierarchyPlanInvalid
		}
		excerpt := strings.Join(strings.Fields(page.Body), " ")
		if utf8.RuneCountInString(excerpt) > limits.MaxExcerptCharacters {
			runes := []rune(excerpt)
			excerpt = strings.TrimSpace(string(runes[:limits.MaxExcerptCharacters]))
		}
		input.Pages = append(input.Pages, knowl.HierarchyPage{
			ID: page.ID, Path: page.Path, Digest: page.Digest, Type: page.OKF.Type, Title: page.Title,
			Description: page.OKF.Description, Tags: append([]string(nil), page.OKF.Tags...), Excerpt: excerpt,
			Catalogs: append([]string(nil), memberships[page.Path]...),
		})
		if page.OKF.Type != "" {
			types[page.OKF.Type] = struct{}{}
		}
	}
	if len(types) >= 2 {
		input.MinRootCatalogs = 2
	}
	forbidden := make([]string, 0, len(inspection.RawSources)*2)
	for _, raw := range inspection.RawSources {
		if id := string(raw.Source.SourceDocument.SourceID); id != "" {
			forbidden = append(forbidden, id)
		}
		if id := raw.Source.Source.ID; id != "" && !strings.ContainsAny(id, "/\\") {
			forbidden = append(forbidden, id)
		}
	}
	sort.Strings(forbidden)
	return input, forbidden, nil
}
