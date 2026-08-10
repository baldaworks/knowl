package knowl

import (
	"bytes"
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

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/internal/httpapi/trustedrequest"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const maxHTTPBodyBytes = 8 << 20

const serviceName = "knowl"

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
	server := &httpHandler{dependencies: dependencies}
	mux := &statusMux{inner: http.NewServeMux()}
	generated := knowlapi.HandlerWithOptions(server, knowlapi.StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: generatedBindingError,
	})
	return &compatHTTPHandler{dependencies: dependencies, next: generated}
}

type httpHandler struct {
	dependencies httpDependencies
}

type compatHTTPHandler struct {
	dependencies httpDependencies
	next         http.Handler
}

func (handler *compatHTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/v1/") {
		if scope := request.URL.Query().Get("scope"); scope != "" {
			writeHTTPError(response, http.StatusForbidden, "scope_override_forbidden")
			return
		}
		if !handler.dependencies.Ready() {
			writeHTTPError(response, http.StatusServiceUnavailable, "not_ready")
			return
		}
		if request.URL.Path == "/v1/pages/" {
			writeHTTPError(response, http.StatusBadRequest, "page_id_required")
			return
		}
		request = normalizeGeneratedRequest(request)
	}
	handler.next.ServeHTTP(response, request)
}

type statusMux struct {
	inner *http.ServeMux
}

func (mux *statusMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	mux.inner.HandleFunc(pattern, handler)
}

func (mux *statusMux) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	recorder := newBufferedResponseWriter()
	mux.inner.ServeHTTP(recorder, request)
	switch recorder.statusCode {
	case http.StatusNotFound:
		writeHTTPError(response, http.StatusNotFound, "not_found")
	case http.StatusMethodNotAllowed:
		copyHeaders(response.Header(), recorder.header)
		writeHTTPError(response, http.StatusMethodNotAllowed, "method_not_allowed")
	default:
		recorder.writeTo(response)
	}
}

type bufferedResponseWriter struct {
	header     http.Header
	statusCode int
	body       bytes.Buffer
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header)}
}

func (writer *bufferedResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *bufferedResponseWriter) Write(bytesValue []byte) (int, error) {
	if writer.statusCode == 0 {
		writer.statusCode = http.StatusOK
	}
	return writer.body.Write(bytesValue)
}

func (writer *bufferedResponseWriter) WriteHeader(statusCode int) {
	if writer.statusCode != 0 {
		return
	}
	writer.statusCode = statusCode
}

func (writer *bufferedResponseWriter) writeTo(response http.ResponseWriter) {
	copyHeaders(response.Header(), writer.header)
	statusCode := writer.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	response.WriteHeader(statusCode)
	if _, err := response.Write(writer.body.Bytes()); err != nil {
		return
	}
}

