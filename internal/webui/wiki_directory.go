package webui

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func (h *Handler) wikiDirectory(w http.ResponseWriter, r *http.Request) {
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
	directory := q.Get("directory")
	result, err := h.dependencies.Operator.WikiDirectoryChildren(r.Context(), directory, options)
	if err != nil {
		h.readError(w, err)
		return
	}
	next := ""
	if result.NextCursor != "" {
		next = fragmentURL("wiki-directory", url.Values{"directory": {directory}, limitParameter: {strconv.Itoa(normalizedLimit(options.Limit))}, cursorParameter: {result.NextCursor}})
	}
	h.render(w, http.StatusOK, "wiki_directory", struct {
		Directory string
		Items     []domain.OperatorWikiEntry
		Next      string
	}{directory, result.Items, next})
}
