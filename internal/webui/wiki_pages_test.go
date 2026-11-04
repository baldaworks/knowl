package webui

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func TestKnowledgeInvalidContextDoesNotInventPathAncestors(t *testing.T) {
	f := canonicalKnowledgeFixture()
	const id = "AI/Prism/guide"
	f.pages[id] = domain.OperatorPage{ID: id, Title: "Guide", Markdown: "# Guide\n\nBody."}
	f.pages["AI/index"] = domain.OperatorPage{ID: "AI/index", Title: "AI", Markdown: "# AI\n"}
	f.pages["AI/Prism/index"] = domain.OperatorPage{ID: "AI/Prism/index", Title: "Prism", Markdown: "# Prism\n"}
	w, doc := fragmentDocument(t, screenHandler(t, f), pageFragment+"?page_id="+url.QueryEscape(id)+"&parent_id=../invalid")
	var titles []string
	walk(doc, func(n *html.Node) {
		if n.DataAtom == atom.Nav && hasAttribute(n, "aria-label", "Breadcrumb") {
			walk(n, func(c *html.Node) {
				if c.DataAtom == atom.Li {
					titles = append(titles, nodeText(c))
				}
			})
		}
	})
	if w.Code != http.StatusOK || !reflect.DeepEqual(titles, []string{renderTestRootTitle, "Guide"}) || f.catalogReads != 0 {
		t.Fatalf("status=%d crumbs=%v catalogReads=%d", w.Code, titles, f.catalogReads)
	}
}

