package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	httpPageID       = "concepts/rest-page"
	httpPageMarkdown = "---\nid: concepts/rest-page\ntype: concept\ntitle: REST Page\nsource_refs: []\n---\n# REST Page\n\n[Related](related.md)\n"
	pageDeniedScope  = domain.ScopeRef("denied-ref")
)

type pageScopeAuthorizer struct{}

func (pageScopeAuthorizer) Authorize(_ context.Context, scope domain.ScopeRef, _ app.OperationKind) error {
	if scope == pageDeniedScope {
		return errors.New("denied")
	}
	return nil
}

type pageHTTPContent struct{ app.ContentStore }

func (pageHTTPContent) ReadPages(_ context.Context, scope domain.ScopeRef, ids []domain.PageID, _ domain.ReadLimits) ([]domain.PageSnapshot, error) {
	if scope != httpTestScope || len(ids) != 1 || ids[0] != httpPageID {
		return nil, nil
	}
	return []domain.PageSnapshot{{
		ID: httpPageID, Title: "REST Page", Content: httpPageMarkdown, Digest: "rest-page-digest",
		UpdatedAt: time.Date(2026, 11, 27, 1, 2, 3, 0, time.UTC),
	}}, nil
}

type pageHTTPIndex struct{ app.SearchIndex }

func (pageHTTPIndex) Links(_ context.Context, scope domain.ScopeRef, page domain.PageID, _ domain.ReadLimits) ([]domain.LinkReference, error) {
	if scope != httpTestScope || page != httpPageID {
		return nil, errors.New("unexpected link lookup")
	}
	return []domain.LinkReference{{From: httpPageID, To: "concepts/related", Relation: "related"}}, nil
}

type pageHTTPOperations struct{ app.OperationStore }

func TestHTTPGetPageReturnsCompletePageAndDirectLinks(t *testing.T) {
	query, err := app.NewQueryService(pageHTTPContent{}, pageHTTPOperations{}, pageHTTPIndex{}, nil, app.QueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(name domain.ScopeName) (domain.ScopeRef, error) {
		if name == "denied" {
			return pageDeniedScope, nil
		}
		return httpTestScope, nil
	}
	handler := NewHandler(Dependencies{Scope: httpTestScope, Query: query, ResolveScope: resolve, Authorizer: pageScopeAuthorizer{}})

	request := httptest.NewRequest(http.MethodGet, "/v1/pages/concepts%2Frest-page", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GetPage status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}
	var result knowlapi.PageNavigationResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Scope != httpTestScope || result.Page.PageId != httpPageID || result.Page.Markdown != httpPageMarkdown || result.Page.Version != result.Page.Digest || result.Version != result.Page.Digest || !result.Page.Untrusted {
		t.Fatalf("GetPage result = %#v", result)
	}
	if len(result.Links) != 1 || result.Links[0].From != httpPageID || result.Links[0].To != "concepts/related" || !result.Links[0].Untrusted {
		t.Fatalf("GetPage links = %#v", result.Links)
	}

	denied := httptest.NewRequest(http.MethodGet, "/v1/pages/concepts%2Frest-page?scope=denied", nil)
	deniedResponse := httptest.NewRecorder()
	handler.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden || !strings.Contains(deniedResponse.Body.String(), `"error":"scope_forbidden"`) {
		t.Fatalf("denied GetPage = %d, %s", deniedResponse.Code, deniedResponse.Body.String())
	}
}

func TestHTTPGetPageErrorClassification(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{app.ErrQueryInvalid, http.StatusBadRequest, invalidRequestCode},
		{app.ErrPageNotFound, http.StatusNotFound, notFoundCode},
		{app.ErrPageResultTooLarge, http.StatusRequestEntityTooLarge, readLimitExceededCode},
		{app.ErrPublicRetrieveResultTooLarge, http.StatusRequestEntityTooLarge, readLimitExceededCode},
		{app.ErrOperatorReadLimitExceeded, http.StatusRequestEntityTooLarge, readLimitExceededCode},
		{app.ErrOperatorWorkspaceUnavailable, http.StatusServiceUnavailable, serviceUnavailableCode},
		{context.DeadlineExceeded, http.StatusServiceUnavailable, serviceUnavailableCode},
	}
	for _, test := range tests {
		status, code := classifyServiceError(test.err)
		if status != test.status || code != test.code {
			t.Errorf("classifyServiceError(%v) = %d, %q; want %d, %q", test.err, status, code, test.status, test.code)
		}
	}
}
