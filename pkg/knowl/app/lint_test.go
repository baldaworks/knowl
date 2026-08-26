package app

import (
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const (
	lintTestSourceID = "source"
	lintPageOneID    = "entities/one"
	lintPageTwoID    = "entities/two"
	lintRelationWiki = "wiki"
	lintRawAdapter   = "raw"
)

func TestLintPagesPreservesMetadataAndLinkFindings(t *testing.T) {
	accepted := knowl.AcceptedSource{
		Source:  knowl.SourceRef{Adapter: lintRawAdapter, ID: lintTestSourceID},
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
	index := knowl.PageSnapshot{Path: rootCatalogPath, Content: "- entities/missing\n[[broken"}
	findings := lintPages(snapshot, index, nil, []knowl.RawSourceRecord{{Valid: true, Source: accepted}})
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

func TestLintAcceptsRootReachableNestedCatalog(t *testing.T) {
	accepted := knowl.AcceptedSource{
		Source: knowl.SourceRef{Adapter: lintRawAdapter, ID: lintTestSourceID}, Version: knowl.SourceVersion{Version: "v1"},
	}
	sourceRef := SourceRefKey(accepted)
	page := knowl.PageSnapshot{
		ID: lintPageOneID, Path: "wiki/entities/one.md",
		Content: "---\nid: entities/one\ntitle: One\ntype: entity\nsource_refs:\n  - " + sourceRef + "\n---\n# One\n",
	}
	index := knowl.PageSnapshot{Path: rootCatalogPath, Content: "# Index\n\n* [Entities](entities/index.md)\n"}
	catalogs := []knowl.PageSnapshot{
		index,
		{Path: "wiki/entities/index.md", Content: "# Entities\n\n* [One](one.md)\n"},
	}
	findings := lintPages(knowl.WorkspaceSnapshot{Pages: []knowl.PageSnapshot{page}}, index, catalogs, []knowl.RawSourceRecord{{Valid: true, Source: accepted}})
	for _, finding := range findings {
		if finding.Code == "index.missing_page" || finding.Code == "page.orphan" || strings.HasPrefix(finding.Code, "catalog.") {
			t.Fatalf("nested catalog finding = %#v", finding)
		}
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
