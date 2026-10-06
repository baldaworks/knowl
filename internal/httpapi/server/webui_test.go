package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"golang.org/x/net/html"
)

const (
	uiTestKnowledgeRoute   = "/ui/fragments/knowledge"
	uiTestToken            = "test-token"
	uiTestCapabilityCode   = "capability_unavailable"
	uiTestUnauthorizedCode = "unauthorized"
)

func TestUIBoundaryErrorsAreTrustedFragments(t *testing.T) {
	h, err := NewWebHandler(nil, Dependencies{Ready: func() bool { return true }}, uiTestToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, token, code string
		status            int
	}{
		{uiTestKnowledgeRoute, "", uiTestUnauthorizedCode, 401},
		{"/ui/fragments/knowledge?scope=", uiTestToken, "scope_override_forbidden", 403},
		{uiTestKnowledgeRoute, uiTestToken, uiTestCapabilityCode, 503},
		{"/ui/fragments/unknown", uiTestToken, "not_found", 404},
	} {
		r := httptest.NewRequest(http.MethodGet, test.path, nil)
		if test.token != "" {
			r.Header.Set("Authorization", "Bearer "+test.token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status || w.Header().Get("X-Knowl-Error") != test.code || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("boundary status/header %d %v", w.Code, w.Header())
		}
		doc, parseErr := html.Parse(w.Body)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		var visit func(*html.Node)
		found := false
		visit = func(n *html.Node) {
			for _, attr := range n.Attr {
				if attr.Key == "data-error-code" && attr.Val == test.code {
					found = true
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				visit(c)
			}
		}
		visit(doc)
		if !found {
			t.Fatal("missing parsed error code")
		}
	}
}
func TestUIPublicProjectionRemovesUnsafeFields(t *testing.T) {
	source := domain.SourceDocument{SourceID: "docs", DocumentID: "guide.md", Revision: "v1", URI: "https://user:secret@example.com/doc?key=secret#private"}
	input := app.QueryResult{Query: "same query", Citations: []app.Citation{{Kind: "wiki", Reference: "page", Path: "/private/workspace/wiki.md"}}, Pages: []domain.PageReference{{ID: "page", Title: "Title", SourceDocument: &source, SourceDocuments: []domain.SourceDocument{source}}}}
	result := safeUIRetrieve(input)
	if result.Query != input.Query || len(result.Evidence) != 1 || *result.Evidence[0].Uri != "https://example.com/doc" || (*result.Evidence[0].SourceDocuments)[0].Uri != "https://example.com/doc" {
		t.Fatalf("unsafe result %#v", result)
	}
	if result.Citations != nil {
		for _, citation := range *result.Citations {
			if citation.Path != nil {
				t.Fatal("filesystem path disclosed")
			}
		}
	}
	if !strings.HasPrefix(source.URI, "https://user:secret") {
		t.Fatal("input mutated")
	}
	operation := safeUIOperation(domain.Operation{ID: "op", Status: domain.StatusFailed, Failure: &domain.Failure{Class: "provider", Reason: "/secret/provider"}})
	if operation.Failure != nil {
		t.Fatal("unfiltered failure disclosed")
	}
}
