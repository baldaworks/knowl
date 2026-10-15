package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const maxDesiredSourceOwners = 128

const (
	SourceLifecycleUnownedCode         = "retirement.unowned_ref"
	CitationSourceAuthorityChangedCode = "citation.source_authority_changed"
)

const (
	maxSourceLifecycleCursorBytes = 4096
	maxSourceLifecycleList        = 128

	// RetirementReplacementNotReadyCode is the stable replacement gate blocker.
	RetirementReplacementNotReadyCode = "retirement.replacement_not_ready"
)

var (
	// ErrSourceLifecycleInvalid reports malformed exact-owner lifecycle input or state.
	ErrSourceLifecycleInvalid = errors.New("invalid Knowl source lifecycle")
	// ErrSourceLifecycleConflict reports a stale or invalid lifecycle transition.
	ErrSourceLifecycleConflict = errors.New("knowl source lifecycle conflicts")
	// ErrSourceLifecycleUnowned reports a ref without an observed exact owner.
	ErrSourceLifecycleUnowned = errors.New(SourceLifecycleUnownedCode)
	// ErrSourceReplacementNotReady reports a same-rollout addition whose exact
	// current configuration has not completed source readiness.
	ErrSourceReplacementNotReady = errors.New(RetirementReplacementNotReadyCode)
)

// SourceLifecycleStore owns durable state for exact adapter-qualified owners.
type SourceLifecycleStore interface {
	ReconcileSourceLifecycles(ctx context.Context, request SourceLifecycleReconcileRequest) error
	SourceLifecycleAuthority(ctx context.Context, scope knowl.ScopeRef) (knowl.SourceLifecycleAuthority, error)
	SourceLifecycle(ctx context.Context, scope knowl.ScopeRef, owner knowl.SourceOwner) (knowl.SourceLifecycle, error)
	SourceLifecycles(ctx context.Context, scope knowl.ScopeRef, options SourceLifecycleListOptions) ([]knowl.SourceLifecycle, error)
	MarkSourceLifecycleReady(ctx context.Context, update SourceLifecycleReadyUpdate) (knowl.SourceLifecycle, error)
	UpdateSourceRetirement(ctx context.Context, update SourceRetirementUpdate) (knowl.SourceLifecycle, error)
	FinalizeSourceRetirement(ctx context.Context, finalization SourceRetirementFinalization) (knowl.SourceLifecycle, error)
}

// DesiredSourceOwner binds one desired exact owner to its normalized config digest.
type DesiredSourceOwner struct {
	Owner        knowl.SourceOwner `json:"owner"`
	ConfigDigest string            `json:"config_digest"`
}

// SourceLifecycleReconcileRequest is one bounded desired-source inventory generation.
type SourceLifecycleReconcileRequest struct {
	Scope                  knowl.ScopeRef       `json:"scope"`
	Desired                []DesiredSourceOwner `json:"desired"`
	AuthorityGeneration    string               `json:"authority_generation"`
	ExpectedAuthorityEpoch uint64               `json:"expected_authority_epoch"`
	UpdatedAt              time.Time            `json:"updated_at"`
}

// SourceLifecycleListOptions bounds deterministic owner-order reads.
type SourceLifecycleListOptions struct {
	State knowl.SourceLifecycleState
	After knowl.SourceOwner
	Limit int
}

// SourceRetirementReadiness is the bounded gate for one retiring owner.
type SourceRetirementReadiness struct {
	Ready        bool                `json:"ready"`
	BlockerCode  string              `json:"blocker_code,omitempty"`
	PendingCount int                 `json:"pending_count"`
	Pending      []knowl.SourceOwner `json:"pending,omitempty"`
}

// SourceLifecycleReadyUpdate marks successful readiness for one exact active config.
type SourceLifecycleReadyUpdate struct {
	Scope               knowl.ScopeRef
	Owner               knowl.SourceOwner
	AuthorityGeneration string
	AuthorityEpoch      uint64
	ActivatedEpoch      uint64
	ConfigDigest        string
	ReadyAt             time.Time
}

// SourceRetirementUpdate compare-and-sets one bounded retirement scan step.
type SourceRetirementUpdate struct {
	Scope                knowl.ScopeRef
	Owner                knowl.SourceOwner
	AuthorityGeneration  string
	AuthorityEpoch       uint64
	RetirementGeneration string
	RetirementEpoch      uint64
	ExpectedCursor       string
	Cursor               string
	RoundMatches         int64
	ScanComplete         bool
	BlockerCode          string
	UpdatedAt            time.Time
}

