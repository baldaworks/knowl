package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	maxRetirementOperationPage      = 100
	maxRetirementOperationsPerOwner = 100_000
	maxRetirementDiagnosticSamples  = 16
	maxRetirementRetrySamples       = 32

	// RetirementReferencesRemainCode reports a completed discovery round that
	// found current canonical references and therefore requires page work.
	RetirementReferencesRemainCode = "retirement.refs_remaining"
	// RetirementWorkPendingCode reports durable page work not yet terminal.
	RetirementWorkPendingCode = "retirement.work_pending"
	// RetirementWorkFailedCode reports page work awaiting supported retry.
	RetirementWorkFailedCode = "retirement.work_failed"
)

var (
	// ErrRetirementReferencesRemain reports a non-zero authoritative scan round.
	ErrRetirementReferencesRemain = errors.New(RetirementReferencesRemainCode)
	// ErrRetirementWorkPending reports page work that can still publish.
	ErrRetirementWorkPending = errors.New(RetirementWorkPendingCode)
	// ErrRetirementWorkFailed reports terminal page work awaiting operator retry.
	ErrRetirementWorkFailed = errors.New(RetirementWorkFailedCode)
)

// RetirementPageReserver is the page-only durable reservation capability.
type RetirementPageReserver interface {
	Reserve(ctx context.Context, scope knowl.ScopeRef, owner knowl.SourceOwner, retirementGeneration string, pageID knowl.PageID) (OperationReservation, error)
}

// RetirementOperationRecord is one bounded detached retirement operation row.
type RetirementOperationRecord struct {
	Operation            knowl.Operation
	Owner                knowl.SourceOwner
	RetirementGeneration string
	PageID               knowl.PageID
}

// RetirementOperationReadRequest is an internal keyset page for one owner and generation.
type RetirementOperationReadRequest struct {
	Scope                knowl.ScopeRef
	Owner                knowl.SourceOwner
	RetirementGeneration string
	After                knowl.OperationID
	Limit                int
}

// RetirementOperationPage is a bounded operation-id ordered page.
type RetirementOperationPage struct {
	Items []RetirementOperationRecord
	Next  knowl.OperationID
}

// ValidateRetirementOperationReadRequest validates one finite durable-operation page.
func ValidateRetirementOperationReadRequest(request RetirementOperationReadRequest) error {
	if strings.TrimSpace(string(request.Scope)) == "" || ValidateSourceOwner(request.Owner) != nil ||
		!validExecutionDigest(request.RetirementGeneration) || request.Limit <= 0 || request.Limit > maxRetirementOperationPage ||
		strings.TrimSpace(string(request.After)) != string(request.After) || strings.ContainsAny(string(request.After), "\x00\r\n") {
		return ErrSourceLifecycleInvalid
	}
	return nil
}

// ValidateRetirementOperationRetry validates one single-row retry CAS.
func ValidateRetirementOperationRetry(scope knowl.ScopeRef, id knowl.OperationID, retirementGeneration string, retriedAt time.Time) error {
	if strings.TrimSpace(string(scope)) == "" || strings.TrimSpace(string(id)) == "" || strings.TrimSpace(string(id)) != string(id) ||
		strings.ContainsAny(string(id), "\x00\r\n") || !validExecutionDigest(retirementGeneration) || retriedAt.IsZero() {
		return ErrSourceLifecycleInvalid
	}
	return nil
}

// ValidateRetirementOperationAssociation validates one exact owner binding.
func ValidateRetirementOperationAssociation(scope knowl.ScopeRef, id knowl.OperationID, owner knowl.SourceOwner, retirementGeneration string, pageID knowl.PageID) error {
	if strings.TrimSpace(string(scope)) == "" || strings.TrimSpace(string(id)) == "" || strings.TrimSpace(string(id)) != string(id) ||
		strings.ContainsAny(string(id), "\x00\r\n") || ValidateSourceOwner(owner) != nil ||
		!validExecutionDigest(retirementGeneration) || !validRetirementPageID(pageID) {
		return ErrSourceLifecycleInvalid
	}
	return nil
}

