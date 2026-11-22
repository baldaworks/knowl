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

const (
	navigationAutoID             = "concepts/auto-mode"
	navigationParentID           = "catalogs/balda/conversations/index"
	navigationAutoTitle          = "Balda auto mode"
	navigationConversationsTitle = "Conversations"
	navigationBaldaTitle         = "Balda"
)

func navigationFixture() *screenReader {
	f := canonicalKnowledgeFixture()
	for _, p := range []domain.OperatorPage{
		{ID: navigationAutoID, Title: navigationAutoTitle, Markdown: "# Balda auto mode\n\nActual article body."},
		{ID: navigationParentID, Title: navigationConversationsTitle, Markdown: "# Conversations\n\nAdditional prose.\n\n[Auto mode](../../../concepts/auto-mode.md)\n"},
		{ID: knowledgeOtherCatalog, Title: "Other context", Markdown: "# Other context\n\n[Auto mode](../../concepts/auto-mode.md)\n"},
	} {
		p.Digest = screenSnapshot
		p.Version = screenSnapshot
		f.pages[p.ID] = p
	}
	return f
}

func breadcrumbTexts(doc *html.Node) []string {
	var result []string
	walk(doc, func(n *html.Node) {
		if n.DataAtom == atom.Nav && hasAttribute(n, "aria-label", "Breadcrumb") {
			walk(n, func(child *html.Node) {
				if child.DataAtom == atom.Li {
					result = append(result, nodeText(child))
				}
			})
		}
	})
	return result
}

func TestKnowledgeExplicitParentKeepsSelectedContext(t *testing.T) {
	for _, tc := range []struct{ parent, title string }{
		{navigationParentID, navigationConversationsTitle},
		{knowledgeOtherCatalog, "Other context"},
	} {
		t.Run(tc.title, func(t *testing.T) {
			f := navigationFixture()
			target := pageFragment + "?" + url.Values{pageIDParameter: {navigationAutoID}, parentIDParameter: {tc.parent}}.Encode()
			r, doc := fragmentDocument(t, screenHandler(t, f), target)
			if r.Code != http.StatusOK {
				t.Fatalf("status=%d", r.Code)
			}
			if got := breadcrumbTexts(doc); !reflect.DeepEqual(got, []string{renderTestRootTitle, tc.title, navigationAutoTitle}) {
				t.Fatalf("breadcrumbs=%v", got)
			}
		})
	}
}

func TestKnowledgeVisitedTrailSurvivesAncestorAndAuthoredURLs(t *testing.T) {
	f := navigationFixture()
	f.pages["catalogs/balda/index"] = domain.OperatorPage{ID: "catalogs/balda/index", Title: navigationBaldaTitle, Markdown: "# Balda\n\n[Conversations](conversations/index.md)\n"}
	ids := []domain.PageID{rootCatalogID, "catalogs/balda/index", navigationParentID}
	target := navigationPageURL(navigationAutoID, ids)
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	r, doc := fragmentDocument(t, screenHandler(t, f), pageFragment+"?"+parsed.RawQuery)
	if r.Code != 200 || !reflect.DeepEqual(breadcrumbTexts(doc), []string{renderTestRootTitle, navigationBaldaTitle, navigationConversationsTitle, navigationAutoTitle}) {
		t.Fatalf("status=%d crumbs=%v", r.Code, breadcrumbTexts(doc))
	}
	var parentURL string
	walk(doc, func(n *html.Node) {
		if n.DataAtom == atom.A && nodeText(n) == navigationConversationsTitle {
			parentURL = attribute(n, "href")
		}
	})
	parsed, err = url.Parse(parentURL)
	if err != nil {
		t.Fatal(err)
	}
	r, doc = fragmentDocument(t, screenHandler(t, f), pageFragment+"?"+parsed.RawQuery)
	if r.Code != 200 || !reflect.DeepEqual(breadcrumbTexts(doc), []string{renderTestRootTitle, navigationBaldaTitle, navigationConversationsTitle}) {
		t.Fatalf("ancestor status=%d crumbs=%v", r.Code, breadcrumbTexts(doc))
	}
	var articleURL string
	walk(doc, func(n *html.Node) {
		if n.DataAtom == atom.A && nodeText(n) == "Auto mode" {
			articleURL = attribute(n, "href")
		}
	})
	parsed, err = url.Parse(articleURL)
	if err != nil {
		t.Fatal(err)
	}
	var got []domain.PageID
	if err := json.Unmarshal([]byte(parsed.Query().Get(trailParameter)), &got); err != nil || !reflect.DeepEqual(got, ids) {
		t.Fatalf("authored URL trail=%v error=%v URL=%q", got, err, articleURL)
	}
	r, doc = fragmentDocument(t, screenHandler(t, f), pageFragment+"?"+parsed.RawQuery)
	if r.Code != 200 || !reflect.DeepEqual(breadcrumbTexts(doc), []string{renderTestRootTitle, navigationBaldaTitle, navigationConversationsTitle, navigationAutoTitle}) {
		t.Fatalf("reload status=%d crumbs=%v", r.Code, breadcrumbTexts(doc))
	}
}