// SourceRetirementFinalization requests deletion after a completed zero-ref scan.
type SourceRetirementFinalization struct {
	Scope                knowl.ScopeRef
	Owner                knowl.SourceOwner
	AuthorityGeneration  string
	AuthorityEpoch       uint64
	RetirementGeneration string
	RetirementEpoch      uint64
	FinalizedAt          time.Time
}

// SourceOwnerFromType returns the provenance owner for a configured source type.
func SourceOwnerFromType(sourceType knowl.SourceType, sourceID knowl.SourceID) (knowl.SourceOwner, error) {
	if ValidateSourceID(sourceID) != nil {
		return knowl.SourceOwner{}, ErrSourceLifecycleInvalid
	}
	owner := knowl.SourceOwner{SourceID: sourceID}
	switch sourceType {
	case knowl.SourceTypeFilesystem:
		owner.Adapter = knowl.SourceAdapterFilesystem
	case knowl.SourceTypeGit:
		owner.Adapter = knowl.SourceAdapterGit
	default:
		return knowl.SourceOwner{}, ErrSourceLifecycleInvalid
	}
	return owner, nil
}

// NormalizeSourceLifecycleReconcile validates and deterministically orders a
// desired exact-owner inventory without merging equal SourceIDs across adapters.
func NormalizeSourceLifecycleReconcile(request SourceLifecycleReconcileRequest) (SourceLifecycleReconcileRequest, error) {
	if strings.TrimSpace(string(request.Scope)) == "" || len(request.Desired) > maxDesiredSourceOwners ||
		!validSHA256(request.AuthorityGeneration) || request.ExpectedAuthorityEpoch > math.MaxInt64 || request.UpdatedAt.IsZero() {
		return SourceLifecycleReconcileRequest{}, ErrSourceLifecycleInvalid
	}
	normalized := request
	normalized.UpdatedAt = request.UpdatedAt.UTC()
	normalized.Desired = append([]DesiredSourceOwner(nil), request.Desired...)
	seen := make(map[knowl.SourceOwner]struct{}, len(normalized.Desired))
	for _, desired := range normalized.Desired {
		if ValidateSourceOwner(desired.Owner) != nil || !validSHA256(desired.ConfigDigest) {
			return SourceLifecycleReconcileRequest{}, ErrSourceLifecycleInvalid
		}
		if _, duplicate := seen[desired.Owner]; duplicate {
			return SourceLifecycleReconcileRequest{}, ErrSourceLifecycleInvalid
		}
		seen[desired.Owner] = struct{}{}
	}
	sort.Slice(normalized.Desired, func(i, j int) bool {
		left, right := normalized.Desired[i].Owner, normalized.Desired[j].Owner
		if left.Adapter != right.Adapter {
			return left.Adapter < right.Adapter
		}
		return left.SourceID < right.SourceID
	})
	if SourceLifecycleGeneration(normalized.Desired) != normalized.AuthorityGeneration {
		return SourceLifecycleReconcileRequest{}, ErrSourceLifecycleInvalid
	}
	return normalized, nil
}

// SourceLifecycleGeneration returns the deterministic digest of one desired
// exact-owner inventory. The persisted authority epoch distinguishes later
// reconciliations that intentionally return to the same inventory.
func SourceLifecycleGeneration(desired []DesiredSourceOwner) string {
	normalized := append([]DesiredSourceOwner(nil), desired...)
	sort.Slice(normalized, func(i, j int) bool {
		left, right := normalized[i].Owner, normalized[j].Owner
		if left.Adapter != right.Adapter {
			return left.Adapter < right.Adapter
		}
		return left.SourceID < right.SourceID
	})
	digest := sha256.New()
	for _, item := range normalized {
		_, _ = digest.Write([]byte(item.Owner.Adapter))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(item.Owner.SourceID))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(item.ConfigDigest))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// EvaluateSourceRetirementReadiness gates one retiring owner on additions first
