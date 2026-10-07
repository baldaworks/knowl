package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func TestSearchExplicitSubmissionSharesResult(t *testing.T) {
	calls := 0
	want := knowlapi.RetrieveResult{Query: "source lifecycle", Evidence: []knowlapi.EvidenceItem{{PageId: "concepts/second", Title: "Second", Snippet: "Exact second snippet"}, {PageId: "concepts/first", Title: "First", Snippet: "Exact first snippet"}}}
	h, err := New(Dependencies{Retrieve: func(_ context.Context, query string, sources []string) (knowlapi.RetrieveResult, error) {
		calls++
		if query != want.Query || !reflect.DeepEqual(sources, []string{activitySourceID}) {
			t.Errorf("input %q %v", query, sources)
		}
		return want, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	mount := httptest.NewRecorder()
	h.Fragments(mount, httptest.NewRequest(http.MethodGet, searchFragment, nil))
	if mount.Code != 200 || calls != 0 {
		t.Fatalf("mount status=%d calls=%d", mount.Code, calls)
	}
	result := httptest.NewRecorder()
	h.Fragments(result, httptest.NewRequest(http.MethodGet, "/ui/fragments/search?query=source+lifecycle&source=docs", nil))
	if result.Code != 200 || calls != 1 {
		t.Fatalf("submit status=%d calls=%d", result.Code, calls)
	}
	doc, err := html.Parse(result.Body)
	if err != nil {
		t.Fatal(err)
	}
	var got knowlapi.RetrieveResult
	var snippets []string
	walk(doc, func(n *html.Node) {
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == "response-json-data" {
				if e := json.Unmarshal([]byte(nodeText(n)), &got); e != nil {
					t.Error(e)
				}
			}
			if a.Key == "class" && a.Val == "evidence-snippet" {
				snippets = append(snippets, nodeText(n))
			}
		}
	})
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(snippets, []string{want.Evidence[0].Snippet, want.Evidence[1].Snippet}) {
		t.Fatalf("different projection: %+v snippets=%v", got, snippets)
	}
}

type searchInventoryReader struct{ calls int }

func (f *searchInventoryReader) ListSources(context.Context, domain.ScopeRef, app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorSourceSummary], error) {
	f.calls++
	return app.OperatorReadPage[domain.OperatorSourceSummary]{Items: []domain.OperatorSourceSummary{{ID: activitySourceID}}}, nil
}
func (*searchInventoryReader) Source(context.Context, domain.ScopeRef, domain.SourceID) (domain.OperatorSourceSummary, error) {
	return domain.OperatorSourceSummary{}, app.ErrSourceNotFound
}

func TestSearchMountHasOnlyBlankLabeledQueryAndDoesNotReadSources(t *testing.T) {
	f := &searchInventoryReader{}
	service, err := app.NewOperatorService("trusted", app.OperatorReaders{Sources: f}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Dependencies{Operator: service})
	if err != nil {
		t.Fatal(err)
	}
	r, doc := fragmentDocument(t, h, searchFragment)
	if r.Code != http.StatusOK {
		t.Fatalf("status=%d", r.Code)
	}
	if f.calls != 0 {
		t.Errorf("mount read source inventory %d times", f.calls)
	}
	var fields []string
	var labeled, query bool
	walk(doc, func(n *html.Node) {
		if n.DataAtom == atom.Label && hasAttribute(n, "for", "query") && nodeText(n) != "" {
			labeled = true
		}
		if n.DataAtom == atom.Input || n.DataAtom == atom.Select {
			for _, a := range n.Attr {
				if a.Key == "name" {
					fields = append(fields, a.Val)
				}
				if a.Key == "placeholder" && a.Val != "" {
					t.Errorf("example placeholder=%q", a.Val)
				}
				if a.Key == "value" && a.Val != "" {
					t.Errorf("prefilled input=%q", a.Val)
				}
			}
		}
		if n.DataAtom == atom.Input && hasAttribute(n, "id", "query") && hasAttribute(n, "type", "search") {
			query = true
		}
	})
	if !query || !labeled || !reflect.DeepEqual(fields, []string{"query"}) {
		t.Errorf("query=%v labeled=%v fields=%v", query, labeled, fields)
	}
}

func TestKnowledgeLeafDetailsAndSourcesStartClosed(t *testing.T) {
	f := canonicalKnowledgeFixture()
	page := f.pages[screenArticleID]
	page.Metadata = &domain.OperatorPageMetadata{Type: "topic", Description: "Useful metadata", Tags: []string{"one", "two"}, Status: "stable", TrustTier: "verified", Stale: true}
	f.pages[screenArticleID] = page
	r, doc := fragmentDocument(t, screenHandler(t, f), pageFragment+"?page_id="+screenArticleID)
	if r.Code != http.StatusOK {
		t.Fatalf("status=%d", r.Code)
	}
	var details *html.Node
	var toggle, hidden, panel bool
	walk(doc, func(n *html.Node) {
		if hasAttribute(n, "class", "page-details") {
			details = n
		}
		if hasAttribute(n, "aria-controls", "page-sources") {
			toggle = true
			if !hasAttribute(n, "aria-expanded", "false") {
				t.Error("sources opener starts expanded")
			}
		}
		if hasAttribute(n, "id", "page-sources") {
			panel = true
		}
		for _, a := range n.Attr {
			if a.Key == "class" && n.DataAtom == atom.Div && strings.Contains(" "+a.Val+" ", " sources-hidden ") {
				hidden = true
			}
		}
	})
	if !toggle || !panel || !hidden || details == nil {
		t.Errorf("toggle=%v panel=%v hidden=%v details=%v", toggle, panel, hidden, details != nil)
	}
	if details == nil {
		return
	}
	if hasAttribute(details, "open", "") {
		t.Error("metadata details starts open")
	}
	var labels, values []string
	walk(details, func(n *html.Node) {
		if n.DataAtom == atom.Dt {
			labels = append(labels, nodeText(n))
		}
		if n.DataAtom == atom.Dd {
			values = append(values, compactNodeText(n))
		}
	})
	wantLabels := []string{"Page ID", "Digest", "Version", "Type", "Description", "Tags", "Status", "Trust tier", "Stale"}
	wantValues := []string{screenArticleID, screenSnapshot, screenSnapshot, "topic", "Useful metadata", "one two", "stable", "verified", "true"}
	if !reflect.DeepEqual(labels, wantLabels) || !reflect.DeepEqual(values, wantValues) {
		t.Errorf("details labels=%v values=%v", labels, values)
	}
}
func nodeText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var s string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		s += nodeText(c)
	}
	return s
}

