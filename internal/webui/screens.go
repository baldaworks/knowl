package webui

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	directoryParameter          = "directory"
	limitParameter              = "limit"
	pageIDParameter             = "page_id"
	knowledgePath               = "/ui/knowledge"
	rootCatalogID               = "index"
	errorCatalogNotFound        = "catalog_not_found"
	parentIDParameter           = "parent_id"
	cursorParameter             = "cursor"
	allPagesView                = "all"
	viewParameter               = "view"
	pageKind                    = "page"
	retrievalFailed             = "failed"
	knowledgeFragment           = "/ui/fragments/knowledge"
	wikiDirectoryFragment       = "/ui/fragments/wiki-directory"
	pageFragment                = "/ui/fragments/page"
	searchFragment              = "/ui/fragments/search"
	sourceRevisionFragment      = "/ui/fragments/source-revision"
	errorCapabilityUnavailable  = "capability_unavailable"
	errorInvalidRequest         = "invalid_request"
	errorWorkspaceUnavailable   = "workspace_unavailable"
	errorSnapshotChanged        = "snapshot_changed"
	errorReadLimitExceeded      = "read_limit_exceeded"
	errorPageNotFound           = "page_not_found"
	errorSourceRevisionNotFound = "source_revision_not_found"
	errorCursorInvalid          = "cursor_invalid"
	errorLimitInvalid           = "limit_invalid"
	errorUnsupportedFormat      = "unsupported_format"
)

type knowledgeView struct {
	Page        *domain.OperatorPage
	Markdown    template.HTML
	Breadcrumbs []knowledgeBreadcrumb
	Outline     []articleHeading
	Ancestors   []domain.PageID
}

type knowledgeBreadcrumb struct {
	Title, URL string
}

func fragmentURL(route string, values url.Values) string {
	return "/ui/fragments/" + route + "?" + values.Encode()
}
func pageURL(id any) string {
	return knowledgePath + "?" + url.Values{pageIDParameter: {strings.TrimSpace(stringID(id))}}.Encode()
}
func knowledgeURL(values url.Values) string {
	if len(values) == 0 {
		return knowledgePath
	}
	return knowledgePath + "?" + values.Encode()
}
func contextualPageURL(id, parent domain.PageID) string {
	values := url.Values{pageIDParameter: {string(id)}}
	if parent != "" {
		values.Set(parentIDParameter, string(parent))
	}
	return knowledgeURL(values)
}
func treePageURL(id domain.PageID) string {
	return knowledgeURL(url.Values{pageIDParameter: {string(id)}, viewParameter: {allPagesView}})
}
func stringID(id any) string {
	switch v := id.(type) {
	case string:
		return v
	case domain.PageID:
		return string(v)
	default:
		return ""
	}
}
func (h *Handler) knowledge(w http.ResponseWriter, r *http.Request) {
	if h.dependencies.Operator == nil {
		h.readError(w, app.ErrOperatorCapabilityUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), app.DefaultReadLimits().Deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		h.readError(w, err)
		return
	}
	q := r.URL.Query()
	options, err := listOptions(q)
	if err != nil {
		h.readError(w, err)
		return
	}
	// Historical inventory URLs now open ordinary pages; directory reads remain separate.
	v := knowledgeView{}
	if options.Cursor != "" {
		h.readError(w, app.ErrOperatorCursorInvalid)
		return
	}
	selected := domain.PageID(q.Get(pageIDParameter))
	if selected == "" {
		selected = domain.PageID(q.Get(parentIDParameter))
	}
	if selected == "" {
		selected = rootCatalogID
	}
	page, err := h.dependencies.Operator.Page(ctx, selected)
	if err != nil {
		h.readError(w, err)
		return
	}
	v.Page = &page
	v.Breadcrumbs, v.Ancestors = h.pageNavigation(ctx, q, page)
	v.Markdown, err = renderTitledPageMarkdown(page.Markdown, string(page.ID), page.Title)
	if err != nil {
		h.readError(w, err)
		return
	}
	contextual, err := navigationArticleLinks(string(v.Markdown), page.ID, v.Ancestors)
	if err != nil {
		h.readError(w, err)
		return
	}
	v.Markdown = template.HTML(contextual) //nolint:gosec // Only reparsed sanitized HTML and server-verified navigation IDs.
	v.Outline, err = articleOutline(v.Markdown)
	if err != nil {
		h.readError(w, err)
		return
	}
	for i := range v.Page.Sources {
		v.Page.Sources[i].OriginalURI = SafeURI(v.Page.Sources[i].OriginalURI)
	}
	h.render(w, http.StatusOK, "knowledge", v)
}

func normalizedLimit(limit int) int {
	if limit == 0 {
		return 50
	}
	return limit
}
func listOptions(q url.Values) (app.OperatorListOptions, error) {
	o := app.OperatorListOptions{Cursor: q.Get(cursorParameter)}
	if q.Has(limitParameter) {
		n, e := strconv.Atoi(q.Get(limitParameter))
		if e != nil || n < 1 || n > 100 {
			return o, app.ErrOperatorLimitInvalid
		}
		o.Limit = n
	}
	if q.Has(cursorParameter) && (o.Cursor == "" || len(o.Cursor) > 8<<10) {
		return o, app.ErrOperatorCursorInvalid
	}
	return o, nil
}
func (h *Handler) sourceRevision(w http.ResponseWriter, r *http.Request) {
	if h.dependencies.Operator == nil {
		h.readError(w, app.ErrOperatorCapabilityUnavailable)
		return
	}
	revision, err := h.dependencies.Operator.SourceRevision(r.Context(), r.URL.Query().Get("source_ref"))
	if err != nil {
		h.readError(w, err)
		return
	}
	revision.OriginalURI = SafeURI(revision.OriginalURI)
	h.render(w, 200, "source-revision", revision)
}