// observed in the same desired-inventory transition. Active config refreshes
// retain their activation epoch, so a stale prior config cannot open this gate.
func EvaluateSourceRetirementReadiness(retiring knowl.SourceLifecycle, states []knowl.SourceLifecycle) (SourceRetirementReadiness, error) {
	if ValidateSourceOwner(retiring.Owner) != nil || retiring.State != knowl.SourceLifecycleRetiring ||
		retiring.RetirementEpoch == 0 || retiring.RetiringAt.IsZero() || retiring.ReplacementPending < 0 ||
		retiring.ReplacementPending > maxDesiredSourceOwners || len(states) > maxSourceLifecycleList {
		return SourceRetirementReadiness{}, ErrSourceLifecycleInvalid
	}
	pending := make([]knowl.SourceOwner, 0)
	for _, state := range states {
		if ValidateSourceOwner(state.Owner) != nil {
			return SourceRetirementReadiness{}, ErrSourceLifecycleInvalid
		}
		if state.State != knowl.SourceLifecycleActive && state.ActivatedEpoch != 0 && state.RetirementEpoch == state.ActivatedEpoch {
			continue
		}
		if state.ActivatedEpoch != retiring.RetirementEpoch || state.Ready {
			continue
		}
		pending = append(pending, state.Owner)
	}
	sort.Slice(pending, func(left, right int) bool {
		if pending[left].Adapter != pending[right].Adapter {
			return pending[left].Adapter < pending[right].Adapter
		}
		return pending[left].SourceID < pending[right].SourceID
	})
	if retiring.ReplacementPending == 0 {
		return SourceRetirementReadiness{Ready: true, Pending: make([]knowl.SourceOwner, 0)}, nil
	}
	return SourceRetirementReadiness{
		BlockerCode:  RetirementReplacementNotReadyCode,
		PendingCount: retiring.ReplacementPending,
		Pending:      pending,
	}, nil
}

// ValidateSourceOwner accepts only configured-source provenance adapters.
func ValidateSourceOwner(owner knowl.SourceOwner) error {
	if ValidateSourceID(owner.SourceID) != nil {
		return ErrSourceLifecycleInvalid
	}
	switch owner.Adapter {
	case knowl.SourceAdapterFilesystem, knowl.SourceAdapterGit:
		return nil
	default:
		return ErrSourceLifecycleInvalid
	}
}

// NormalizeSourceLifecycleListOptions validates one finite keyset page.
func NormalizeSourceLifecycleListOptions(options SourceLifecycleListOptions) (SourceLifecycleListOptions, error) {
	if options.Limit <= 0 || options.Limit > maxSourceLifecycleList ||
		(options.After != (knowl.SourceOwner{}) && ValidateSourceOwner(options.After) != nil) || !validLifecycleState(options.State, true) {
		return SourceLifecycleListOptions{}, ErrSourceLifecycleInvalid
	}
	return options, nil
}

// ValidateSourceLifecycle verifies a decoded durable row before it is trusted.
func ValidateSourceLifecycle(state knowl.SourceLifecycle) error {
	if strings.TrimSpace(string(state.Scope)) == "" || ValidateSourceOwner(state.Owner) != nil ||
		!validSHA256(state.ConfigDigest) || !validSHA256(state.AuthorityGeneration) ||
		state.AuthorityEpoch > math.MaxInt64 || state.ActivatedEpoch > math.MaxInt64 || state.RetirementEpoch > math.MaxInt64 ||
		state.ActivatedEpoch > state.AuthorityEpoch || state.RetirementEpoch > state.AuthorityEpoch ||
		!validSHA256(state.ActivatedGeneration) ||
		(state.RetirementGeneration != "" && !validSHA256(state.RetirementGeneration)) ||
		!validLifecycleCursor(state.ScanCursor) || state.ScanRoundMatches < 0 ||
		state.ReplacementPending < 0 || state.ReplacementPending > maxDesiredSourceOwners ||
		(state.BlockerCode != "" && !failurePattern.MatchString(state.BlockerCode)) ||
		state.ActivatedAt.IsZero() || state.UpdatedAt.IsZero() || state.UpdatedAt.Before(state.ActivatedAt) ||
		(!state.RetiringAt.IsZero() && (state.RetiringAt.Before(state.ActivatedAt) || state.UpdatedAt.Before(state.RetiringAt))) ||
		(!state.DeletedAt.IsZero() && (state.RetiringAt.IsZero() || state.DeletedAt.Before(state.RetiringAt) || state.UpdatedAt.Before(state.DeletedAt))) {
		return ErrSourceLifecycleInvalid
	}
	switch state.State {
	case knowl.SourceLifecycleActive:
		if (state.AuthorityEpoch == 0) != (state.ActivatedEpoch == 0) || state.Ready && state.ActivatedEpoch == 0 ||
			!state.RetiringAt.IsZero() || !state.DeletedAt.IsZero() ||
			state.ScanCursor != "" || state.ScanRoundMatches != 0 || state.ScanComplete || state.ReplacementPending != 0 || state.BlockerCode != "" {
			return ErrSourceLifecycleInvalid
		}
	case knowl.SourceLifecycleRetiring:
		if state.RetirementGeneration == "" || state.AuthorityEpoch == 0 || state.ActivatedEpoch == 0 ||
			state.RetirementEpoch == 0 || state.RetirementEpoch < state.ActivatedEpoch || state.RetiringAt.IsZero() || !state.DeletedAt.IsZero() || state.Ready {
			return ErrSourceLifecycleInvalid
		}
	case knowl.SourceLifecycleDeleted:
		if state.RetirementGeneration == "" || state.AuthorityEpoch == 0 || state.ActivatedEpoch == 0 ||
			state.RetirementEpoch == 0 || state.RetirementEpoch < state.ActivatedEpoch || state.RetiringAt.IsZero() || state.DeletedAt.IsZero() || state.Ready ||
			!state.ScanComplete || state.ScanRoundMatches != 0 || state.ReplacementPending != 0 || state.BlockerCode != "" {
			return ErrSourceLifecycleInvalid
		}
	default:
		return ErrSourceLifecycleInvalid
	}
	return nil
}

