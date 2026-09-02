package knowl

import "time"

// SourceID identifies one configured authoritative knowledge source.
type SourceID string

// DocumentID identifies one logical document within a configured source.
type DocumentID string

// SyncRunID identifies one durable source synchronization attempt.
type SyncRunID string

// SourceType selects a source adapter family.
type SourceType string

const (
	// SourceTypeFilesystem selects the built-in filesystem source contract.
	SourceTypeFilesystem SourceType = "filesystem"
	// SourceFlavorMarkdown preserves ordinary Markdown references.
	SourceFlavorMarkdown = "markdown"
	// SourceFlavorObsidian enables Obsidian reference normalization.
	SourceFlavorObsidian = "obsidian"
	// SourceFlavorOKF preserves validated Open Knowledge Format semantics.
	SourceFlavorOKF = "okf"
)

// SourceConfig is the typed configuration union for one source.
type SourceConfig struct {
	Filesystem *FilesystemSourceConfig `json:"filesystem,omitempty"`
}

// FilesystemSourceConfig configures a filesystem-backed source.
type FilesystemSourceConfig struct {
	Root    string   `json:"root"`
	Include []string `json:"include,omitempty"`
	Flavor  string   `json:"flavor,omitempty"`
	URIBase string   `json:"uri_base,omitempty"`
}

// SourceSyncPolicy controls on-start, periodic, and bounded retry scheduling.
type SourceSyncPolicy struct {
	OnStart      bool          `json:"on_start"`
	Interval     time.Duration `json:"interval,omitempty"`
	RetryInitial time.Duration `json:"retry_initial,omitempty"`
	RetryMaximum time.Duration `json:"retry_maximum,omitempty"`
}

// Source describes one configured authoritative source.
type Source struct {
	ID           SourceID         `json:"id"`
	Type         SourceType       `json:"type"`
	Enabled      bool             `json:"enabled"`
	Config       SourceConfig     `json:"config"`
	Sync         SourceSyncPolicy `json:"sync"`
	ConfigDigest string           `json:"config_digest,omitempty"`
}

// DocumentRef is a cheap source descriptor used before content is fetched.
type DocumentRef struct {
	ExternalID DocumentID        `json:"external_id"`
	Revision   string            `json:"revision"`
	Path       string            `json:"path"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// DocumentPage is one deterministic page of source descriptors.
type DocumentPage struct {
	Documents     []DocumentRef `json:"documents"`
	NextPageToken string        `json:"next_page_token,omitempty"`
}

// Document is one fetched immutable source revision.
type Document struct {
	DocumentRef
	Title     string `json:"title"`
	URI       string `json:"uri"`
	MediaType string `json:"media_type"`
	Content   []byte `json:"content"`
}

// SourceDocument is the canonical provenance of one configured-source revision.
type SourceDocument struct {
	SourceID   SourceID   `json:"source_id" yaml:"source_id"`
	DocumentID DocumentID `json:"document_id" yaml:"document_id"`
	Revision   string     `json:"revision" yaml:"revision"`
	URI        string     `json:"uri" yaml:"uri"`
}

// SourceMutationAction selects one canonical source-owned filesystem action.
type SourceMutationAction string

const (
	// SourceMutationWrite replaces or creates one source-owned file.
	SourceMutationWrite SourceMutationAction = "write"
	// SourceMutationDelete removes one source-owned file while retaining recovery state.
	SourceMutationDelete SourceMutationAction = "delete"
)

// SourceMutation is one bounded write or delete in a source namespace.
type SourceMutation struct {
	Action         SourceMutationAction `json:"action"`
	Path           string               `json:"path"`
	ExpectedDigest string               `json:"expected_digest,omitempty"`
	Content        []byte               `json:"content,omitempty"`
}

// SourceMutationPlan is one deterministic canonical mutation request.
type SourceMutationPlan struct {
	RunID     SyncRunID        `json:"run_id"`
	Scope     ScopeRef         `json:"scope"`
	SourceID  SourceID         `json:"source_id"`
	Mutations []SourceMutation `json:"mutations"`
}

// StagedSourceMutation identifies one verified source-owned staged artifact.
type StagedSourceMutation struct {
	RunID      SyncRunID `json:"run_id"`
	Scope      ScopeRef  `json:"scope"`
	SourceID   SourceID  `json:"source_id"`
	Generation string    `json:"generation"`
	Files      []string  `json:"files"`
	CreatedAt  time.Time `json:"created_at"`
}

// SyncStatus is the durable lifecycle status of one synchronization run.
type SyncStatus string

const (
	SyncStatusScanning         SyncStatus = "scanning"
	SyncStatusPrepared         SyncStatus = "prepared"
	SyncStatusContentCommitted SyncStatus = "content_committed"
	SyncStatusProjected        SyncStatus = "projected"
	SyncStatusSucceeded        SyncStatus = "succeeded"
	SyncStatusFailed           SyncStatus = "failed"
)

// SyncCounts summarizes reconciliation outcomes for one run.
type SyncCounts struct {
	Added     int64 `json:"added"`
	Updated   int64 `json:"updated"`
	Unchanged int64 `json:"unchanged"`
	Deleted   int64 `json:"deleted"`
	Failed    int64 `json:"failed"`
}

// SyncRun is the redacted durable read model for one synchronization attempt.
type SyncRun struct {
	ID                SyncRunID  `json:"id"`
	Scope             ScopeRef   `json:"scope"`
	SourceID          SourceID   `json:"source_id"`
	ConfigDigest      string     `json:"config_digest"`
	Status            SyncStatus `json:"status"`
	Cursor            string     `json:"cursor,omitempty"`
	NextPageToken     string     `json:"next_page_token,omitempty"`
	CompleteScan      bool       `json:"complete_scan"`
	Counts            SyncCounts `json:"counts"`
	FailureClass      string     `json:"failure_class,omitempty"`
	ContentGeneration string     `json:"content_generation,omitempty"`
	Checkpoint        string     `json:"checkpoint,omitempty"`
	StartedAt         time.Time  `json:"started_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	CompletedAt       time.Time  `json:"completed_at,omitempty"`
}

