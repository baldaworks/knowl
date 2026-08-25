package fs

import (
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

func okfLimits(maxBytes int) okf.Limits {
	limits := okf.DefaultLimits()
	if maxBytes > 0 && maxBytes <= 64<<20 {
		limits.MaxBytes = maxBytes
	}
	return limits
}

func markdownTitle(content []byte) string {
	if metadata, err := knowlwiki.ParseFrontmatter(string(content)); err == nil && metadata.Title != "" {
		return metadata.Title
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func markdownSourceRefs(content []byte) []string {
	return knowlwiki.SourceRefs(string(content))
}

func markdownSourceDocument(content []byte) *knowl.SourceDocument {
	return knowlwiki.SourceDocument(string(content))
}

func markdownLinks(from knowl.PageID, content []byte) []knowl.LinkReference {
	links := knowlwiki.Links(from, string(content))
	return append(links, knowlwiki.ConceptLinks(from, string(content))...)
}

func uniqueLinks(links []knowl.LinkReference) []knowl.LinkReference {
	if len(links) < 2 {
		return links
	}
	unique := links[:0]
	for _, link := range links {
		if len(unique) == 0 || unique[len(unique)-1].From != link.From || unique[len(unique)-1].To != link.To || unique[len(unique)-1].Relation != link.Relation {
			unique = append(unique, link)
		}
	}
	return unique
}

func schemaVersion(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "schema_version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "schema_version:"))
		}
	}
	return "1"
}
