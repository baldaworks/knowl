package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	maxSourceIDBytes       = 64
	maxDocumentIDBytes     = 1024
	maxRevisionBytes       = 4096
	maxCursorBytes         = 4096
	maxMetadataEntries     = 64
	maxMetadataKeyBytes    = 256
	maxMetadataValueBytes  = 4096
	maxSourceStatePage     = 1000
	maxSyncRunIDBytes      = 255
	maxFailureClassBytes   = 128
	maxDocumentTitleBytes  = 1024
	maxDocumentURIBytes    = 8192
	maxMediaTypeBytes      = 255
	maxDocumentContent     = 64 << 20
	maxDocumentPage        = 1000
	maxSourceMutationPath  = 2048
	maxSourceMutations     = 2048
	maxSourceMutationFile  = 64 << 20
	maxSourceMutationPlan  = 512 << 20
	maxRetryFailureClasses = 16
	maxRetryResultIDs      = 100
)

var (
	ErrSourceInvalid         = errors.New("invalid Knowl source")
	ErrSourceNotFound        = errors.New("knowl source not found")
	ErrSyncRunNotFound       = errors.New("knowl sync run not found")
	ErrSyncConflict          = errors.New("knowl sync state conflict")
	ErrSyncStateTransition   = errors.New("invalid Knowl sync state transition")
	ErrSourceMutationInvalid = errors.New("invalid Knowl source mutation")
	ErrSourceMutationLimit   = errors.New("knowl source mutation exceeds a limit")
	ErrSourceRetryConflict   = errors.New("knowl source maintenance retry conflicts")

	sourceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	failurePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)
)

// SourceAdapter lists cheap descriptors and fetches only selected documents.
type SourceAdapter interface {
	List(ctx context.Context, source knowl.Source, pageToken string) (knowl.DocumentPage, error)
	Fetch(ctx context.Context, source knowl.Source, ref knowl.DocumentRef) (knowl.Document, error)
}

// SourceNormalizationInput requests deterministic normalization of one fetched
// or restored raw document against its complete descriptor catalog.
type SourceNormalizationInput struct {
	Source    knowl.Source
	Document  knowl.Document
	RawSource knowl.AcceptedSource
	Catalog   []knowl.DocumentRef
}

// SourceNormalizationResult carries immutable identities plus detached,
// deterministically ordered canonical write mutations.
type SourceNormalizationResult struct {
	FormatVersion string
	CatalogDigest string
	MirrorDigest  string
	Mutations     []knowl.SourceMutation
	Diagnostics   []knowl.SourceDiagnostic
}

// SourceNormalizer renders raw source documents into canonical mirrors without
// exposing any concrete catalog representation.
type SourceNormalizer interface {
	NormalizeSource(ctx context.Context, input SourceNormalizationInput) (SourceNormalizationResult, error)
}

// SourceContentStore owns deterministic canonical source mirrors and assets.
type SourceContentStore interface {
	SourceDigests(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID, limit int) ([]SourceDigestEntry, error)
	StageSourcePlan(ctx context.Context, plan knowl.SourceMutationPlan) (knowl.StagedSourceMutation, error)
	LoadSourceStage(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID, runID knowl.SyncRunID) (knowl.StagedSourceMutation, error)
	CommitSource(ctx context.Context, staged knowl.StagedSourceMutation) (knowl.ContentCommit, error)
}

// SourceDigestEntry is the exact write precondition of one canonical source file.
type SourceDigestEntry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// BeginSyncRequest begins or idempotently replays one durable run.
type BeginSyncRequest struct {
	Run  knowl.SyncRun
	Type knowl.SourceType
}

// ScanPageRecord atomically records one listed descriptor page and next token.
type ScanPageRecord struct {
	RunID             knowl.SyncRunID
	Scope             knowl.ScopeRef
	SourceID          knowl.SourceID
	ExpectedPageToken string
	NextPageToken     string
	Documents         []knowl.DocumentRef
	RecordedAt        time.Time
}

// SyncDocumentAction identifies the prepared active or tombstone outcome.
type SyncDocumentAction string

const (
	SyncDocumentActive    SyncDocumentAction = "active"
	SyncDocumentTombstone SyncDocumentAction = "tombstone"
)

// PreparedDocumentState is one durable candidate applied only at finalization.
type PreparedDocumentState struct {
	Action SyncDocumentAction
	State  knowl.DocumentState
}

