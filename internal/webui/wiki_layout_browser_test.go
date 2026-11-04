//go:build browser

package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	workspacefs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

// TestBrowserWikiLayout reads the checked-in canonical wiki through the real
// filesystem adapter. Its isolated copy keeps browser reads off local state.
func TestBrowserWikiLayout(t *testing.T) {
	const browserToken = "browser-secret-token"
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "wiki"), os.DirFS("../../knowledge/wiki")); err != nil {
		t.Fatal(err)
	}
	// Add real physical pages independently of the checked-in index links.
	writeWikiBrowserPage(t, root, "AI/index.md", []byte("AI tools and techniques.\n\n## Authored reading\n\n[Unrelated article](../concepts/knowl-overview.md)\n\nKeep this additional information.\n"))
	writeWikiBrowserPage(t, root, "concepts/ADRs/index.md", nil)
	writeWikiBrowserPage(t, root, "releases/index.md", []byte("# Balda releases\n\nPublished release information.\n"))
	for _, page := range []struct{ relative, title, body string }{
		{"AI/Prism.md", "Prism", "# Prism\n\n## Workflow\n\nA published workflow page.\n"},
		{"AI/OpenSpec.md", "OpenSpec", "# OpenSpec\n\nA separate published page.\n"},
		{"concepts/ADRs/first.md", "First decision", "# First decision\n\nA child of an empty parent page.\n"},
	} {
		content, err := okf.RenderConcept(page.relative, okf.Document{Metadata: okf.Metadata{Type: "concept", Title: page.title}, Body: page.body}, okf.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		writeWikiBrowserPage(t, root, page.relative, content)
	}
	if err := os.Mkdir(filepath.Join(root, ".knowl"), 0o700); err != nil {
		t.Fatal(err)
	}
	workspace, err := workspacefs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	// A newer accepted revision exists, but the published page still cites v1.
	ref := acceptWikiBrowserSource(t, workspace, "accepted-v1", "Original accepted Prism source.\n")
	acceptWikiBrowserSource(t, workspace, "accepted-v2", "Newer accepted Prism source.\n")
	prism, err := okf.RenderConcept("AI/Prism.md", okf.Document{Metadata: okf.Metadata{Type: "concept", Title: "Prism", Extensions: map[string]any{"knowl": map[string]any{"id": "AI/Prism", "source_refs": []any{ref}}}}, Body: "# Prism\n\n## Workflow\n\nA published workflow page.\n"}, okf.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	writeWikiBrowserPage(t, root, "AI/Prism.md", prism)
	operator, err := app.NewOperatorService("trusted", app.OperatorReaders{Catalogs: workspace, Pages: workspace, Page: workspace, Revisions: workspace, Directories: workspace}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ui, err := New(Dependencies{Operator: operator})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ui/fragments/") {
			if r.Header.Get("Authorization") != "Bearer "+browserToken {
				ui.Error(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			ui.Fragments(w, r)
			return
		}
		ui.ServeHTTP(w, r)
	}))
	defer host.Close()
	script, err := filepath.Abs("../../tools/webui-browser/wiki_layout.cjs")
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

func writeWikiBrowserPage(t *testing.T, root, relative string, content []byte) {
	t.Helper()
	filename := filepath.Join(root, "wiki", filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func acceptWikiBrowserSource(t *testing.T, workspace *workspacefs.Workspace, revision, text string) string {
	t.Helper()
	content := []byte(text)
	digest := sha256.Sum256(content)
	accepted, err := workspace.AcceptSource(t.Context(), domain.SourceEnvelope{Scope: "trusted", Source: domain.SourceRef{Adapter: "fixture", ID: "prism"}, Version: domain.SourceVersion{Version: revision, Digest: hex.EncodeToString(digest[:])}, MediaType: "text/plain", Content: content, SourceDocument: domain.SourceDocument{SourceID: "wiki-browser", DocumentID: "prism.md", Revision: revision, URI: "https://example.test/prism?secret=value#fragment"}})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := app.EncodeSourceRef(app.SourceRefIdentity{Source: accepted.Source, Revision: accepted.Version.Version})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
