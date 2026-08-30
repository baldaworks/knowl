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

// Values is the normalized, backend-neutral representation of one page in the
// rebuildable search projection. Tags contains newline-separated semantic OKF
// tags; the original structured values remain in Metadata.
type Values struct {
	Format      string
	Tags        string
	Description string
	Body        string
	Metadata    []byte
}

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

// ValuesForPage returns normalized, transport-safe projection values. OKF
// pages use their separately parsed user body, including a legitimately empty
// body. Only standard semantic tags enter the flattened search field.
func ValuesForPage(page knowl.PageSnapshot) (Values, error) {
	values := Values{Body: page.Body}
	if page.OKF == nil {
		if values.Body == "" {
			values.Body = knowlwiki.Body(page.Content)
		}
		return values, nil
	}

	metadata, err := json.Marshal(page.OKF)
	if err != nil {
		return Values{}, fmt.Errorf("encode OKF metadata: %w", err)
	}
	values.Format = OKFFormat
	values.Tags = normalizeTags(page.OKF.Tags)
	values.Description = page.OKF.Description
	values.Metadata = metadata
	return values, nil
}

// PageValues preserves the original tuple API while adapters migrate to the
// shared Values representation.
func PageValues(page knowl.PageSnapshot) (format, description, body string, metadata []byte, err error) {
	values, err := ValuesForPage(page)
	if err != nil {
		return "", "", "", nil, err
	}
	return values.Format, values.Description, values.Body, values.Metadata, nil
}

func normalizeTags(tags []string) string {
	normalized := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.Join(strings.Fields(tag), " ")
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, tag)
	}
	return strings.Join(normalized, "\n")
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