// PreparedSyncState is the immutable complete-scan result prepared for commit.
type PreparedSyncState struct {
	RunID           knowl.SyncRunID
	Scope           knowl.ScopeRef
	SourceID        knowl.SourceID
	CompleteScan    bool
	Checkpoint      string
	Counts          knowl.SyncCounts
	Documents       []PreparedDocumentState
	CandidateDigest string
	PreparedAt      time.Time
}

// SyncGeneration records a canonical or projection generation transition.
type SyncGeneration struct {
	RunID      knowl.SyncRunID
	Scope      knowl.ScopeRef
	SourceID   knowl.SourceID
	Generation string
	UpdatedAt  time.Time
}

// SyncFinalization atomically publishes prepared document heads and checkpoint.
type SyncFinalization struct {
	RunID           knowl.SyncRunID
	Scope           knowl.ScopeRef
	SourceID        knowl.SourceID
	CandidateDigest string
	Generation      string
	Checkpoint      string
	Counts          knowl.SyncCounts
	FinalizedAt     time.Time
}

// DocumentListOptions bounds source document-state reads.
type DocumentListOptions struct {
	IncludeDeleted bool
	Limit          int
}

// SourceStateStore owns durable sync runs, document heads, and checkpoints.
type SourceStateStore interface {
	BeginSync(ctx context.Context, request BeginSyncRequest) (run knowl.SyncRun, replay bool, err error)
	SyncRun(ctx context.Context, scope knowl.ScopeRef, id knowl.SyncRunID) (knowl.SyncRun, error)
	ScanDocuments(ctx context.Context, scope knowl.ScopeRef, id knowl.SyncRunID, limit int) ([]knowl.DocumentRef, error)
	RecordScanPage(ctx context.Context, record ScanPageRecord) (knowl.SyncRun, error)
	PrepareSync(ctx context.Context, prepared PreparedSyncState) (knowl.SyncRun, error)
	PreparedSync(ctx context.Context, scope knowl.ScopeRef, id knowl.SyncRunID) (PreparedSyncRead, error)
	MarkContentCommitted(ctx context.Context, generation SyncGeneration) (knowl.SyncRun, error)
	MarkProjected(ctx context.Context, generation SyncGeneration) (knowl.SyncRun, error)
	FinalizeSync(ctx context.Context, finalization SyncFinalization) (knowl.SyncRun, error)
	FailSync(ctx context.Context, scope knowl.ScopeRef, id knowl.SyncRunID, failureClass string, failedAt time.Time) (knowl.SyncRun, error)
	DocumentState(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID, documentID knowl.DocumentID) (knowl.DocumentState, error)
	DocumentStates(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID, options DocumentListOptions) ([]knowl.DocumentState, error)
	SourceStatus(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID) (knowl.SourceStatus, error)
	RetrySourceMaintenance(ctx context.Context, request SourceMaintenanceRetryRequest) (SourceMaintenanceRetryResult, error)
	ResumableSyncRuns(ctx context.Context, scope knowl.ScopeRef, limit int) ([]knowl.SyncRun, error)
}

// SourceMaintenanceRetryRequest selects terminal current-revision maintenance
// operations for explicit operator recovery.
type SourceMaintenanceRetryRequest struct {
	Scope          knowl.ScopeRef `json:"scope"`
	SourceID       knowl.SourceID `json:"source_id"`
	FailureClasses []string       `json:"failure_classes"`
	DryRun         bool           `json:"dry_run"`
}

// SourceMaintenanceRetryResult is bounded even when the selected source has
// more matching operations than can be listed individually.
type SourceMaintenanceRetryResult struct {
	SourceID     knowl.SourceID      `json:"source_id"`
	DryRun       bool                `json:"dry_run"`
	Matched      int64               `json:"matched"`
	Requeued     int64               `json:"requeued"`
	Rejected     int64               `json:"rejected"`
	OperationIDs []knowl.OperationID `json:"operation_ids"`
	Truncated    bool                `json:"truncated"`
}

