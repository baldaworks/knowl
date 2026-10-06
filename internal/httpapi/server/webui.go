package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/internal/webui"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const uiInternalErrorCode = "internal_error"

// NewWebHandler mounts no routes itself. Composition mounts its public shell and
// protected fragments on the same host listener only when web is enabled.
func NewWebHandler(service *app.OperatorService, dependencies Dependencies, token string) (http.Handler, error) {
	ui, err := webui.New(webui.Dependencies{Operator: service, OperationStatus: httpOperationStatus, EmbeddingsEnabled: dependencies.EmbeddingsEnabled,
		Retrieve: func(ctx context.Context, query string, sources []string) (knowlapi.RetrieveResult, error) {
			if dependencies.Query == nil {
				return knowlapi.RetrieveResult{}, app.ErrOperatorCapabilityUnavailable
			}
			ids := make([]domain.SourceID, len(sources))
			for i, source := range sources {
				ids[i] = domain.SourceID(source)
			}
			result, readErr := dependencies.Query.Query(ctx, dependencies.Scope, strings.TrimSpace(query), domain.ReadLimits{}, ids)
			return safeUIRetrieve(result), readErr
		},
		Operation: func(ctx context.Context, id string) (knowlapi.OperationResult, error) {
			if dependencies.Query == nil {
				return knowlapi.OperationResult{}, app.ErrOperatorCapabilityUnavailable
			}
			result, readErr := dependencies.Query.Operation(ctx, dependencies.Scope, domain.OperationID(strings.TrimSpace(id)))
			if readErr != nil {
				return knowlapi.OperationResult{}, readErr
			}
			return safeUIOperation(result), nil
		}})
	if err != nil {
		return nil, err
	}
	fragments := WithOperatorAuth(WithOperatorReadBoundary(http.HandlerFunc(ui.Fragments), dependencies.Ready), token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/ui/fragments/") {
			ui.ServeHTTP(w, r)
			return
		}
		buffer := newBufferedResponseWriter()
		fragments.ServeHTTP(buffer, r)
		if buffer.statusCode >= 400 && buffer.header.Get("Content-Type") == "application/json" {
			var failure struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(buffer.body.Bytes(), &failure)
			if failure.Error == "" {
				failure.Error = uiInternalErrorCode
			}
			copyHeaders(w.Header(), buffer.header)
			ui.Error(w, buffer.statusCode, failure.Error)
			return
		}
		buffer.writeTo(w)
	}), nil
}
func safeUIRetrieve(result app.QueryResult) knowlapi.RetrieveResult {
	mapped := httpRetrieveResult(result)
	for i := range mapped.Citations {
		mapped.Citations[i].Path = ""
	}
	for i := range mapped.Evidence {
		item := &mapped.Evidence[i]
		item.URI = webui.SafeURI(item.URI)
		item.OKF = nil
		for j := range item.SourceDocuments {
			item.SourceDocuments[j].URI = webui.SafeURI(item.SourceDocuments[j].URI)
		}
	}
	return mustConvertJSON[knowlapi.RetrieveResult](mapped)
}
func safeUIOperation(result domain.Operation) knowlapi.OperationResult {
	mapped := httpOperationResult(result)
	mapped.Failure = nil
	return mustConvertJSON[knowlapi.OperationResult](mapped)
}