type searchView struct {
	Embeddings bool
}
type searchResults struct {
	Result knowlapi.RetrieveResult
	JSON   string
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !q.Has("query") {
		if len(q) != 0 {
			h.readError(w, app.ErrOperatorInvalidRequest)
			return
		}
		v := searchView{Embeddings: h.dependencies.EmbeddingsEnabled}
		h.render(w, 200, "search", v)
		return
	}
	query := strings.TrimSpace(q.Get("query"))
	if query == "" {
		h.readError(w, app.ErrOperatorInvalidRequest)
		return
	}
	var sources []string
	for _, id := range q["source"] {
		if id == "" {
			continue
		}
		if app.ValidateSourceID(domain.SourceID(id)) != nil {
			h.readError(w, app.ErrOperatorInvalidRequest)
			return
		}
		sources = append(sources, id)
	}
	if h.dependencies.Retrieve == nil {
		h.readError(w, app.ErrOperatorCapabilityUnavailable)
		return
	}
	result, err := h.dependencies.Retrieve(r.Context(), query, sources)
	status := http.StatusOK
	if err != nil && result.Retrieval != nil && result.Retrieval.Effective == retrievalFailed {
		status = http.StatusServiceUnavailable
		w.Header().Set("X-Knowl-Error", "retrieval_failed")
	}
	if err != nil && status == http.StatusOK {
		h.readError(w, err)
		return
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		h.readError(w, err)
		return
	}
	h.render(w, status, "search-results", searchResults{Result: result, JSON: string(data)})
}
func (h *Handler) readError(w http.ResponseWriter, err error) {
	status, code := 500, "internal_error"
	for _, e := range []struct {
		err    error
		status int
		code   string
	}{
		{app.ErrOperatorInvalidRequest, 400, errorInvalidRequest}, {app.ErrQueryInvalid, 400, errorInvalidRequest}, {app.ErrOperatorLimitInvalid, 400, errorLimitInvalid}, {app.ErrOperatorCursorInvalid, 400, errorCursorInvalid},
		{app.ErrOperationNotFound, 404, "operation_not_found"}, {app.ErrSourceNotFound, 404, "source_not_found"},
		{app.ErrOperatorCatalogNotFound, 404, errorCatalogNotFound}, {app.ErrPageNotFound, 404, errorPageNotFound}, {app.ErrOperatorSourceRevisionNotFound, 404, errorSourceRevisionNotFound},
		{app.ErrOperatorDirectoryNotFound, 404, "directory_not_found"},
		{app.ErrOperatorSnapshotChanged, 409, errorSnapshotChanged}, {app.ErrOperatorReadLimitExceeded, 413, errorReadLimitExceeded}, {app.ErrOperatorUnsupportedFormat, 415, errorUnsupportedFormat},
		{app.ErrOperatorCapabilityUnavailable, 503, errorCapabilityUnavailable}, {app.ErrOperatorNotReady, 503, "not_ready"}, {app.ErrOperatorWorkspaceUnavailable, 503, errorWorkspaceUnavailable}, {context.Canceled, 503, errorWorkspaceUnavailable}, {context.DeadlineExceeded, 503, errorWorkspaceUnavailable},
	} {
		if errors.Is(err, e.err) {
			status, code = e.status, e.code
			break
		}
	}
	h.Error(w, status, code)
}
func validFragmentQuery(r *http.Request) bool {
	allowed := map[string]bool{}
	switch r.URL.Path {
	case knowledgeFragment:
		for _, k := range []string{parentIDParameter, pageIDParameter, trailParameter, viewParameter, limitParameter, cursorParameter} {
			allowed[k] = true
		}
	case wikiDirectoryFragment:
		for _, k := range []string{directoryParameter, limitParameter, cursorParameter} {
			allowed[k] = true
		}
	case wikiPagesFragment:
		for _, k := range []string{directoryParameter, limitParameter, cursorParameter} {
			allowed[k] = true
		}
	case pageFragment:
		for _, k := range []string{pageIDParameter, parentIDParameter, trailParameter, viewParameter, limitParameter, cursorParameter} {
			allowed[k] = true
		}
	case sourceRevisionFragment:
		allowed["source_ref"] = true
	case operationsFragment:
		for _, k := range []string{"status", sourceIDParameter, limitParameter, cursorParameter} {
			allowed[k] = true
		}
	case operationFragment:
		allowed["operation_id"] = true
	case sourcesFragment:
		allowed[limitParameter] = true
		allowed[cursorParameter] = true
	case sourceFragment:
		allowed[sourceIDParameter] = true
		allowed[limitParameter] = true
		allowed[cursorParameter] = true
	case searchFragment:
		allowed["query"] = true
		allowed["source"] = true
	case statisticsFragment:
	}
	q := r.URL.Query()
	for key, values := range q {
		if !allowed[key] || (len(values) != 1 && key != "source") {
			return false
		}
	}
	return (!q.Has(viewParameter) || q.Get(viewParameter) == allPagesView) && (!q.Has(viewParameter) || !q.Has(parentIDParameter))
}

func shortRevision(value string) string {
	r := []rune(value)
	if len(r) > 12 {
		return string(r[:12]) + "…"
	}
	return value
}
