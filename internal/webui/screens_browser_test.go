//go:build browser

package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestBrowserKnowledgeSearch(t *testing.T) {
	fixture := &screenReader{snapshot: screenSnapshot, invalidateContinuation: true, body: "---\ntype: topic\ntitle: Article\n---\n# Article\n\nCurrent published body. [[concepts/other]]" + strings.Repeat("\n\nPublished paragraph for responsive reading and saved-source focus restoration.", 24)}
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

// Browser-only long provenance exercises expanded metadata and two real revisions.
type responsiveScreenReader struct{ *screenReader }

func (f *responsiveScreenReader) Page(ctx context.Context, scope domain.ScopeRef, id domain.PageID, limits domain.ReadLimits) (domain.OperatorPage, error) {
	page, err := f.screenReader.Page(ctx, scope, id, limits)
	page.Sources = append(page.Sources, domain.OperatorPageSource{SourceRef: "git:docs/" + strings.Repeat("long-reference", 30) + "@second", Revision: strings.Repeat("r", 100)})
	return page, err
}
