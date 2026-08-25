package okf

import (
	"strings"
	"time"
)

// Version is the Open Knowledge Format version implemented by this package.
const Version = "0.2"

// DocumentKind identifies an OKF document's role within a bundle.
type DocumentKind string

const (
	// DocumentConcept is a non-reserved Markdown concept.
	DocumentConcept DocumentKind = "concept"
	// DocumentIndex is an index.md reserved document.
	DocumentIndex DocumentKind = "index"
	// DocumentLog is a log.md reserved document.
	DocumentLog DocumentKind = "log"
	// DocumentAsset is a non-Markdown bundle asset.
	DocumentAsset DocumentKind = "asset"
)

// TrustTier is the advisory trust tier derived from verification actors.
type TrustTier string

const (
	// TrustUnverified means the concept has no verification event.
	TrustUnverified TrustTier = "unverified"
	// TrustMachineConfirmed means only non-human actors verified the concept.
	TrustMachineConfirmed TrustTier = "machine-confirmed"
	// TrustHumanReviewed means at least one human actor verified the concept.
	TrustHumanReviewed TrustTier = "human-reviewed"
)

// Status is an explicitly declared OKF lifecycle state.
type Status string

const (
	// StatusDraft identifies possibly incomplete content.
	StatusDraft Status = "draft"
	// StatusStable identifies content ready for consumption.
	StatusStable Status = "stable"
	// StatusDeprecated identifies retained but no-longer-current content.
	StatusDeprecated Status = "deprecated"
)

// Document is a parsed OKF concept.
type Document struct {
	Metadata Metadata `json:"metadata"`
	Body     string   `json:"body"`
}

// Metadata contains the standard OKF v0.2 concept fields and extensions.
// Empty optional fields remain absent when rendered. Extensions contain
// transport-safe YAML values: nil, bool, string, int64, uint64, float64,
// []any, and map[string]any.
type Metadata struct {
	Type           string         `json:"type"`
	Title          string         `json:"title,omitempty"`
	Description    string         `json:"description,omitempty"`
	Resource       string         `json:"resource,omitempty"`
	Tags           []string       `json:"tags,omitempty"`
	Sources        []Source       `json:"sources,omitempty"`
	UsageWindow    *UsageWindow   `json:"usage_window,omitempty"`
	Generated      *Generation    `json:"generated,omitempty"`
	Verified       []Verification `json:"verified,omitempty"`
	Status         Status         `json:"status,omitempty"`
	StaleAfter     *time.Time     `json:"stale_after,omitempty"`
	Runtime        string         `json:"runtime,omitempty"`
	Parameters     []Parameter    `json:"parameters,omitempty"`
	Computation    string         `json:"computation,omitempty"`
	Executor       *Executor      `json:"executor,omitempty"`
	Attester       *Attester      `json:"attester,omitempty"`
	Extensions     map[string]any `json:"extensions,omitempty"`
	TrustTier      TrustTier      `json:"trust_tier,omitempty"`
	ResolvedStatus Status         `json:"effective_status,omitempty"`
	Stale          bool           `json:"stale"`
}

// Source records material from which a concept derives.
type Source struct {
	ID           string         `json:"id,omitempty"`
	Resource     string         `json:"resource"`
	Title        string         `json:"title,omitempty"`
	Author       string         `json:"author,omitempty"`
	UsageCount   *uint64        `json:"usage_count,omitempty"`
	LastModified *time.Time     `json:"last_modified,omitempty"`
	UsageWindow  *UsageWindow   `json:"usage_window,omitempty"`
	Extensions   map[string]any `json:"extensions,omitempty"`
}

// UsageWindow frames source usage counts.
type UsageWindow struct {
	From       time.Time      `json:"from"`
	To         time.Time      `json:"to"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// Generation records how the current concept content was produced.
type Generation struct {
	By         string         `json:"by"`
	At         *time.Time     `json:"at,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// Verification records one confirmation of concept content.
type Verification struct {
	By         string         `json:"by"`
	At         time.Time      `json:"at"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// Parameter declares one typed, named Attested Computation input.
type Parameter struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Required   bool           `json:"required"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// Executor describes inert run instructions and the declared receipt shape.
type Executor struct {
	Resource   string         `json:"resource"`
	Receipt    []string       `json:"receipt,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// Attester describes an inert deterministic receipt checker.
type Attester struct {
	Resource   string         `json:"resource"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// Index is a parsed reserved index document.
type Index struct {
	ObservedVersion string `json:"observed_version,omitempty"`
	Body            string `json:"body"`
}

// Log is a parsed reserved log document.
type Log struct {
	Title  string     `json:"title"`
	Groups []LogGroup `json:"groups"`
}

// LogGroup contains entries for one ISO calendar date.
type LogGroup struct {
	Date    time.Time `json:"date"`
	Entries []string  `json:"entries"`
}

// EffectiveStatus returns stable when status is absent, as required by OKF.
func (m Metadata) EffectiveStatus() Status {
	if m.Status == "" {
		return StatusStable
	}

	return m.Status
}

// DeriveTrustTier derives the advisory tier solely from verification actors.
func DeriveTrustTier(metadata Metadata) TrustTier {
	if len(metadata.Verified) == 0 {
		return TrustUnverified
	}

	for _, verification := range metadata.Verified {
		if strings.HasPrefix(verification.By, "human:") {
			return TrustHumanReviewed
		}
	}

	return TrustMachineConfirmed
}

// IsStale reports whether now is on or after stale_after.
func IsStale(metadata Metadata, now time.Time) bool {
	return metadata.StaleAfter != nil && !now.Before(*metadata.StaleAfter)
}

// WithDerivedSemantics returns a metadata copy with advisory trust, effective
// status, and staleness evaluated at now. It does not mutate the input.
func WithDerivedSemantics(metadata Metadata, now time.Time) Metadata {
	metadata.TrustTier = DeriveTrustTier(metadata)
	metadata.ResolvedStatus = metadata.EffectiveStatus()
	metadata.Stale = IsStale(metadata, now)
	return metadata
}
