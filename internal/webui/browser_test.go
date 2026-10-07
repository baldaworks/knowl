//go:build browser

package webui_test

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/internal/httpapi/server"
	"github.com/baldaworks/knowl/internal/webui"
)

// This test-only server uses the production shell, renderer and auth/read boundary.
// Browser tooling never enters the binary or the normal Go test/build dependency path.
func TestBrowserSecurity(t *testing.T) { runSecurityBrowser(t, "security.cjs") }

func TestBrowserRepairs(t *testing.T) { runSecurityBrowser(t, "repairs.cjs") }

func runSecurityBrowser(t *testing.T, scriptName string) {
	t.Helper()
	ui, err := webui.New(webui.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	markdown, err := webui.RenderMarkdown("# Protected document\n\n**Safe formatting** [external](https://user:secret@evil.invalid/doc?secret=value#fragment)\n\n![external](https://evil.invalid/pixel)\n\n<script>window.pwned=true</script><svg onload='window.pwned=true'></svg><div hx-get='https://evil.invalid/hx' hx-trigger='load' data-hx-get='/v1/retrieve' onclick='window.pwned=true'>attack</div>\n\n[bad](javascript:alert%281%29)")
	if err != nil {
		t.Fatal(err)
	}
	view := template.Must(template.New("fixture").Parse(`<article id="protected-document" hx-disable><div class="document">{{.Markdown}}</div><pre id="raw">{{.Raw}}</pre><pre id="json">{{.JSON}}</pre></article>`))
	fragments := server.WithOperatorAuth(server.WithOperatorReadBoundary(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", webui.CSP)
		if err := view.Execute(w, struct {
			Markdown  template.HTML
			Raw, JSON string
		}{markdown, `<img src="https://evil.invalid/raw" onerror="window.pwned=true">`, `{"content":"<script>window.pwned=true</script>"}`}); err != nil {
			t.Error(err)
		}
	}), func() bool { return true }), "browser-secret-token")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ui/fragments/knowledge" || r.URL.Path == "/ui/fragments/search" || r.URL.Path == "/ui/fragments/operations" || r.URL.Path == "/ui/fragments/sources" {
			fragments.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/ui/fragments/") {
			server.WithOperatorAuth(server.WithOperatorReadBoundary(http.HandlerFunc(ui.Fragments), func() bool { return true }), "browser-secret-token").ServeHTTP(w, r)
			return
		}
		ui.ServeHTTP(w, r)
	})
	host := httptest.NewServer(handler)
	defer host.Close()
	script, err := filepath.Abs("../../tools/webui-browser/" + scriptName)
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
