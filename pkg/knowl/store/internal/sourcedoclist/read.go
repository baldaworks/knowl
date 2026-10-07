// Package sourcedoclist contains validation shared by durable document readers.
package sourcedoclist

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// Validate rejects unbounded reads and malformed backend document positions.
func Validate(id knowl.SourceID, options app.OperatorReadOptions) error {
	if options.Limit < 1 || options.Limit > 100 {
		return app.ErrOperatorLimitInvalid
	}
	if app.ValidateSourceID(id) != nil {
		return app.ErrOperatorInvalidRequest
	}
	if options.Continuation.Key != "" && (len(options.Continuation.Key) > 4096 || app.ValidateDocumentID(knowl.DocumentID(options.Continuation.Key)) != nil) {
		return app.ErrOperatorCursorInvalid
	}
	return nil
}

// AcceptedRevision extracts only a bounded, validated immutable revision. Invalid
// legacy provenance stays unavailable, without inventing an accepted revision.
func AcceptedRevision(raw string, scope knowl.ScopeRef, sourceID knowl.SourceID, documentID knowl.DocumentID, head string) string {
	if raw == "" || len(raw) > 65536 {
		return ""
	}
	var accepted knowl.AcceptedSource
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&accepted) != nil {
		return ""
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || accepted.Scope != scope || accepted.Version.Version != head {
		return ""
	}
	// Match the required accepted identity fields and bounds used by durable
	// source candidates, without returning their internal values.
	if !validAcceptedText(accepted.Source.Adapter, 255) || !validAcceptedText(accepted.Source.ID, 2048) ||
		!validAcceptedText(accepted.Version.Digest, 4096) || !validAcceptedText(accepted.ManifestRef, 4096) {
		return ""
	}
	if app.ValidateDocumentRef(knowl.DocumentRef{ExternalID: documentID, Path: string(documentID), Revision: accepted.Version.Version}) != nil {
		return ""
	}
	if document := accepted.SourceDocument; document != (knowl.SourceDocument{}) && (app.ValidateOwnedSourceDocument(sourceID, document) != nil || document.DocumentID != documentID || document.Revision != head) {
		return ""
	}
	return accepted.Version.Version
}

func validAcceptedText(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && !strings.ContainsAny(value, "\x00\r\n")
}
