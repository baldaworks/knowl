package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const knowledgeOtherCatalog = "catalogs/other/index"
const knowledgeDistantLeaf = "sources/distant/leaf"
const knowledgeDistantTitle = "Distant leaf"
const knowledgeOtherTitle = "Other catalog"
const knowledgeCatalogKind = "catalog"
const folderKind = "folder"
const indexFileName = "index.md"
const catalogsDir = "catalogs"
const conceptsDir = "concepts"
const sourcesDir = "sources"
const emptyDir = "empty"

func canonicalKnowledgeFixture() *screenReader {
	children := func(id domain.PageID, title string, items ...domain.OperatorCatalogChild) app.OperatorCatalogRead {
		return app.OperatorCatalogRead{Parent: domain.OperatorCatalogSummary{ID: id, Title: title}, Children: app.OperatorReadPage[domain.OperatorCatalogChild]{Items: items}}
	}
	page := func(id domain.PageID, title, markdown string) domain.OperatorPage {
		return domain.OperatorPage{ID: id, Title: title, Markdown: markdown, Digest: screenSnapshot, Version: screenSnapshot}
	}
	return &screenReader{snapshot: screenSnapshot, catalogs: map[domain.PageID]app.OperatorCatalogRead{
		renderTestRootID:      children(renderTestRootID, renderTestRootTitle, domain.OperatorCatalogChild{ID: renderTestCatalogID, Title: renderTestCatalogTitle, Kind: knowledgeCatalogKind}, domain.OperatorCatalogChild{ID: screenArticleID, Title: screenArticleTitle, Kind: pageKind}),
		renderTestCatalogID:   children(renderTestCatalogID, renderTestCatalogTitle, domain.OperatorCatalogChild{ID: knowledgeOtherCatalog, Title: knowledgeOtherTitle, Kind: knowledgeCatalogKind}, domain.OperatorCatalogChild{ID: knowledgeDistantLeaf, Title: knowledgeDistantTitle, Kind: pageKind}),
		knowledgeOtherCatalog: children(knowledgeOtherCatalog, knowledgeOtherTitle, domain.OperatorCatalogChild{ID: knowledgeDistantLeaf, Title: knowledgeDistantTitle, Kind: pageKind}),
	}, directories: map[string][]domain.OperatorWikiEntry{
		"": {
			{Path: catalogsDir, Name: catalogsDir, Kind: folderKind},
			{Path: conceptsDir, Name: conceptsDir, Kind: folderKind},
			{Path: emptyDir, Name: emptyDir, Kind: folderKind},
			{Path: indexFileName, Name: indexFileName, Kind: pageKind, PageID: rootCatalogID},
			{Path: sourcesDir, Name: sourcesDir, Kind: folderKind},
		},
		catalogsDir:       {{Path: "catalogs/team", Name: "team", Kind: folderKind}},
		"catalogs/team":   {{Path: "catalogs/team/index.md", Name: indexFileName, Kind: pageKind, PageID: renderTestCatalogID}},
		conceptsDir:       {{Path: "concepts/article.md", Name: "article.md", Kind: pageKind, PageID: screenArticleID}},
		emptyDir:          {},
		sourcesDir:        {{Path: "sources/distant", Name: "distant", Kind: folderKind}},
		"sources/distant": {{Path: "sources/distant/leaf.md", Name: "leaf.md", Kind: pageKind, PageID: knowledgeDistantLeaf}},
	}, pages: map[domain.PageID]domain.OperatorPage{
		renderTestRootID:      page(renderTestRootID, renderTestRootTitle, "---\nokf_version: \"0.2\"\n---\n# Root\n\n* [Team](catalogs/team/index.md)\n"),
		renderTestCatalogID:   page(renderTestCatalogID, renderTestCatalogTitle, "# Team\n\n* [Distant leaf](../../sources/distant/leaf.md)\n"),
		knowledgeOtherCatalog: page(knowledgeOtherCatalog, knowledgeOtherTitle, "# Other catalog\n"),
		screenArticleID:       page(screenArticleID, screenArticleTitle, "# Article\n\nFirst leaf body."),
		knowledgeDistantLeaf:  page(knowledgeDistantLeaf, knowledgeDistantTitle, "# Distant leaf\n\nExact distant body."),
	}}
}

