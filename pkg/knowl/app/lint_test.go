package app

import (
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const (
	lintTestSourceID = "source"
	lintPageOneID    = "entities/one"
	lintPageTwoID    = "entities/two"
	lintRelationWiki = "wiki"
)

func TestLintPagesPreservesMetadataAndLinkFindings(t *testing.T) {
	accepted := knowl.AcceptedSource{
		Source:  knowl.SourceRef{Adapter: "raw", ID: lintTestSourceID},
		Version: knowl.SourceVersion{Version: "v1"},
	}
	snapshot := knowl.WorkspaceSnapshot{
		Pages: []knowl.PageSnapshot{
			{
				ID:      lintPageOneID,
				Path:    "wiki/" + lintPageOneID + ".md",
				Content: "---\nid: " + lintPageTwoID + "\ntitle:\ntype:\nsource_refs:\n  - missing:source@v1\n---\n[[" + lintPageTwoID + "]] [[broken",
			},
		},
		Links: []knowl.LinkReference{{From: lintPageOneID, To: lintPageTwoID, Relation: lintRelationWiki}},
	}
	index := knowl.PageSnapshot{Path: "wiki/index.md", Content: "- entities/missing\n[[broken"}
	findings := lintPages(snapshot, index, []knowl.RawSourceRecord{{Valid: true, Source: accepted}})
	for _, code := range []string{
		"frontmatter.malformed",
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
	content := "---\nid: " + lintPageOneID + "\ntitle: One\ntype: entity\nsource_refs:\n  - raw:source@v1\n---\n[[wiki/" + lintPageTwoID + ".md|Two]]\n"
	metadata, err := knowlwiki.ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("ParseFrontmatter() error = %v", err)
	}
	targets, malformed := knowlwiki.MarkdownTargets(content)
	if malformed {
		t.Fatal("MarkdownTargets() malformed = true, want false")
	}
	if metadata.ID != lintPageOneID || len(targets) != 1 || targets[0] != lintPageTwoID {
		t.Fatalf("shared wiki semantics mismatch: metadata=%#v targets=%#v", metadata, targets)
	}
}

func TestLintLogReadsOKFAuditComments(t *testing.T) {
	t.Parallel()

	inspection := knowl.WorkspaceInspection{
		Snapshot: knowl.WorkspaceSnapshot{
			SchemaDigest: "schema",
			PageDigests:  map[string]string{"wiki/entities/one.md": "digest"},
		},
		Log: knowl.PageSnapshot{Path: "wiki/log.md", Content: "# Knowl Update Log\n\n## 2026-08-25\n" +
			"* **Update**: committed. <!-- knowl:{\"operation_id\":\"op\",\"generation\":\"generation\",\"schema_digest\":\"schema\",\"files\":[\"wiki/entities/one.md\"]} -->\n" +
			"* **Update**: no-op. <!-- knowl:{\"operation_id\":\"noop\",\"generation\":\"generation\",\"schema_digest\":\"schema\",\"files\":[]} -->\n"},
	}
	if findings := lintLog(inspection); len(findings) != 0 {
		t.Fatalf("lintLog(valid) = %#v", findings)
	}
	inspection.Log.Content += "* missing audit\n"
	if findings := lintLog(inspection); !hasFinding(findings, "log.malformed") {
		t.Fatalf("lintLog(malformed) = %#v", findings)
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
