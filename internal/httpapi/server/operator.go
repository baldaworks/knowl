package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/baldaworks/knowl/internal/httpapi/operatorapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const maxOperatorQueryBytes = 16 << 10
const maxOperatorBodyBytes = 16 << 10

// NewOperatorHandler serves exactly the generated read contract. Register it
// only when enabled, behind WithOperatorAuth, on the existing host mux.
func NewOperatorHandler(service *app.OperatorService, ready func() bool) http.Handler {
	handler := &operatorHandler{service: service}
	generated := operatorapi.HandlerWithOptions(handler, operatorapi.StdHTTPServerOptions{
		BaseRouter: &statusMux{inner: http.NewServeMux()},
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeHTTPError(w, http.StatusBadRequest, invalidRequestCode)
		},
	})
	return WithOperatorReadBoundary(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validOperatorQuery(w, r) {
			return
		}
		if service == nil {
			writeOperatorError(w, app.ErrOperatorCapabilityUnavailable)
			return
		}
		generated.ServeHTTP(w, r)
	}), ready)
}

// WithOperatorReadBoundary is shared by JSON and future authenticated fragments.
// Scope belongs exclusively to the composed service. It rejects every supplied
// override (including blank values), any GET body, and unavailable lifecycle
// state before application dispatch. It does not perform authentication.
func WithOperatorReadBoundary(next http.Handler, ready func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if len(r.URL.RawQuery) > maxOperatorQueryBytes {
			writeOperatorError(w, app.ErrOperatorReadLimitExceeded)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeOperatorError(w, app.ErrOperatorInvalidRequest)
			return
		}
		for key := range query {
			if operatorOverrideKey(key) {
				writeHTTPError(w, http.StatusForbidden, "scope_override_forbidden")
				return
			}
		}
		for key := range r.Header {
			if operatorOverrideKey(key) {
				writeHTTPError(w, http.StatusForbidden, "scope_override_forbidden")
				return
			}
		}
		if r.Body != nil {
			body, err := io.ReadAll(io.LimitReader(r.Body, maxOperatorBodyBytes+1))
			if len(body) > maxOperatorBodyBytes {
				writeOperatorError(w, app.ErrOperatorReadLimitExceeded)
				return
			}
			if err != nil {
				writeOperatorError(w, app.ErrOperatorInvalidRequest)
				return
			}
			if len(body) != 0 {
				if operatorBodyOverride(body) {
					writeHTTPError(w, http.StatusForbidden, "scope_override_forbidden")
				} else {
					writeOperatorError(w, app.ErrOperatorInvalidRequest)
				}
				return
			}
		}
		if ready != nil && !ready() {
			writeOperatorError(w, app.ErrOperatorNotReady)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func operatorOverrideKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	key = strings.TrimPrefix(key, "x_")
	key = strings.TrimPrefix(key, "knowl_")
	switch key {
	case "scope", "scope_id", "scope_ref", "workspace", "workspace_id", "workspace_path", "workspace_root":
		return true
	default:
		return false
	}
}

func validOperatorQuery(w http.ResponseWriter, r *http.Request) bool {
	allowed := map[string]bool{}
	switch r.URL.Path {
	case "/operator/v1/catalogs":
		allowed["parent_id"] = true
		allowed["limit"] = true
		allowed["cursor"] = true
	case "/operator/v1/pages", "/operator/v1/sources":
		allowed["limit"] = true
		allowed["cursor"] = true
	case "/operator/v1/page":
		allowed["page_id"] = true
	case "/operator/v1/source-revision":
		allowed["source_ref"] = true
	case "/operator/v1/operations":
		allowed["status"] = true
		allowed["source_id"] = true
		allowed["limit"] = true
		allowed["cursor"] = true
	default:
		if strings.HasPrefix(r.URL.Path, "/operator/v1/sources/") {
			allowed["limit"] = true
			allowed["cursor"] = true
		}
	}
	for key, values := range r.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			writeOperatorError(w, app.ErrOperatorInvalidRequest)
			return false
		}
		if key == "limit" {
			limit, err := strconv.Atoi(values[0])
			if err != nil || limit < 1 || limit > 100 {
				writeOperatorError(w, app.ErrOperatorLimitInvalid)
				return false
			}
		}
		if key == "cursor" && (values[0] == "" || len(values[0]) > 8<<10) {
			writeOperatorError(w, app.ErrOperatorCursorInvalid)
			return false
		}
	}
	return true
}

