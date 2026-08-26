// Package projectionmeta defines the shared OKF projection encoding used by
// both durable search adapters.
package projectionmeta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

// OKFFormat is the discriminator persisted with OKF v0.2 projection metadata.
const OKFFormat = "okf/0.2"

// SemanticPage reports whether a snapshot page belongs in the searchable
// semantic projection. Legacy derived source mirrors are deliberately omitted.
func SemanticPage(page knowl.PageSnapshot) bool {
	pagePath := strings.TrimPrefix(strings.TrimSpace(page.Path), "./")
	base := path.Base(pagePath)
	return base != "index.md" && base != "log.md" &&
		!strings.HasPrefix(pagePath, "wiki/sources/") && !strings.HasPrefix(pagePath, "sources/")
}

// SourceDocuments returns detached, sorted provenance for projection. The
// singular field remains a compatibility fallback for older snapshots.
func SourceDocuments(page knowl.PageSnapshot) []knowl.SourceDocument {
	documents := append([]knowl.SourceDocument(nil), page.SourceDocuments...)
	if len(documents) == 0 && page.SourceDocument != nil {
		documents = append(documents, *page.SourceDocument)
	}
	sort.Slice(documents, func(left, right int) bool {
		if documents[left].SourceID != documents[right].SourceID {
			return documents[left].SourceID < documents[right].SourceID
		}
		if documents[left].DocumentID != documents[right].DocumentID {
			return documents[left].DocumentID < documents[right].DocumentID
		}
		if documents[left].Revision != documents[right].Revision {
			return documents[left].Revision < documents[right].Revision
		}
		return documents[left].URI < documents[right].URI
	})
	unique := documents[:0]
	for _, document := range documents {
		if len(unique) > 0 && unique[len(unique)-1] == document {
			continue
		}
		unique = append(unique, document)
	}
	return unique
}

// PageValues returns normalized, transport-safe projection values. OKF pages
// use their separately parsed user body, including a legitimately empty body.
func PageValues(page knowl.PageSnapshot) (format, description, body string, metadata []byte, err error) {
	body = page.Body
	if page.OKF == nil {
		if body == "" {
			body = knowlwiki.Body(page.Content)
		}
		return "", "", body, nil, nil
	}

	metadata, err = json.Marshal(page.OKF)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("encode OKF metadata: %w", err)
	}
	return OKFFormat, page.OKF.Description, body, metadata, nil
}

// Decode validates a stored format discriminator and returns a detached OKF
// metadata value. Legacy rows have both values absent.
func Decode(format string, metadata []byte) (*okf.Metadata, error) {
	if format == "" {
		if len(bytes.TrimSpace(metadata)) == 0 || bytes.Equal(bytes.TrimSpace(metadata), []byte("null")) {
			return nil, nil
		}
		return nil, fmt.Errorf("OKF metadata has no format discriminator")
	}
	if format != OKFFormat {
		return nil, fmt.Errorf("unsupported projection format %q", format)
	}
	if len(bytes.TrimSpace(metadata)) == 0 || bytes.Equal(bytes.TrimSpace(metadata), []byte("null")) {
		return nil, fmt.Errorf("projection format %q has no metadata", format)
	}
	var value okf.Metadata
	if err := json.Unmarshal(metadata, &value); err != nil {
		return nil, fmt.Errorf("decode OKF metadata: %w", err)
	}
	return &value, nil
}