func TestKnowledgeContextRequiresActualSafeLinkOrVisibleChild(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"prose Markdown", "Further reading: [Auto](../../../concepts/auto-mode.md).", true},
		{"wiki link", "Further reading: [[concepts/auto-mode|Auto]].", true},
		{"code", "```md\n[Auto](../../../concepts/auto-mode.md)\n[[concepts/auto-mode]]\n```", false},
		{"image", "![Auto](../../../concepts/auto-mode.md)", false},
		{"bare list", "* concepts/auto-mode.md", false},
		{"absolute path", "[Auto](/concepts/auto-mode.md)", false},
		{"query", "[Auto](../../../concepts/auto-mode.md?fake=1)", false},
		{"remote", "[Auto](https://example.test/ui/knowledge?page_id=concepts%2Fauto-mode)", false},
		{"raw HTML", "<a href='../../../concepts/auto-mode.md'>Auto</a>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := navigationFixture()
			p := f.pages[navigationParentID]
			p.Markdown = "# Conversations\n\n" + tc.body
			f.pages[p.ID] = p
			target := pageFragment + "?" + url.Values{pageIDParameter: {navigationAutoID}, parentIDParameter: {string(p.ID)}}.Encode()
			r, doc := fragmentDocument(t, screenHandler(t, f), target)
			want := []string{renderTestRootTitle, navigationAutoTitle}
			if tc.valid {
				want = []string{renderTestRootTitle, navigationConversationsTitle, navigationAutoTitle}
			}
			if r.Code != 200 || !reflect.DeepEqual(breadcrumbTexts(doc), want) {
				t.Fatalf("status=%d crumbs=%v want=%v", r.Code, breadcrumbTexts(doc), want)
			}
		})
	}
	t.Run("empty index actual child", func(t *testing.T) {
		f := navigationFixture()
		f.pages["AI/index"] = domain.OperatorPage{ID: "AI/index", Title: "AI", Markdown: ""}
		f.pages["AI/topic"] = domain.OperatorPage{ID: "AI/topic", Title: "Topic", Markdown: "# Topic\n\nBody"}
		r, doc := fragmentDocument(t, screenHandler(t, f), pageFragment+"?page_id=AI%2Ftopic&parent_id=AI%2Findex")
		if r.Code != 200 || !reflect.DeepEqual(breadcrumbTexts(doc), []string{renderTestRootTitle, "AI", "Topic"}) {
			t.Fatalf("status=%d crumbs=%v", r.Code, breadcrumbTexts(doc))
		}
		if f.catalogReads != 0 {
			t.Fatalf("strict catalog calls=%d", f.catalogReads)
		}
	})
}

