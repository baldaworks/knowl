package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const (
	lintTestSourceID = "source"
	lintPageOneID    = "entities/one"
	lintPageTwoID    = "entities/two"
	lintPageOnePath  = "wiki/entities/one.md"
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
		ID: lintPageOneID, Path: lintPageOnePath,
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
			SchemaDigest: fixtureSchema,
			PageDigests:  map[string]string{lintPageOnePath: "digest"},
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

func TestLintLogPreservesHistoricalCommits(t *testing.T) {
	t.Parallel()
	const (
		firstGeneration = "first"
		duplicateCode   = "log.duplicate_operation"
	)

	tests := []struct {
		name         string
		records      []logRecord
		wantCode     string
		wantSeverity string
	}{
		{name: "different generations", records: []logRecord{
			{OperationID: "op", Generation: firstGeneration, SchemaDigest: fixtureSchema},
			{OperationID: "op", Generation: "second", SchemaDigest: fixtureSchema},
		}},
		{name: "different operations", records: []logRecord{
			{OperationID: "first", Generation: "generation", SchemaDigest: fixtureSchema},
			{OperationID: "second", Generation: "generation", SchemaDigest: fixtureSchema},
		}},
		{name: "deleted historical catalog", records: []logRecord{
			{OperationID: "op", Generation: firstGeneration, SchemaDigest: fixtureSchema, Files: []string{"wiki/catalogs/old/index.md"}},
		}},
		{name: "identical replay", records: []logRecord{
			{OperationID: "op", Generation: firstGeneration, SchemaDigest: fixtureSchema},
			{OperationID: "op", Generation: firstGeneration, SchemaDigest: fixtureSchema},
		}, wantCode: duplicateCode, wantSeverity: lintWarning},
		{name: "conflicting commit identity", records: []logRecord{
			{OperationID: "op", Generation: firstGeneration, SchemaDigest: fixtureSchema},
			{OperationID: "op", Generation: firstGeneration, SchemaDigest: fixtureSchema, Files: []string{lintPageOnePath}},
		}, wantCode: duplicateCode, wantSeverity: lintError},
		{name: "conflicting source references", records: []logRecord{
			{OperationID: "op", Generation: firstGeneration, SchemaDigest: fixtureSchema, SourceRefs: []string{"raw:source@v1"}},
			{OperationID: "op", Generation: firstGeneration, SchemaDigest: fixtureSchema, SourceRefs: []string{"raw:source@v2"}},
		}, wantCode: duplicateCode, wantSeverity: lintError},
		{name: "schema mismatch", records: []logRecord{
			{OperationID: "op", Generation: firstGeneration, SchemaDigest: "different"},
		}, wantCode: "log.schema_mismatch", wantSeverity: lintError},
		{name: "unsorted historical targets", records: []logRecord{
			{OperationID: "op", Generation: firstGeneration, SchemaDigest: fixtureSchema, Files: []string{"wiki/z.md", "wiki/a.md"}},
		}, wantCode: "log.order", wantSeverity: lintError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var content strings.Builder
			content.WriteString("# Knowl Update Log\n\n## 2026-08-25\n")
			for _, record := range test.records {
				audit, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				content.WriteString("* **Update**: committed. <!-- knowl:" + string(audit) + " -->\n")
			}
			findings := lintLog(knowl.WorkspaceInspection{
				Snapshot: knowl.WorkspaceSnapshot{SchemaDigest: fixtureSchema},
				Log:      knowl.PageSnapshot{Content: content.String()},
			})
			if test.wantCode == "" {
				if len(findings) != 0 {
					t.Fatalf("lintLog() = %#v, want no findings", findings)
				}
				return
			}
			if len(findings) != 1 || findings[0].Code != test.wantCode || findings[0].Severity != test.wantSeverity {
				t.Fatalf("lintLog() = %#v, want %s (%s)", findings, test.wantCode, test.wantSeverity)
			}
		})
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
