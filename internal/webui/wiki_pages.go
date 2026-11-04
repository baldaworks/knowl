package webui

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const wikiPagesFragment = "/ui/fragments/wiki-pages"

type wikiPageView struct {
	domain.OperatorWikiPage
	URL, ChildrenURL string
}
type wikiPagesView struct {
	Directory string
	Items     []wikiPageView
	Next      string
}

func wikiPagesURL(directory string, options app.OperatorListOptions) string {
	values := url.Values{directoryParameter: {directory}}
	if options.Limit != 0 {
		values.Set(limitParameter, strconv.Itoa(options.Limit))
	}
	if options.Cursor != "" {
		values.Set(cursorParameter, options.Cursor)
	}
	return fragmentURL("wiki-pages", values)
}
func (h *Handler) wikiPages(w http.ResponseWriter, r *http.Request) {
	if h.dependencies.Operator == nil {
		h.readError(w, app.ErrOperatorCapabilityUnavailable)
		return
	}
	options, err := listOptions(r.URL.Query())
	if err != nil {
		h.readError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), app.DefaultReadLimits().Deadline)
	defer cancel()
	directory := r.URL.Query().Get(directoryParameter)
	branch, err := h.dependencies.Operator.WikiPageChildren(ctx, directory, options)
	if err != nil {
		h.readError(w, err)
		return
	}
	view := wikiPagesView{Directory: directory}
	for _, page := range branch.Items {
		parent := domain.PageID(rootCatalogID)
		if directory != "" {
			parent = domain.PageID(directory + "/index")
		}
		item := wikiPageView{OperatorWikiPage: page, URL: navigationPageURL(page.PageID, []domain.PageID{parent})}
		if page.ChildrenDirectory != "" {
			item.ChildrenURL = wikiPagesURL(page.ChildrenDirectory, app.OperatorListOptions{})
		}
		view.Items = append(view.Items, item)
	}
	if branch.NextCursor != "" {
		view.Next = wikiPagesURL(directory, app.OperatorListOptions{Limit: normalizedLimit(options.Limit), Cursor: branch.NextCursor})
	}
	h.render(w, http.StatusOK, "wiki_pages", view)
}
