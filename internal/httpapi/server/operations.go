package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const inlineSourceAdapter = "inline"

func (handler *handler) GetHealth(response http.ResponseWriter, _ *http.Request) {
	health := knowlapi.GetHealth200JSONResponse{
		Service: serviceName,
		Status:  knowlapi.Ok,
	}
	_ = health.VisitGetHealthResponse(response)
}

func (handler *handler) GetReady(response http.ResponseWriter, _ *http.Request) {
	if !handler.dependencies.Ready() {
		ready := knowlapi.GetReady503JSONResponse{Service: serviceName, Status: knowlapi.NotReady}
		_ = ready.VisitGetReadyResponse(response)
		return
	}
	scope := string(handler.dependencies.Scope)
	ready := knowlapi.GetReady200JSONResponse{Service: serviceName, Status: knowlapi.Ready, Scope: &scope}
	_ = ready.VisitGetReadyResponse(response)
}

func (handler *handler) RetrieveKnowledge(response http.ResponseWriter, request *http.Request, params knowlapi.RetrieveKnowledgeParams) {
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

func (handler *handler) IngestKnowledge(response http.ResponseWriter, request *http.Request) {
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

func (handler *handler) GetOperation(response http.ResponseWriter, request *http.Request, operationID knowlapi.OperationID) {
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

func (handler *handler) do(ctx context.Context, fn func(context.Context) error) error {
	if handler.dependencies.Doer == nil {
		return fn(ctx)
	}
	return handler.dependencies.Doer.Do(ctx, fn)
}
