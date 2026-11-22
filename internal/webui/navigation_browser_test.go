//go:build browser

package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestBrowserNavigationContext(t *testing.T) {
	f := navigationFixture()
	root := f.pages[rootCatalogID]
	root.Markdown = "# Root\n\n[Balda](catalogs/balda/index.md)\n\n[Other context](catalogs/other/index.md)\n"
	f.pages[root.ID] = root
	f.pages["catalogs/balda/index"] = domain.OperatorPage{ID: "catalogs/balda/index", Title: navigationBaldaTitle, Markdown: "# Balda\n\n[Conversations](conversations/index.md)\n"}
	ui := screenHandler(t, f)
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
	script, err := filepath.Abs("../../tools/webui-browser/navigation.cjs")
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
