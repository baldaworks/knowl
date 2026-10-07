// Package webui serves the embedded, workspace-free shell and trusted fragments.
package webui

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

//go:embed templates/*.html assets
var embedded embed.FS

const CSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"

// Dependencies are read-only presentation inputs. They never expose writers.
type Dependencies struct {
	EmbeddingsEnabled bool
	Operator          *app.OperatorService
	Retrieve          func(context.Context, string, []string) (knowlapi.RetrieveResult, error)
	OperationStatus   func(domain.OperationStatus) string
	Operation         func(context.Context, string) (knowlapi.OperationResult, error)
}
type Handler struct {
	templates    *template.Template
	assets       fs.FS
	dependencies Dependencies
}

func New(dependencies Dependencies) (*Handler, error) { return newHandler(embedded, dependencies) }
func newHandler(files fs.FS, dependencies Dependencies) (*Handler, error) {
	if err := validateAssets(files); err != nil {
		return nil, err
	}
	t, err := template.New("ui").Funcs(template.FuncMap{
		"lower":         strings.ToLower,
		"pageURL":       pageURL,
		"treePageURL":   treePageURL,
		"rawURL":        func(ref string) string { return fragmentURL("source-revision", url.Values{"source_ref": {ref}}) },
		"shortRevision": shortRevision,
		"shortIdentity": shortIdentity,
		"activityTime":  activityTime,
		"operationURL":  operationURL,
		"sourceURL":     sourceURL,
		"publicStatus": func(status domain.OperationStatus) string {
			if status == "" || dependencies.OperationStatus == nil {
				return "unavailable"
			}
			return dependencies.OperationStatus(status)
		},
		"label":  func(value any) string { return strings.ReplaceAll(fmt.Sprint(value), "_", " ") },
		"number": func(i int) int { return i + 1 },
	}).ParseFS(files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse UI templates: %w", err)
	}
	for _, name := range []string{"shell", "error"} {
		if t.Lookup(name) == nil {
			return nil, errors.New("missing required UI template")
		}
	}
	a, err := fs.Sub(files, "assets")
	if err != nil {
		return nil, fmt.Errorf("open UI assets: %w", err)
	}
	return &Handler{templates: t, assets: a, dependencies: dependencies}, nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", CSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/ui/assets/") {
		name := strings.TrimPrefix(r.URL.Path, "/ui/assets/")
		if !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		info, err := fs.Stat(h.assets, name)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.StripPrefix("/ui/assets/", http.FileServerFS(h.assets)).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	title := ""
	switch r.URL.Path {
	case "/ui/", "/ui/knowledge":
		title = "Knowledge"
	case "/ui/search":
		title = "Search"
	case "/ui/operations":
		title = "Operations"
	case "/ui/sources":
		title = "Sources"
	default:
		http.NotFound(w, r)
		return
	}
	h.render(w, 200, "shell", struct {
		Title   string
		Screens []string
	}{title, []string{"Knowledge", "Search", "Operations", "Sources"}})
}
func (h *Handler) Fragments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.Error(w, 405, "method_not_allowed")
		return
	}
	switch r.URL.Path {
	case knowledgeFragment, pageFragment, wikiDirectoryFragment, sourceRevisionFragment, searchFragment, operationsFragment, operationFragment, sourcesFragment, sourceFragment:
		if !validFragmentQuery(r) {
			h.Error(w, 400, errorInvalidRequest)
			return
		}
		switch r.URL.Path {
		case knowledgeFragment, pageFragment:
			h.knowledge(w, r)
		case wikiDirectoryFragment:
			h.wikiDirectory(w, r)
		case sourceRevisionFragment:
			h.sourceRevision(w, r)
		case searchFragment:
			h.search(w, r)
		case operationsFragment:
			h.operations(w, r)
		case operationFragment:
			h.operation(w, r)
		case sourcesFragment:
			h.sources(w, r)
		case sourceFragment:
			h.source(w, r)
		}
	default:
		h.Error(w, 404, "not_found")
	}
}

// Error preserves the HTTP failure status and exposes only a stable machine code.
func (h *Handler) Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("X-Knowl-Error", code)
	messages := map[string]string{
		errorCapabilityUnavailable:  "This view is not available yet.",
		errorInvalidRequest:         "The request is invalid. Check the selected view and try again.",
		"unauthorized":              "Reconnect with a valid operator token.",
		"scope_override_forbidden":  "This connection cannot select another knowledge scope.",
		"not_found":                 "The requested view was not found.",
		"directory_not_found":       "This wiki folder is no longer available.",
		"not_ready":                 "The server is starting. Please try again shortly.",
		errorWorkspaceUnavailable:   "Knowledge is temporarily unavailable. Please try again.",
		errorSnapshotChanged:        "Knowledge changed. Refresh this view to continue.",
		errorReadLimitExceeded:      "This request exceeds the read limit.",
		errorPageNotFound:           "This published page is no longer available.",
		errorSourceRevisionNotFound: "This saved source revision is unavailable. No upstream source was fetched.",
		errorCursorInvalid:          "This continuation expired. Open the first page to continue.",
		errorLimitInvalid:           "Choose a page size between 1 and 100.",
		errorUnsupportedFormat:      "This source format cannot be displayed.",
	}
	message := messages[code]
	if message == "" {
		message = "The request could not be completed. Please try again."
	}
	h.render(w, status, "error", struct{ Code, Message string }{code, message})
}
func (h *Handler) render(w http.ResponseWriter, status int, name string, data any) {
	var b bytes.Buffer
	if err := h.templates.ExecuteTemplate(&b, name, data); err != nil {
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", CSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}