func TestKnowledgeCanonicalRootAndCatalogBody(t *testing.T) {
	for _, tc := range []struct{ name, route, id, title, body string }{
		{"root", knowledgeFragment, renderTestRootID, renderTestRootTitle, "Team"},
		{"nested", knowledgeFragment + "?parent_id=" + url.QueryEscape(renderTestCatalogID), renderTestCatalogID, renderTestCatalogTitle, "Distant leaf"},
		{"heading only", knowledgeFragment + "?parent_id=" + url.QueryEscape(knowledgeOtherCatalog), knowledgeOtherCatalog, knowledgeOtherTitle, ""},
		{"explicit leaf", pageFragment + "?page_id=" + url.QueryEscape(knowledgeDistantLeaf), knowledgeDistantLeaf, knowledgeDistantTitle, "Exact distant body."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := canonicalKnowledgeFixture()
			if tc.name == "heading only" {
				catalog := f.catalogs[knowledgeOtherCatalog]
				catalog.Children.Items = nil
				f.catalogs[knowledgeOtherCatalog] = catalog
			}
			r, doc := fragmentDocument(t, screenHandler(t, f), tc.route)
			if r.Code != http.StatusOK || (len(f.pageIDs) == 0 || f.pageIDs[0] != domain.PageID(tc.id)) {
				t.Fatalf("status=%d selected=%v", r.Code, f.pageIDs)
			}
			var body string
			var headings []string
			var sourcePanels int
			var documentOrder, detailsOrder, order int
			walk(doc, func(n *html.Node) {
				order++
				if hasAttribute(n, "class", "document") {
					body = compactNodeText(n)
					documentOrder = order
				}
				if hasAttribute(n, "id", "details") {
					detailsOrder = order
				}
				if n.DataAtom == atom.H1 {
					headings = append(headings, nodeText(n))
				}
				if hasAttribute(n, "id", "page-sources") {
					sourcePanels++
				}
			})
			if body != tc.body {
				t.Errorf("body=%q want %q", body, tc.body)
			}
			if tc.name == "root" && (documentOrder == 0 || detailsOrder <= documentOrder) {
				t.Errorf("root content must precede technical details: document=%d details=%d", documentOrder, detailsOrder)
			}
			if !reflect.DeepEqual(headings, []string{tc.title}) || sourcePanels != 1 {
				t.Errorf("index headings=%v source panels=%d", headings, sourcePanels)
			}
		})
	}
}

func compactNodeText(n *html.Node) string { return strings.Join(strings.Fields(nodeText(n)), " ") }
func hasAttribute(n *html.Node, key, value string) bool {
	for _, a := range n.Attr {
		if a.Key == key && a.Val == value {
			return true
		}
	}
	return false
}

func TestKnowledgeOneCurrentBreadcrumb(t *testing.T) {
	f := canonicalKnowledgeFixture()
	r, doc := fragmentDocument(t, screenHandler(t, f), knowledgeFragment+"?view=all")
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	count := 0
	current := ""
	paths := map[string]string{}
	walk(doc, func(n *html.Node) {
		if n.DataAtom == atom.Nav && hasAttribute(n, "aria-label", "Breadcrumb") {
			walk(n, func(child *html.Node) {
				if hasAttribute(child, "aria-current", "page") {
					count++
					current = nodeText(child)
				}
			})
		}
		if n.DataAtom == atom.Li {
			for _, a := range n.Attr {
				if a.Key == "data-wiki-path" {
					paths[a.Val] = nodeText(n)
				}
			}
		}
	})
	if count != 1 || current != allPagesTitle || len(f.pageIDs) != 0 || paths[indexFileName] != indexFileName || !strings.HasSuffix(paths[catalogsDir], catalogsDir) {
		t.Fatalf("current count=%d title=%q pages=%v paths=%v", count, current, f.pageIDs, paths)
	}
}

func TestKnowledgeTreeFileKeepsAllPagesOrigin(t *testing.T) {
	f := canonicalKnowledgeFixture()
	r, doc := fragmentDocument(t, screenHandler(t, f), pageFragment+"?view=all&page_id="+url.QueryEscape(knowledgeDistantLeaf))
	if r.Code != http.StatusOK {
		t.Fatalf("status=%d", r.Code)
	}
	var crumbs []string
	var allURL string
	walk(doc, func(n *html.Node) {
		if n.DataAtom != atom.Nav || !hasAttribute(n, "aria-label", "Breadcrumb") {
			return
		}
		walk(n, func(child *html.Node) {
			if child.DataAtom == atom.Li {
				crumbs = append(crumbs, nodeText(child))
			}
			if child.DataAtom == atom.A && nodeText(child) == allPagesTitle {
				for _, a := range child.Attr {
					if a.Key == linkHrefAttribute {
						allURL = a.Val
					}
				}
			}
		})
	})
	if !reflect.DeepEqual(crumbs, []string{"Root", allPagesTitle, knowledgeDistantTitle}) || allURL != "/ui/knowledge?view=all" {
		t.Fatalf("breadcrumbs=%v all URL=%q", crumbs, allURL)
	}
}

func TestKnowledgeCanceledReadKeepsTypedError(t *testing.T) {
	f := canonicalKnowledgeFixture()
	h := screenHandler(t, f)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := httptest.NewRecorder()
	h.Fragments(r, httptest.NewRequest(http.MethodGet, knowledgeFragment, nil).WithContext(ctx))
	if r.Code != 503 || r.Header().Get("X-Knowl-Error") != errorWorkspaceUnavailable {
		t.Fatalf("canceled status=%d code=%q", r.Code, r.Header().Get("X-Knowl-Error"))
	}
}
