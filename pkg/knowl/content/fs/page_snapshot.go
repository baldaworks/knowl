package fs

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

func parsedPageSnapshot(
	id knowl.PageID,
	relative string,
	content []byte,
	digest string,
	updatedAt time.Time,
	document okf.Document,
	now time.Time,
) (knowl.PageSnapshot, error) {
	frontmatter, err := knowlwiki.FrontmatterFromMetadata(document.Metadata)
	if err != nil {
		return knowl.PageSnapshot{}, err
	}
	title := strings.TrimSpace(document.Metadata.Title)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(relative), filepath.Ext(relative))
	}
	refs := append([]string(nil), frontmatter.SourceRefs...)
	for index := range refs {
		refs[index] = strings.TrimSpace(refs[index])
	}
	sort.Strings(refs)
	refs = uniquePageRefs(refs)
	var sourceDocument *knowl.SourceDocument
	if frontmatter.SourceDocument != nil {
		documentCopy := *frontmatter.SourceDocument
		sourceDocument = &documentCopy
	}
	metadata := okf.WithDerivedSemantics(document.Metadata, now)
	return knowl.PageSnapshot{
		ID: id, Path: relative, Digest: digest, Title: title, Content: string(content), Body: document.Body,
		OKF: &metadata, SourceRefs: refs, SourceDocument: sourceDocument, UpdatedAt: updatedAt.UTC(),
	}, nil
}

func uniquePageRefs(refs []string) []string {
	result := refs[:0]
	for _, ref := range refs {
		if ref == "" || (len(result) > 0 && result[len(result)-1] == ref) {
			continue
		}
		result = append(result, ref)
	}
	return result
}

func resolvePageProvenance(page *knowl.PageSnapshot, rawSources map[string]knowl.AcceptedSource) {
	documents := make([]knowl.SourceDocument, 0, len(page.SourceRefs))
	for _, sourceRef := range page.SourceRefs {
		source, exists := rawSources[sourceRef]
		if !exists || source.SourceDocument == (knowl.SourceDocument{}) {
			continue
		}
		documents = append(documents, source.SourceDocument)
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
	page.SourceDocuments = unique
	if page.SourceDocument == nil && len(unique) > 0 {
		document := unique[0]
		page.SourceDocument = &document
	}
}