// NormalizeRetryFailureClasses validates, de-duplicates, and sorts the
// operator-selected stable failure identifiers.
func NormalizeRetryFailureClasses(classes []string) ([]string, error) {
	if len(classes) == 0 || len(classes) > maxRetryFailureClasses {
		return nil, ErrSourceInvalid
	}
	unique := make(map[string]struct{}, len(classes))
	for _, class := range classes {
		class = strings.TrimSpace(class)
		if !validFailureClass(class) || class == "" {
			return nil, ErrSourceInvalid
		}
		unique[class] = struct{}{}
	}
	normalized := make([]string, 0, len(unique))
	for class := range unique {
		normalized = append(normalized, class)
	}
	sort.Strings(normalized)
	return normalized, nil
}

// MaxSourceMaintenanceRetryResultIDs is the public bound for affected IDs.
func MaxSourceMaintenanceRetryResultIDs() int { return maxRetryResultIDs }

// ValidateSourceID validates a canonical configured source identifier.
func ValidateSourceID(id knowl.SourceID) error {
	value := string(id)
	if len(value) == 0 || len(value) > maxSourceIDBytes || !sourceIDPattern.MatchString(value) {
		return ErrSourceInvalid
	}
	return nil
}

// ValidateDocumentID validates a canonical relative logical document identity.
func ValidateDocumentID(id knowl.DocumentID) error {
	value := string(id)
	if value == "" || len(value) > maxDocumentIDBytes || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\x00\r\n") || path.IsAbs(value) || path.Clean(value) != value {
		return ErrSourceInvalid
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return ErrSourceInvalid
		}
	}
	return nil
}

// ValidateSyncRunID validates one bounded opaque durable run identifier.
func ValidateSyncRunID(id knowl.SyncRunID) error {
	if len(id) == 0 || len(id) > maxSyncRunIDBytes || strings.ContainsAny(string(id), "\x00\r\n") || !utf8.ValidString(string(id)) {
		return ErrSourceInvalid
	}
	return nil
}

// ValidateSource validates the source discriminator, identity, and digest shape.
func ValidateSource(source knowl.Source) error {
	if err := ValidateSourceID(source.ID); err != nil {
		return err
	}
	if source.Type != knowl.SourceTypeFilesystem || source.Config.Filesystem == nil {
		return ErrSourceInvalid
	}
	if source.ConfigDigest != "" && !validSHA256(source.ConfigDigest) {
		return ErrSourceInvalid
	}
	return nil
}

// ValidateDocumentRef validates one untrusted source descriptor.
func ValidateDocumentRef(ref knowl.DocumentRef) error {
	if err := ValidateDocumentID(ref.ExternalID); err != nil {
		return err
	}
	if err := ValidateDocumentID(knowl.DocumentID(ref.Path)); err != nil {
		return err
	}
	if !validOpaque(ref.Revision, maxRevisionBytes, false) || !validMetadata(ref.Metadata) {
		return ErrSourceInvalid
	}
	return nil
}

// ValidateDocument validates one fetched adapter result against an explicit
// content limit and the common source metadata bounds.
func ValidateDocument(document knowl.Document, maxContentBytes int) error {
	if maxContentBytes <= 0 || maxContentBytes > maxDocumentContent || len(document.Content) > maxContentBytes ||
		ValidateDocumentRef(document.DocumentRef) != nil ||
		!validOpaque(document.Title, maxDocumentTitleBytes, false) ||
		!validOpaque(document.URI, maxDocumentURIBytes, false) ||
		!validOpaque(document.MediaType, maxMediaTypeBytes, false) {
		return ErrSourceInvalid
	}
	return nil
}

// ValidateSourceDocument validates complete bounded canonical provenance.
func ValidateSourceDocument(document knowl.SourceDocument) error {
	uri, uriErr := url.Parse(document.URI)
	if ValidateSourceID(document.SourceID) != nil || ValidateDocumentID(document.DocumentID) != nil ||
		!validOpaque(document.Revision, maxRevisionBytes, false) || !validOpaque(document.URI, maxDocumentURIBytes, false) ||
		uriErr != nil || !uri.IsAbs() || uri.User != nil || uri.String() != document.URI {
		return ErrSourceInvalid
	}
	return nil
}

// ValidateOwnedSourceDocument validates provenance against its configured owner.
func ValidateOwnedSourceDocument(sourceID knowl.SourceID, document knowl.SourceDocument) error {
	if document.SourceID != sourceID || ValidateSourceDocument(document) != nil {
		return ErrSourceInvalid
	}
	return nil
}

