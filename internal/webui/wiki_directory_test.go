package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"golang.org/x/net/html"
)

type wikiDirectoryFixture struct{ unsupported bool }

const wikiDirectoryNestedName = "nested"

func (f wikiDirectoryFixture) WikiDirectoryChildren(_ context.Context, _ domain.ScopeRef, directory string, o app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorWikiEntry], error) {
	if directory != "entities" {
		return app.OperatorReadPage[domain.OperatorWikiEntry]{}, app.ErrOperatorDirectoryNotFound
	}
	if f.unsupported {
		return app.OperatorReadPage[domain.OperatorWikiEntry]{Items: []domain.OperatorWikiEntry{{Path: "entities/%2e%2e.md", Name: "%2e%2e.md", Kind: "unsupported"}}, SnapshotVersion: screenSnapshot}, nil
	}
	items := []domain.OperatorWikiEntry{{Path: "entities/" + wikiDirectoryNestedName, Name: wikiDirectoryNestedName, Kind: "folder"}, {Path: "entities/index.md", Name: "index.md", Kind: "page", PageID: "entities/index"}}
	if o.Continuation.Key != "" {
		items = items[1:]
		return app.OperatorReadPage[domain.OperatorWikiEntry]{Items: items, SnapshotVersion: screenSnapshot}, nil
	}
	return app.OperatorReadPage[domain.OperatorWikiEntry]{Items: items[:1], NextKey: "entities/" + wikiDirectoryNestedName, SnapshotVersion: screenSnapshot}, nil
}

func TestWikiDirectoryUnsupportedPathIsNotLinked(t *testing.T) {
	service, err := app.NewOperatorService("trusted", app.OperatorReaders{Directories: wikiDirectoryFixture{unsupported: true}}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Dependencies{Operator: service})
	if err != nil {
		t.Fatal(err)
	}
	r, doc := fragmentDocument(t, h, wikiDirectoryFragment+"?directory=entities")
	if r.Code != http.StatusOK {
		t.Fatalf("status = %d", r.Code)
	}
	found := false
	walk(doc, func(node *html.Node) {
		if node.Type != html.ElementNode || node.Data != "li" {
			return
		}
		for _, attribute := range node.Attr {
			if attribute.Key == "data-wiki-kind" && attribute.Val == "unsupported" {
				found = nodeText(node) == "%2e%2e.md (unsupported path)"
			}
			if attribute.Key == "data-page-id" {
				t.Fatal("unsupported path exposed as page")
			}
		}
	})
	if !found {
		t.Fatal("unsupported entry not explicitly identified")
	}
}

func TestWikiDirectoryFragment(t *testing.T) {
	service, err := app.NewOperatorService("trusted", app.OperatorReaders{Directories: wikiDirectoryFixture{}}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Dependencies{Operator: service})
	if err != nil {
		t.Fatal(err)
	}
	r, doc := fragmentDocument(t, h, "/ui/fragments/wiki-directory?directory=entities&limit=1")
	if r.Code != http.StatusOK {
		t.Fatalf("status = %d", r.Code)
	}
	paths := markedText(doc, "data-wiki-path")
	if paths["entities/"+wikiDirectoryNestedName] != wikiDirectoryNestedName || len(paths) != 1 {
		t.Fatalf("paths = %#v", paths)
	}
	var next string
	walk(doc, func(n *html.Node) {
		for _, a := range n.Attr {
			if a.Key == "data-next" {
				for _, b := range n.Attr {
					if b.Key == "href" {
						next = b.Val
					}
				}
			}
		}
	})
	if next == "" {
		t.Fatal("missing bounded continuation")
	}
	r, doc = fragmentDocument(t, h, next)
	paths = markedText(doc, "data-wiki-path")
	if r.Code != http.StatusOK || paths["entities/index.md"] != "index.md" || len(paths) != 1 {
		t.Fatalf("second = %d %#v", r.Code, paths)
	}
	for _, uri := range []string{"/ui/fragments/wiki-directory?directory=../raw", "/ui/fragments/wiki-directory?directory=entities&scope=other"} {
		r = httptest.NewRecorder()
		h.Fragments(r, httptest.NewRequest(http.MethodGet, uri, nil))
		if r.Code != http.StatusBadRequest {
			t.Errorf("%q status = %d", uri, r.Code)
		}
	}
}
