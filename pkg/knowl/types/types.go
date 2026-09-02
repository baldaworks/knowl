package knowl

import (
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
)

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
	Scope          ScopeRef       `json:"scope"`
	Source         SourceRef      `json:"source"`
	Version        SourceVersion  `json:"version"`
	MediaType      string         `json:"media_type"`
	SourceDocument SourceDocument `json:"source_document,omitzero"`
	Content        []byte         `json:"content"`
	Provenance     map[string]any `json:"provenance,omitempty"`
	ReceivedAt     time.Time      `json:"received_at"`
}

// AcceptedSource describes an immutable source version stored in the workspace.
type AcceptedSource struct {
	Scope          ScopeRef       `json:"scope"`
	Source         SourceRef      `json:"source"`
	Version        SourceVersion  `json:"version"`
	MediaType      string         `json:"media_type"`
	SourceDocument SourceDocument `json:"source_document,omitzero"`
	ManifestRef    string         `json:"manifest_ref"`
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
	ID              PageID           `json:"id"`
	Path            string           `json:"path"`
	Digest          string           `json:"digest"`
	Title           string           `json:"title"`
	Content         string           `json:"content"`
	Body            string           `json:"body"`
	OKF             *okf.Metadata    `json:"okf,omitempty"`
	SourceRefs      []string         `json:"source_refs,omitempty"`
	SourceDocument  *SourceDocument  `json:"source_document,omitempty"`
	SourceDocuments []SourceDocument `json:"source_documents,omitempty"`
	Untrusted       bool             `json:"untrusted"`
	UpdatedAt       time.Time        `json:"updated_at"`
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
	OperationID       string     `json:"operation_id"`
	Scope             ScopeRef   `json:"scope"`
	SchemaDigest      string     `json:"schema_digest"`
	RequiredSourceRef string     `json:"required_source_ref,omitempty"`
	SourceRefs        []string   `json:"source_refs"`
	Edits             []FileEdit `json:"edits"`
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

// WorkKind distinguishes durable source maintenance from hierarchy work.
type WorkKind string

const (
	WorkSourceMaintenance WorkKind = "source"
	WorkHierarchy         WorkKind = "hierarchy"
)

// OperationIdentity is the generic deterministic identity of durable work.
type OperationIdentity struct {
	Scope    ScopeRef `json:"scope"`
	Kind     WorkKind `json:"kind"`
	Subject  string   `json:"subject"`
	Revision string   `json:"revision"`
	Digest   string   `json:"digest"`
}

// OperationKey is the idempotency identity for an immutable source revision.
type OperationKey struct {
	Scope   ScopeRef      `json:"scope"`
	Source  SourceRef     `json:"source"`
	Version SourceVersion `json:"version"`
}

// OperationMeta contains the bounded internal inputs persisted at reservation.
type OperationMeta struct {
	Key            OperationKey   `json:"key"`
	AcceptedSource AcceptedSource `json:"accepted_source"`
	Schema         SchemaDocument `json:"schema"`
	SchemaDigest   string         `json:"schema_digest"`
	CreatedAt      time.Time      `json:"created_at"`
}

// ExecutionDescriptor contains the bounded durable inputs needed to resume an
// accepted operation. It is internal operational state, not a public operation
// read model.
type ExecutionDescriptor struct {
	OperationID OperationID                   `json:"operation_id"`
	Kind        WorkKind                      `json:"kind,omitempty"`
	Source      AcceptedSource                `json:"source,omitzero"`
	Hierarchy   *HierarchyExecutionDescriptor `json:"hierarchy,omitempty"`
	Schema      SchemaDocument                `json:"schema"`
}

// HierarchyExecutionDescriptor is the bounded versioned payload needed to
// re-inspect and execute one reserved hierarchy snapshot.
type HierarchyExecutionDescriptor struct {
	SnapshotDigest string `json:"snapshot_digest"`
	PlannerVersion string `json:"planner_version"`
}

// WorkLease grants temporary ownership of application-level operation work.
// It is separate from Lease, which fences canonical content application.
type WorkLease struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// WorkClaim combines an exclusively claimed operation with its durable inputs.
type WorkClaim struct {
	Operation  Operation           `json:"operation"`
	Descriptor ExecutionDescriptor `json:"descriptor"`
	Lease      WorkLease           `json:"lease"`
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
	ID               OperationID     `json:"id"`
	Kind             WorkKind        `json:"kind,omitempty"`
	Key              OperationKey    `json:"key,omitzero"`
	Status           OperationStatus `json:"status"`
	Attempt          int             `json:"attempt"`
	WorkAttempt      int             `json:"work_attempt"`
	RetryAttempt     int             `json:"retry_attempt"`
	ManualRetryCount int             `json:"manual_retry_count"`
	ReadyAt          time.Time       `json:"ready_at,omitempty"`
	Failure          *Failure        `json:"failure,omitempty"`
	UpdatedAt        time.Time       `json:"updated_at"`
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
	Reason      string `json:"reason,omitempty"`
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
	ID              PageID           `json:"id"`
	Path            string           `json:"path"`
	Title           string           `json:"title"`
	Snippet         string           `json:"snippet"`
	SourceRefs      []string         `json:"source_refs"`
	SourceDocument  *SourceDocument  `json:"source_document,omitempty"`
	SourceDocuments []SourceDocument `json:"source_documents,omitempty"`
	OKF             *okf.Metadata    `json:"okf,omitempty"`
	Untrusted       bool             `json:"untrusted"`
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

// RawSourceRecord is a bounded, metadata-only inspection of one raw source version.
type RawSourceRecord struct {
	Path          string         `json:"path"`
	Source        AcceptedSource `json:"source"`
	ContentDigest string         `json:"content_digest,omitempty"`
	Valid         bool           `json:"valid"`
	ErrorClass    string         `json:"error_class,omitempty"`
}

// WorkspaceInspection combines canonical projections needed by deterministic lint.
type WorkspaceInspection struct {
	Scope      ScopeRef          `json:"scope"`
	Snapshot   WorkspaceSnapshot `json:"snapshot"`
	Index      PageSnapshot      `json:"index"`
	Catalogs   []PageSnapshot    `json:"catalogs,omitempty"`
	Log        PageSnapshot      `json:"log"`
	RawSources []RawSourceRecord `json:"raw_sources"`
}

// LintFinding is one deterministic or suggestion-only workspace diagnostic.
type LintFinding struct {
	Code       string   `json:"code"`
	Severity   string   `json:"severity"`
	Path       string   `json:"path,omitempty"`
	PageID     PageID   `json:"page_id,omitempty"`
	Message    string   `json:"message"`
	Suggestion bool     `json:"suggestion,omitempty"`
	SourceRefs []string `json:"source_refs,omitempty"`
}

// LintReport contains bounded, redacted workspace health findings.
type LintReport struct {
	Scope     ScopeRef      `json:"scope"`
	Findings  []LintFinding `json:"findings"`
	CheckedAt time.Time     `json:"checked_at"`
}

// Healthy reports whether lint found no error or warning findings.
func (report LintReport) Healthy() bool {
	for _, finding := range report.Findings {
		if finding.Severity == "error" || finding.Severity == "warning" {
			return false
		}
	}
	return true
}

// MaintenanceInput is the bounded data supplied to a maintainer provider.
type MaintenanceInput struct {
	Scope      ScopeRef       `json:"scope"`
	Schema     SchemaDocument `json:"schema"`
	Source     AcceptedSource `json:"source"`
	SourceText string         `json:"source_text"`
	Pages      []PageSnapshot `json:"pages"`
	Catalogs   []PageSnapshot `json:"catalogs,omitempty"`
	Limits     ReadLimits     `json:"limits"`
}

// ModelEditPlan is structured provider output before application validation.
type ModelEditPlan struct {
	SchemaDigest string     `json:"schema_digest"`
	SourceRefs   []string   `json:"source_refs"`
	Edits        []FileEdit `json:"edits"`
	Rationale    string     `json:"rationale,omitempty"`
}
