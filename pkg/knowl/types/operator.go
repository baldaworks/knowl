package knowl

import "time"

// OperatorList is the bounded public list envelope. SnapshotVersion identifies
// only the current canonical read; it does not promise historical storage.
type OperatorList[T any] struct {
	Items           []T    `json:"items"`
	NextCursor      string `json:"next_cursor,omitempty"`
	SnapshotVersion string `json:"snapshot_version,omitempty"`
}

// OperatorCatalogSummary identifies one canonical navigation catalog.
type OperatorCatalogSummary struct {
	ID    PageID `json:"id"`
	Title string `json:"title"`
}

// OperatorCatalogChild is one direct catalog or factual-page child.
type OperatorCatalogChild struct {
	ID          PageID `json:"id"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
}

// OperatorCatalog contains a parent and its bounded direct children.
type OperatorCatalog struct {
	Parent          OperatorCatalogSummary `json:"parent"`
	Items           []OperatorCatalogChild `json:"items"`
	NextCursor      string                 `json:"next_cursor,omitempty"`
	SnapshotVersion string                 `json:"snapshot_version"`
}

// OperatorWikiEntry is one immediate filesystem-path tree node. A folder and
// its index.md are separate entries; PageID is set only for readable pages.
// Kind "unsupported" names a canonical Markdown or directory path excluded
// by the operator alias policy and must not be offered as a navigation link.
type OperatorWikiEntry struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	PageID PageID `json:"page_id,omitempty"`
}

// OperatorPageSummary exposes factual page identity without filesystem paths.
type OperatorPageSummary struct {
	ID          PageID    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Digest      string    `json:"digest"`
	Version     string    `json:"version"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// OperatorPageMetadata allows supported display facts, never arbitrary extensions.
type OperatorPageMetadata struct {
	Type        string   `json:"type,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Status      string   `json:"status,omitempty"`
	TrustTier   string   `json:"trust_tier,omitempty"`
	Stale       bool     `json:"stale"`
}

// OperatorPageSource is resolved page-level provenance. OriginalURI must be
// a credential-free safe http/https URL, or empty when unavailable.
type OperatorPageSource struct {
	SourceRef   string     `json:"source_ref"`
	SourceID    SourceID   `json:"source_id,omitempty"`
	DocumentID  DocumentID `json:"document_id,omitempty"`
	Revision    string     `json:"revision"`
	OriginalURI string     `json:"original_uri,omitempty"`
}

// OperatorPage is a detached current canonical page projection.
type OperatorPage struct {
	ID             PageID                `json:"id"`
	Title          string                `json:"title"`
	Markdown       string                `json:"markdown"`
	Digest         string                `json:"digest"`
	Version        string                `json:"version"`
	Metadata       *OperatorPageMetadata `json:"metadata,omitempty"`
	RelatedPageIDs []PageID              `json:"related_page_ids"`
	Sources        []OperatorPageSource  `json:"sources"`
}

// OperatorSourceRevision exposes immutable accepted text without manifest paths.
type OperatorSourceRevision struct {
	SourceRef   string        `json:"source_ref"`
	Source      SourceRef     `json:"source"`
	Version     SourceVersion `json:"version"`
	MediaType   string        `json:"media_type"`
	Digest      string        `json:"digest"`
	OriginalURI string        `json:"original_uri,omitempty"`
	Text        string        `json:"text"`
}

// OperatorSourceStatus allows durable processing facts without configuration,
// checkpoints, repository identities, or arbitrary provider error messages.
type OperatorSourceStatus struct {
	Status            SyncStatus        `json:"status,omitempty"`
	Counts            SyncCounts        `json:"counts"`
	LastAttemptAt     time.Time         `json:"last_attempt_at,omitzero"`
	LastSuccessfulAt  time.Time         `json:"last_successful_at,omitzero"`
	UpdatedAt         time.Time         `json:"updated_at"`
	MaintenanceCounts MaintenanceCounts `json:"maintenance_counts"`
}

// OperatorSourceSummary is the safe configured identity and durable status.
type OperatorSourceSummary struct {
	ID      SourceID              `json:"id"`
	Type    SourceType            `json:"type"`
	Enabled bool                  `json:"enabled"`
	Status  *OperatorSourceStatus `json:"status,omitempty"`
}

// OperatorDocumentSummary exposes stored document processing associations.
// Revision is the saved head; AcceptedRevision is present only for validated
// accepted metadata. An empty MaintenanceStatus means the scoped associated
// operation is unavailable, rather than an inferred maintenance outcome.
type OperatorDocumentSummary struct {
	ID                     DocumentID      `json:"id"`
	Revision               string          `json:"revision"`
	AcceptedRevision       string          `json:"accepted_revision,omitempty"`
	MaintenanceStatus      OperationStatus `json:"maintenance_status,omitempty"`
	MaintenanceRevision    string          `json:"maintenance_revision,omitempty"`
	MaintenanceOperationID OperationID     `json:"maintenance_operation_id,omitempty"`
	Deleted                bool            `json:"deleted"`
	UpdatedAt              time.Time       `json:"updated_at"`
}

// OperatorSourceDetail combines safe source status and a bounded document list.
type OperatorSourceDetail struct {
	Source    OperatorSourceSummary                 `json:"source"`
	Documents OperatorList[OperatorDocumentSummary] `json:"documents"`
}

// OperatorOperationSummary includes the stored source association and immutable
// creation tuple used for continuation, without execution inputs or raw failures.
type OperatorOperationSummary struct {
	ID        OperationID     `json:"id"`
	Kind      WorkKind        `json:"kind,omitempty"`
	Status    OperationStatus `json:"status"`
	SourceID  SourceID        `json:"source_id,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}
