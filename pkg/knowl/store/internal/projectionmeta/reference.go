package projectionmeta

import (
	"encoding/json"
	"fmt"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// Reference decodes the shared original-evidence SQL projection shape. Both
// TEXT and JSONB drivers scan JSON into bytes; null metadata remains absent.
func Reference(scanner interface{ Scan(dest ...any) error }, terms []string, characters int) (knowl.PageReference, error) {
	var reference knowl.PageReference
	var tags, description, body, format string
	var sourceRefs, sourceDocument, sourceDocuments, metadata []byte
	if err := scanner.Scan(&reference.ID, &reference.Path, &reference.Title, &tags, &description, &body, &sourceRefs, &sourceDocument, &sourceDocuments, &format, &metadata); err != nil {
		return reference, err
	}
	if err := json.Unmarshal(sourceRefs, &reference.SourceRefs); err != nil {
		return reference, fmt.Errorf("decode source refs: %w", err)
	}
	if len(sourceDocument) > 0 {
		reference.SourceDocument = new(knowl.SourceDocument)
		if err := json.Unmarshal(sourceDocument, reference.SourceDocument); err != nil {
			return reference, err
		}
	}
	if err := json.Unmarshal(sourceDocuments, &reference.SourceDocuments); err != nil {
		return reference, err
	}
	if len(reference.SourceDocuments) == 0 && reference.SourceDocument != nil {
		reference.SourceDocuments = []knowl.SourceDocument{*reference.SourceDocument}
	}
	if reference.SourceDocument == nil && len(reference.SourceDocuments) > 0 {
		document := reference.SourceDocuments[0]
		reference.SourceDocument = &document
	}
	var err error
	reference.OKF, err = Decode(format, metadata)
	if err != nil {
		return reference, err
	}
	reference.Snippet = lexical.ExcerptFields("", lexical.DocumentFields{Title: reference.Title, Tags: tags, Description: description, Body: body}, terms, characters)
	reference.Untrusted = true
	return reference, nil
}