func TestKnowledgeContextFollowsOnlyExistingGroupsWithoutSkippingPages(t *testing.T) {
	f := canonicalKnowledgeFixture()
	f.pages = map[domain.PageID]domain.OperatorPage{
		rootCatalogID:        {ID: rootCatalogID, Title: wikiTestHomeTitle, Markdown: "# Home"},
		wikiTestCustomPageID: {ID: wikiTestCustomPageID, Title: screenArticleTitle, Markdown: "# Article\n\nBody."},
	}
	target := pageFragment + "?" + url.Values{pageIDParameter: {wikiTestCustomPageID}, parentIDParameter: {rootCatalogID}}.Encode()
	r, doc := fragmentDocument(t, screenHandler(t, f), target)
	if r.Code != 200 || !reflect.DeepEqual(breadcrumbTexts(doc), []string{wikiTestHomeTitle, screenArticleTitle}) {
		t.Fatalf("group route status=%d crumbs=%v", r.Code, breadcrumbTexts(doc))
	}
	// A real intervening page is a different visited context and cannot be
	// silently crossed as though it were another indexless group.
	f.pages["研究/index"] = domain.OperatorPage{ID: "研究/index", Title: "Research", Markdown: "# Research"}
	q := url.Values{pageIDParameter: {wikiTestCustomPageID}, trailParameter: {navigationTrailJSON(t, rootCatalogID, "研究/index")}}
	r, doc = fragmentDocument(t, screenHandler(t, f), pageFragment+"?"+q.Encode())
	if r.Code != 200 || !reflect.DeepEqual(breadcrumbTexts(doc), []string{wikiTestHomeTitle, "Research", screenArticleTitle}) {
		t.Fatalf("visited page status=%d crumbs=%v", r.Code, breadcrumbTexts(doc))
	}
	budget := &navigationBudget{pages: map[domain.PageID]domain.OperatorPage{}}
	ok, err := screenHandler(t, f).navigationEdge(t.Context(), f.pages[rootCatalogID], wikiTestCustomPageID, budget)
	if err != nil || ok {
		t.Fatalf("skipped actual page edge=%v, %v", ok, err)
	}
}

func navigationTrailJSON(t *testing.T, ids ...domain.PageID) string {
	t.Helper()
	encoded, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestKnowledgeInvalidTrailKeepsArticleAndNoFakeAncestors(t *testing.T) {
	limits := app.DefaultCatalogLimits()
	cases := []struct {
		name, raw string
		validJSON bool
	}{
		{"empty", `[]`, true}, {"malformed", `not-json`, false},
		{"invalid path", navigationTrailJSON(t, "../invalid"), true},
		{"self", navigationTrailJSON(t, navigationAutoID), true},
		{"repeated", navigationTrailJSON(t, navigationParentID, navigationParentID), true},
		{"missing parent", navigationTrailJSON(t, "catalogs/missing/index"), true},
		{"unrelated", navigationTrailJSON(t, knowledgeOtherCatalog, navigationParentID), true},
		{"oversized", navigationTrailJSON(t, domain.PageID(strings.Repeat("x", limits.MaxDepth*(limits.MaxPathBytes+4)+3))), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.validJSON && !json.Valid([]byte(tc.raw)) {
				t.Fatal("semantic trail fixture is not valid JSON")
			}
			f := navigationFixture()
			target := pageFragment + "?" + url.Values{pageIDParameter: {navigationAutoID}, trailParameter: {tc.raw}}.Encode()
			r, doc := fragmentDocument(t, screenHandler(t, f), target)
			if r.Code != 200 || !reflect.DeepEqual(breadcrumbTexts(doc), []string{renderTestRootTitle, navigationAutoTitle}) {
				t.Fatalf("status=%d crumbs=%v", r.Code, breadcrumbTexts(doc))
			}
			assertNavigationArticle(t, doc)
		})
	}
}

func assertNavigationArticle(t *testing.T, doc *html.Node) {
	t.Helper()
	found := false
	body := ""
	walk(doc, func(n *html.Node) {
		if hasAttribute(n, "id", "article") && attribute(n, "data-page-id") == navigationAutoID {
			found = true
		}
		if hasAttribute(n, "class", "document") {
			body = compactNodeText(n)
		}
	})
	if !found || body != "Actual article body." {
		t.Fatalf("selected article=%v body=%q", found, body)
	}
}

