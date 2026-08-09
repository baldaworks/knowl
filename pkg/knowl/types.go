// Package knowl contains the public, transport-neutral Knowl domain.
package knowl

import "time"

// ScopeRef identifies an opaque knowledge scope. Knowl does not interpret it.
type ScopeRef string

// SourceRef identifies a source adapter and a stable source identity.
type SourceRef struct {
	Adapter string `json:"adapter"`
	ID      string `json:"id"`
}

// SourceVersion identifies an immutable source revision.
type SourceVersion struct {
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// SourceEnvelope is the bounded input accepted from a source adapter.
type SourceEnvelope struct {
	Scope      ScopeRef       `json:"scope"`
	Source     SourceRef      `json:"source"`
	Version    SourceVersion  `json:"version"`
	MediaType  string         `json:"media_type"`
	Content    []byte         `json:"content"`
	Provenance map[string]any `json:"provenance,omitempty"`
	ReceivedAt time.Time      `json:"received_at"`
}

// AcceptedSource describes an immutable source version stored in the workspace.
type AcceptedSource struct {
	Scope       ScopeRef      `json:"scope"`
	Source      SourceRef     `json:"source"`
	Version     SourceVersion `json:"version"`
	MediaType   string        `json:"media_type"`
	ManifestRef string        `json:"manifest_ref"`
}

// SchemaDocument is the operator-owned workspace policy document.
type SchemaDocument struct {
	Scope     ScopeRef  `json:"scope"`
	Digest    string    `json:"digest"`
	Version   string    `json:"version"`
	Content   []byte    `json:"content"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PageID is a stable wiki page identifier.
type PageID string

// PageSnapshot is a bounded read of a canonical Markdown page.
type PageSnapshot struct {
	ID         PageID    `json:"id"`
	Path       string    `json:"path"`
	Digest     string    `json:"digest"`
	Title      string    `json:"title"`
	Content    string    `json:"content"`
	SourceRefs []string  `json:"source_refs,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ReadLimits bounds context and retrieval operations.
type ReadLimits struct {
	Pages      int           `json:"pages"`
	Bytes      int           `json:"bytes"`
	Characters int           `json:"characters"`
	Depth      int           `json:"depth"`
	Deadline   time.Duration `json:"deadline"`
}

// FileEdit is one validated canonical workspace file replacement.
type FileEdit struct {
	Path           string `json:"path"`
	ExpectedDigest string `json:"expected_digest,omitempty"`
	Content        []byte `json:"content"`
}

// ValidatedEditPlan is an application-validated model plan ready to stage.
type ValidatedEditPlan struct {
	OperationID  string     `json:"operation_id"`
	Scope        ScopeRef   `json:"scope"`
	SchemaDigest string     `json:"schema_digest"`
	SourceRefs   []string   `json:"source_refs"`
	Edits        []FileEdit `json:"edits"`
}

// StagedChange identifies a plan staged for review or apply.
type StagedChange struct {
	OperationID string    `json:"operation_id"`
	Digest      string    `json:"digest"`
	Files       []string  `json:"files"`
	CreatedAt   time.Time `json:"created_at"`
}

// ContentCommit describes a canonical workspace commit.
type ContentCommit struct {
	OperationID string            `json:"operation_id"`
	Generation  string            `json:"generation"`
	Files       []string          `json:"files"`
	Snapshot    WorkspaceSnapshot `json:"snapshot"`
	CommittedAt time.Time         `json:"committed_at"`
}

// RecoveryResult describes recovery of one interrupted content operation.
type RecoveryResult struct {
	OperationID string `json:"operation_id"`
	Action      string `json:"action"`
	ErrorClass  string `json:"error_class,omitempty"`
}

// OperationID identifies an ingest or filing operation.
type OperationID string

// OperationKey is the idempotency identity for an immutable source revision.
type OperationKey struct {
	Scope   ScopeRef      `json:"scope"`
	Source  SourceRef     `json:"source"`
	Version SourceVersion `json:"version"`
}

// OperationMeta contains redacted operation metadata.
type OperationMeta struct {
	Key          OperationKey `json:"key"`
	SchemaDigest string       `json:"schema_digest"`
	CreatedAt    time.Time    `json:"created_at"`
}

// OperationStatus is the lifecycle state of a maintenance operation.
type OperationStatus string

const (
	StatusReceived       OperationStatus = "received"
	StatusPlanned        OperationStatus = "planned"
	StatusAwaitingReview OperationStatus = "awaiting_review"
	StatusApplying       OperationStatus = "applying"
	StatusCommitted      OperationStatus = "committed"
	StatusFailed         OperationStatus = "failed"
)

// Operation is a redacted operation read model.
type Operation struct {
	ID        OperationID     `json:"id"`
	Key       OperationKey    `json:"key"`
	Status    OperationStatus `json:"status"`
	Attempt   int             `json:"attempt"`
	Failure   *Failure        `json:"failure,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// PlanSummary is the durable redacted summary of a model plan.
type PlanSummary struct {
	OperationID string    `json:"operation_id"`
	Digest      string    `json:"digest"`
	FileCount   int       `json:"file_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// Lease prevents duplicate worker completion.
type Lease struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Failure is a stable, redacted operation failure.
type Failure struct {
	Class       string `json:"class"`
	OperationID string `json:"operation_id"`
}

// SourceSummary is bounded source context for a maintainer or index.
type SourceSummary struct {
	Source  SourceRef     `json:"source"`
	Version SourceVersion `json:"version"`
	Title   string        `json:"title"`
}

// PageReference is an untrusted bounded search result.
type PageReference struct {
	ID         PageID   `json:"id"`
	Path       string   `json:"path"`
	Title      string   `json:"title"`
	Snippet    string   `json:"snippet"`
	SourceRefs []string `json:"source_refs"`
	Untrusted  bool     `json:"untrusted"`
}

// LinkReference is an untrusted bounded graph result.
type LinkReference struct {
	From      PageID `json:"from"`
	To        PageID `json:"to"`
	Relation  string `json:"relation"`
	Untrusted bool   `json:"untrusted"`
}

// WorkspaceSnapshot identifies canonical content used to build projections.
type WorkspaceSnapshot struct {
	Scope        ScopeRef          `json:"scope"`
	SchemaDigest string            `json:"schema_digest"`
	PageDigests  map[string]string `json:"page_digests"`
	Pages        []PageSnapshot    `json:"pages"`
	Links        []LinkReference   `json:"links"`
	CapturedAt   time.Time         `json:"captured_at"`
}

// MaintenanceInput is the bounded data supplied to a maintainer provider.
type MaintenanceInput struct {
	Scope      ScopeRef       `json:"scope"`
	Schema     SchemaDocument `json:"schema"`
	Source     AcceptedSource `json:"source"`
	SourceText string         `json:"source_text"`
	Pages      []PageSnapshot `json:"pages"`
	Limits     ReadLimits     `json:"limits"`
}

// ModelEditPlan is structured provider output before application validation.
type ModelEditPlan struct {
	SchemaDigest string     `json:"schema_digest"`
	SourceRefs   []string   `json:"source_refs"`
	Edits        []FileEdit `json:"edits"`
	Rationale    string     `json:"rationale,omitempty"`
}
