package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testPageID          = "entities/one"
	testPagePath        = "wiki/entities/one.md"
	testPageTwoPath     = "wiki/entities/two.md"
	testRootCatalogPath = "wiki/index.md"
	cleanPageOne        = "---\nid: entities/one\ntitle: One\ntype: entity\nsource_refs:\n  - " + testSourceRef + "\n---\n# One\n\n[[entities/two]]\n"
	cleanPageTwo        = "---\nid: entities/two\ntitle: Two\ntype: entity\nsource_refs:\n  - " + testSourceRef + "\n---\n# Two\n\n[[entities/one]]\n"
)

func TestQueryIsWikiFirstBoundedAndCited(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, maintainer := newWorkflow(t, false, nil)
	_ = service
	_ = maintainer
	prepareCanonicalQueryWorkspace(t, workspace, store)
	queryService, err := app.NewQueryService(workspace, store, store, nil, app.QueryOptions{})
	if err != nil {
		t.Fatalf("new query service: %v", err)
	}
	page, err := queryService.Page(ctx, "local", testPageID, knowl.ReadLimits{Pages: 1})
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	if !page.Untrusted || page.ID != testPageID || page.OKF == nil || page.OKF.Type != "entity" || page.Body != "# One\n\n[[entities/two]]\n" {
		t.Fatalf("page = %#v, want untrusted entities/one", page)
	}
	search, err := queryService.Search(ctx, "local", "One", knowl.ReadLimits{Pages: 1}, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(search) != 1 || !search[0].Untrusted || search[0].ID != testPageID {
		t.Fatalf("search = %#v", search)
	}
	result, err := queryService.Query(ctx, "local", "One", knowl.ReadLimits{Pages: 1}, nil)
	if err != nil {
		t.Fatalf("query assembly: %v", err)
	}
	if len(result.Pages) != 1 || len(result.Links) != 1 {
		t.Fatalf("query result = %#v", result)
	}
	if !hasCitation(result.Citations, "wiki", testPageID) || !hasCitation(result.Citations, "raw", testSourceRef) {
		t.Fatalf("query citations = %#v", result.Citations)
	}
	if _, err := queryService.Page(ctx, "local", "entities/missing", knowl.ReadLimits{Pages: 1}); !errors.Is(err, app.ErrPageNotFound) {
		t.Fatalf("missing page error = %v, want page-not-found", err)
	}
	for _, reserved := range []knowl.PageID{"index", "log", "entities/index"} {
		if _, err := queryService.Page(ctx, "local", reserved, knowl.ReadLimits{Pages: 1}); !errors.Is(err, app.ErrPageNotFound) {
			t.Errorf("reserved page %q error = %v, want page-not-found", reserved, err)
		}
	}
}

func TestExplicitQueryFilingUsesTheStandardPlanApplyGate(t *testing.T) {
	ctx := context.Background()
	workspace, store, ingester, maintainer := newWorkflow(t, false, nil)
	prepareCanonicalQueryWorkspace(t, workspace, store)
	queryService, err := app.NewQueryService(workspace, store, store, ingester, app.QueryOptions{})
	if err != nil {
		t.Fatalf("new query service: %v", err)
	}
	schema, err := workspace.Schema(ctx, "local")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	inspection, err := workspace.Inspect(ctx, "local")
	if err != nil {
		t.Fatalf("inspect catalogs: %v", err)
	}
	request := app.FilingRequest{
		Query:  "file this result",
		Result: app.QueryResult{Scope: testSourceScope, Query: "file this result", Pages: []knowl.PageReference{{ID: "entities/source", Path: "wiki/entities/source.md", Title: "Source", Untrusted: true}}, Citations: []app.Citation{{Kind: "raw", Reference: testSourceRef, SourceRef: testSourceRef, Untrusted: true}}},
		Plan: knowl.ModelEditPlan{SchemaDigest: schema.Digest, SourceRefs: []string{testSourceRef}, Edits: []knowl.FileEdit{
			{Path: "wiki/entities/filed.md", Content: []byte("---\nid: entities/filed\ntitle: Filed\ntype: entity\nsource_refs:\n  - " + testSourceRef + "\n---\n# Filed\n")},
			{Path: testRootCatalogPath, ExpectedDigest: inspection.Index.Digest, Content: []byte(inspection.Index.Content + "\n* [Filed](entities/filed.md)\n")},
		}},
	}
	planned, err := queryService.File(ctx, "local", request)
	if err != nil {
		t.Fatalf("file query result: %v", err)
	}
	if planned.Operation.Status != knowl.StatusAwaitingReview {
		t.Fatalf("file status = %q, want awaiting_review", planned.Operation.Status)
	}
	if maintainer.calls() != 0 {
		t.Fatalf("explicit filing invoked maintainer %d times", maintainer.calls())
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki", "entities", "filed.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("filed page stat before apply = %v, want absent", err)
	}
	applied, err := ingester.Apply(ctx, "local", planned.Operation.ID)
	if err != nil {
		t.Fatalf("apply filed result: %v", err)
	}
	if applied.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("filed operation status = %q, want committed", applied.Operation.Status)
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki", "entities", "filed.md")); err != nil {
		t.Fatalf("filed page missing after apply: %v", err)
	}
}