// RetirementOperationStore exposes only the enumeration and single-row retry
// mutations needed by source retirement orchestration.
type RetirementOperationStore interface {
	RetirementOperations(ctx context.Context, request RetirementOperationReadRequest) (RetirementOperationPage, error)
	RetryRetirementOperation(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, retirementGeneration string, retriedAt time.Time) (bool, error)
}

// RetirementOperationAssociator records that one exact retiring owner is
// blocked by an already deterministic page operation shared with another owner.
type RetirementOperationAssociator interface {
	AssociateRetirementOperation(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, owner knowl.SourceOwner, retirementGeneration string, pageID knowl.PageID) error
}

// RetirementProjectionReader distinguishes canonical-committed work awaiting
// projection/outcome replay from other applying operations.
type RetirementProjectionReader interface {
	RetirementProjectionPending(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (bool, error)
}

// SourceRetirementCoordinatorDependencies are host-owned orchestration capabilities.
type SourceRetirementCoordinatorDependencies struct {
	Scope            knowl.ScopeRef
	Lifecycles       SourceLifecycleStore
	Provenance       CanonicalProvenanceReader
	Pages            RetirementPageReserver
	Operations       RetirementOperationStore
	Projection       RetirementProjectionReader
	ApplyCoordinator ApplyCoordinator
	Limits           CanonicalProvenanceLimits
	Now              func() time.Time
	Wake             func(knowl.OperationID)
}

// SourceRetirementCoordinator advances exact owners in bounded windows.
type SourceRetirementCoordinator struct {
	scope            knowl.ScopeRef
	lifecycles       SourceLifecycleStore
	provenance       CanonicalProvenanceReader
	pages            RetirementPageReserver
	operations       RetirementOperationStore
	projection       RetirementProjectionReader
	applyCoordinator ApplyCoordinator
	limits           CanonicalProvenanceLimits
	now              func() time.Time
	wake             func(knowl.OperationID)
	advanceMu        sync.Mutex
	ownerCursor      knowl.SourceOwner
}

// SourceRetirementAdvanceResult describes one bounded coordinator step.
type SourceRetirementAdvanceResult struct {
	Owner                 knowl.SourceOwner          `json:"owner"`
	State                 knowl.SourceLifecycleState `json:"state"`
	Reserved              int                        `json:"reserved"`
	DiscoveryEOF          bool                       `json:"discovery_eof"`
	VerificationRestarted bool                       `json:"verification_restarted"`
	Finalized             bool                       `json:"finalized"`
	BlockerCode           string                     `json:"blocker_code,omitempty"`
}

// SourceRetirementRetryRequest selects one failed page or all failed pages for an owner.
type SourceRetirementRetryRequest struct {
	Owner  knowl.SourceOwner `json:"owner"`
	PageID knowl.PageID      `json:"page_id,omitempty"`
	DryRun bool              `json:"dry_run"`
}

// SourceRetirementRetryResult is a bounded supported-recovery result.
type SourceRetirementRetryResult struct {
	Owner        knowl.SourceOwner   `json:"owner"`
	PageID       knowl.PageID        `json:"page_id,omitempty"`
	DryRun       bool                `json:"dry_run"`
	Matched      int                 `json:"matched"`
	Requeued     int                 `json:"requeued"`
	OperationIDs []knowl.OperationID `json:"operation_ids"`
	Truncated    bool                `json:"truncated"`
}

// NewSourceRetirementCoordinator constructs the bounded orchestrator.
func NewSourceRetirementCoordinator(dependencies SourceRetirementCoordinatorDependencies) (*SourceRetirementCoordinator, error) {
	limits, err := NormalizeCanonicalProvenanceLimits(dependencies.Limits)
	if strings.TrimSpace(string(dependencies.Scope)) == "" || dependencies.Lifecycles == nil || dependencies.Provenance == nil ||
		dependencies.Pages == nil || dependencies.Operations == nil || dependencies.Projection == nil || dependencies.ApplyCoordinator == nil || err != nil {
		return nil, ErrSourceLifecycleInvalid
	}
	now := dependencies.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	wake := dependencies.Wake
	if wake == nil {
		wake = func(knowl.OperationID) {}
	}
	return &SourceRetirementCoordinator{
		scope: dependencies.Scope, lifecycles: dependencies.Lifecycles, provenance: dependencies.Provenance,
		pages: dependencies.Pages, operations: dependencies.Operations, projection: dependencies.Projection,
		applyCoordinator: dependencies.ApplyCoordinator, limits: limits, now: now, wake: wake,
	}, nil
}

// Advance advances one rotating bounded owner window, preserving independent progress.
func (coordinator *SourceRetirementCoordinator) Advance(ctx context.Context) ([]SourceRetirementAdvanceResult, error) {
	coordinator.advanceMu.Lock()
	defer coordinator.advanceMu.Unlock()
	ctx = nonNilContext(ctx)
	states, err := coordinator.lifecycles.SourceLifecycles(ctx, coordinator.scope, SourceLifecycleListOptions{
		State: knowl.SourceLifecycleRetiring, After: coordinator.ownerCursor, Limit: maxSourceLifecycleList,
	})
	if err != nil {
		return nil, err
	}
	if len(states) == 0 && coordinator.ownerCursor != (knowl.SourceOwner{}) {
		coordinator.ownerCursor = knowl.SourceOwner{}
		states, err = coordinator.lifecycles.SourceLifecycles(ctx, coordinator.scope, SourceLifecycleListOptions{
			State: knowl.SourceLifecycleRetiring, Limit: maxSourceLifecycleList,
		})
		if err != nil {
			return nil, err
		}
	}
	if len(states) != 0 {
		coordinator.ownerCursor = states[len(states)-1].Owner
	}
	results := make([]SourceRetirementAdvanceResult, 0, len(states))
	failures := make([]error, 0)
	for _, state := range states {
		result, advanceErr := coordinator.advanceOwner(ctx, state.Owner)
		results = append(results, result)
		if advanceErr != nil && !knownRetirementBlocker(advanceErr) {
			failures = append(failures, fmt.Errorf("advance retirement %s/%s: %w", state.Owner.Adapter, state.Owner.SourceID, advanceErr))
		}
	}
	return results, errors.Join(failures...)
}

// AdvanceOwner performs at most one provenance window or one lifecycle step.
func (coordinator *SourceRetirementCoordinator) AdvanceOwner(ctx context.Context, owner knowl.SourceOwner) (SourceRetirementAdvanceResult, error) {
	coordinator.advanceMu.Lock()
	defer coordinator.advanceMu.Unlock()
	return coordinator.advanceOwner(ctx, owner)
}

func (coordinator *SourceRetirementCoordinator) advanceOwner(ctx context.Context, owner knowl.SourceOwner) (SourceRetirementAdvanceResult, error) {
	ctx = nonNilContext(ctx)
	result := SourceRetirementAdvanceResult{Owner: owner}
	if err := contextErr(ctx); err != nil {
		return result, err
	}
	state, err := coordinator.lifecycles.SourceLifecycle(ctx, coordinator.scope, owner)
	if err != nil {
		return result, err
	}
	result.State = state.State
	if ValidateSourceLifecycle(state) != nil || state.Scope != coordinator.scope || state.Owner != owner {
		return result, ErrSourceLifecycleInvalid
	}
	if state.State != knowl.SourceLifecycleRetiring {
		return result, nil
	}
	if state.ReplacementPending != 0 {
		result.BlockerCode = RetirementReplacementNotReadyCode
		return result, ErrSourceReplacementNotReady
	}
	progress, err := coordinator.operationProgress(ctx, state)
	if err != nil {
		return result, err
	}
	if state.ScanComplete {
		if blocker := retirementOperationBlocker(progress.Operations); blocker != "" {
			result.BlockerCode = blocker
			return result, retirementBlockerError(blocker)
		}
		if state.ScanRoundMatches != 0 {
			updated, updateErr := coordinator.updateProgress(ctx, state, "", 0, false, "")
			result.VerificationRestarted = updateErr == nil
			result.State = updated.State
			return result, updateErr
		}
		return coordinator.finalizeZero(ctx, state, result)
	}

	page, err := coordinator.provenance.PageProvenance(ctx, coordinator.scope, owner, CanonicalProvenanceCursor(state.ScanCursor), coordinator.limits)
	if err != nil {
		if errors.Is(err, ErrCanonicalProvenanceRestart) {
			_, resetErr := coordinator.updateProgress(ctx, state, "", 0, false, "")
			return result, resetErr
		}
		return result, err
	}
	for _, item := range page.Items {
		reservation, reserveErr := coordinator.pages.Reserve(ctx, coordinator.scope, owner, state.RetirementGeneration, item.ID)
		if reserveErr != nil {
			return result, reserveErr
		}
		result.Reserved++
		coordinator.wake(reservation.ID)
	}
	matches := state.ScanRoundMatches + int64(len(page.Items))
	if !page.EOF {
		if page.NextCursor == "" {
			return result, ErrCanonicalProvenanceCursor
		}
		_, err = coordinator.updateProgress(ctx, state, string(page.NextCursor), matches, false, "")
		return result, err
	}
	result.DiscoveryEOF = true
	if matches != 0 {
		_, err = coordinator.updateProgress(ctx, state, state.ScanCursor, matches, true, "")
		result.BlockerCode = RetirementReferencesRemainCode
		return result, err
	}
	return coordinator.finalizeZero(ctx, state, result)
}

func (coordinator *SourceRetirementCoordinator) finalizeZero(ctx context.Context, state knowl.SourceLifecycle, result SourceRetirementAdvanceResult) (SourceRetirementAdvanceResult, error) {
	release, err := coordinator.applyCoordinator.Acquire(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	current, err := coordinator.lifecycles.SourceLifecycle(ctx, coordinator.scope, state.Owner)
	if err != nil || !sameRetirementLifecycle(current, state) || current.ReplacementPending != 0 {
		return result, errors.Join(ErrRetirementLifecycleConflict, err)
	}
	progress, err := coordinator.operationProgress(ctx, current)
	if err != nil {
		return result, err
	}
	if blocker := retirementOperationBlocker(progress.Operations); blocker != "" {
		result.BlockerCode = blocker
		return result, retirementBlockerError(blocker)
	}
	page, err := coordinator.provenance.PageProvenance(ctx, coordinator.scope, current.Owner, CanonicalProvenanceCursor(current.ScanCursor), coordinator.limits)
	if err != nil {
		return result, err
	}
	if len(page.Items) != 0 || !page.EOF || !page.Authoritative {
		for _, item := range page.Items {
			reservation, reserveErr := coordinator.pages.Reserve(ctx, coordinator.scope, current.Owner, current.RetirementGeneration, item.ID)
			if reserveErr != nil {
				return result, reserveErr
			}
			result.Reserved++
			coordinator.wake(reservation.ID)
		}
		matches := current.ScanRoundMatches + int64(len(page.Items))
		cursor := current.ScanCursor
		if !page.EOF {
			cursor = string(page.NextCursor)
		}
		_, updateErr := coordinator.updateProgress(ctx, current, cursor, matches, page.EOF, "")
		result.BlockerCode = RetirementReferencesRemainCode
		if updateErr != nil {
			return result, updateErr
		}
		return result, ErrRetirementReferencesRemain
	}
	if !current.ScanComplete || current.ScanRoundMatches != 0 || current.BlockerCode != "" {
		current, err = coordinator.updateProgress(ctx, current, current.ScanCursor, 0, true, "")
		if err != nil {
			return result, err
		}
	}
	progress, err = coordinator.operationProgress(ctx, current)
	if err != nil {
		return result, err
	}
	if blocker := retirementOperationBlocker(progress.Operations); blocker != "" {
		result.BlockerCode = blocker
		return result, retirementBlockerError(blocker)
	}
	finalizedAt := monotonicRetirementTime(coordinator.now(), current.UpdatedAt)
	deleted, err := coordinator.lifecycles.FinalizeSourceRetirement(ctx, SourceRetirementFinalization{
		Scope: coordinator.scope, Owner: current.Owner, AuthorityGeneration: current.AuthorityGeneration,
		AuthorityEpoch: current.AuthorityEpoch, RetirementGeneration: current.RetirementGeneration,
		RetirementEpoch: current.RetirementEpoch, FinalizedAt: finalizedAt,
	})
	if err != nil {
		return result, err
	}
	result.Finalized = true
	result.State = deleted.State
	result.BlockerCode = ""
	return result, nil
}

// Status returns the detached bounded view for one exact owner.
func (coordinator *SourceRetirementCoordinator) Status(ctx context.Context, owner knowl.SourceOwner) (knowl.SourceRetirementStatus, error) {
	state, err := coordinator.lifecycles.SourceLifecycle(nonNilContext(ctx), coordinator.scope, owner)
	if err != nil {
		return knowl.SourceRetirementStatus{}, err
	}
	if ValidateSourceLifecycle(state) != nil {
		return knowl.SourceRetirementStatus{}, ErrSourceLifecycleInvalid
	}
	status := knowl.SourceRetirementStatus{
		Owner: state.Owner, State: state.State, AuthorityGeneration: state.AuthorityGeneration, AuthorityEpoch: state.AuthorityEpoch,
		ActivatedGeneration: state.ActivatedGeneration, ActivatedEpoch: state.ActivatedEpoch,
		RetirementGeneration: state.RetirementGeneration, RetirementEpoch: state.RetirementEpoch,
		Discovery:   knowl.SourceRetirementDiscovery{Cursor: retirementCursorToken(state.ScanCursor), EOF: state.ScanComplete, RoundMatches: state.ScanRoundMatches},
		BlockerCode: state.BlockerCode, ActivatedAt: state.ActivatedAt, RetiringAt: state.RetiringAt, DeletedAt: state.DeletedAt, UpdatedAt: state.UpdatedAt,
	}
	if state.State == knowl.SourceLifecycleActive {
		status.Readiness.Ready = state.Ready
		return status, nil
	}
	if state.State == knowl.SourceLifecycleRetiring {
		states, listErr := coordinator.lifecycles.SourceLifecycles(ctx, coordinator.scope, SourceLifecycleListOptions{Limit: maxSourceLifecycleList})
		if listErr != nil {
			return knowl.SourceRetirementStatus{}, listErr
		}
		readiness, readinessErr := EvaluateSourceRetirementReadiness(state, states)
		if readinessErr != nil {
			return knowl.SourceRetirementStatus{}, readinessErr
		}
		status.Readiness = knowl.SourceRetirementReadinessStatus{
			Ready: readiness.Ready, BlockerCode: readiness.BlockerCode, PendingCount: readiness.PendingCount,
			Pending: slices.Clone(readiness.Pending), Truncated: len(readiness.Pending) < readiness.PendingCount,
		}
		progress, progressErr := coordinator.operationProgress(ctx, state)
		if progressErr != nil {
			return knowl.SourceRetirementStatus{}, progressErr
		}
		status.Operations, status.Diagnostics, status.DiagnosticsTruncated = progress.Operations, progress.Diagnostics, progress.DiagnosticsTruncated
		if state.ScanComplete && state.ScanRoundMatches > 0 {
			status.Discovery.RemainingReferences = state.ScanRoundMatches
		}
		if blocker := retirementOperationBlocker(status.Operations); blocker != "" {
			status.BlockerCode = blocker
		} else if state.ScanComplete && state.ScanRoundMatches > 0 {
			status.BlockerCode = RetirementReferencesRemainCode
		}
	}
	return status, nil
}

func retirementCursorToken(cursor string) string {
	if cursor == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(cursor))
	return hex.EncodeToString(digest[:])
}

// Retry requeues only failed page work for the current exact retirement generation.
func (coordinator *SourceRetirementCoordinator) Retry(ctx context.Context, request SourceRetirementRetryRequest) (SourceRetirementRetryResult, error) {
	result := SourceRetirementRetryResult{Owner: request.Owner, PageID: request.PageID, DryRun: request.DryRun, OperationIDs: make([]knowl.OperationID, 0)}
	if ValidateSourceOwner(request.Owner) != nil || request.PageID != "" && !validRetirementPageID(request.PageID) {
		return result, ErrSourceLifecycleInvalid
	}
	state, err := coordinator.lifecycles.SourceLifecycle(nonNilContext(ctx), coordinator.scope, request.Owner)
	if err != nil {
		return result, err
	}
	if state.State != knowl.SourceLifecycleRetiring {
		return result, ErrRetirementLifecycleConflict
	}
	after := knowl.OperationID("")
	for examined := 0; examined < maxRetirementOperationsPerOwner; {
		page, readErr := coordinator.operations.RetirementOperations(ctx, RetirementOperationReadRequest{Scope: coordinator.scope, Owner: request.Owner, RetirementGeneration: state.RetirementGeneration, After: after, Limit: maxRetirementOperationPage})
		if readErr != nil {
			return result, readErr
		}
		for _, record := range page.Items {
			examined++
			if record.Operation.Status != knowl.StatusFailed || request.PageID != "" && record.PageID != request.PageID {
				continue
			}
			result.Matched++
			if len(result.OperationIDs) < maxRetirementRetrySamples {
				result.OperationIDs = append(result.OperationIDs, record.Operation.ID)
			} else {
				result.Truncated = true
			}
			if request.DryRun {
				continue
			}
			requeued, retryErr := coordinator.operations.RetryRetirementOperation(ctx, coordinator.scope, record.Operation.ID, state.RetirementGeneration, monotonicRetirementTime(coordinator.now(), record.Operation.UpdatedAt))
			if retryErr != nil {
				return result, retryErr
			}
			if requeued {
				result.Requeued++
				coordinator.wake(record.Operation.ID)
			}
		}
		if page.Next == "" {
			return result, nil
		}
		if page.Next <= after || len(page.Items) == 0 {
			return result, ErrSourceLifecycleInvalid
		}
		after = page.Next
	}
	return result, ErrCanonicalProvenanceLimit
}

type retirementOperationProgress struct {
	Operations           knowl.SourceRetirementOperationCounts
	Diagnostics          []knowl.MaintenanceDiagnostic
	DiagnosticsTruncated bool
}

func (coordinator *SourceRetirementCoordinator) operationProgress(ctx context.Context, state knowl.SourceLifecycle) (retirementOperationProgress, error) {
	var progress retirementOperationProgress
	after := knowl.OperationID("")
	for examined := 0; examined < maxRetirementOperationsPerOwner; {
		page, err := coordinator.operations.RetirementOperations(ctx, RetirementOperationReadRequest{
			Scope: coordinator.scope, Owner: state.Owner, RetirementGeneration: state.RetirementGeneration,
			After: after, Limit: maxRetirementOperationPage,
		})
		if err != nil {
			return retirementOperationProgress{}, err
		}
		for _, record := range page.Items {
			examined++
			if record.Owner != state.Owner || record.RetirementGeneration != state.RetirementGeneration || record.Operation.Kind != knowl.WorkSourceRetirementPage {
				return retirementOperationProgress{}, ErrSourceLifecycleInvalid
			}
			progress.Operations.Total++
			switch record.Operation.Status {
			case knowl.StatusReceived:
				progress.Operations.Pending++
			case knowl.StatusPlanned, knowl.StatusAwaitingReview:
				progress.Operations.Planned++
			case knowl.StatusApplying:
				progress.Operations.Applying++
				pending, pendingErr := coordinator.projection.RetirementProjectionPending(ctx, coordinator.scope, record.Operation.ID)
				if pendingErr != nil {
					return retirementOperationProgress{}, pendingErr
				}
				if pending {
					progress.Operations.ProjectionPending++
				}
			case knowl.StatusCommitted:
				progress.Operations.Committed++
			case knowl.StatusFailed:
				progress.Operations.Failed++
			default:
				return retirementOperationProgress{}, ErrSourceLifecycleInvalid
			}
			for _, diagnostic := range record.Operation.Diagnostics {
				if len(progress.Diagnostics) < maxRetirementDiagnosticSamples {
					progress.Diagnostics = append(progress.Diagnostics, diagnostic)
				} else {
					progress.DiagnosticsTruncated = true
				}
			}
		}
		if page.Next == "" {
			normalized, normalizeErr := NormalizeMaintenanceDiagnostics(progress.Diagnostics)
			if normalizeErr != nil {
				return retirementOperationProgress{}, normalizeErr
			}
			progress.Diagnostics = normalized
			return progress, nil
		}
		if page.Next <= after || len(page.Items) == 0 {
			return retirementOperationProgress{}, ErrSourceLifecycleInvalid
		}
		after = page.Next
	}
	return retirementOperationProgress{}, ErrCanonicalProvenanceLimit
}

func (coordinator *SourceRetirementCoordinator) updateProgress(ctx context.Context, state knowl.SourceLifecycle, cursor string, matches int64, complete bool, blocker string) (knowl.SourceLifecycle, error) {
	return coordinator.lifecycles.UpdateSourceRetirement(ctx, SourceRetirementUpdate{
		Scope: coordinator.scope, Owner: state.Owner, AuthorityGeneration: state.AuthorityGeneration,
		AuthorityEpoch: state.AuthorityEpoch, RetirementGeneration: state.RetirementGeneration,
		RetirementEpoch: state.RetirementEpoch, ExpectedCursor: state.ScanCursor, Cursor: cursor,
		RoundMatches: matches, ScanComplete: complete, BlockerCode: blocker,
		UpdatedAt: monotonicRetirementTime(coordinator.now(), state.UpdatedAt),
	})
}

func retirementOperationBlocker(counts knowl.SourceRetirementOperationCounts) string {
	if counts.Failed != 0 {
		return RetirementWorkFailedCode
	}
	if counts.Pending != 0 || counts.Planned != 0 || counts.Applying != 0 || counts.ProjectionPending != 0 {
		return RetirementWorkPendingCode
	}
	return ""
}

func retirementBlockerError(code string) error {
	switch code {
	case RetirementWorkFailedCode:
		return ErrRetirementWorkFailed
	case RetirementWorkPendingCode:
		return ErrRetirementWorkPending
	case RetirementReferencesRemainCode:
		return ErrRetirementReferencesRemain
	default:
		return ErrRetirementLifecycleConflict
	}
}

func knownRetirementBlocker(err error) bool {
	return errors.Is(err, ErrSourceReplacementNotReady) || errors.Is(err, ErrRetirementReferencesRemain) ||
		errors.Is(err, ErrRetirementWorkPending) || errors.Is(err, ErrRetirementWorkFailed)
}

func sameRetirementLifecycle(left, right knowl.SourceLifecycle) bool {
	return left.Scope == right.Scope && left.Owner == right.Owner && left.State == knowl.SourceLifecycleRetiring &&
		left.AuthorityGeneration == right.AuthorityGeneration && left.AuthorityEpoch == right.AuthorityEpoch &&
		left.RetirementGeneration == right.RetirementGeneration && left.RetirementEpoch == right.RetirementEpoch
}

func monotonicRetirementTime(candidate time.Time, predecessors ...time.Time) time.Time {
	result := candidate.UTC()
	for _, predecessor := range predecessors {
		if result.Before(predecessor) {
			result = predecessor.UTC()
		}
	}
	return result
}