func attribute(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func TestKnowledgeAuthoredBodyIndependentOfLegacyCatalog(t *testing.T) {
	f := canonicalKnowledgeFixture()
	p := f.pages[renderTestCatalogID]
	p.Markdown = "# Team\n\nComplete introduction.\n\n## Further reading\n\n* [One](../../sources/distant/leaf.md)\n* [Again](../../sources/distant/leaf.md)\n\nLast paragraph.\n"
	f.pages[renderTestCatalogID] = p
	w, doc := fragmentDocument(t, screenHandler(t, f), pageFragment+"?page_id="+url.QueryEscape(renderTestCatalogID))
	var body string
	var destinations []string
	walk(doc, func(n *html.Node) {
		if hasAttribute(n, "class", "document") {
			body = compactNodeText(n)
			walk(n, func(c *html.Node) {
				if c.DataAtom == atom.A {
					destinations = append(destinations, attribute(c, "href"))
				}
			})
		}
	})
	if w.Code != 200 || body != "Complete introduction. Further reading One Again Last paragraph." || len(destinations) != 2 || destinations[0] != destinations[1] || f.catalogReads != 0 {
		t.Fatalf("status=%d body=%q destinations=%v catalogs=%d", w.Code, body, destinations, f.catalogReads)
	}
}

func (f *screenReader) WikiPageChildren(_ context.Context, _ domain.ScopeRef, directory string, o app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorWikiPage], error) {
	var items []domain.OperatorWikiPage
	nearestParent := func(id domain.PageID) string {
		parent := path.Dir(string(id))
		if path.Base(string(id)) == rootCatalogID {
			parent = path.Dir(parent)
		}
		for parent != "." && parent != "" {
			if _, ok := f.pages[domain.PageID(parent+"/index")]; ok {
				break
			}
			parent = path.Dir(parent)
		}
		if parent == "." {
			return ""
		}
		return parent
	}
	for id, p := range f.pages {
		relative := string(id) + ".md"
		if id == rootCatalogID || relative == path.Join(directory, "index.md") {
			continue
		}
		parent := nearestParent(id)
		if parent != directory {
			continue
		}
		item := domain.OperatorWikiPage{PageID: id, Path: relative, Title: p.Title}
		if path.Base(string(id)) == rootCatalogID {
			for childID := range f.pages {
				if childID != id && childID != rootCatalogID && nearestParent(childID) == path.Dir(string(id)) {
					item.ChildrenDirectory = path.Dir(string(id))
					break
				}
			}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	var selected []domain.OperatorWikiPage
	for _, item := range items {
		if item.Path > o.Continuation.Key {
			selected = append(selected, item)
		}
	}
	next := ""
	if len(selected) > o.Limit {
		selected = selected[:o.Limit]
		next = selected[len(selected)-1].Path
	}
	return app.OperatorReadPage[domain.OperatorWikiPage]{Items: selected, NextKey: next, SnapshotVersion: f.snapshot}, nil
}

func TestWikiPagesTitlesCaretsContinuationAndTypedFailures(t *testing.T) {
	f := canonicalKnowledgeFixture()
	f.pages["catalogs/other/article"] = domain.OperatorPage{ID: "catalogs/other/article", Title: "Actual child", Markdown: "# Actual child"}
	h := screenHandler(t, f)
	w, doc := fragmentDocument(t, h, wikiPagesFragment+"?limit=1")
	var titles, carets []string
	next := ""
	walk(doc, func(n *html.Node) {
		if n.DataAtom == atom.A {
			titles = append(titles, nodeText(n))
			u, err := url.Parse(attribute(n, "href"))
			if err != nil || u.Query().Get(pageIDParameter) == "" {
				t.Errorf("page href=%q", attribute(n, "href"))
			}
		}
		if hasAttribute(n, "data-page-toggle", "") {
			carets = append(carets, attribute(n, "data-page-url"))
		}
		if hasAttribute(n, "data-page-more", "") {
			next = attribute(n, "data-page-url")
		}
	})
	if w.Code != 200 || len(titles) != 1 || len(carets) != 1 || next == "" {
		t.Fatalf("status=%d titles=%v carets=%v next=%q", w.Code, titles, carets, next)
	}
	w, _ = fragmentDocument(t, h, next)
	if w.Code != 200 {
		t.Fatalf("continuation status=%d", w.Code)
	}
	for _, tc := range []struct {
		query, code string
		status      int
	}{{"scope=x", errorInvalidRequest, 400}, {"directory=AI&directory=ADRs", errorInvalidRequest, 400}, {"presentation=members", errorInvalidRequest, 400}, {"limit=101", errorLimitInvalid, 400}, {"cursor=", errorCursorInvalid, 400}, {"directory=../private", errorInvalidRequest, 400}} {
		w, _ = fragmentDocument(t, h, wikiPagesFragment+"?"+tc.query)
		if w.Code != tc.status || w.Header().Get("X-Knowl-Error") != tc.code {
			t.Errorf("query=%q status=%d code=%q", tc.query, w.Code, w.Header().Get("X-Knowl-Error"))
		}
	}
	f.snapshot = strings.Repeat("b", 64)
	w, _ = fragmentDocument(t, h, next)
	if w.Code != 409 || w.Header().Get("X-Knowl-Error") != errorSnapshotChanged {
		t.Fatalf("stale=%d %q", w.Code, w.Header().Get("X-Knowl-Error"))
	}
}

func TestKnowledgeOptionalIndexContent(t *testing.T) {
	for _, body := range []string{"", "Additional information without a title.\n", "## Authored sections\n\nOne paragraph.\n"} {
		f := canonicalKnowledgeFixture()
		p := f.pages[renderTestCatalogID]
		p.Markdown = body
		f.pages[renderTestCatalogID] = p
		w, doc := fragmentDocument(t, screenHandler(t, f), pageFragment+"?page_id="+url.QueryEscape(renderTestCatalogID))
		var documents, titles int
		var content string
		walk(doc, func(n *html.Node) {
			if hasAttribute(n, "class", "document") {
				documents++
				content = compactNodeText(n)
			}
			if hasAttribute(n, "id", "article-title") {
				titles++
			}
		})
		if w.Code != 200 || documents != 1 || titles != 1 || (body == "" && content != "") || (body != "" && content == "") {
			t.Fatalf("body=%q status=%d documents=%d titles=%d content=%q", body, w.Code, documents, titles, content)
		}
	}
}
