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
	"testing"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
)

func TestBrowserSharedTheme(t *testing.T) {
	pages := canonicalKnowledgeFixture()
	activity := &activityReader{}
	service, err := app.NewOperatorService("trusted", app.OperatorReaders{Catalogs: pages, Pages: pages, Page: pages, Revisions: pages, Directories: pages, Operations: activity, Sources: activity, Documents: activity, Statistics: statisticsFixture{value: webStatisticsFixture()}}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Dependencies{Operator: service, Retrieve: func(_ context.Context, query string, _ []string) (knowlapi.RetrieveResult, error) {
		return knowlapi.RetrieveResult{Query: query, Evidence: []knowlapi.EvidenceItem{{PageId: screenArticleID, Title: "Published article", Snippet: "Saved knowledge matching the query."}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ui/fragments/") {
			if r.Header.Get("Authorization") != "Bearer browser-secret-token" {
				handler.Error(w, 401, "unauthorized")
				return
			}
			handler.Fragments(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer host.Close()
	script, err := filepath.Abs("../../tools/webui-browser/shared_theme.cjs")
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