func TestKnowledgeSharedContextBudgetsKeepArticle(t *testing.T) {
	for _, mode := range []string{"bytes", "edges"} {
		t.Run(mode, func(t *testing.T) {
			f := navigationFixture()
			var ids []domain.PageID
			const parents = 14
			for i := 0; i < parents; i++ {
				ids = append(ids, domain.PageID(fmt.Sprintf("catalogs/context-%02d/index", i)))
			}
			padding := strings.Repeat("x", 200<<10)
			if mode == "edges" {
				padding = strings.Repeat("[x](https://e.test) ", 2000)
			}
			for i, id := range ids {
				next := "../../concepts/auto-mode.md"
				if i+1 < len(ids) {
					next = fmt.Sprintf("../context-%02d/index.md", i+1)
				}
				f.pages[id] = domain.OperatorPage{ID: id, Title: "Verified context", Markdown: "# Verified context\n\n" + padding + "\n\n[Next](" + next + ")\n"}
			}
			u, err := url.Parse(navigationPageURL(navigationAutoID, ids))
			if err != nil {
				t.Fatal(err)
			}
			r, doc := fragmentDocument(t, screenHandler(t, f), pageFragment+"?"+u.RawQuery)
			if r.Code != 200 {
				t.Fatalf("status=%d", r.Code)
			}
			assertNavigationArticle(t, doc)
			got := breadcrumbTexts(doc)
			if !reflect.DeepEqual(got, []string{navigationAutoTitle}) && !reflect.DeepEqual(got, []string{renderTestRootTitle, navigationAutoTitle}) {
				t.Fatalf("exhausted context invented crumbs=%v", got)
			}
			if len(f.pageIDs) < 3 {
				t.Fatalf("aggregate %s case did not read multiple individually valid parents: %v", mode, f.pageIDs)
			}
			if len(f.pageIDs) >= parents+1 {
				t.Fatalf("read all %d parents despite exhausted %s budget: %v", parents, mode, f.pageIDs)
			}
		})
	}
}

type navigationFailureReader struct {
	*screenReader
	failure error
	cancel  context.CancelFunc
}

func (f *navigationFailureReader) Page(ctx context.Context, scope domain.ScopeRef, id domain.PageID, limits domain.ReadLimits) (domain.OperatorPage, error) {
	if id == navigationParentID {
		if f.cancel != nil {
			f.cancel()
		}
		return domain.OperatorPage{}, f.failure
	}
	return f.screenReader.Page(ctx, scope, id, limits)
}

func TestKnowledgeFailedOptionalContextKeepsReadArticle(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(fmt.Sprintf("%T-%v", failure, failure), func(t *testing.T) {
			f := navigationFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader := &navigationFailureReader{screenReader: f, failure: failure}
			if failure == context.Canceled {
				reader.cancel = cancel
			}
			service, err := app.NewOperatorService("trusted", app.OperatorReaders{Page: reader, Directories: f}, app.OperatorOptions{})
			if err != nil {
				t.Fatal(err)
			}
			h, err := New(Dependencies{Operator: service})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, pageFragment+"?page_id=concepts%2Fauto-mode&parent_id=catalogs%2Fbalda%2Fconversations%2Findex", nil).WithContext(ctx)
			response := httptest.NewRecorder()
			h.Fragments(response, request)
			doc, err := html.Parse(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 {
				t.Fatalf("status=%d", response.Code)
			}
			assertNavigationArticle(t, doc)
			for _, crumb := range breadcrumbTexts(doc) {
				if crumb == navigationConversationsTitle {
					t.Fatal("failed context became clickable ancestor")
				}
			}
		})
	}
}

func TestKnowledgeMismatchedParentAndTrailDoesNotReadSuppliedParents(t *testing.T) {
	f := navigationFixture()
	q := url.Values{pageIDParameter: {navigationAutoID}, parentIDParameter: {knowledgeOtherCatalog}, trailParameter: {navigationTrailJSON(t, navigationParentID)}}
	if !json.Valid([]byte(q.Get(trailParameter))) {
		t.Fatal("parent mismatch fixture is not valid JSON")
	}
	response, doc := fragmentDocument(t, screenHandler(t, f), pageFragment+"?"+q.Encode())
	if response.Code != 200 || !reflect.DeepEqual(breadcrumbTexts(doc), []string{renderTestRootTitle, navigationAutoTitle}) {
		t.Fatalf("status=%d crumbs=%v", response.Code, breadcrumbTexts(doc))
	}
	if !reflect.DeepEqual(f.pageIDs, []domain.PageID{navigationAutoID, rootCatalogID}) {
		t.Fatalf("invalid context reached parent reads=%v", f.pageIDs)
	}
	assertNavigationArticle(t, doc)
}
