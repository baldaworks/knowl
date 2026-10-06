package webui

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	unavailableLabel   = "Unavailable"
	operationsFragment = "/ui/fragments/operations"
	operationFragment  = "/ui/fragments/operation"
	sourcesFragment    = "/ui/fragments/sources"
	sourceFragment     = "/ui/fragments/source"
	sourceIDParameter  = "source_id"
)

type operationsView struct {
	Items                  []domain.OperatorOperationSummary
	Next, Status, SourceID string
}
type sourcesView struct {
	Items []domain.OperatorSourceSummary
	Next  string
}
type sourceView struct {
	domain.OperatorSourceDetail
	Next string
}

func activityTime(t time.Time) string {
	if t.IsZero() {
		return unavailableLabel
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}
func operationURL(id any) string {
	return fragmentURL("operation", url.Values{"operation_id": {stringOperationID(id)}})
}
func stringOperationID(id any) string {
	switch v := id.(type) {
	case string:
		return v
	case domain.OperationID:
		return string(v)
	default:
		return ""
	}
}
func sourceURL(id domain.SourceID) string {
	return fragmentURL("source", url.Values{sourceIDParameter: {string(id)}})
}
func nextActivityURL(route string, q url.Values, cursor string) string {
	if cursor == "" {
		return ""
	}
	q.Set(cursorParameter, cursor)
	if !q.Has(limitParameter) {
		q.Set(limitParameter, strconv.Itoa(normalizedLimit(0)))
	}
	return fragmentURL(route, q)
}

func (h *Handler) operations(w http.ResponseWriter, r *http.Request) {
	if h.dependencies.Operator == nil {
		h.readError(w, app.ErrOperatorCapabilityUnavailable)
		return
	}
	q := r.URL.Query()
	if !q.Has(limitParameter) {
		q.Set(limitParameter, "10")
	}
	o, err := listOptions(q)
	if err != nil {
		h.readError(w, err)
		return
	}
	result, err := h.dependencies.Operator.Operations(r.Context(), app.OperatorOperationListOptions{OperatorListOptions: o, Status: domain.OperationStatus(q.Get("status")), SourceID: domain.SourceID(q.Get(sourceIDParameter))})
	if err != nil {
		h.readError(w, err)
		return
	}
	h.render(w, 200, "operations", operationsView{Items: result.Items, Next: nextActivityURL("operations", q, result.NextCursor), Status: q.Get("status"), SourceID: q.Get(sourceIDParameter)})
}
func (h *Handler) operation(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("operation_id")
	if strings.TrimSpace(id) == "" || len(id) > 2048 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		h.readError(w, app.ErrOperatorInvalidRequest)
		return
	}
	if h.dependencies.Operation == nil {
		h.readError(w, app.ErrOperatorCapabilityUnavailable)
		return
	}
	result, err := h.dependencies.Operation(r.Context(), id)
	if err != nil {
		h.readError(w, err)
		return
	}
	// The server adapter provides detached public diagnostics; raw failures never render.
	result.Failure = nil
	h.render(w, 200, "operation", struct {
		Result knowlapi.OperationResult
		URL    string
	}{result, operationURL(id)})
}
func (h *Handler) sources(w http.ResponseWriter, r *http.Request) {
	if h.dependencies.Operator == nil {
		h.readError(w, app.ErrOperatorCapabilityUnavailable)
		return
	}
	q := r.URL.Query()
	o, err := listOptions(q)
	if err != nil {
		h.readError(w, err)
		return
	}
	result, err := h.dependencies.Operator.Sources(r.Context(), o)
	if err != nil {
		h.readError(w, err)
		return
	}
	h.render(w, 200, "sources", sourcesView{Items: result.Items, Next: nextActivityURL("sources", q, result.NextCursor)})
}
func (h *Handler) source(w http.ResponseWriter, r *http.Request) {
	if h.dependencies.Operator == nil {
		h.readError(w, app.ErrOperatorCapabilityUnavailable)
		return
	}
	q := r.URL.Query()
	o, err := listOptions(q)
	if err != nil {
		h.readError(w, err)
		return
	}
	result, err := h.dependencies.Operator.Source(r.Context(), domain.SourceID(q.Get(sourceIDParameter)), o)
	if err != nil {
		h.readError(w, err)
		return
	}
	h.render(w, 200, "source", sourceView{OperatorSourceDetail: result, Next: nextActivityURL("source", q, result.Documents.NextCursor)})
}

func shortIdentity(value any) string {
	text := []rune(strings.TrimSpace(fmt.Sprint(value)))
	if len(text) > 64 {
		return string(text[:16]) + "…" + string(text[len(text)-32:])
	}
	return string(text)
}
