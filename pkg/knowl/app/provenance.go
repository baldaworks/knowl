package app

import (
	"context"
	"errors"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	maxCanonicalProvenanceReturned   = 32
	maxCanonicalProvenanceExamined   = 1_024
	maxCanonicalProvenancePathBytes  = 2_048
	maxCanonicalProvenancePageBytes  = 4 << 20
	maxCanonicalProvenanceTotalBytes = 4 << 20
	maxCanonicalProvenanceSourceRefs = 64
	maxCanonicalProvenanceRoundPages = 100_000

	// CanonicalProvenanceCodePageMalformed identifies invalid canonical page structure.
	CanonicalProvenanceCodePageMalformed = "retirement.page_malformed"
	// CanonicalProvenanceCodeRefMalformed identifies a malformed accepted-ref key.
	CanonicalProvenanceCodeRefMalformed = "retirement.ref_malformed"
	// CanonicalProvenanceCodeRefUnowned identifies a ref without an exact accepted owner.
	CanonicalProvenanceCodeRefUnowned = "retirement.unowned_ref"
	// CanonicalProvenanceCodeLimit identifies a finite pager ceiling.
	CanonicalProvenanceCodeLimit = "retirement.provenance_limit"
	// CanonicalProvenanceCodeCursor identifies an invalid internal keyset cursor.
	CanonicalProvenanceCodeCursor = "retirement.provenance_cursor"
	// CanonicalProvenanceCodeRestart identifies a round invalidated by canonical mutation.
	CanonicalProvenanceCodeRestart = "retirement.provenance_restart"
)

var (
	// ErrCanonicalProvenanceInvalid reports malformed pager inputs or state.
	ErrCanonicalProvenanceInvalid = errors.New("invalid canonical provenance request")
	// ErrCanonicalProvenanceLimit reports a finite page, byte, ref, or round ceiling.
	ErrCanonicalProvenanceLimit = errors.New("retirement.provenance_limit")
	// ErrCanonicalProvenanceMalformed reports malformed canonical page provenance.
	ErrCanonicalProvenanceMalformed = errors.New("retirement.provenance_malformed")
	// ErrCanonicalProvenanceCursor reports an invalid or non-progressing internal cursor.
	ErrCanonicalProvenanceCursor = errors.New("retirement.provenance_cursor")
	// ErrCanonicalProvenanceRestart requires discarding the cursor and restarting the round.
	ErrCanonicalProvenanceRestart = errors.New("retirement.provenance_restart")
)

// CanonicalProvenanceReader enumerates only bounded canonical provenance for
// one exact configured-source owner. It deliberately does not expose page bodies.
// Earlier windows do not require the Host's in-process ApplyCoordinator: a
// cooperating publication invalidates their cursor. Before relying on a final
// continuation as authoritative EOF, the caller must acquire ApplyCoordinator
// before that PageProvenance call and retain it through EOF and Authoritative
// validation and the lifecycle zero-reference CAS.
type CanonicalProvenanceReader interface {
	PageProvenance(ctx context.Context, scope knowl.ScopeRef, owner knowl.SourceOwner, cursor CanonicalProvenanceCursor, limits CanonicalProvenanceLimits) (CanonicalProvenancePage, error)
}

// CanonicalProvenanceCursor is an opaque, round-bound keyset position. A
// cooperating publication invalidates it with ErrCanonicalProvenanceRestart.
type CanonicalProvenanceCursor string

// CanonicalProvenanceLimits independently bound one call and one complete scan round.
type CanonicalProvenanceLimits struct {
	Returned   int `json:"returned"`
	Examined   int `json:"examined"`
	PathBytes  int `json:"path_bytes"`
	PageBytes  int `json:"page_bytes"`
	TotalBytes int `json:"total_bytes"`
	SourceRefs int `json:"source_refs"`
	RoundPages int `json:"round_pages"`
}

// CanonicalPageProvenance is the exact current precondition and provenance of
// one matching canonical page.
type CanonicalPageProvenance struct {
	ID         knowl.PageID `json:"id"`
	Path       string       `json:"path"`
	Digest     string       `json:"digest"`
	SourceRefs []string     `json:"source_refs"`
}

// CanonicalProvenancePage is one bounded deterministic keyset window.
// Examined counts parsed page bodies, not names scanned by bounded selection.
// Authoritative is true only when EOF completes an unchanged publisher
// generation in the supported single-Host writer model. Direct filesystem and
// shared multi-process mutations are outside this authority guarantee. It does
// not protect changes after PageProvenance returns; zero-reference finalization
// uses the ApplyCoordinator protocol on CanonicalProvenanceReader.
type CanonicalProvenancePage struct {
	Items         []CanonicalPageProvenance `json:"items"`
	NextCursor    CanonicalProvenanceCursor `json:"next_cursor,omitempty"`
	EOF           bool                      `json:"eof"`
	Authoritative bool                      `json:"authoritative"`
	Examined      int                       `json:"examined"`
	Returned      int                       `json:"returned"`
	RoundPages    int                       `json:"round_pages"`
}

// CanonicalProvenanceFailure carries stable machine-readable failure context.
type CanonicalProvenanceFailure struct {
	Code      string
	Path      string
	SourceRef string
	Cause     error
}

func (failure *CanonicalProvenanceFailure) Error() string { return failure.Code }
func (failure *CanonicalProvenanceFailure) Unwrap() error { return failure.Cause }

// NormalizeCanonicalProvenanceLimits supplies finite defaults only for an all-zero value.
func NormalizeCanonicalProvenanceLimits(limits CanonicalProvenanceLimits) (CanonicalProvenanceLimits, error) {
	if limits == (CanonicalProvenanceLimits{}) {
		limits = CanonicalProvenanceLimits{
			Returned: maxCanonicalProvenanceReturned, Examined: maxCanonicalProvenanceExamined,
			PathBytes: maxCanonicalProvenancePathBytes,
			PageBytes: maxCanonicalProvenancePageBytes, TotalBytes: maxCanonicalProvenanceTotalBytes,
			SourceRefs: maxCanonicalProvenanceSourceRefs, RoundPages: maxCanonicalProvenanceRoundPages,
		}
	}
	if limits.Returned <= 0 || limits.Returned > maxCanonicalProvenanceReturned ||
		limits.Examined <= 0 || limits.Examined > maxCanonicalProvenanceExamined ||
		limits.PathBytes <= 0 || limits.PathBytes > maxCanonicalProvenancePathBytes ||
		limits.PageBytes <= 0 || limits.PageBytes > maxCanonicalProvenancePageBytes ||
		limits.TotalBytes <= 0 || limits.TotalBytes > maxCanonicalProvenanceTotalBytes || limits.PageBytes > limits.TotalBytes ||
		limits.SourceRefs <= 0 || limits.SourceRefs > maxCanonicalProvenanceSourceRefs ||
		limits.RoundPages <= 0 || limits.RoundPages > maxCanonicalProvenanceRoundPages {
		return CanonicalProvenanceLimits{}, ErrCanonicalProvenanceInvalid
	}
	return limits, nil
}
