package knowl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	pgstore "github.com/baldaworks/knowl/pkg/knowl/store/postgres"
	sqlitestore "github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const maxHTTPBodyBytes = 8 << 20

const serviceName = "knowl"
const inlineSourceAdapter = "inline"

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
	if path == "" {
		path = "/"
	}
	rawPath := request.URL.RawPath
	if rawPath == "" {
		rawPath = path
	} else {
		rawPath = strings.TrimSuffix(rawPath, "/")
		if rawPath == "" {
			rawPath = "/"
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

func generatedBindingError(response http.ResponseWriter, _ *http.Request, err error) {
	var required *knowlapi.RequiredParamError
	if errors.As(err, &required) && required.ParamName == "query" {
		writeHTTPError(response, http.StatusBadRequest, "query_required")
		return
	}
	var invalid *knowlapi.InvalidParamFormatError
	if errors.As(err, &invalid) {
		switch invalid.ParamName {
		case "operation_id":
			writeHTTPError(response, http.StatusBadRequest, "operation_id_invalid")
		case "query":
			writeHTTPError(response, http.StatusBadRequest, "query_required")
		default:
			writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		}
		return
	}
	writeHTTPError(response, http.StatusBadRequest, "invalid_request")
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

func (handler *httpHandler) RetrieveKnowledge(response http.ResponseWriter, request *http.Request, params knowlapi.RetrieveKnowledgeParams) {
	result, err := handler.dependencies.Query.Query(
		request.Context(),
		handler.dependencies.Scope,
		strings.TrimSpace(params.Query),
		domain.ReadLimits{},
	)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[knowlapi.RetrieveResult](httpRetrieveResult(result))
	_ = knowlapi.RetrieveKnowledge200JSONResponse(transport).VisitRetrieveKnowledgeResponse(response)
}

func (handler *httpHandler) IngestKnowledge(response http.ResponseWriter, request *http.Request) {
	var ingestRequest knowlapi.IngestRequest
	if err := decodeJSON(response, request, &ingestRequest); err != nil {
		return
	}
	envelope, err := httpIngestEnvelope(handler.dependencies.Scope, ingestRequest)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	var result app.IngestResult
	work := func(ctx context.Context) error {
		var workErr error
		result, workErr = handler.dependencies.Ingest.Ingest(ctx, envelope)
		if workErr != nil {
			return workErr
		}
		if result.Operation.Status != domain.StatusAwaitingReview {
			return nil
		}
		applied, applyErr := handler.dependencies.Ingest.Apply(ctx, handler.dependencies.Scope, result.Operation.ID)
		if applyErr != nil {
			return applyErr
		}
		result.Operation = applied.Operation
		return nil
	}
	if err := handler.do(request.Context(), work); err != nil {
		writeServiceError(response, err)
		return
	}
	transport := mustConvertJSON[knowlapi.IngestResult](httpIngestResult(result.Operation))
	_ = knowlapi.IngestKnowledge200JSONResponse(transport).VisitIngestKnowledgeResponse(response)
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
	transport := mustConvertJSON[knowlapi.OperationResult](httpOperationResult(result))
	_ = knowlapi.GetOperation200JSONResponse(transport).VisitGetOperationResponse(response)
}

type httpEvidenceItem struct {
	PageID     domain.PageID `json:"page_id"`
	Title      string        `json:"title"`
	Snippet    string        `json:"snippet"`
	SourceRefs []string      `json:"source_refs,omitempty"`
	Untrusted  bool          `json:"untrusted"`
}

type httpRetrieveResponse struct {
	Query     string             `json:"query"`
	Evidence  []httpEvidenceItem `json:"evidence"`
	Citations []app.Citation     `json:"citations,omitempty"`
}

type httpIngestResponse struct {
	OperationID domain.OperationID `json:"operation_id"`
	Status      string             `json:"status"`
}

type httpOperationResponse struct {
	ID        domain.OperationID `json:"id"`
	Status    string             `json:"status"`
	UpdatedAt time.Time          `json:"updated_at"`
	Failure   *domain.Failure    `json:"failure,omitempty"`
}

func httpRetrieveResult(result app.QueryResult) httpRetrieveResponse {
	evidence := make([]httpEvidenceItem, 0, len(result.Pages))
	for _, page := range result.Pages {
		evidence = append(evidence, httpEvidenceItem{
			PageID:     page.ID,
			Title:      page.Title,
			Snippet:    page.Snippet,
			SourceRefs: append([]string(nil), page.SourceRefs...),
			Untrusted:  page.Untrusted,
		})
	}
	citations := make([]app.Citation, len(result.Citations))
	copy(citations, result.Citations)
	return httpRetrieveResponse{
		Query:     result.Query,
		Evidence:  evidence,
		Citations: citations,
	}
}

func httpIngestResult(operation domain.Operation) httpIngestResponse {
	return httpIngestResponse{
		OperationID: operation.ID,
		Status:      httpOperationStatus(operation.Status),
	}
}

func httpOperationResult(operation domain.Operation) httpOperationResponse {
	return httpOperationResponse{
		ID:        operation.ID,
		Status:    httpOperationStatus(operation.Status),
		UpdatedAt: operation.UpdatedAt,
		Failure:   operation.Failure,
	}
}

func httpOperationStatus(status domain.OperationStatus) string {
	switch status {
	case domain.StatusApplying:
		return "running"
	case domain.StatusCommitted:
		return "completed"
	case domain.StatusFailed:
		return "failed"
	default:
		return "queued"
	}
}

func httpIngestEnvelope(scope domain.ScopeRef, request knowlapi.IngestRequest) (domain.SourceEnvelope, error) {
	content := strings.TrimSpace(valueOrEmpty(request.Content))
	uri := strings.TrimSpace(valueOrEmpty(request.Uri))
	mediaType := strings.TrimSpace(valueOrEmpty(request.MediaType))
	origin := strings.TrimSpace(valueOrEmpty(request.Origin))
	idempotencyKey := strings.TrimSpace(valueOrEmpty(request.IdempotencyKey))

	switch {
	case content == "" && uri == "":
		return domain.SourceEnvelope{}, fmt.Errorf("one of %q or %q is required: %w", "content", "uri", app.ErrQueryInvalid)
	case content != "" && uri != "":
		return domain.SourceEnvelope{}, fmt.Errorf("only one of %q or %q is allowed: %w", "content", "uri", app.ErrQueryInvalid)
	}

	payload := []byte(content)
	adapter := inlineSourceAdapter
	sourceHint := origin
	if uri != "" {
		payload = []byte(uri)
		adapter = "uri"
		sourceHint = uri
		if mediaType == "" {
			mediaType = "text/uri-list"
		}
	}
	if mediaType == "" {
		mediaType = "text/plain"
	}
	digest := sha256.Sum256(payload)
	digestText := hex.EncodeToString(digest[:])
	sourceID := httpStableSourceID(sourceHint, idempotencyKey, digestText)
	version := httpStableSourceVersion(idempotencyKey, digestText)
	return domain.SourceEnvelope{
		Scope:     scope,
		Source:    domain.SourceRef{Adapter: adapter, ID: sourceID},
		Version:   domain.SourceVersion{Version: version, Digest: digestText},
		MediaType: mediaType,
		Content:   payload,
	}, nil
}

func httpStableSourceID(origin, idempotencyKey, digest string) string {
	switch {
	case origin != "":
		return origin
	case idempotencyKey != "":
		return idempotencyKey
	default:
		return "source-" + digest[:16]
	}
}

func httpStableSourceVersion(idempotencyKey, digest string) string {
	if idempotencyKey != "" {
		return idempotencyKey
	}
	return "sha256-" + digest[:16]
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
	case errors.Is(err, os.ErrNotExist), errors.Is(err, app.ErrPageNotFound), errors.Is(err, pgstore.ErrNotFound), errors.Is(err, sqlitestore.ErrNotFound):
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
