package app

import (
	"testing"

	internalwiki "github.com/baldaworks/knowl/internal/wiki"
	"github.com/baldaworks/knowl/pkg/knowl"
)

func TestLintPagesPreservesMetadataAndLinkFindings(t *testing.T) {
	accepted := knowl.AcceptedSource{
		Source:  knowl.SourceRef{Adapter: "raw", ID: "source"},
		Version: knowl.SourceVersion{Version: "v1"},
	}
	snapshot := knowl.WorkspaceSnapshot{
		Pages: []knowl.PageSnapshot{
			{
				ID:      "entities/one",
				Path:    "wiki/entities/one.md",
				Content: "---\nid: entities/two\ntitle:\ntype:\nsource_refs:\n  - missing:source@v1\n---\n[[entities/two]] [[broken",
			},
		},
		Links: []knowl.LinkReference{{From: "entities/one", To: "entities/two", Relation: "wiki"}},
	}
	index := knowl.PageSnapshot{Path: "wiki/index.md", Content: "- entities/missing\n[[broken"}
	findings := lintPages(snapshot, index, []knowl.RawSourceRecord{{Valid: true, Source: accepted}})
	for _, code := range []string{
		"frontmatter.id_mismatch",
		"frontmatter.title_missing",
		"frontmatter.type_missing",
		"citation.unknown_source",
		"link.malformed",
		"index.malformed",
		"index.broken_page",
		"link.broken",
	} {
		if !hasFinding(findings, code) {
			t.Fatalf("lintPages() missing %q in %#v", code, findings)
		}
	}
}

func TestLintUsesSharedWikiSemantics(t *testing.T) {
	content := "---\nid: entities/one\ntitle: One\ntype: entity\nsource_refs:\n  - raw:source@v1\n---\n[[wiki/entities/two.md|Two]]\n"
	metadata, err := internalwiki.ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("ParseFrontmatter() error = %v", err)
	}
	targets, malformed := internalwiki.MarkdownTargets(content)
	if malformed {
		t.Fatal("MarkdownTargets() malformed = true, want false")
	}
	if metadata.ID != "entities/one" || len(targets) != 1 || targets[0] != "entities/two" {
		t.Fatalf("shared wiki semantics mismatch: metadata=%#v targets=%#v", metadata, targets)
	}
}

func hasFinding(findings []knowl.LintFinding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