func TestLintReportsCleanWorkspaceAndSuggestionOnlyModelPass(t *testing.T) {
	workspace, store, service, maintainer := newWorkflow(t, false, nil, func(schema knowl.SchemaDocument) knowl.ModelEditPlan {
		return knowl.ModelEditPlan{SchemaDigest: schema.Digest}
	})
	_ = service
	prepareCanonicalQueryWorkspace(t, workspace, store)
	lintService, err := app.NewLintService(workspace, store, app.LintOptions{Maintainer: maintainer})
	if err != nil {
		t.Fatalf("new lint service: %v", err)
	}
	report, err := lintService.Lint(context.Background(), "local")
	if err != nil {
		t.Fatalf("lint clean workspace: %v", err)
	}
	if !report.Healthy() || len(report.Findings) != 0 {
		t.Fatalf("clean lint report = %#v", report)
	}
	if maintainer.calls() != 1 {
		t.Fatalf("optional lint maintainer calls = %d, want 1", maintainer.calls())
	}
}

func TestLintReportsRawPageIndexAndProjectionProblems(t *testing.T) {
	workspace, store, service, maintainer := newWorkflow(t, false, nil)
	_ = service
	_ = maintainer
	accepted := prepareCanonicalQueryWorkspace(t, workspace, store)
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "entities", "orphan.md"), []byte("# Orphan\n"), 0o600); err != nil {
		t.Fatalf("write orphan page: %v", err)
	}
	sourcePath := filepath.Join(workspace.Root(), filepath.Dir(filepath.FromSlash(accepted.ManifestRef)), "source")
	if err := os.WriteFile(sourcePath, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper raw source: %v", err)
	}
	lintService, err := app.NewLintService(workspace, store, app.LintOptions{})
	if err != nil {
		t.Fatalf("new lint service: %v", err)
	}
	report, err := lintService.Lint(context.Background(), "local")
	if err != nil {
		t.Fatalf("lint invalid workspace: %v", err)
	}
	for _, code := range []string{"raw.source_digest_mismatch", "frontmatter.malformed", "index.missing_page", "page.orphan", "projection.drift"} {
		if !hasFinding(report.Findings, code) {
			t.Fatalf("lint report missing %q: %#v", code, report.Findings)
		}
	}
	if report.Healthy() {
		t.Fatal("invalid workspace reported healthy")
	}
}

func prepareCanonicalQueryWorkspace(t *testing.T, workspace interface {
	AcceptSource(ctx context.Context, envelope knowl.SourceEnvelope) (knowl.AcceptedSource, error)
	Snapshot(ctx context.Context, scope knowl.ScopeRef) (knowl.WorkspaceSnapshot, error)
	Root() string
}, store interface {
	Rebuild(ctx context.Context, snapshot knowl.WorkspaceSnapshot) error
}) knowl.AcceptedSource {
	t.Helper()
	accepted, err := workspace.AcceptSource(context.Background(), sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("accept query fixture source: %v", err)
	}
	for relative, content := range map[string]string{
		testPagePath:        cleanPageOne,
		testPageTwoPath:     cleanPageTwo,
		testRootCatalogPath: "---\nokf_version: \"0.2\"\n---\n# Knowl Index\n\n* [One](entities/one.md)\n* [Two](entities/two.md)\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace.Root(), filepath.FromSlash(relative)), []byte(content), 0o600); err != nil {
			t.Fatalf("write query fixture %q: %v", relative, err)
		}
	}
	snapshot, err := workspace.Snapshot(context.Background(), "local")
	if err != nil {
		t.Fatalf("snapshot query fixture: %v", err)
	}
	if err := store.Rebuild(context.Background(), snapshot); err != nil {
		t.Fatalf("project query fixture: %v", err)
	}
	return accepted
}

func hasCitation(citations []app.Citation, kind, reference string) bool {
	for _, citation := range citations {
		if citation.Kind == kind && citation.Reference == reference {
			return true
		}
	}
	return false
}

func hasFinding(findings []knowl.LintFinding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
