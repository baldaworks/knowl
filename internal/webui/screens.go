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
	limitParameter              = "limit"
	parentIDParameter           = "parent_id"
	cursorParameter             = "cursor"
	allPagesView                = "all"
	pageKind                    = "page"
	retrievalFailed             = "failed"
	knowledgeFragment           = "/ui/fragments/knowledge"
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
	Parent                domain.OperatorCatalogSummary
	Items                 []domain.OperatorCatalogChild
	All                   bool
	Uncatalogued          bool
	NavigationUnavailable bool
	Next                  string
	Page                  *domain.OperatorPage
	Markdown              template.HTML
}

func fragmentURL(route string, values url.Values) string {
	return "/ui/fragments/" + route + "?" + values.Encode()
}
func pageURL(id any) string {
	return "/ui/knowledge?" + url.Values{"page_id": {strings.TrimSpace(stringID(id))}}.Encode()
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
	q := r.URL.Query()
	options, err := listOptions(q)
	if err != nil {
		h.readError(w, err)
		return
	}
	v := knowledgeView{All: q.Get("view") == allPagesView}
	if r.URL.Path == pageFragment {
		page, readErr := h.dependencies.Operator.Page(r.Context(), domain.PageID(q.Get("page_id")))
		if readErr != nil {
			h.readError(w, readErr)
			return
		}
		v.Page = &page
	}
	if v.All {
		pages, readErr := h.dependencies.Operator.PageSummaries(r.Context(), options)
		if readErr != nil {
			h.readError(w, readErr)
			return
		}
		for _, p := range pages.Items {
			v.Items = append(v.Items, domain.OperatorCatalogChild{ID: p.ID, Title: p.Title, Description: p.Description, Kind: pageKind})
		}
		if pages.NextCursor != "" {
			v.Next = fragmentURL("knowledge", url.Values{"view": {allPagesView}, cursorParameter: {pages.NextCursor}, limitParameter: {strconv.Itoa(normalizedLimit(options.Limit))}})
		}
	} else {
		catalog, readErr := h.dependencies.Operator.CatalogChildren(r.Context(), domain.PageID(q.Get(parentIDParameter)), options)
		switch {
		case errors.Is(readErr, app.ErrOperatorCatalogNotFound):
			if q.Get(parentIDParameter) != "" {
				h.readError(w, readErr)
				return
			}
			v.Uncatalogued = true
		case readErr != nil:
			if v.Page == nil {
				h.readError(w, readErr)
				return
			}
			v.NavigationUnavailable = true
		default:
			v.Parent = catalog.Parent
			v.Items = catalog.Items
			if catalog.NextCursor != "" {
				v.Next = fragmentURL("knowledge", url.Values{parentIDParameter: {q.Get(parentIDParameter)}, cursorParameter: {catalog.NextCursor}, limitParameter: {strconv.Itoa(normalizedLimit(options.Limit))}})
			}
		}
	}
	if v.Page == nil {
		for _, p := range v.Items {
			if p.Kind == pageKind {
				page, readErr := h.dependencies.Operator.Page(r.Context(), p.ID)
				if readErr != nil {
					h.readError(w, readErr)
					return
				}
				v.Page = &page
				break
			}
		}
	}
	if v.Page == nil && !v.All && !v.Uncatalogued && q.Get(parentIDParameter) == "" && len(v.Items) > 0 {
		pages, readErr := h.dependencies.Operator.PageSummaries(r.Context(), app.OperatorListOptions{Limit: 1})
		if readErr == nil && len(pages.Items) > 0 {
			page, pageErr := h.dependencies.Operator.Page(r.Context(), pages.Items[0].ID)
			if pageErr != nil {
				h.readError(w, pageErr)
				return
			}
			v.Page = &page
		} else if readErr != nil && !errors.Is(readErr, app.ErrOperatorCapabilityUnavailable) {
			h.readError(w, readErr)
			return
		}
	}
	if v.Page != nil {
		v.Markdown, err = renderPageMarkdown(v.Page.Markdown, string(v.Page.ID))
		if err != nil {
			h.readError(w, err)
			return
		}
		for i := range v.Page.Sources {
			v.Page.Sources[i].OriginalURI = SafeURI(v.Page.Sources[i].OriginalURI)
		}
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
	Embeddings         bool
	Sources            []domain.OperatorSourceSummary
	SourcesUnavailable bool
	MoreSources        bool
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
		if h.dependencies.Operator != nil {
			sources, err := h.dependencies.Operator.Sources(r.Context(), app.OperatorListOptions{Limit: 100})
			v.Sources = sources.Items
			v.MoreSources = sources.NextCursor != ""
			v.SourcesUnavailable = err != nil
		} else {
			v.SourcesUnavailable = true
		}
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
		{app.ErrOperatorCatalogNotFound, 404, "catalog_not_found"}, {app.ErrPageNotFound, 404, errorPageNotFound}, {app.ErrOperatorSourceRevisionNotFound, 404, errorSourceRevisionNotFound},
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
		for _, k := range []string{parentIDParameter, "view", limitParameter, cursorParameter} {
			allowed[k] = true
		}
	case pageFragment:
		allowed["page_id"] = true
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
	}
	q := r.URL.Query()
	for key, values := range q {
		if !allowed[key] || (len(values) != 1 && key != "source") {
			return false
		}
	}
	return (!q.Has("view") || q.Get("view") == allPagesView) && (!q.Has("view") || !q.Has(parentIDParameter))
}

func shortRevision(value string) string {
	r := []rune(value)
	if len(r) > 12 {
		return string(r[:12]) + "…"
	}
	return value
}
