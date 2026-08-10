package knowl

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const maxHTTPBodyBytes = 8 << 20

const serviceName = "knowl"

const serviceKey = "service"

const statusKey = "status"

type httpDependencies struct {
	Scope         domain.ScopeRef
	OperatorToken string
	Ingest        *app.IngestService
	Query         *app.QueryService
	Lint          *app.LintService
	Worker        *worker
	Ready         func() bool
}

func newHTTPHandler(dependencies httpDependencies) http.Handler {
	if dependencies.Ready == nil {
		dependencies.Ready = func() bool { return true }
	}
	return &httpHandler{dependencies: dependencies}
}

type httpHandler struct {
	dependencies httpDependencies
}

func (handler *httpHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/healthz" || request.URL.Path == "/health" {
		handler.health(response)
		return
	}
	if request.URL.Path == "/readyz" || request.URL.Path == "/health/ready" {
		handler.ready(response)
		return
	}
	if !strings.HasPrefix(request.URL.Path, "/v1/") {
		writeHTTPError(response, http.StatusNotFound, "not_found")
		return
	}
	if scope := request.URL.Query().Get("scope"); scope != "" {
		writeHTTPError(response, http.StatusForbidden, "scope_override_forbidden")
		return
	}
	if !handler.dependencies.Ready() {
		writeHTTPError(response, http.StatusServiceUnavailable, "not_ready")
		return
	}
	path := strings.TrimSuffix(request.URL.Path, "/")
	switch {
	case path == "/v1/search" && request.Method == http.MethodGet:
		handler.search(response, request)
	case path == "/v1/query" && request.Method == http.MethodGet:
		handler.query(response, request)
	case path == "/v1/lint" && request.Method == http.MethodGet:
		handler.lint(response, request)
	case path == "/v1/lint/results" && request.Method == http.MethodGet:
		handler.lint(response, request)
	case path == "/v1/lint-results" && request.Method == http.MethodGet:
		handler.lint(response, request)
	case path == "/v1/ingest" && request.Method == http.MethodPost:
		handler.ingest(response, request, false)
	case path == "/v1/ingest/preview" && request.Method == http.MethodPost:
		handler.ingest(response, request, true)
	case path == "/v1/query/file" && request.Method == http.MethodPost:
		handler.fileQuery(response, request)
	case strings.HasPrefix(path, "/v1/operations/"):
		handler.operation(response, request, strings.TrimPrefix(path, "/v1/operations/"))
	case strings.HasPrefix(path, "/v1/status/"):
		handler.operation(response, request, strings.TrimPrefix(path, "/v1/status/"))
	case strings.HasPrefix(path, "/v1/pages/"):
		handler.page(response, request, strings.TrimPrefix(path, "/v1/pages/"))
	default:
		writeHTTPError(response, http.StatusNotFound, "not_found")
	}
}

func (handler *httpHandler) health(response http.ResponseWriter) {
	writeJSON(response, http.StatusOK, map[string]any{serviceKey: serviceName, statusKey: "ok"})
}

func (handler *httpHandler) ready(response http.ResponseWriter) {
	if !handler.dependencies.Ready() {
		writeJSON(response, http.StatusServiceUnavailable, map[string]any{serviceKey: serviceName, statusKey: "not_ready"})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"scope": handler.dependencies.Scope, serviceKey: serviceName, statusKey: "ready"})
}

