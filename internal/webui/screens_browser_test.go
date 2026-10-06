//go:build browser

package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestBrowserKnowledgeSearch(t *testing.T) {
	fixture := &screenReader{snapshot: screenSnapshot, invalidateContinuation: true, body: "---\ntype: topic\ntitle: Article\n---\n# Article\n\nCurrent published body. [[concepts/other]]" + strings.Repeat("\n\nPublished paragraph for responsive reading and saved-source focus restoration.", 24)}
	canonical := canonicalKnowledgeFixture()
	fixture.catalogs, fixture.pages = canonical.catalogs, canonical.pages
	for _, id := range []domain.PageID{screenArticleID, "concepts/other"} {
		fixture.pages[id] = domain.OperatorPage{ID: id, Title: screenArticleTitle, Markdown: fixture.body, Digest: screenSnapshot, Version: screenSnapshot, Metadata: &domain.OperatorPageMetadata{Type: "topic", Description: "Published article metadata", Tags: []string{"navigation", "provenance"}}, Sources: []domain.OperatorPageSource{{SourceRef: screenSourceRef, Revision: "accepted"}}}
	}
	longTitle := strings.Repeat("CanonicalDocumentTitle", 8)
	fixture.catalogs["catalogs/long/index"] = app.OperatorCatalogRead{Parent: domain.OperatorCatalogSummary{ID: "catalogs/long/index", Title: longTitle}, Children: app.OperatorReadPage[domain.OperatorCatalogChild]{}}
	fixture.pages["catalogs/long/index"] = domain.OperatorPage{ID: "catalogs/long/index", Title: longTitle, Markdown: "# " + longTitle + "\n", Digest: screenSnapshot, Version: screenSnapshot}
	unicodeID := domain.PageID("catalogs/архитектура/🦉/index")
	fixture.catalogs[unicodeID] = app.OperatorCatalogRead{Parent: domain.OperatorCatalogSummary{ID: unicodeID, Title: "Unicode catalog"}, Children: app.OperatorReadPage[domain.OperatorCatalogChild]{Items: []domain.OperatorCatalogChild{{ID: knowledgeDistantLeaf, Title: knowledgeDistantTitle, Kind: pageKind}}}}
	fixture.pages[unicodeID] = domain.OperatorPage{ID: unicodeID, Title: "Unicode catalog", Markdown: "# Unicode catalog\n\n* [Distant leaf](../../../sources/distant/leaf.md)\n", Digest: screenSnapshot, Version: screenSnapshot}
	root := fixture.catalogs[rootCatalogID]
	root.Children.Items = append(root.Children.Items, domain.OperatorCatalogChild{ID: unicodeID, Title: "Unicode catalog", Kind: "catalog"})
	slices.SortFunc(root.Children.Items, func(a, b domain.OperatorCatalogChild) int { return strings.Compare(string(a.ID), string(b.ID)) })
	fixture.catalogs[rootCatalogID] = root
	ui := screenHandler(t, fixture)
	reader := &responsiveScreenReader{fixture}
	operator, err := app.NewOperatorService("trusted", app.OperatorReaders{Catalogs: reader, Pages: reader, Page: reader, Revisions: reader}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ui.dependencies.Operator = operator
	var calls atomic.Int64
	ui.dependencies.EmbeddingsEnabled = true
	ui.dependencies.Retrieve = func(_ context.Context, query string, _ []string) (knowlapi.RetrieveResult, error) {
		calls.Add(1)
		time.Sleep(150 * time.Millisecond)
		reason := knowlapi.RetrievalStatusReason("unavailable")
		refs := []string{"git:docs/" + strings.Repeat("long-identity", 30) + "@" + screenSnapshot}
		result := knowlapi.RetrieveResult{Query: query, Evidence: []knowlapi.EvidenceItem{{PageId: "concepts/other", Title: "Second", SourceRefs: &refs, Snippet: strings.Repeat("Exact ordered snippet. ", 80)}, {PageId: screenArticleID, Title: "First", Snippet: "Another exact snippet."}}}
		if query != "unreported" {
			result.Retrieval = &knowlapi.RetrievalStatus{Effective: knowlapi.RetrievalStatusEffective(query), Reason: &reason}
		}
		return result, nil
	}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ui/fragments/") {
			if r.Header.Get("Authorization") != "Bearer browser-secret-token" {
				ui.Error(w, 401, "unauthorized")
				return
			}
			ui.Fragments(w, r)
			return
		}
		ui.ServeHTTP(w, r)
	}))
	defer host.Close()
	script, err := filepath.Abs("../../tools/webui-browser/screens.cjs")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "node", script)
	cmd.Env = append(os.Environ(), "KNOWL_BROWSER_URL="+host.URL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	t.Log(string(output))
	if calls.Load() != 4 {
		t.Fatalf("retrieval calls=%d want 4 explicit submissions", calls.Load())
	}
}

// Browser-only long provenance exercises expanded metadata and seven revisions.
type responsiveScreenReader struct{ *screenReader }

func (f *responsiveScreenReader) Page(ctx context.Context, scope domain.ScopeRef, id domain.PageID, limits domain.ReadLimits) (domain.OperatorPage, error) {
	page, err := f.screenReader.Page(ctx, scope, id, limits)
	if kind, _ := okf.ClassifyPath(string(id) + ".md"); kind == okf.DocumentConcept && id != knowledgeDistantLeaf {
		page.Sources[0].OriginalURI = "https://example.test/guide"
		for _, name := range []string{"missing", "unsupported", "limit", "a & b?🦉", "additional"} {
			page.Sources = append(page.Sources, domain.OperatorPageSource{SourceRef: "git:docs/" + name + "@accepted", Revision: "accepted", DocumentID: domain.DocumentID(name)})
		}
		page.Sources = append(page.Sources, domain.OperatorPageSource{SourceRef: "git:docs/" + strings.Repeat("long-reference", 30) + "@second", Revision: strings.Repeat("r", 100)})
	}
	return page, err
}

func (f *responsiveScreenReader) SourceRevision(ctx context.Context, scope domain.ScopeRef, ref string, limits domain.ReadLimits) (domain.OperatorSourceRevision, error) {
	for name, err := range map[string]error{"missing": app.ErrOperatorSourceRevisionNotFound, "unsupported": app.ErrOperatorUnsupportedFormat, "limit": app.ErrOperatorReadLimitExceeded} {
		if ref == "git:docs/"+name+"@accepted" {
			return domain.OperatorSourceRevision{}, err
		}
	}
	revision, err := f.screenReader.SourceRevision(ctx, scope, ref, limits)
	revision.OriginalURI = "https://example.test/guide"
	return revision, err
}
