package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// maxPreparedDocuments is the durable candidate ceiling shared by both stores.
const maxPreparedDocuments = 1000

// PreparedSyncRead is the immutable prepared-scan result restored from durable state.
type PreparedSyncRead struct {
	RunID           knowl.SyncRunID         `json:"run_id"`
	Scope           knowl.ScopeRef          `json:"scope"`
	SourceID        knowl.SourceID          `json:"source_id"`
	Checkpoint      string                  `json:"checkpoint,omitempty"`
	Counts          knowl.SyncCounts        `json:"counts"`
	Documents       []PreparedDocumentState `json:"documents,omitempty"`
	CandidateDigest string                  `json:"candidate_digest"`
}

// ValidateScanDocumentLimit validates one bounded descriptor read limit.
func ValidateScanDocumentLimit(limit int) error {
	if limit <= 0 || limit > maxSourceStatePage {
		return ErrSourceInvalid
	}
	return nil
}

// ValidateSourceDigestLimit validates one bounded canonical inventory limit.
func ValidateSourceDigestLimit(limit int) error {
	if limit <= 0 || limit > maxSourceMutations {
		return ErrSourceInvalid
	}
	return nil
}

// NormalizePreparedDocuments copies, deterministically orders, and validates the
// candidate set of one complete-scan prepared state against its owning run.
func NormalizePreparedDocuments(scope knowl.ScopeRef, sourceID knowl.SourceID, runID knowl.SyncRunID, documents []PreparedDocumentState) ([]PreparedDocumentState, error) {
	if strings.TrimSpace(string(scope)) == "" || ValidateSourceID(sourceID) != nil || ValidateSyncRunID(runID) != nil || len(documents) > maxPreparedDocuments {
		return nil, ErrSourceInvalid
	}
	normalized := make([]PreparedDocumentState, len(documents))
	copy(normalized, documents)
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left].State.DocumentID < normalized[right].State.DocumentID
	})
	for index, document := range normalized {
		if !validPreparedCandidate(document, scope, sourceID, runID) || (index > 0 && normalized[index-1].State.DocumentID == document.State.DocumentID) {
			return nil, ErrSourceInvalid
		}
	}
	return normalized, nil
}

// PreparedSyncDigest returns the canonical lowercase SHA-256 candidate digest of
// one prepared payload. The reconciliation service and both concrete stores
// recompute it so an arbitrary caller-supplied digest cannot bless different
// durable candidates. Timing fields are excluded; deleted timestamps are
// encoded at microsecond precision for backend parity.
func PreparedSyncDigest(prepared PreparedSyncState) (string, error) {
	if !prepared.CompleteScan || strings.TrimSpace(string(prepared.Scope)) == "" ||
		ValidateSyncRunID(prepared.RunID) != nil || ValidateSourceID(prepared.SourceID) != nil ||
		!validStoredText(prepared.Checkpoint, maxCursorBytes, true) || ValidateSyncCounts(prepared.Counts) != nil {
		return "", ErrSourceInvalid
	}
	documents, err := NormalizePreparedDocuments(prepared.Scope, prepared.SourceID, prepared.RunID, prepared.Documents)
	if err != nil {
		return "", err
	}
	payload := preparedDigestPayload{
		RunID:      prepared.RunID,
		Scope:      prepared.Scope,
		SourceID:   prepared.SourceID,
		Checkpoint: prepared.Checkpoint,
		Counts:     prepared.Counts,
		Documents:  make([]preparedDigestDocument, len(documents)),
	}
	for index, document := range documents {
		state := document.State
		var deletedAtMicros int64
		if !state.DeletedAt.IsZero() {
			deletedAtMicros = state.DeletedAt.UTC().Truncate(time.Microsecond).UnixMicro()
		}
		payload.Documents[index] = preparedDigestDocument{
			Action:          document.Action,
			DocumentID:      state.DocumentID,
			Revision:        state.Revision,
			AcceptedSource:  state.AcceptedSource,
			MirrorPath:      state.MirrorPath,
			MirrorDigest:    state.MirrorDigest,
			LastSeenRunID:   state.LastSeenRunID,
			DeletedAtMicros: deletedAtMicros,
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode prepared digest payload: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

type preparedDigestPayload struct {
	RunID      knowl.SyncRunID          `json:"run_id"`
	Scope      knowl.ScopeRef           `json:"scope"`
	SourceID   knowl.SourceID           `json:"source_id"`
	Checkpoint string                   `json:"checkpoint"`
	Counts     knowl.SyncCounts         `json:"counts"`
	Documents  []preparedDigestDocument `json:"documents"`
}

type preparedDigestDocument struct {
	Action          SyncDocumentAction   `json:"action"`
	DocumentID      knowl.DocumentID     `json:"document_id"`
	Revision        string               `json:"revision"`
	AcceptedSource  knowl.AcceptedSource `json:"accepted_source"`
	MirrorPath      string               `json:"mirror_path,omitempty"`
	MirrorDigest    string               `json:"mirror_digest,omitempty"`
	LastSeenRunID   knowl.SyncRunID      `json:"last_seen_run_id"`
	DeletedAtMicros int64                `json:"deleted_at_unix_micros,omitempty"`
}

func validPreparedCandidate(document PreparedDocumentState, scope knowl.ScopeRef, sourceID knowl.SourceID, runID knowl.SyncRunID) bool {
	state := document.State
	if ValidateDocumentID(state.DocumentID) != nil || state.Scope != scope || state.SourceID != sourceID ||
		state.LastSeenRunID != runID || !validStoredText(state.Revision, maxRevisionBytes, false) {
		return false
	}
	if !state.DeletedAt.IsZero() && !state.DeletedAt.Equal(state.DeletedAt.UTC().Truncate(time.Microsecond)) {
		return false
	}
	if state.AcceptedSource.Scope != scope || !validStoredText(state.AcceptedSource.Source.Adapter, 255, false) ||
		!validStoredText(state.AcceptedSource.Source.ID, 2048, false) ||
		!validStoredText(state.AcceptedSource.Version.Version, maxRevisionBytes, false) ||
		!validStoredText(state.AcceptedSource.Version.Digest, maxRevisionBytes, false) ||
		!validStoredText(state.AcceptedSource.ManifestRef, maxRevisionBytes, false) {
		return false
	}
	if state.MirrorPath != "" && ValidateDocumentID(knowl.DocumentID(state.MirrorPath)) != nil {
		return false
	}
	if state.MirrorDigest != "" && !validSHA256(state.MirrorDigest) {
		return false
	}
	if document.Action == SyncDocumentActive {
		return !state.Deleted && state.DeletedAt.IsZero()
	}
	return document.Action == SyncDocumentTombstone && state.Deleted && !state.DeletedAt.IsZero()
}

func validStoredText(value string, maxBytes int, empty bool) bool {
	return len(value) <= maxBytes && (empty || value != "") && !strings.ContainsAny(value, "\x00\r\n")
}
