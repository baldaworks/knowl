package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/internal/httpapi/trustedrequest"
)

const (
	authTestRetrievePath = "/v1/retrieve"
	authTestIngestPath   = "/v1/ingest"
)

func TestOperatorAuth(t *testing.T) {
	t.Parallel()

	const token = "local-secret"
	next := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	handler := WithOperatorAuth(next, token)

	tests := []struct {
		name          string
		path          string
		authorization string
		trusted       bool
		wantStatus    int
	}{
		{name: "health remains public", path: "/healthz", wantStatus: http.StatusNoContent},
		{name: "missing token", path: authTestRetrievePath, wantStatus: http.StatusUnauthorized},
		{name: "wrong token", path: authTestIngestPath, authorization: "Bearer wrong", wantStatus: http.StatusUnauthorized},
		{name: "valid token", path: "/v1/operations/op-1", authorization: "Bearer " + token, wantStatus: http.StatusNoContent},
		{name: "mcp requires token", path: mcpEndpointPath, wantStatus: http.StatusUnauthorized},
		{name: "trusted local workflow", path: authTestIngestPath, trusted: true, wantStatus: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://knowl"+test.path, nil)
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			if test.trusted {
				request = trustedrequest.Mark(request)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if test.wantStatus == http.StatusUnauthorized {
				if response.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Fatalf("WWW-Authenticate = %q, want Bearer", response.Header().Get("WWW-Authenticate"))
				}
				if !strings.Contains(response.Body.String(), `"error":"unauthorized"`) {
					t.Fatalf("body = %q, want structured unauthorized error", response.Body.String())
				}
			}
		})
	}
}

func TestOperatorAuthDisabledWithoutToken(t *testing.T) {
	t.Parallel()

	handler := WithOperatorAuth(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}), " ")
	request := httptest.NewRequest(http.MethodGet, "http://knowl"+authTestRetrievePath, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestFacadePolicy(t *testing.T) {
	t.Parallel()

	const token = "facade-secret"
	tests := []struct {
		name       string
		method     string
		path       string
		policy     FacadePolicy
		bearer     string
		trusted    bool
		wantStatus int
		wantCalls  int
	}{
		{name: "public read", method: http.MethodGet, path: authTestRetrievePath, policy: FacadePolicy{ReadOnly: true}, wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "public write denied", method: http.MethodPost, path: authTestIngestPath, policy: FacadePolicy{ReadOnly: true}, wantStatus: http.StatusForbidden},
		{name: "token read missing bearer", method: http.MethodGet, path: authTestRetrievePath, policy: FacadePolicy{RequireToken: true, ReadOnly: true}, wantStatus: http.StatusUnauthorized},
		{name: "token read", method: http.MethodGet, path: authTestRetrievePath, policy: FacadePolicy{RequireToken: true, ReadOnly: true}, bearer: token, wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "token write", method: http.MethodPost, path: authTestIngestPath, policy: FacadePolicy{RequireToken: true}, bearer: token, wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "exact mcp protocol post", method: http.MethodPost, path: mcpEndpointPath, policy: FacadePolicy{ReadOnly: true, ProtocolHTTP: true}, wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "exact mcp protocol delete", method: http.MethodDelete, path: mcpEndpointPath, policy: FacadePolicy{ReadOnly: true, ProtocolHTTP: true}, wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "mcp prefix post denied", method: http.MethodPost, path: "/mcp/unknown", policy: FacadePolicy{ReadOnly: true, ProtocolHTTP: true}, wantStatus: http.StatusForbidden},
		{name: "mcp prefix delete denied", method: http.MethodDelete, path: "/mcp/unknown", policy: FacadePolicy{ReadOnly: true, ProtocolHTTP: true}, wantStatus: http.StatusForbidden},
		{name: "trusted write bypass", method: http.MethodPost, path: authTestIngestPath, policy: FacadePolicy{RequireToken: true, ReadOnly: true}, trusted: true, wantStatus: http.StatusNoContent, wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler := WithFacadePolicy(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				calls++
				response.WriteHeader(http.StatusNoContent)
			}), token, test.policy)
			request := httptest.NewRequest(test.method, "http://knowl"+test.path, nil)
			if test.bearer != "" {
				request.Header.Set("Authorization", "Bearer "+test.bearer)
			}
			if test.trusted {
				request = trustedrequest.Mark(request)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || calls != test.wantCalls {
				t.Fatalf("status/calls = %d/%d, want %d/%d", response.Code, calls, test.wantStatus, test.wantCalls)
			}
			if test.wantStatus == http.StatusForbidden && !strings.Contains(response.Body.String(), `"error":"read_only"`) {
				t.Fatalf("body = %q, want read_only", response.Body.String())
			}
		})
	}
}
