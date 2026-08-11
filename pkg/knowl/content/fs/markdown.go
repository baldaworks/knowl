package fs

import (
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

func markdownTitle(content []byte) string {
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

func markdownLinks(from knowl.PageID, content []byte) []knowl.LinkReference {
	return knowlwiki.Links(from, string(content))
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
