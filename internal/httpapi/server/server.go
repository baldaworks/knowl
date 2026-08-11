package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const maxBodyBytes = 8 << 20

const serviceName = "knowl"

type Doer interface {
	Do(ctx context.Context, fn func(context.Context) error) error
}

type Dependencies struct {
	Scope  domain.ScopeRef
	Ingest *app.IngestService
	Query  *app.QueryService
	Ready  func() bool
	Doer   Doer
}

func NewHandler(dependencies Dependencies) http.Handler {
	if dependencies.Ready == nil {
		dependencies.Ready = func() bool { return true }
	}
	server := &handler{dependencies: dependencies}
	mux := &statusMux{inner: http.NewServeMux()}
	generated := knowlapi.HandlerWithOptions(server, knowlapi.StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: generatedBindingError,
	})
	return &compatHandler{dependencies: dependencies, next: generated}
}

type handler struct {
	dependencies Dependencies
}

type compatHandler struct {
	dependencies Dependencies
	next         http.Handler
}

func (handler *compatHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
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
	_, _ = response.Write(writer.body.Bytes())
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

func decodeJSON(response http.ResponseWriter, request *http.Request, destination any) error {
	body := http.MaxBytesReader(response, request.Body, maxBodyBytes)
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

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeHTTPError(response http.ResponseWriter, status int, class string) {
	writeJSON(response, status, map[string]string{"error": class})
}