// ResolveSourceDocument returns persisted configured-source provenance, or a
// validated deterministic fallback for a legacy accepted manifest.
func ResolveSourceDocument(sourceID knowl.SourceID, accepted knowl.AcceptedSource, fallback knowl.SourceDocument) (knowl.SourceDocument, error) {
	document := accepted.SourceDocument
	if document == (knowl.SourceDocument{}) {
		document = fallback
	}
	if ValidateOwnedSourceDocument(sourceID, document) != nil || document.Revision != accepted.Version.Version {
		return knowl.SourceDocument{}, ErrSourceInvalid
	}
	return document, nil
}

// NormalizeSourceMutationPlan validates, copies, and deterministically orders a
// source-owned canonical plan before it reaches a content store.
func NormalizeSourceMutationPlan(plan knowl.SourceMutationPlan) (knowl.SourceMutationPlan, error) {
	if strings.TrimSpace(string(plan.Scope)) == "" || ValidateSourceID(plan.SourceID) != nil || ValidateSyncRunID(plan.RunID) != nil || len(plan.Mutations) == 0 {
		return knowl.SourceMutationPlan{}, ErrSourceMutationInvalid
	}
	if len(plan.Mutations) > maxSourceMutations {
		return knowl.SourceMutationPlan{}, ErrSourceMutationLimit
	}
	seen := make(map[string]struct{}, len(plan.Mutations))
	totalBytes := 0
	for _, mutation := range plan.Mutations {
		if !validSourceMutationPath(mutation.Path, plan.SourceID) || (mutation.ExpectedDigest != "" && !validSHA256(mutation.ExpectedDigest)) {
			return knowl.SourceMutationPlan{}, ErrSourceMutationInvalid
		}
		if _, exists := seen[mutation.Path]; exists {
			return knowl.SourceMutationPlan{}, ErrSourceMutationInvalid
		}
		seen[mutation.Path] = struct{}{}
		switch mutation.Action {
		case knowl.SourceMutationWrite:
			if mutation.Content == nil {
				return knowl.SourceMutationPlan{}, ErrSourceMutationInvalid
			}
			if len(mutation.Content) > maxSourceMutationFile || totalBytes > maxSourceMutationPlan-len(mutation.Content) {
				return knowl.SourceMutationPlan{}, ErrSourceMutationLimit
			}
			totalBytes += len(mutation.Content)
		case knowl.SourceMutationDelete:
			if mutation.ExpectedDigest == "" || mutation.Content != nil {
				return knowl.SourceMutationPlan{}, ErrSourceMutationInvalid
			}
		default:
			return knowl.SourceMutationPlan{}, ErrSourceMutationInvalid
		}
	}
	normalized := plan
	normalized.Mutations = make([]knowl.SourceMutation, len(plan.Mutations))
	for index, mutation := range plan.Mutations {
		if mutation.Action == knowl.SourceMutationWrite {
			content := make([]byte, len(mutation.Content))
			copy(content, mutation.Content)
			mutation.Content = content
		}
		normalized.Mutations[index] = mutation
	}
	sort.Slice(normalized.Mutations, func(left, right int) bool {
		return normalized.Mutations[left].Path < normalized.Mutations[right].Path
	})
	return normalized, nil
}

func validSourceMutationPath(value string, sourceID knowl.SourceID) bool {
	if value == "" || len(value) > maxSourceMutationPath || !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.Contains(value, "\\") || path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	for _, character := range value {
		if character < ' ' || character == 0x7f {
			return false
		}
	}
	prefix := "wiki/sources/" + string(sourceID) + "/"
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix)
}

// ValidateDocumentPage validates a bounded descriptor page returned by an adapter.
func ValidateDocumentPage(page knowl.DocumentPage, maxDocuments int) error {
	if maxDocuments <= 0 || maxDocuments > maxDocumentPage || len(page.Documents) > maxDocuments || !validOpaque(page.NextPageToken, maxCursorBytes, true) {
		return ErrSourceInvalid
	}
	for _, document := range page.Documents {
		if ValidateDocumentRef(document) != nil {
			return ErrSourceInvalid
		}
	}
	return nil
}

// ValidateSyncRun validates one bounded redacted sync-run read model.
func ValidateSyncRun(run knowl.SyncRun) error {
	if strings.TrimSpace(string(run.Scope)) == "" ||
		ValidateSyncRunID(run.ID) != nil ||
		ValidateSourceID(run.SourceID) != nil || !validSHA256(run.ConfigDigest) ||
		!validSyncStatus(run.Status) || !validOpaque(run.Cursor, maxCursorBytes, true) ||
		!validOpaque(run.NextPageToken, maxCursorBytes, true) || !validOpaque(run.Checkpoint, maxCursorBytes, true) ||
		!validFailureClass(run.FailureClass) ||
		ValidateSyncCounts(run.Counts) != nil {
		return ErrSourceInvalid
	}
	return nil
}

