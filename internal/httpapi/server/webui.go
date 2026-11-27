package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/internal/webui"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const uiInternalErrorCode = "internal_error"

type WebOptions struct {
	AuthMode string
	Token    string
}

// NewWebHandler mounts no routes itself. Composition mounts its public shell and
// protected fragments on the same host listener only when web is enabled.
func NewWebHandler(service *app.OperatorService, dependencies Dependencies, options WebOptions) (http.Handler, error) {
	type scopedUI struct {
		ui        *webui.Handler
		fragments http.Handler
	}
	var mu sync.Mutex
	cache := make(map[domain.ScopeRef]scopedUI)
	build := func(scope domain.ScopeRef, name domain.ScopeName, operator *app.OperatorService) (scopedUI, error) {
		mu.Lock()
		defer mu.Unlock()
		if existing, ok := cache[scope]; ok {
			return existing, nil
		}
		ui, err := webui.New(webui.Dependencies{AuthMode: webui.AuthMode(options.AuthMode), ScopeName: name, Operator: operator, OperationStatus: httpOperationStatus, EmbeddingsEnabled: dependencies.EmbeddingsEnabled,
			Retrieve: func(ctx context.Context, query string, sources []string) (webui.RetrieveResult, error) {
				if dependencies.Query == nil {
					return webui.RetrieveResult{}, app.ErrOperatorCapabilityUnavailable
				}
				ids := make([]domain.SourceID, len(sources))
				for i, source := range sources {
					ids[i] = domain.SourceID(source)
				}
				result, readErr := dependencies.Query.Query(ctx, scope, strings.TrimSpace(query), domain.ReadLimits{}, ids)
				return safeUIRetrieve(result), readErr
			},
			Operation: func(ctx context.Context, id string) (knowlapi.OperationResult, error) {
				if dependencies.Query == nil {
					return knowlapi.OperationResult{}, app.ErrOperatorCapabilityUnavailable
				}
				result, readErr := dependencies.Query.Operation(ctx, scope, domain.OperationID(strings.TrimSpace(id)))
				if readErr != nil {
					return knowlapi.OperationResult{}, readErr
				}
				return safeUIOperation(result), nil
			}})
		if err != nil {
			return scopedUI{}, err
		}
		result := scopedUI{ui: ui, fragments: WithOperatorAuth(WithOperatorReadBoundary(http.HandlerFunc(ui.Fragments), dependencies.Ready), options.Token)}
		cache[scope] = result
		return result, nil
	}
	if dependencies.OperatorFor == nil {
		dependencies.OperatorFor = func(domain.ScopeRef) *app.OperatorService { return service }
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope := dependencies.Scope
		name := dependencies.PublicScope
		if dependencies.ResolveScope != nil {
			requested := domain.ScopeName(strings.TrimSpace(r.URL.Query().Get("scope")))
			selected, err := dependencies.ResolveScope(requested)
			if err != nil || dependencies.Authorizer == nil || dependencies.Authorizer.Authorize(r.Context(), selected, app.OperationRead) != nil {
				writeHTTPError(w, http.StatusForbidden, "scope_forbidden")
				return
			}
			scope = selected
			if requested != "" {
				name = requested
			}
			r = cloneRequestWithoutScope(r)
		}
		scoped, err := build(scope, name, dependencies.OperatorFor(scope))
		if err != nil {
			writeHTTPError(w, http.StatusInternalServerError, uiInternalErrorCode)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/ui/fragments/") {
			scoped.ui.ServeHTTP(w, r)
			return
		}
		buffer := newBufferedResponseWriter()
		scoped.fragments.ServeHTTP(buffer, r)
		if buffer.statusCode >= 400 && buffer.header.Get("Content-Type") == "application/json" {
			var failure struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(buffer.body.Bytes(), &failure)
			if failure.Error == "" {
				failure.Error = uiInternalErrorCode
			}
			copyHeaders(w.Header(), buffer.header)
			scoped.ui.Error(w, buffer.statusCode, failure.Error)
			return
		}
		buffer.writeTo(w)
	}), nil
}

func cloneRequestWithoutScope(request *http.Request) *http.Request {
	cloned := request.Clone(request.Context())
	query := cloned.URL.Query()
	query.Del("scope")
	cloned.URL.RawQuery = query.Encode()
	return cloned
}
func safeUIRetrieve(result app.QueryResult) webui.RetrieveResult {
	mapped := webui.RetrieveResult{Retrieval: app.PublicRetrievalStatus(result.Retrieval), Query: result.Query, Evidence: make([]webui.RetrieveEvidence, 0, len(result.Pages)), Citations: make([]webui.RetrieveCitation, 0, len(result.Citations))}
	for _, page := range result.Pages {
		item := webui.RetrieveEvidence{PageId: string(page.ID), Title: page.Title, Snippet: page.Snippet, SourceRefs: append([]string(nil), page.SourceRefs...), Untrusted: page.Untrusted}
		documents := append([]domain.SourceDocument(nil), page.SourceDocuments...)
		if len(documents) == 0 && page.SourceDocument != nil {
			documents = append(documents, *page.SourceDocument)
		}
		for _, document := range documents {
			item.SourceDocuments = append(item.SourceDocuments, webui.RetrieveSourceDocument{SourceId: string(document.SourceID), DocumentId: string(document.DocumentID), Revision: document.Revision, Uri: webui.SafeURI(document.URI)})
		}
		mapped.Evidence = append(mapped.Evidence, item)
	}
	for _, citation := range result.Citations {
		item := webui.RetrieveCitation{Kind: citation.Kind, Reference: citation.Reference, Untrusted: citation.Untrusted}
		if citation.PageID != "" {
			value := string(citation.PageID)
			item.PageId = &value
		}
		if citation.SourceRef != "" {
			value := citation.SourceRef
			item.SourceRef = &value
		}
		mapped.Citations = append(mapped.Citations, item)
	}
	return mapped
}
func safeUIOperation(result domain.Operation) knowlapi.OperationResult {
	mapped := httpOperationResult(result)
	mapped.Failure = nil
	return mustConvertJSON[knowlapi.OperationResult](mapped)
}