// ValidateSourceLifecycleReadyUpdate validates one readiness CAS.
func ValidateSourceLifecycleReadyUpdate(update SourceLifecycleReadyUpdate) error {
	if strings.TrimSpace(string(update.Scope)) == "" || ValidateSourceOwner(update.Owner) != nil ||
		!validSHA256(update.AuthorityGeneration) || !validSHA256(update.ConfigDigest) || update.ReadyAt.IsZero() {
		return ErrSourceLifecycleInvalid
	}
	if update.AuthorityEpoch == 0 || update.AuthorityEpoch > math.MaxInt64 || update.ActivatedEpoch == 0 ||
		update.ActivatedEpoch > update.AuthorityEpoch {
		return ErrSourceLifecycleInvalid
	}
	return nil
}

// ValidateSourceRetirementUpdate validates one bounded scan progress CAS.
func ValidateSourceRetirementUpdate(update SourceRetirementUpdate) error {
	if strings.TrimSpace(string(update.Scope)) == "" || ValidateSourceOwner(update.Owner) != nil ||
		!validSHA256(update.AuthorityGeneration) || !validSHA256(update.RetirementGeneration) ||
		update.AuthorityEpoch == 0 || update.AuthorityEpoch > math.MaxInt64 || update.RetirementEpoch == 0 || update.RetirementEpoch > update.AuthorityEpoch ||
		!validLifecycleCursor(update.ExpectedCursor) || !validLifecycleCursor(update.Cursor) ||
		update.RoundMatches < 0 || (update.BlockerCode != "" && !failurePattern.MatchString(update.BlockerCode)) || update.UpdatedAt.IsZero() {
		return ErrSourceLifecycleInvalid
	}
	return nil
}

// ValidateSourceRetirementFinalization validates one zero-ref deletion CAS.
func ValidateSourceRetirementFinalization(finalization SourceRetirementFinalization) error {
	if strings.TrimSpace(string(finalization.Scope)) == "" || ValidateSourceOwner(finalization.Owner) != nil ||
		!validSHA256(finalization.AuthorityGeneration) || !validSHA256(finalization.RetirementGeneration) || finalization.FinalizedAt.IsZero() {
		return ErrSourceLifecycleInvalid
	}
	if finalization.AuthorityEpoch == 0 || finalization.AuthorityEpoch > math.MaxInt64 || finalization.RetirementEpoch == 0 ||
		finalization.RetirementEpoch > finalization.AuthorityEpoch {
		return ErrSourceLifecycleInvalid
	}
	return nil
}

func validLifecycleState(state knowl.SourceLifecycleState, empty bool) bool {
	if empty && state == "" {
		return true
	}
	switch state {
	case knowl.SourceLifecycleActive, knowl.SourceLifecycleRetiring, knowl.SourceLifecycleDeleted:
		return true
	default:
		return false
	}
}

func validLifecycleCursor(cursor string) bool {
	return len(cursor) <= maxSourceLifecycleCursorBytes && !strings.ContainsAny(cursor, "\x00\r\n")
}