func TestSearchFailedDiagnosticsArePreserved(t *testing.T) {
	reason := knowlapi.RetrievalStatusReason("unavailable")
	h, err := New(Dependencies{Retrieve: func(context.Context, string, []string) (knowlapi.RetrieveResult, error) {
		return knowlapi.RetrieveResult{Evidence: []knowlapi.EvidenceItem{}, Retrieval: &knowlapi.RetrievalStatus{Effective: retrievalFailed, Reason: &reason}}, &app.EmbeddingError{Code: domain.RetrievalUnavailable}
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRecorder()
	h.Fragments(r, httptest.NewRequest(http.MethodGet, "/ui/fragments/search?query=example", nil))
	if r.Code != 503 || r.Header().Get("X-Knowl-Error") != "retrieval_failed" {
		t.Fatalf("status=%d headers=%v", r.Code, r.Header())
	}
	doc, err := html.Parse(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var effective string
	walk(doc, func(n *html.Node) {
		for _, a := range n.Attr {
			if a.Key == "data-effective" {
				effective = a.Val
			}
		}
	})
	if effective != retrievalFailed {
		t.Fatalf("effective=%q", effective)
	}
}

const screenSnapshot = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const screenSourceRef = "git:docs/guide@accepted"
const screenRootTitle = "Topics"

type screenReader struct {
	invalidateContinuation      bool
	pageErr, catalogErr, rawErr error
	calls                       int
	snapshot, body              string
	empty                       bool
	catalogs                    map[domain.PageID]app.OperatorCatalogRead
	pages                       map[domain.PageID]domain.OperatorPage
	catalogErrors               map[domain.PageID]error
	pageIDs                     []domain.PageID
	catalogReads                int
	directories                 map[string][]domain.OperatorWikiEntry
}

func (f *screenReader) WikiDirectoryChildren(_ context.Context, _ domain.ScopeRef, directory string, o app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorWikiEntry], error) {
	f.calls++
	items, ok := f.directories[directory]
	if !ok {
		return app.OperatorReadPage[domain.OperatorWikiEntry]{}, app.ErrOperatorDirectoryNotFound
	}
	if o.Continuation.SnapshotVersion != "" && o.Continuation.SnapshotVersion != f.snapshot {
		return app.OperatorReadPage[domain.OperatorWikiEntry]{}, app.ErrOperatorSnapshotChanged
	}
	start := 0
	for start < len(items) && items[start].Path <= o.Continuation.Key {
		start++
	}
	end := min(start+o.Limit, len(items))
	result := app.OperatorReadPage[domain.OperatorWikiEntry]{Items: items[start:end], SnapshotVersion: f.snapshot}
	if end < len(items) {
		result.NextKey = items[end-1].Path
	}
	return result, nil
}

func (f *screenReader) CatalogChildren(_ context.Context, _ domain.ScopeRef, parent domain.PageID, o app.OperatorReadOptions) (app.OperatorCatalogRead, error) {
	f.calls++
	f.catalogReads++
	if f.catalogs != nil {
		if parent == "" {
			parent = renderTestRootID
		}
		if err := f.catalogErrors[parent]; err != nil {
			return app.OperatorCatalogRead{}, err
		}
		catalog, exists := f.catalogs[parent]
		if !exists {
			return app.OperatorCatalogRead{}, app.ErrOperatorCatalogNotFound
		}
		catalog.Children.SnapshotVersion = f.snapshot
		items := catalog.Children.Items
		start := 0
		for start < len(items) && string(items[start].ID) <= o.Continuation.Key {
			start++
		}
		end := min(start+o.Limit, len(items))
		catalog.Children.Items = items[start:end]
		if end < len(items) {
			catalog.Children.NextKey = string(items[end-1].ID)
		}
		return catalog, nil
	}
	items := []domain.OperatorCatalogChild{{ID: screenArticleID, Title: screenArticleTitle, Kind: pageKind}}
	if f.empty {
		items = nil
	}
	return app.OperatorCatalogRead{Parent: domain.OperatorCatalogSummary{ID: "index", Title: screenRootTitle}, Children: app.OperatorReadPage[domain.OperatorCatalogChild]{Items: items, SnapshotVersion: f.snapshot}}, f.catalogErr
}
func (f *screenReader) PageSummaries(_ context.Context, _ domain.ScopeRef, o app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorPageSummary], error) {
	f.calls++
	next := screenArticleID
	if o.Continuation.Key != "" {
		next = ""
	}
	snapshot := f.snapshot
	if f.invalidateContinuation && o.Continuation.Key != "" {
		snapshot = strings.Repeat("b", 64)
	}
	return app.OperatorReadPage[domain.OperatorPageSummary]{Items: []domain.OperatorPageSummary{{ID: screenArticleID, Title: screenArticleTitle}}, SnapshotVersion: snapshot, NextKey: next}, nil
}
func (f *screenReader) Page(_ context.Context, _ domain.ScopeRef, id domain.PageID, _ domain.ReadLimits) (domain.OperatorPage, error) {
	f.calls++
	f.pageIDs = append(f.pageIDs, id)
	if f.pages != nil {
		page, exists := f.pages[id]
		if !exists {
			return domain.OperatorPage{}, app.ErrPageNotFound
		}
		return page, f.pageErr
	}
	if id == renderTestRootID {
		return domain.OperatorPage{ID: id, Title: screenRootTitle, Markdown: "# Topics\n", Digest: screenSnapshot, Version: screenSnapshot}, f.pageErr
	}
	return domain.OperatorPage{ID: id, Title: screenArticleTitle, Markdown: f.body, Digest: screenSnapshot, Version: screenSnapshot, RelatedPageIDs: []domain.PageID{"concepts/other"}, Sources: []domain.OperatorPageSource{{SourceRef: screenSourceRef, Revision: "accepted", OriginalURI: "file:///private/source"}}}, f.pageErr
}
func (f *screenReader) SourceRevision(_ context.Context, _ domain.ScopeRef, ref string, _ domain.ReadLimits) (domain.OperatorSourceRevision, error) {
	f.calls++
	return domain.OperatorSourceRevision{SourceRef: ref, Text: "<script>immutable accepted text</script>", MediaType: "text/plain", Digest: screenSnapshot}, f.rawErr
}
func screenHandler(t *testing.T, f *screenReader) *Handler {
	t.Helper()
	service, err := app.NewOperatorService("trusted", app.OperatorReaders{Catalogs: f, Pages: f, Page: f, Revisions: f, Directories: f}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Dependencies{Operator: service, Retrieve: func(context.Context, string, []string) (knowlapi.RetrieveResult, error) {
		t.Error("browse invoked retrieval")
		return knowlapi.RetrieveResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func fragmentDocument(t *testing.T, h *Handler, path string) (*httptest.ResponseRecorder, *html.Node) {
	t.Helper()
	r := httptest.NewRecorder()
	h.Fragments(r, httptest.NewRequest(http.MethodGet, path, nil))
	doc, err := html.Parse(strings.NewReader(r.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	return r, doc
}
func TestKnowledgeCurrentPageAndImmutableRaw(t *testing.T) {
	f := &screenReader{snapshot: screenSnapshot, body: "# Article\n\nCurrent body"}
	h := screenHandler(t, f)
	r, doc := fragmentDocument(t, h, pageFragment+"?"+url.Values{pageIDParameter: {screenArticleID}}.Encode())
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	var rawURL string
	var article, source int
	walk(doc, func(n *html.Node) {
		if n.Data == "h2" && nodeText(n) == screenArticleTitle {
			article++
		}
		for _, a := range n.Attr {
			if a.Key == hxGetAttribute && strings.HasPrefix(a.Val, "/ui/fragments/source-revision?") {
				rawURL = a.Val
				source++
			}
			if a.Key == linkHrefAttribute && strings.HasPrefix(a.Val, "file:") {
				t.Error("private URI exposed")
			}
		}
	})
	if article != 1 || source != 1 {
		t.Fatalf("article=%d sources=%d", article, source)
	}
	f.body = "# Changed page\n\nNew body"
	r, doc = fragmentDocument(t, h, rawURL)
	if r.Code != 200 {
		t.Fatalf("raw status=%d", r.Code)
	}
	var text string
	walk(doc, func(n *html.Node) {
		if n.Data == "pre" {
			text = nodeText(n)
		}
		if n.Data == "script" {
			t.Error("raw became active HTML")
		}
	})
	if text != "<script>immutable accepted text</script>" {
		t.Fatalf("raw=%q", text)
	}
}
func TestKnowledgeTypedFailuresAndInputBounds(t *testing.T) {
	for _, tc := range []struct {
		route  string
		err    error
		status int
		code   string
	}{
		{screenArticleRoute, app.ErrPageNotFound, 404, errorPageNotFound},
		{"knowledge?parent_id=catalogs/missing/index", app.ErrOperatorCatalogNotFound, 404, "catalog_not_found"},
		{"source-revision?source_ref=git:docs/guide@accepted", app.ErrOperatorSourceRevisionNotFound, 404, errorSourceRevisionNotFound},
		{"source-revision?source_ref=git:docs/guide@accepted", app.ErrOperatorUnsupportedFormat, 415, errorUnsupportedFormat},
		{screenArticleRoute, app.ErrOperatorReadLimitExceeded, 413, errorReadLimitExceeded},
		{screenArticleRoute, app.ErrOperatorCapabilityUnavailable, 503, errorCapabilityUnavailable},
		{"knowledge?limit=0", nil, 400, errorLimitInvalid}, {"knowledge?cursor=", nil, 400, errorCursorInvalid},
		{"knowledge?cursor=forged", nil, 400, errorCursorInvalid}, {"page?page_id=../escape", nil, 400, errorInvalidRequest},
		{"page?page_id=concepts/article&page_id=other", nil, 400, errorInvalidRequest}, {"search?source=docs", nil, 400, errorInvalidRequest},
	} {
		t.Run(tc.route+tc.code, func(t *testing.T) {
			f := &screenReader{snapshot: screenSnapshot, pageErr: tc.err, rawErr: tc.err, catalogErr: tc.err}
			h := screenHandler(t, f)
			r, doc := fragmentDocument(t, h, "/ui/fragments/"+tc.route)
			if r.Code != tc.status || r.Header().Get("X-Knowl-Error") != tc.code {
				t.Fatalf("status=%d header=%v", r.Code, r.Header())
			}
			found := false
			walk(doc, func(n *html.Node) {
				for _, a := range n.Attr {
					if a.Key == "data-error-code" && a.Val == tc.code {
						found = true
					}
				}
			})
			if !found {
				t.Fatal("missing typed failure")
			}
			if tc.err == nil && f.calls != 0 {
				t.Fatalf("invalid input reached reader %d", f.calls)
			}
		})
	}
}
func TestKnowledgeSnapshotContinuation(t *testing.T) {
	f := &screenReader{snapshot: screenSnapshot, directories: map[string][]domain.OperatorWikiEntry{"": {
		{Path: "a.md", Name: "a.md", Kind: pageKind, PageID: "a"},
		{Path: "b.md", Name: "b.md", Kind: pageKind, PageID: "b"},
	}}}
	h := screenHandler(t, f)
	r, doc := fragmentDocument(t, h, "/ui/fragments/knowledge?view=all&limit=1")
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	var next string
	walk(doc, func(n *html.Node) {
		if n.DataAtom == atom.Button && nodeText(n) == "More files" {
			for _, a := range n.Attr {
				if a.Key == "data-directory-url" {
					next = a.Val
				}
			}
		}
	})
	if next == "" {
		t.Fatal("missing continuation")
	}
	f.snapshot = strings.Repeat("b", 64)
	r, _ = fragmentDocument(t, h, next)
	if r.Code != 409 || r.Header().Get("X-Knowl-Error") != errorSnapshotChanged {
		t.Fatalf("stale status=%d header=%v", r.Code, r.Header())
	}
}
func TestKnowledgeEmptyAndUncataloguedRemainDistinct(t *testing.T) {
	for _, tc := range []struct {
		err     error
		heading string
	}{{nil, screenRootTitle}, {app.ErrOperatorCatalogNotFound, "No catalog available"}} {
		f := &screenReader{snapshot: screenSnapshot, empty: true, catalogErr: tc.err}
		r, doc := fragmentDocument(t, screenHandler(t, f), knowledgeFragment)
		if r.Code != 200 {
			t.Fatalf("status=%d", r.Code)
		}
		found := false
		walk(doc, func(n *html.Node) {
			if (n.DataAtom == atom.H1 || n.DataAtom == atom.H2) && nodeText(n) == tc.heading {
				found = true
			}
		})
		if !found {
			t.Errorf("missing heading %q", tc.heading)
		}
	}
}

const screenArticleTitle = "Article"
const screenArticleRoute = "page?page_id=concepts/article"

const screenArticleID = "concepts/article"
