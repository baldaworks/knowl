package webui

import (
	"context"
	"encoding/json"
	"fmt"
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
		{"root", knowledgeFragment, renderTestRootID, renderTestRootTitle, "Root Team"},
		{"nested", knowledgeFragment + "?parent_id=" + url.QueryEscape(renderTestCatalogID), renderTestCatalogID, renderTestCatalogTitle, "Team Distant leaf"},
		{"heading only", knowledgeFragment + "?parent_id=" + url.QueryEscape(knowledgeOtherCatalog), knowledgeOtherCatalog, knowledgeOtherTitle, knowledgeOtherTitle},
		{"explicit leaf", pageFragment + "?page_id=" + url.QueryEscape(knowledgeDistantLeaf), knowledgeDistantLeaf, knowledgeDistantTitle, "Distant leaf Exact distant body."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := canonicalKnowledgeFixture()
			if tc.name == "heading only" {
				catalog := f.catalogs[knowledgeOtherCatalog]
				catalog.Children.Items = nil
				f.catalogs[knowledgeOtherCatalog] = catalog
			}
			r, doc := fragmentDocument(t, screenHandler(t, f), tc.route)
			if r.Code != http.StatusOK || !reflect.DeepEqual(f.pageIDs, []domain.PageID{domain.PageID(tc.id)}) {
				t.Fatalf("status=%d selected=%v", r.Code, f.pageIDs)
			}
			var body string
			var headings []string
			var sourcePanels int
			walk(doc, func(n *html.Node) {
				if hasAttribute(n, "class", "document") {
					body = compactNodeText(n)
				}
				if n.DataAtom == atom.H1 || n.DataAtom == atom.H2 {
					headings = append(headings, nodeText(n))
				}
				if hasAttribute(n, "id", "page-sources") {
					sourcePanels++
				}
			})
			if body != tc.body {
				t.Errorf("body=%q want %q", body, tc.body)
			}
			if tc.id != knowledgeDistantLeaf && (!reflect.DeepEqual(headings, []string{tc.title}) || sourcePanels != 0) {
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

func TestKnowledgeCatalogFallbackAndContext(t *testing.T) {
	for _, tc := range []struct {
		name, parent string
		catalogErr   error
		status       int
		code         string
		linked       bool
	}{
		{name: "default absent", catalogErr: app.ErrOperatorCatalogNotFound, status: 200},
		{name: "explicit missing", parent: renderTestCatalogID, catalogErr: app.ErrOperatorCatalogNotFound, status: 404, code: errorCatalogNotFound},
		{name: "explicit invalid", parent: renderTestCatalogID, catalogErr: app.ErrOperatorWorkspaceUnavailable, status: 503, code: errorWorkspaceUnavailable},
		{name: "distant context", parent: renderTestCatalogID, status: 200, linked: true},
		{name: "second parent", parent: knowledgeOtherCatalog, status: 200, linked: true},
		{name: "context unavailable", parent: renderTestCatalogID, catalogErr: app.ErrOperatorCatalogNotFound, status: 200},
		{name: "context malformed", parent: renderTestCatalogID, catalogErr: app.ErrOperatorWorkspaceUnavailable, status: 200},
		{name: "context traversal", parent: "../private", status: 200},
		{name: "nonmember context", parent: renderTestRootID, status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := canonicalKnowledgeFixture()
			f.catalogErrors = map[domain.PageID]error{domain.PageID(tc.parent): tc.catalogErr}
			if tc.parent == "" {
				f.catalogErrors[renderTestRootID] = tc.catalogErr
			}
			q := url.Values{}
			route := knowledgeFragment
			if tc.parent != "" {
				q.Set(parentIDParameter, tc.parent)
			}
			if tc.linked || strings.HasPrefix(tc.name, "context") || tc.name == "nonmember context" {
				route = pageFragment
				q.Set("page_id", knowledgeDistantLeaf)
				q.Set(limitParameter, "1")
			}
			r, doc := fragmentDocument(t, screenHandler(t, f), route+"?"+q.Encode())
			if r.Code != tc.status || r.Header().Get("X-Knowl-Error") != tc.code {
				t.Fatalf("status=%d error=%q", r.Code, r.Header().Get("X-Knowl-Error"))
			}
			if route != pageFragment {
				return
			}
			if !reflect.DeepEqual(f.pageIDs, []domain.PageID{knowledgeDistantLeaf}) {
				t.Errorf("selection=%v", f.pageIDs)
			}
			var breadcrumbs *html.Node
			var body string
			walk(doc, func(n *html.Node) {
				if hasAttribute(n, "class", "document") {
					body = compactNodeText(n)
				}
				if n.DataAtom == atom.Nav && hasAttribute(n, "aria-label", "Breadcrumb") {
					breadcrumbs = n
				}
			})
			if breadcrumbs == nil {
				t.Fatal("missing semantic breadcrumbs")
			}
			if body != "Distant leaf Exact distant body." {
				t.Errorf("explicit body=%q", body)
			}
			links := map[string]string{}
			current := ""
			walk(breadcrumbs, func(n *html.Node) {
				if hasAttribute(n, "aria-current", "page") {
					current = nodeText(n)
				}
				if n.DataAtom == atom.A {
					for _, a := range n.Attr {
						if a.Key == linkHrefAttribute {
							links[nodeText(n)] = a.Val
						}
					}
				}
			})
			if current != knowledgeDistantTitle || links[renderTestRootTitle] != knowledgePath {
				t.Errorf("breadcrumbs=%v current=%q", links, current)
			}
			catalogURL := "/ui/knowledge?" + url.Values{parentIDParameter: {tc.parent}}.Encode()
			found := false
			for _, link := range links {
				if link == catalogURL {
					found = true
				}
			}
			if found != tc.linked {
				t.Errorf("context links=%v linked=%v", links, tc.linked)
			}
			if tc.name == "distant context" && f.catalogReads < 2 {
				t.Error("membership beyond first catalog window was not checked")
			}
		})
	}
}

func TestKnowledgeContextEdgeBound(t *testing.T) {
	f := canonicalKnowledgeFixture()
	limits := app.DefaultCatalogLimits()
	catalog := f.catalogs[renderTestCatalogID]
	catalog.Children.Items = nil
	for i := range limits.MaxEdges {
		catalog.Children.Items = append(catalog.Children.Items, domain.OperatorCatalogChild{ID: domain.PageID(fmt.Sprintf("a/page-%05d", i)), Title: "Bounded child", Kind: pageKind})
	}
	const selected = "z/leaf"
	catalog.Children.Items = append(catalog.Children.Items, domain.OperatorCatalogChild{ID: selected, Title: knowledgeDistantTitle, Kind: pageKind})
	f.catalogs[renderTestCatalogID] = catalog
	f.pages[selected] = domain.OperatorPage{ID: selected, Title: knowledgeDistantTitle, Markdown: "# Distant leaf\n"}
	r, doc := fragmentDocument(t, screenHandler(t, f), pageFragment+"?"+url.Values{pageIDParameter: {selected}, parentIDParameter: {renderTestCatalogID}}.Encode())
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	var current, contextID string
	walk(doc, func(n *html.Node) {
		if hasAttribute(n, "aria-current", "page") {
			current = nodeText(n)
		}
		if n.DataAtom == atom.Nav {
			for _, a := range n.Attr {
				if a.Key == "data-context-id" {
					contextID = a.Val
				}
			}
		}
	})
	if current != knowledgeDistantTitle || contextID != "" || f.catalogReads > limits.MaxEdges/100+3 {
		t.Fatalf("current=%q context=%q catalog reads=%d", current, contextID, f.catalogReads)
	}
}

func TestKnowledgeValidatedHistoryTrail(t *testing.T) {
	for _, tc := range []struct {
		name   string
		trail  any
		broken bool
		want   []string
	}{
		{name: "known ancestry", trail: []string{rootCatalogID, renderTestCatalogID, knowledgeOtherCatalog}, want: []string{renderTestRootTitle, renderTestCatalogTitle, knowledgeOtherTitle, knowledgeDistantTitle}},
		{name: "changed ancestor", trail: []string{rootCatalogID, renderTestCatalogID, knowledgeOtherCatalog}, broken: true, want: []string{renderTestRootTitle, knowledgeOtherTitle, knowledgeDistantTitle}},
		{name: "unrelated prefix", trail: []string{renderTestCatalogID, rootCatalogID, knowledgeOtherCatalog}, want: []string{renderTestRootTitle, knowledgeOtherTitle, knowledgeDistantTitle}},
		{name: "cycle", trail: []string{rootCatalogID, renderTestCatalogID, rootCatalogID, knowledgeOtherCatalog}, want: []string{renderTestRootTitle, knowledgeOtherTitle, knowledgeDistantTitle}},
		{name: "wrong terminal", trail: []string{rootCatalogID, renderTestCatalogID}, want: []string{renderTestRootTitle, knowledgeOtherTitle, knowledgeDistantTitle}},
		{name: "alias", trail: []string{"wiki/index", knowledgeOtherCatalog}, want: []string{renderTestRootTitle, knowledgeOtherTitle, knowledgeDistantTitle}},
		{name: "oversize", trail: strings.Repeat("x", 40_000), want: []string{renderTestRootTitle, knowledgeOtherTitle, knowledgeDistantTitle}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := canonicalKnowledgeFixture()
			if tc.broken {
				catalog := f.catalogs[rootCatalogID]
				catalog.Children.Items = nil
				f.catalogs[rootCatalogID] = catalog
			}
			h := screenHandler(t, f)
			request := httptest.NewRequest(http.MethodGet, pageFragment+"?"+url.Values{pageIDParameter: {knowledgeDistantLeaf}, parentIDParameter: {knowledgeOtherCatalog}}.Encode(), nil)
			data, err := json.Marshal(tc.trail)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("X-Knowl-Catalog-Trail", string(data))
			r := httptest.NewRecorder()
			h.Fragments(r, request)
			if r.Code != 200 {
				t.Fatalf("explicit page suppressed: %d", r.Code)
			}
			doc, err := html.Parse(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			var titles []string
			walk(doc, func(n *html.Node) {
				if n.DataAtom == atom.Nav && hasAttribute(n, "aria-label", "Breadcrumb") {
					walk(n, func(child *html.Node) {
						if child.DataAtom == atom.Li {
							titles = append(titles, nodeText(child))
						}
					})
				}
			})
			if !reflect.DeepEqual(titles, tc.want) {
				t.Errorf("breadcrumbs=%v want %v", titles, tc.want)
			}
		})
	}
}

func TestKnowledgeOneCurrentBreadcrumb(t *testing.T) {
	r, doc := fragmentDocument(t, screenHandler(t, canonicalKnowledgeFixture()), knowledgeFragment+"?view=all")
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	count := 0
	current := ""
	allLink := ""
	walk(doc, func(n *html.Node) {
		if n.DataAtom == atom.Nav && hasAttribute(n, "aria-label", "Breadcrumb") {
			walk(n, func(child *html.Node) {
				if hasAttribute(child, "aria-current", "page") {
					count++
					current = nodeText(child)
				}
				if child.DataAtom == atom.A && nodeText(child) == "All pages" {
					for _, a := range child.Attr {
						if a.Key == linkHrefAttribute {
							allLink = a.Val
						}
					}
				}
			})
		}
	})
	if count != 1 || current != screenArticleTitle || allLink != "/ui/knowledge?view=all" {
		t.Fatalf("current count=%d title=%q All pages URL=%q", count, current, allLink)
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

func TestKnowledgeHistoryTrailSharesEdgeBudget(t *testing.T) {
	f := canonicalKnowledgeFixture()
	for _, parent := range []domain.PageID{rootCatalogID, renderTestCatalogID} {
		catalog := f.catalogs[parent]
		catalog.Children.Items = nil
		for i := range app.DefaultCatalogLimits().MaxEdges/2 + 1 {
			catalog.Children.Items = append(catalog.Children.Items, domain.OperatorCatalogChild{ID: domain.PageID(fmt.Sprintf("a/page-%05d", i)), Title: "Bounded child", Kind: pageKind})
		}
		child := domain.PageID(renderTestCatalogID)
		if parent == renderTestCatalogID {
			child = knowledgeOtherCatalog
		}
		catalog.Children.Items = append(catalog.Children.Items, domain.OperatorCatalogChild{ID: child, Title: renderTestCatalogTitle, Kind: knowledgeCatalogKind})
		f.catalogs[parent] = catalog
	}
	h := screenHandler(t, f)
	request := httptest.NewRequest(http.MethodGet, pageFragment+"?"+url.Values{pageIDParameter: {knowledgeDistantLeaf}, parentIDParameter: {knowledgeOtherCatalog}}.Encode(), nil)
	data, err := json.Marshal([]string{rootCatalogID, renderTestCatalogID, knowledgeOtherCatalog})
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(catalogTrailHeader, string(data))
	r := httptest.NewRecorder()
	h.Fragments(r, request)
	if r.Code != 200 {
		t.Fatalf("optional oversized ancestry suppressed page: %d", r.Code)
	}
	doc, err := html.Parse(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var trail []domain.PageID
	walk(doc, func(n *html.Node) {
		if n.DataAtom == atom.Nav {
			for _, a := range n.Attr {
				if a.Key == "data-catalog-trail" {
					if decodeErr := json.Unmarshal([]byte(a.Val), &trail); decodeErr != nil {
						t.Error(decodeErr)
					}
				}
			}
		}
	})
	if !reflect.DeepEqual(trail, []domain.PageID{knowledgeOtherCatalog}) || f.catalogReads > app.DefaultCatalogLimits().MaxEdges/100+5 {
		t.Fatalf("trail=%v calls=%d", trail, f.catalogReads)
	}
}