type operatorHandler struct{ service *app.OperatorService }

func operatorValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func operatorOptions(limit *int, cursor *string) app.OperatorListOptions {
	options := app.OperatorListOptions{Cursor: operatorValue(cursor)}
	if limit != nil {
		options.Limit = *limit
	}
	return options
}
func operatorResult[T any](w http.ResponseWriter, result T, err error) {
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (h *operatorHandler) GetCatalogs(w http.ResponseWriter, r *http.Request, p operatorapi.GetCatalogsParams) {
	result, err := h.service.CatalogChildren(r.Context(), domain.PageID(operatorValue(p.ParentId)), operatorOptions(p.Limit, p.Cursor))
	operatorResult(w, result, err)
}
func (h *operatorHandler) GetPages(w http.ResponseWriter, r *http.Request, p operatorapi.GetPagesParams) {
	result, err := h.service.PageSummaries(r.Context(), operatorOptions(p.Limit, p.Cursor))
	operatorResult(w, result, err)
}
func (h *operatorHandler) GetPage(w http.ResponseWriter, r *http.Request, p operatorapi.GetPageParams) {
	result, err := h.service.Page(r.Context(), domain.PageID(p.PageId))
	operatorResult(w, result, err)
}
func (h *operatorHandler) GetSourceRevision(w http.ResponseWriter, r *http.Request, p operatorapi.GetSourceRevisionParams) {
	result, err := h.service.SourceRevision(r.Context(), p.SourceRef)
	operatorResult(w, result, err)
}
func (h *operatorHandler) GetSources(w http.ResponseWriter, r *http.Request, p operatorapi.GetSourcesParams) {
	result, err := h.service.Sources(r.Context(), operatorOptions(p.Limit, p.Cursor))
	operatorResult(w, result, err)
}
func (h *operatorHandler) GetSource(w http.ResponseWriter, r *http.Request, id string, p operatorapi.GetSourceParams) {
	result, err := h.service.Source(r.Context(), domain.SourceID(id), operatorOptions(p.Limit, p.Cursor))
	operatorResult(w, result, err)
}
func (h *operatorHandler) GetOperations(w http.ResponseWriter, r *http.Request, p operatorapi.GetOperationsParams) {
	result, err := h.service.Operations(r.Context(), app.OperatorOperationListOptions{OperatorListOptions: operatorOptions(p.Limit, p.Cursor), Status: domain.OperationStatus(operatorValue(p.Status)), SourceID: domain.SourceID(operatorValue(p.SourceId))})
	operatorResult(w, result, err)
}

func writeOperatorError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	for _, entry := range []struct {
		err    error
		status int
		code   string
	}{
		{app.ErrOperatorInvalidRequest, 400, invalidRequestCode}, {app.ErrOperatorLimitInvalid, 400, "limit_invalid"}, {app.ErrOperatorCursorInvalid, 400, "cursor_invalid"},
		{app.ErrOperatorCatalogNotFound, 404, "catalog_not_found"}, {app.ErrPageNotFound, 404, "page_not_found"}, {app.ErrOperatorSourceRevisionNotFound, 404, "source_revision_not_found"}, {app.ErrSourceNotFound, 404, "source_not_found"}, {app.ErrOperationNotFound, 404, "operation_not_found"},
		{app.ErrOperatorSnapshotChanged, 409, "snapshot_changed"}, {app.ErrOperatorReadLimitExceeded, 413, "read_limit_exceeded"}, {app.ErrOperatorUnsupportedFormat, 415, "unsupported_format"},
		{app.ErrOperatorCapabilityUnavailable, 503, "capability_unavailable"}, {app.ErrOperatorNotReady, 503, "not_ready"}, {app.ErrOperatorWorkspaceUnavailable, 503, operatorWorkspaceUnavailableCode},
		{context.Canceled, 503, operatorWorkspaceUnavailableCode}, {context.DeadlineExceeded, 503, operatorWorkspaceUnavailableCode},
	} {
		if errors.Is(err, entry.err) {
			status, code = entry.status, entry.code
			break
		}
	}
	writeHTTPError(w, status, code)
}

const operatorWorkspaceUnavailableCode = "workspace_unavailable"
