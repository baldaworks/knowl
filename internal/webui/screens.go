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
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const (
	limitParameter              = "limit"
	pageIDParameter             = "page_id"
	knowledgePath               = "/ui/knowledge"
	rootCatalogID               = "index"
	errorCatalogNotFound        = "catalog_not_found"
	catalogTrailHeader          = "X-Knowl-Catalog-Trail"
	parentIDParameter           = "parent_id"
	cursorParameter             = "cursor"
	allPagesView                = "all"
	viewParameter               = "view"
	rootBreadcrumbTitle         = "Root"
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
	Parent                domain.OperatorCatalogSummary
	Items                 []domain.OperatorCatalogChild
	All                   bool
	Uncatalogued          bool
	NavigationUnavailable bool
	Next                  string
	Page                  *domain.OperatorPage
	Markdown              template.HTML
	Index                 bool
	ContextID             domain.PageID
	Breadcrumbs           []knowledgeBreadcrumb
	MaxDepth              int
	TrailJSON             string
}

type knowledgeBreadcrumb struct {
	Title, URL string
}

type navigationBudget struct{ edges, bytes, windows int }

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
	ctx, cancel := context.WithTimeout(r.Context(), app.DefaultReadLimits().Deadline)
	defer cancel()
	v := knowledgeView{All: q.Get(viewParameter) == allPagesView, MaxDepth: app.DefaultCatalogLimits().MaxDepth}
	if r.URL.Path == pageFragment || q.Has(pageIDParameter) {
		page, readErr := h.dependencies.Operator.Page(ctx, domain.PageID(q.Get(pageIDParameter)))
		if readErr != nil {
			h.readError(w, readErr)
			return
		}
		v.Page = &page
	}
	options, err := listOptions(q)
	if err != nil {
		if v.Page == nil {
			h.readError(w, err)
			return
		}
		v.NavigationUnavailable = true
	}
	var nextCursor string
	budget := &navigationBudget{}
	if v.All {
		pages, readErr := h.dependencies.Operator.PageSummaries(ctx, options)
		if readErr != nil {
			if v.Page == nil {
				h.readError(w, readErr)
				return
			}
			v.NavigationUnavailable = true
		}
		for _, p := range pages.Items {
			v.Items = append(v.Items, domain.OperatorCatalogChild{ID: p.ID, Title: p.Title, Description: p.Description, Kind: pageKind})
		}
		nextCursor = pages.NextCursor
	} else if !v.NavigationUnavailable {
		parent := domain.PageID(q.Get(parentIDParameter))
		catalog, readErr := h.readNavigationCatalog(ctx, parent, options, budget)
		switch {
		case errors.Is(readErr, app.ErrOperatorCatalogNotFound):
			if v.Page != nil {
				v.NavigationUnavailable = true
				break
			}
			if parent != "" {
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
			if v.Page != nil && parent != "" && (catalog.Parent.ID != parent || !h.catalogContains(ctx, parent, v.Page.ID, catalog, options, budget)) {
				v.NavigationUnavailable = true
				break
			}
			v.Parent = catalog.Parent
			v.Items = catalog.Items
			nextCursor = catalog.NextCursor
			if v.Page == nil {
				page, pageErr := h.dependencies.Operator.Page(ctx, catalog.Parent.ID)
				if pageErr != nil {
					h.readError(w, pageErr)
					return
				}
				v.Page = &page
				v.ContextID = catalog.Parent.ID
			} else if parent != "" {
				v.ContextID = parent
			}
		}
	}
	if v.Page == nil && v.All {
		for _, p := range v.Items {
			if p.Kind == pageKind {
				page, readErr := h.dependencies.Operator.Page(ctx, p.ID)
				if readErr != nil {
					h.readError(w, readErr)
					return
				}
				v.Page = &page
				break
			}
		}
	}
	if nextCursor != "" {
		values := url.Values{cursorParameter: {nextCursor}, limitParameter: {strconv.Itoa(normalizedLimit(options.Limit))}}
		if v.All {
			values.Set(viewParameter, allPagesView)
		} else if q.Get(parentIDParameter) != "" {
			values.Set(parentIDParameter, q.Get(parentIDParameter))
		}
		if q.Has(pageIDParameter) {
			values.Set(pageIDParameter, q.Get(pageIDParameter))
		}
		v.Next = knowledgeURL(values)
	}
	v.Breadcrumbs = []knowledgeBreadcrumb{{Title: rootBreadcrumbTitle, URL: knowledgePath}}
	if v.All {
		crumb := knowledgeBreadcrumb{Title: "All pages"}
		if v.Page != nil {
			crumb.URL = knowledgeURL(url.Values{viewParameter: {allPagesView}})
		}
		v.Breadcrumbs = append(v.Breadcrumbs, crumb)
	}
	trail := []domain.PageID{}
	if v.ContextID != "" {
		trail = []domain.PageID{v.ContextID}
	}
	ancestors := []knowledgeBreadcrumb{}
	if v.ContextID != "" && v.ContextID != rootCatalogID {
		ancestors = []knowledgeBreadcrumb{{Title: v.Parent.Title, URL: knowledgeURL(url.Values{parentIDParameter: {string(v.ContextID)}})}}
	}
	if header := r.Header.Get(catalogTrailHeader); header != "" && v.ContextID != "" {
		validated, crumbs, valid := h.validatedCatalogTrail(ctx, header, v.ContextID, v.Parent.Title, budget)
		if valid {
			trail, ancestors = validated, crumbs
		} else {
			v.NavigationUnavailable = true
		}
	}
	encoded, _ := json.Marshal(trail)
	v.TrailJSON = string(encoded)
	if v.Page != nil {
		kind, _ := okf.ClassifyPath(string(v.Page.ID) + ".md")
		v.Index = kind == okf.DocumentIndex
		for _, ancestor := range ancestors {
			if ancestor.URL != knowledgeURL(url.Values{parentIDParameter: {string(v.Page.ID)}}) {
				v.Breadcrumbs = append(v.Breadcrumbs, ancestor)
			}
		}
		if v.Page.ID == rootCatalogID && !v.All {
			v.Breadcrumbs[0] = knowledgeBreadcrumb{Title: v.Page.Title}
		} else {
			v.Breadcrumbs = append(v.Breadcrumbs, knowledgeBreadcrumb{Title: v.Page.Title})
		}
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

// Context is optional presentation. Scan only this catalog's bounded windows,
// never filesystem ancestors or a graph, and retain a valid explicit page on failure.
func (h *Handler) catalogContains(ctx context.Context, parent, selected domain.PageID, catalog domain.OperatorCatalog, options app.OperatorListOptions, budget *navigationBudget) bool {
	seen := make(map[string]bool)
	if options.Cursor != "" || (catalog.NextCursor != "" && normalizedLimit(options.Limit) != 100) {
		version := catalog.SnapshotVersion
		var err error
		catalog, err = h.readNavigationCatalog(ctx, parent, app.OperatorListOptions{Limit: 100}, budget)
		if err != nil || catalog.SnapshotVersion != version {
			return false
		}
	}
	for {
		if ctx.Err() != nil || catalog.Parent.ID != parent {
			return false
		}
		for _, child := range catalog.Items {
			if child.ID == selected {
				return true
			}
		}
		if catalog.NextCursor == "" || seen[catalog.NextCursor] {
			return false
		}
		seen[catalog.NextCursor] = true
		var err error
		catalog, err = h.readNavigationCatalog(ctx, parent, app.OperatorListOptions{Limit: 100, Cursor: catalog.NextCursor}, budget)
		if err != nil {
			return false
		}
	}
}

func (h *Handler) readNavigationCatalog(ctx context.Context, parent domain.PageID, options app.OperatorListOptions, budget *navigationBudget) (domain.OperatorCatalog, error) {
	limits := app.DefaultCatalogLimits()
	if err := ctx.Err(); err != nil {
		return domain.OperatorCatalog{}, err
	}
	if budget.windows >= limits.MaxEdges {
		return domain.OperatorCatalog{}, app.ErrOperatorReadLimitExceeded
	}
	catalog, err := h.dependencies.Operator.CatalogChildren(ctx, parent, options)
	if err != nil {
		return catalog, err
	}
	budget.windows++
	budget.edges += len(catalog.Items)
	data, err := json.Marshal(catalog)
	if err != nil {
		return domain.OperatorCatalog{}, app.ErrOperatorWorkspaceUnavailable
	}
	budget.bytes += len(data)
	if budget.edges > limits.MaxEdges || budget.bytes > limits.MaxSnapshotBytes {
		return domain.OperatorCatalog{}, app.ErrOperatorReadLimitExceeded
	}
	return catalog, nil
}

// The private header carries presentation IDs only. Every supplied adjacent
// edge is re-read; history never supplies labels or authority.
func (h *Handler) validatedCatalogTrail(ctx context.Context, header string, current domain.PageID, title string, budget *navigationBudget) ([]domain.PageID, []knowledgeBreadcrumb, bool) {
	limits := app.DefaultCatalogLimits()
	if len(header) > limits.MaxDepth*(limits.MaxPathBytes+4)+2 {
		return nil, nil, false
	}
	var ids []domain.PageID
	if json.Unmarshal([]byte(header), &ids) != nil || len(ids) == 0 || len(ids) > limits.MaxDepth || ids[len(ids)-1] != current {
		return nil, nil, false
	}
	seen := make(map[domain.PageID]bool)
	for _, id := range ids {
		kind, err := okf.ClassifyPath(string(id) + ".md")
		if seen[id] || !validPageIdentity(string(id)) || wiki.NormalizePageTarget(string(id)) != string(id) || err != nil || kind != okf.DocumentIndex {
			return nil, nil, false
		}
		seen[id] = true
	}
	crumbs := []knowledgeBreadcrumb{}
	for i, id := range ids {
		label := title
		if i < len(ids)-1 {
			catalog, err := h.readNavigationCatalog(ctx, id, app.OperatorListOptions{Limit: 100}, budget)
			if err != nil || !h.catalogContains(ctx, id, ids[i+1], catalog, app.OperatorListOptions{Limit: 100}, budget) {
				return nil, nil, false
			}
			label = catalog.Parent.Title
		}
		if id != rootCatalogID {
			crumbs = append(crumbs, knowledgeBreadcrumb{Title: label, URL: knowledgeURL(url.Values{parentIDParameter: {string(id)}})})
		}
	}
	return ids, crumbs, true
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
		for _, k := range []string{parentIDParameter, pageIDParameter, viewParameter, limitParameter, cursorParameter} {
			allowed[k] = true
		}
	case wikiDirectoryFragment:
		for _, k := range []string{"directory", limitParameter, cursorParameter} {
			allowed[k] = true
		}
	case pageFragment:
		for _, k := range []string{pageIDParameter, parentIDParameter, viewParameter, limitParameter, cursorParameter} {
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
