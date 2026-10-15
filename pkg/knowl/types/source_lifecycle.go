package knowl

import "time"

// SourceAdapterID identifies the provenance adapter that owns a configured source.
type SourceAdapterID string

const (
	// SourceAdapterFilesystem is the canonical provenance adapter for filesystem sources.
	SourceAdapterFilesystem SourceAdapterID = "wiki-filesystem"
	// SourceAdapterGit is the canonical provenance adapter for Git sources.
	SourceAdapterGit SourceAdapterID = "git"
)

// SourceOwner identifies one configured source without implying equivalence
// between adapters that happen to use the same SourceID.
type SourceOwner struct {
	Adapter  SourceAdapterID `json:"adapter"`
	SourceID SourceID        `json:"source_id"`
}

// SourceLifecycleState is the durable publication-authority state of one owner.
type SourceLifecycleState string

const (
	SourceLifecycleActive   SourceLifecycleState = "active"
	SourceLifecycleRetiring SourceLifecycleState = "retiring"
	SourceLifecycleDeleted  SourceLifecycleState = "deleted"
)

// SourceLifecycle is the bounded durable state of one exact source owner.
type SourceLifecycle struct {
	Scope                ScopeRef             `json:"scope"`
	Owner                SourceOwner          `json:"owner"`
	State                SourceLifecycleState `json:"state"`
	ConfigDigest         string               `json:"config_digest"`
	AuthorityGeneration  string               `json:"authority_generation"`
	AuthorityEpoch       uint64               `json:"authority_epoch"`
	ActivatedGeneration  string               `json:"activated_generation"`
	ActivatedEpoch       uint64               `json:"activated_epoch"`
	RetirementGeneration string               `json:"retirement_generation,omitempty"`
	RetirementEpoch      uint64               `json:"retirement_epoch,omitempty"`
	ScanCursor           string               `json:"scan_cursor,omitempty"`
	ScanRoundMatches     int64                `json:"scan_round_matches"`
	ScanComplete         bool                 `json:"scan_complete"`
	Ready                bool                 `json:"ready"`
	ReplacementPending   int                  `json:"replacement_pending"`
	BlockerCode          string               `json:"blocker_code,omitempty"`
	ActivatedAt          time.Time            `json:"activated_at"`
	RetiringAt           time.Time            `json:"retiring_at,omitempty"`
	DeletedAt            time.Time            `json:"deleted_at,omitempty"`
	UpdatedAt            time.Time            `json:"updated_at"`
}

// SourceLifecycleAuthority is the current scope-wide inventory fence.
type SourceLifecycleAuthority struct {
	Scope      ScopeRef  `json:"scope"`
	Generation string    `json:"generation,omitempty"`
	Epoch      uint64    `json:"epoch"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// SourceRetirementDiscovery is the durable bounded scan position for one
// exact retiring owner. Cursor is opaque and never contains a public path.
type SourceRetirementDiscovery struct {
	Cursor              string `json:"cursor,omitempty"`
	EOF                 bool   `json:"eof"`
	RoundMatches        int64  `json:"round_matches"`
	RemainingReferences int64  `json:"remaining_references"`
}

// SourceRetirementOperationCounts summarizes page-scoped durable work without
// exposing operation payloads or retaining a complete affected-page set.
type SourceRetirementOperationCounts struct {
	Pending           int64 `json:"pending"`
	Planned           int64 `json:"planned"`
	Applying          int64 `json:"applying"`
	ProjectionPending int64 `json:"projection_pending"`
	Committed         int64 `json:"committed"`
	Failed            int64 `json:"failed"`
	Total             int64 `json:"total"`
}

// SourceRetirementReadinessStatus is the detached replacement gate state.
type SourceRetirementReadinessStatus struct {
	Ready        bool          `json:"ready"`
	BlockerCode  string        `json:"blocker_code,omitempty"`
	PendingCount int           `json:"pending_count"`
	Pending      []SourceOwner `json:"pending,omitempty"`
	Truncated    bool          `json:"truncated"`
}

// SourceRetirementStatus is the typed operator view of one exact owner.
type SourceRetirementStatus struct {
	Owner                SourceOwner                     `json:"owner"`
	State                SourceLifecycleState            `json:"state"`
	AuthorityGeneration  string                          `json:"authority_generation"`
	AuthorityEpoch       uint64                          `json:"authority_epoch"`
	ActivatedGeneration  string                          `json:"activated_generation"`
	ActivatedEpoch       uint64                          `json:"activated_epoch"`
	RetirementGeneration string                          `json:"retirement_generation,omitempty"`
	RetirementEpoch      uint64                          `json:"retirement_epoch,omitempty"`
	Readiness            SourceRetirementReadinessStatus `json:"readiness"`
	Discovery            SourceRetirementDiscovery       `json:"discovery"`
	Operations           SourceRetirementOperationCounts `json:"operations"`
	BlockerCode          string                          `json:"blocker_code,omitempty"`
	Diagnostics          []MaintenanceDiagnostic         `json:"diagnostics,omitempty"`
	DiagnosticsTruncated bool                            `json:"diagnostics_truncated"`
	ActivatedAt          time.Time                       `json:"activated_at"`
	RetiringAt           time.Time                       `json:"retiring_at,omitzero"`
	DeletedAt            time.Time                       `json:"deleted_at,omitzero"`
	UpdatedAt            time.Time                       `json:"updated_at"`
}
