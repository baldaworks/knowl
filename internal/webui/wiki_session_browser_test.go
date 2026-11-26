//go:build browser

package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	wikiSessionToken   = "browser-secret-token"
	wikiSessionPrismID = "AI/Prism"
)

type wikiSessionReader struct {
	pages    map[domain.PageID]domain.OperatorPage
	branches map[string][]domain.OperatorWikiPage
}

func (f *wikiSessionReader) Page(_ context.Context, _ domain.ScopeRef, id domain.PageID, _ domain.ReadLimits) (domain.OperatorPage, error) {
	page, exists := f.pages[id]
	if !exists {
		return domain.OperatorPage{}, app.ErrPageNotFound
	}
	return page, nil
}

func (*wikiSessionReader) WikiDirectoryChildren(context.Context, domain.ScopeRef, string, app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorWikiEntry], error) {
	return app.OperatorReadPage[domain.OperatorWikiEntry]{SnapshotVersion: screenSnapshot}, nil
}

func (f *wikiSessionReader) WikiPageChildren(_ context.Context, _ domain.ScopeRef, directory string, options app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorWikiPage], error) {
	items, exists := f.branches[directory]
	if !exists {
		return app.OperatorReadPage[domain.OperatorWikiPage]{}, app.ErrOperatorDirectoryNotFound
	}
	if options.Continuation.SnapshotVersion != "" && options.Continuation.SnapshotVersion != screenSnapshot {
		return app.OperatorReadPage[domain.OperatorWikiPage]{}, app.ErrOperatorSnapshotChanged
	}
	start := 0
	for start < len(items) && items[start].Path <= options.Continuation.Key {
		start++
	}
	end := min(start+options.Limit, len(items))
	result := app.OperatorReadPage[domain.OperatorWikiPage]{Items: items[start:end], SnapshotVersion: screenSnapshot}
	if end < len(items) {
		result.NextKey = items[end-1].Path
	}
	return result, nil
}

func TestBrowserWikiSession(t *testing.T) {
	fixture := &wikiSessionReader{pages: map[domain.PageID]domain.OperatorPage{}, branches: map[string][]domain.OperatorWikiPage{"": {}, "AI": {}, "ADRs": {}}}
	add := func(id domain.PageID, title, body, children string) {
		fixture.pages[id] = domain.OperatorPage{ID: id, Title: title, Markdown: body, Digest: screenSnapshot, Version: screenSnapshot}
		if id == rootCatalogID {
			return
		}
		directory := ""
		if strings.HasPrefix(string(id), "AI/") && id != "AI/index" {
			directory = "AI"
		}
		fixture.branches[directory] = append(fixture.branches[directory], domain.OperatorWikiPage{PageID: id, Path: string(id) + ".md", Title: title, ChildrenDirectory: children})
	}
	fixture.branches[""] = append(fixture.branches[""], domain.OperatorWikiPage{Path: "A-empty", Title: "A-empty"})
	add(rootCatalogID, "Home", "# Home\n\nPublished root.\n", " ")
	add("ADRs/index", "ADRs", "", "ADRs")
	add("AI/index", "AI", "Optional AI information.\n", "AI")
	add("AI/OpenSpec", "OpenSpec", "# OpenSpec\n\n## Open section\n\nOther page.\n", "")
	add(wikiSessionPrismID, strings.TrimPrefix(wikiSessionPrismID, "AI/"), "# Prism\n\n## Prism section\n\nPublished content.\n", "")
	for i := range 10 {
		directory := fmt.Sprintf("B%02d", i)
		fixture.branches[directory] = []domain.OperatorWikiPage{}
		add(domain.PageID(directory+"/index"), directory, "", directory)
	}
	for i := range 58 {
		id := domain.PageID(fmt.Sprintf("AI/topic-%03d", i))
		add(id, string(id), "# "+string(id)+"\n", "")
	}
	for i := range 1050 {
		id := domain.PageID(fmt.Sprintf("topic-%04d", i))
		add(id, string(id), "# "+string(id)+"\n", "")
	}
	operator, err := app.NewOperatorService("trusted", app.OperatorReaders{Page: fixture, Directories: fixture}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ui, err := New(Dependencies{Operator: operator})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ui/fragments/") {
			if r.Header.Get("Authorization") != "Bearer "+wikiSessionToken {
				ui.Error(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			ui.Fragments(w, r)
			return
		}
		ui.ServeHTTP(w, r)
	}))
	defer host.Close()
	script, err := filepath.Abs("../../tools/webui-browser/tree_session.cjs")
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
}
