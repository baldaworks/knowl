package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowlmcp "github.com/baldaworks/knowl/pkg/knowl/mcp"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type compactLimitContent struct{ app.ContentStore }
type compactLimitOperations struct{ app.OperationStore }
type compactLimitIndex struct{ app.SearchIndex }

const compactLimitQuery = "oversized"

func (compactLimitIndex) Search(context.Context, domain.ScopeRef, string, domain.ReadLimits, []domain.SourceID) ([]domain.PageReference, error) {
	return []domain.PageReference{{ID: compactLimitQuery, Title: strings.Repeat("x", app.MaxPublicRetrieveResultBytes), Snippet: "evidence"}}, nil
}

func TestCompactRetrieveOversizedErrorParity(t *testing.T) {
	t.Parallel()

	query, err := app.NewQueryService(compactLimitContent{}, compactLimitOperations{}, compactLimitIndex{}, nil, app.QueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Dependencies{Scope: httpTestScope, Query: query})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/retrieve?query="+compactLimitQuery, nil))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("REST status = %d, body=%s", response.Code, response.Body.String())
	}
	var restFailure knowlapi.ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &restFailure); err != nil {
		t.Fatal(err)
	}
	if restFailure.Error != readLimitExceededCode {
		t.Fatalf("REST error = %#v", restFailure)
	}

	registry, err := knowlmcp.NewServer(query, new(app.IngestService), &httpRecordingWaker{}, httpTestScope, domain.ReadLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Call(t.Context(), "knowl_retrieve", map[string]any{httpQueryArgument: compactLimitQuery}); !errors.Is(err, knowlmcp.ErrReadLimit) {
		t.Fatalf("MCP error = %v, want read limit", err)
	}
}