func (handler *httpHandler) search(response http.ResponseWriter, request *http.Request) {
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	if query == "" {
		writeHTTPError(response, http.StatusBadRequest, "query_required")
		return
	}
	result, err := handler.dependencies.Query.Search(request.Context(), handler.dependencies.Scope, query, domain.ReadLimits{})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *httpHandler) query(response http.ResponseWriter, request *http.Request) {
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	if query == "" {
		writeHTTPError(response, http.StatusBadRequest, "query_required")
		return
	}
	result, err := handler.dependencies.Query.Query(request.Context(), handler.dependencies.Scope, query, domain.ReadLimits{})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *httpHandler) lint(response http.ResponseWriter, request *http.Request) {
	result, err := handler.dependencies.Lint.Lint(request.Context(), handler.dependencies.Scope)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *httpHandler) ingest(response http.ResponseWriter, request *http.Request, preview bool) {
	if !handler.requireOperator(response, request) {
		return
	}
	var envelope domain.SourceEnvelope
	if err := decodeJSON(response, request, &envelope); err != nil {
		return
	}
	envelope, err := trustedEnvelope(handler.dependencies.Scope, envelope)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	var result app.IngestResult
	work := func(ctx context.Context) error {
		var workErr error
		if preview {
			result, workErr = handler.dependencies.Ingest.Preview(ctx, envelope)
		} else {
			result, workErr = handler.dependencies.Ingest.Ingest(ctx, envelope)
		}
		return workErr
	}
	if err := handler.do(request.Context(), work); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *httpHandler) fileQuery(response http.ResponseWriter, request *http.Request) {
	if !handler.requireOperator(response, request) {
		return
	}
	var filing app.FilingRequest
	if err := decodeJSON(response, request, &filing); err != nil {
		return
	}
	if filing.Result.Scope == "" {
		filing.Result.Scope = handler.dependencies.Scope
	}
	var result app.IngestResult
	if err := handler.do(request.Context(), func(ctx context.Context) error {
		var err error
		result, err = handler.dependencies.Query.File(ctx, handler.dependencies.Scope, filing)
		return err
	}); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *httpHandler) operation(response http.ResponseWriter, request *http.Request, rawPath string) {
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 || (len(parts) == 2 && parts[1] != "apply" && parts[1] != "status") {
		writeHTTPError(response, http.StatusNotFound, "not_found")
		return
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(id) == "" {
		writeHTTPError(response, http.StatusBadRequest, "operation_id_invalid")
		return
	}
	if len(parts) == 2 && parts[1] == "apply" {
		if request.Method != http.MethodPost || !handler.requireOperator(response, request) {
			if request.Method != http.MethodPost {
				writeHTTPError(response, http.StatusMethodNotAllowed, "method_not_allowed")
			}
			return
		}
		var result app.ApplyResult
		if err := handler.do(request.Context(), func(ctx context.Context) error {
			var err error
			result, err = handler.dependencies.Ingest.Apply(ctx, handler.dependencies.Scope, domain.OperationID(id))
			return err
		}); err != nil {
			writeServiceError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result)
		return
	}
	if request.Method != http.MethodGet {
		writeHTTPError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	result, err := handler.dependencies.Query.Operation(request.Context(), handler.dependencies.Scope, domain.OperationID(id))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *httpHandler) page(response http.ResponseWriter, request *http.Request, rawPath string) {
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeHTTPError(response, http.StatusBadRequest, "page_id_required")
		return
	}
	isLinks := len(parts) > 1 && parts[len(parts)-1] == "links"
	if isLinks {
		parts = parts[:len(parts)-1]
	}
	pageID, err := url.PathUnescape(strings.Join(parts, "/"))
	if err != nil || !validPathID(pageID) {
		writeHTTPError(response, http.StatusBadRequest, "page_id_invalid")
		return
	}
	if request.Method != http.MethodGet {
		writeHTTPError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if isLinks {
		result, linkErr := handler.dependencies.Query.Links(request.Context(), handler.dependencies.Scope, domain.PageID(pageID), domain.ReadLimits{})
		if linkErr != nil {
			writeServiceError(response, linkErr)
			return
		}
		writeJSON(response, http.StatusOK, result)
		return
	}
	result, readErr := handler.dependencies.Query.Page(request.Context(), handler.dependencies.Scope, domain.PageID(pageID), domain.ReadLimits{})
	if readErr != nil {
		writeServiceError(response, readErr)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *httpHandler) requireOperator(response http.ResponseWriter, request *http.Request) bool {
	token := strings.TrimSpace(handler.dependencies.OperatorToken)
	if token == "" {
		writeHTTPError(response, http.StatusUnauthorized, "operator_authorization_required")
		return false
	}
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if strings.HasPrefix(authorization, "Bearer ") {
		authorization = strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(authorization)) != 1 {
		response.Header().Set("WWW-Authenticate", "Bearer")
		writeHTTPError(response, http.StatusUnauthorized, "operator_authorization_required")
		return false
	}
	return true
}

func (handler *httpHandler) do(ctx context.Context, fn func(context.Context) error) error {
	if handler.dependencies.Worker == nil {
		return fn(ctx)
	}
	return handler.dependencies.Worker.do(ctx, fn)
}

func trustedEnvelope(scope domain.ScopeRef, envelope domain.SourceEnvelope) (domain.SourceEnvelope, error) {
	if envelope.Scope == "" {
		envelope.Scope = scope
	}
	if envelope.Scope != scope {
		return domain.SourceEnvelope{}, fmt.Errorf("source scope is not authorized: %w", app.ErrQueryInvalid)
	}
	return envelope, nil
}

func validPathID(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.HasPrefix(value, "/") && !strings.Contains(value, "\x00") && !strings.Contains(value, "../") && !strings.Contains(value, `/..`)
}

func decodeJSON(response http.ResponseWriter, request *http.Request, destination any) error {
	body := http.MaxBytesReader(response, request.Body, maxHTTPBodyBytes)
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(destination); err != nil {
		writeHTTPError(response, http.StatusBadRequest, "invalid_json")
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeHTTPError(response, http.StatusBadRequest, "multiple_json_values")
		return fmt.Errorf("request contains multiple JSON values")
	}
	return nil
}

func writeServiceError(response http.ResponseWriter, err error) {
	status, class := classifyServiceError(err)
	writeHTTPError(response, status, class)
}

func classifyServiceError(err error) (int, string) {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, app.ErrPageNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, app.ErrQueryInvalid), errors.Is(err, app.ErrFilingInvalid):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, app.ErrProjection):
		return http.StatusServiceUnavailable, "service_unavailable"
	case errors.Is(err, app.ErrOperationNotApplyable):
		return http.StatusConflict, "operation_not_applyable"
	default:
		return http.StatusUnprocessableEntity, "operation_failed"
	}
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		return
	}
}

func writeHTTPError(response http.ResponseWriter, status int, class string) {
	writeJSON(response, status, map[string]string{"error": class})
}