// DocumentState is the durable head or tombstone for one logical document.
type DocumentState struct {
	Scope                  ScopeRef       `json:"scope"`
	SourceID               SourceID       `json:"source_id"`
	DocumentID             DocumentID     `json:"document_id"`
	Revision               string         `json:"revision"`
	AcceptedSource         AcceptedSource `json:"accepted_source,omitempty"`
	MaintenanceRevision    string         `json:"maintenance_revision,omitempty"`
	MaintenanceOperationID OperationID    `json:"maintenance_operation_id,omitempty"`
	MirrorPath             string         `json:"mirror_path,omitempty"`
	MirrorDigest           string         `json:"mirror_digest,omitempty"`
	LastSeenRunID          SyncRunID      `json:"last_seen_run_id"`
	Deleted                bool           `json:"deleted"`
	DeletedAt              time.Time      `json:"deleted_at,omitempty"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
}

// SourceStatus is a bounded, redacted status for one configured source.
type SourceStatus struct {
	Scope               ScopeRef                `json:"scope"`
	SourceID            SourceID                `json:"source_id"`
	Type                SourceType              `json:"type"`
	ConfigDigest        string                  `json:"config_digest"`
	Checkpoint          string                  `json:"checkpoint,omitempty"`
	LastAttemptRunID    SyncRunID               `json:"last_attempt_run_id,omitempty"`
	LastSuccessfulRunID SyncRunID               `json:"last_successful_run_id,omitempty"`
	Status              SyncStatus              `json:"status,omitempty"`
	Counts              SyncCounts              `json:"counts"`
	Maintenance         SourceMaintenanceStatus `json:"maintenance"`
	CreatedAt           time.Time               `json:"created_at"`
	LastAttemptAt       time.Time               `json:"last_attempt_at,omitempty"`
	LastSuccessfulAt    time.Time               `json:"last_successful_at,omitempty"`
	UpdatedAt           time.Time               `json:"updated_at"`
}

// MaintenanceCounts summarizes the asynchronous operations reserved by the
// active documents of one configured source. Replayed counts operations with
// more than one durable worker attempt and may overlap another outcome.
type MaintenanceCounts struct {
	Queued    int64 `json:"queued"`
	Retrying  int64 `json:"retrying"`
	Replayed  int64 `json:"replayed"`
	Committed int64 `json:"committed"`
	Failed    int64 `json:"failed"`
}

// MaintenanceSample is one bounded, redacted source-to-operation correlation.
type MaintenanceSample struct {
	DocumentID       DocumentID      `json:"document_id"`
	Revision         string          `json:"revision"`
	OperationID      OperationID     `json:"operation_id"`
	Status           OperationStatus `json:"status"`
	Replayed         bool            `json:"replayed"`
	WorkAttempt      int             `json:"work_attempt"`
	RetryAttempt     int             `json:"retry_attempt"`
	ManualRetryCount int             `json:"manual_retry_count"`
	FailureClass     string          `json:"failure_class,omitempty"`
	FailureReason    string          `json:"failure_reason,omitempty"`
	NextRetryAt      time.Time       `json:"next_retry_at,omitempty"`
}

// SourceMaintenanceStatus separates raw sync reservation success from the
// asynchronous maintenance operation outcomes visible at read time.
type SourceMaintenanceStatus struct {
	Counts    MaintenanceCounts   `json:"counts"`
	Samples   []MaintenanceSample `json:"samples"`
	Truncated bool                `json:"truncated"`
}

// SourceDiagnostic reports one bounded, non-fatal source compatibility
// observation without exposing source content or host filesystem paths.
type SourceDiagnostic struct {
	Code            string `json:"code"`
	Path            string `json:"path"`
	ObservedVersion string `json:"observed_version,omitempty"`
}