// ValidateSyncCounts rejects negative reconciliation counters.
func ValidateSyncCounts(counts knowl.SyncCounts) error {
	if counts.Added < 0 || counts.Updated < 0 || counts.Unchanged < 0 || counts.Deleted < 0 || counts.Failed < 0 {
		return ErrSourceInvalid
	}
	return nil
}

// AddSyncCounts accumulates reconciliation counters without permitting signed
// integer overflow to become valid-looking durable state.
func AddSyncCounts(left, right knowl.SyncCounts) (knowl.SyncCounts, error) {
	if ValidateSyncCounts(left) != nil || ValidateSyncCounts(right) != nil {
		return knowl.SyncCounts{}, ErrSourceInvalid
	}
	added, ok := addSyncCount(left.Added, right.Added)
	if !ok {
		return knowl.SyncCounts{}, ErrSourceInvalid
	}
	updated, ok := addSyncCount(left.Updated, right.Updated)
	if !ok {
		return knowl.SyncCounts{}, ErrSourceInvalid
	}
	unchanged, ok := addSyncCount(left.Unchanged, right.Unchanged)
	if !ok {
		return knowl.SyncCounts{}, ErrSourceInvalid
	}
	deleted, ok := addSyncCount(left.Deleted, right.Deleted)
	if !ok {
		return knowl.SyncCounts{}, ErrSourceInvalid
	}
	failed, ok := addSyncCount(left.Failed, right.Failed)
	if !ok {
		return knowl.SyncCounts{}, ErrSourceInvalid
	}
	return knowl.SyncCounts{Added: added, Updated: updated, Unchanged: unchanged, Deleted: deleted, Failed: failed}, nil
}

func addSyncCount(left, right int64) (int64, bool) {
	const maxInt64 = int64(1<<63 - 1)
	if left > maxInt64-right {
		return 0, false
	}
	return left + right, true
}

// ValidateDocumentListOptions validates and defaults a bounded state read.
func ValidateDocumentListOptions(options DocumentListOptions) (DocumentListOptions, error) {
	if options.Limit < 0 || options.Limit > maxSourceStatePage {
		return DocumentListOptions{}, ErrSourceInvalid
	}
	if options.Limit == 0 {
		options.Limit = 100
	}
	return options, nil
}

// SourceConfigDigest returns a deterministic one-way digest of normalized config.
func SourceConfigDigest(source knowl.Source) (string, error) {
	clone := source
	clone.ConfigDigest = ""
	if err := ValidateSource(clone); err != nil {
		return "", err
	}
	if clone.Config.Filesystem != nil {
		clone.Config.Filesystem = cloneFilesystemConfig(*clone.Config.Filesystem)
	}
	encoded, err := json.Marshal(clone)
	if err != nil {
		return "", fmt.Errorf("encode source config: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneFilesystemConfig(config knowl.FilesystemSourceConfig) *knowl.FilesystemSourceConfig {
	config.Include = append([]string(nil), config.Include...)
	sort.Strings(config.Include)
	return &config
}

func validMetadata(metadata map[string]string) bool {
	if len(metadata) > maxMetadataEntries {
		return false
	}
	for key, value := range metadata {
		if key == "" || len(key) > maxMetadataKeyBytes || len(value) > maxMetadataValueBytes || !utf8.ValidString(key) || !utf8.ValidString(value) {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validSyncStatus(status knowl.SyncStatus) bool {
	switch status {
	case knowl.SyncStatusScanning,
		knowl.SyncStatusPrepared,
		knowl.SyncStatusContentCommitted,
		knowl.SyncStatusProjected,
		knowl.SyncStatusSucceeded,
		knowl.SyncStatusFailed:
		return true
	default:
		return false
	}
}

func validOpaque(value string, maxBytes int, empty bool) bool {
	if (!empty && value == "") || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < ' ' || character == 0x7f {
			return false
		}
	}
	return true
}

func validFailureClass(value string) bool {
	return value == "" || (len(value) <= maxFailureClassBytes && failurePattern.MatchString(value))
}