func copyHeaders(destination, source http.Header) {
	for key, values := range source {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func normalizeGeneratedRequest(request *http.Request) *http.Request {
	path := strings.TrimSuffix(request.URL.Path, "/")
	rawPath := request.URL.RawPath
	if rawPath == "" {
		rawPath = path
	}
	const pagePrefix = "/v1/pages/"
	const linksSuffix = "/links"
	if strings.HasPrefix(path, pagePrefix) {
		remainder := strings.TrimPrefix(path, pagePrefix)
		switch {
		case strings.HasSuffix(remainder, linksSuffix):
			pageID := strings.TrimSuffix(remainder, linksSuffix)
			rawPath = pagePrefix + url.PathEscape(pageID) + linksSuffix
		case remainder != "":
			rawPath = pagePrefix + url.PathEscape(remainder)
		}
	}
	if path == request.URL.Path && rawPath == request.URL.RawPath {
		return request
	}
	cloned := request.Clone(request.Context())
	cloned.URL = cloneURL(request.URL)
	cloned.URL.Path = path
	cloned.URL.RawPath = rawPath
	return cloned
}

func cloneURL(source *url.URL) *url.URL {
	cloned := *source
	return &cloned
}

func generatedBindingError(response http.ResponseWriter, request *http.Request, err error) {
	var required *knowlapi.RequiredParamError
	if errors.As(err, &required) && required.ParamName == "q" {
		writeHTTPError(response, http.StatusBadRequest, "query_required")
		return
	}
	var invalid *knowlapi.InvalidParamFormatError
	if errors.As(err, &invalid) {
		switch invalid.ParamName {
		case "operation_id":
			writeHTTPError(response, http.StatusBadRequest, "operation_id_invalid")
		case "page_id":
			writeHTTPError(response, http.StatusBadRequest, "page_id_invalid")
		default:
			writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		}
		return
	}
	writeHTTPError(response, http.StatusBadRequest, "invalid_request")
}

func (handler *httpHandler) GetHealthAlias(response http.ResponseWriter, _ *http.Request) {
	health := knowlapi.GetHealthAlias200JSONResponse{
		Service: serviceName,
		Status:  knowlapi.Ok,
	}
	_ = health.VisitGetHealthAliasResponse(response)
}

func (handler *httpHandler) GetReadyAlias(response http.ResponseWriter, _ *http.Request) {
	if !handler.dependencies.Ready() {
		ready := knowlapi.GetReadyAlias503JSONResponse{Service: serviceName, Status: knowlapi.NotReady}
		_ = ready.VisitGetReadyAliasResponse(response)
		return
	}
	scope := string(handler.dependencies.Scope)
	ready := knowlapi.GetReadyAlias200JSONResponse{Service: serviceName, Status: knowlapi.Ready, Scope: &scope}
	_ = ready.VisitGetReadyAliasResponse(response)
}

func (handler *httpHandler) GetHealth(response http.ResponseWriter, _ *http.Request) {
	health := knowlapi.GetHealth200JSONResponse{
		Service: serviceName,
		Status:  knowlapi.Ok,
	}
	_ = health.VisitGetHealthResponse(response)
}

func (handler *httpHandler) GetReady(response http.ResponseWriter, _ *http.Request) {
	if !handler.dependencies.Ready() {
		ready := knowlapi.GetReady503JSONResponse{Service: serviceName, Status: knowlapi.NotReady}
		_ = ready.VisitGetReadyResponse(response)
		return
	}
	scope := string(handler.dependencies.Scope)
	ready := knowlapi.GetReady200JSONResponse{Service: serviceName, Status: knowlapi.Ready, Scope: &scope}
	_ = ready.VisitGetReadyResponse(response)
}

func (handler *httpHandler) SearchPages(response http.ResponseWriter, request *http.Request, params knowlapi.SearchPagesParams) {
	result, err := handler.dependencies.Query.Search(request.Context(), handler.dependencies.Scope, strings.TrimSpace(params.Q), domain.ReadLimits{})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[[]knowlapi.PageReference](result)
	_ = knowlapi.SearchPages200JSONResponse(transport).VisitSearchPagesResponse(response)
}

func (handler *httpHandler) QueryKnowledge(response http.ResponseWriter, request *http.Request, params knowlapi.QueryKnowledgeParams) {
	result, err := handler.dependencies.Query.Query(request.Context(), handler.dependencies.Scope, strings.TrimSpace(params.Q), domain.ReadLimits{})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[knowlapi.QueryResult](result)
	_ = knowlapi.QueryKnowledge200JSONResponse(transport).VisitQueryKnowledgeResponse(response)
}

func (handler *httpHandler) LintWorkspace(response http.ResponseWriter, request *http.Request) {
	result, err := handler.dependencies.Lint.Lint(request.Context(), handler.dependencies.Scope)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[knowlapi.LintReport](result)
	_ = knowlapi.LintWorkspace200JSONResponse(transport).VisitLintWorkspaceResponse(response)
}

func (handler *httpHandler) LintWorkspaceDashAlias(response http.ResponseWriter, request *http.Request) {
	result, err := handler.dependencies.Lint.Lint(request.Context(), handler.dependencies.Scope)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[knowlapi.LintReport](result)
	_ = knowlapi.LintWorkspaceDashAlias200JSONResponse(transport).VisitLintWorkspaceDashAliasResponse(response)
}

func (handler *httpHandler) LintWorkspaceResultsAlias(response http.ResponseWriter, request *http.Request) {
	result, err := handler.dependencies.Lint.Lint(request.Context(), handler.dependencies.Scope)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[knowlapi.LintReport](result)
	_ = knowlapi.LintWorkspaceResultsAlias200JSONResponse(transport).VisitLintWorkspaceResultsAliasResponse(response)
}

func (handler *httpHandler) IngestSource(response http.ResponseWriter, request *http.Request) {
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
		result, workErr = handler.dependencies.Ingest.Ingest(ctx, envelope)
		return workErr
	}
	if err := handler.do(request.Context(), work); err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[knowlapi.IngestResult](result)
	_ = knowlapi.IngestSource200JSONResponse(transport).VisitIngestSourceResponse(response)
}

func (handler *httpHandler) PreviewSource(response http.ResponseWriter, request *http.Request) {
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
	if err := handler.do(request.Context(), func(ctx context.Context) error {
		var previewErr error
		result, previewErr = handler.dependencies.Ingest.Preview(ctx, envelope)
		return previewErr
	}); err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[knowlapi.IngestResult](result)
	_ = knowlapi.PreviewSource200JSONResponse(transport).VisitPreviewSourceResponse(response)
}

func (handler *httpHandler) FileQueryResult(response http.ResponseWriter, request *http.Request) {
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
	transport := mustConvertJSON[knowlapi.IngestResult](result)
	_ = knowlapi.FileQueryResult200JSONResponse(transport).VisitFileQueryResultResponse(response)
}

func (handler *httpHandler) GetOperation(response http.ResponseWriter, request *http.Request, operationID knowlapi.OperationID) {
	id := strings.TrimSpace(operationID)
	if id == "" {
		writeHTTPError(response, http.StatusBadRequest, "operation_id_invalid")
		return
	}
	result, err := handler.dependencies.Query.Operation(request.Context(), handler.dependencies.Scope, domain.OperationID(id))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[knowlapi.Operation](result)
	_ = knowlapi.GetOperation200JSONResponse(transport).VisitGetOperationResponse(response)
}

func (handler *httpHandler) ApplyOperation(response http.ResponseWriter, request *http.Request, operationID knowlapi.OperationID) {
	if !handler.requireOperator(response, request) {
		return
	}
	id := strings.TrimSpace(operationID)
	if id == "" {
		writeHTTPError(response, http.StatusBadRequest, "operation_id_invalid")
		return
	}
	var result app.ApplyResult
	if err := handler.do(request.Context(), func(ctx context.Context) error {
		var applyErr error
		result, applyErr = handler.dependencies.Ingest.Apply(ctx, handler.dependencies.Scope, domain.OperationID(id))
		return applyErr
	}); err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[knowlapi.ApplyResult](result)
	_ = knowlapi.ApplyOperation200JSONResponse(transport).VisitApplyOperationResponse(response)
}

func (handler *httpHandler) GetOperationStatusAlias(response http.ResponseWriter, request *http.Request, operationID knowlapi.OperationID) {
	id := strings.TrimSpace(operationID)
	if id == "" {
		writeHTTPError(response, http.StatusBadRequest, "operation_id_invalid")
		return
	}
	result, err := handler.dependencies.Query.Operation(request.Context(), handler.dependencies.Scope, domain.OperationID(id))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[knowlapi.Operation](result)
	_ = knowlapi.GetOperationStatusAlias200JSONResponse(transport).VisitGetOperationStatusAliasResponse(response)
}

func (handler *httpHandler) GetOperationLegacyStatusAlias(response http.ResponseWriter, request *http.Request, operationID knowlapi.OperationID) {
	id := strings.TrimSpace(operationID)
	if id == "" {
		writeHTTPError(response, http.StatusBadRequest, "operation_id_invalid")
		return
	}
	result, err := handler.dependencies.Query.Operation(request.Context(), handler.dependencies.Scope, domain.OperationID(id))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[knowlapi.Operation](result)
	_ = knowlapi.GetOperationLegacyStatusAlias200JSONResponse(transport).VisitGetOperationLegacyStatusAliasResponse(response)
}

func (handler *httpHandler) GetPage(response http.ResponseWriter, request *http.Request, pageID knowlapi.PageID) {
	id := pageID
	if strings.TrimSpace(id) == "" {
		writeHTTPError(response, http.StatusBadRequest, "page_id_required")
		return
	}
	if !validPathID(id) {
		writeHTTPError(response, http.StatusBadRequest, "page_id_invalid")
		return
	}
	result, readErr := handler.dependencies.Query.Page(request.Context(), handler.dependencies.Scope, domain.PageID(id), domain.ReadLimits{})
	if readErr != nil {
		writeServiceError(response, readErr)
		return
	}
	transport := mustConvertJSON[knowlapi.PageSnapshot](result)
	_ = knowlapi.GetPage200JSONResponse(transport).VisitGetPageResponse(response)
}

func (handler *httpHandler) GetPageLinks(response http.ResponseWriter, request *http.Request, pageID knowlapi.PageID) {
	id := pageID
	if strings.TrimSpace(id) == "" {
		writeHTTPError(response, http.StatusBadRequest, "page_id_required")
		return
	}
	if !validPathID(id) {
		writeHTTPError(response, http.StatusBadRequest, "page_id_invalid")
		return
	}
	result, err := handler.dependencies.Query.Links(request.Context(), handler.dependencies.Scope, domain.PageID(id), domain.ReadLimits{})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[[]knowlapi.LinkReference](result)
	_ = knowlapi.GetPageLinks200JSONResponse(transport).VisitGetPageLinksResponse(response)
}

func (handler *httpHandler) requireOperator(response http.ResponseWriter, request *http.Request) bool {
	if trustedrequest.IsMarked(request.Context()) {
		return true
	}
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

func mustConvertJSON[T any](value any) T {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Errorf("encode generated transport value: %w", err))
	}
	var target T
	if err := json.Unmarshal(encoded, &target); err != nil {
		panic(fmt.Errorf("decode generated transport value: %w", err))
	}
	return target
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
