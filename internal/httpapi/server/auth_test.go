package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/internal/httpapi/trustedrequest"
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
		{name: "missing token", path: "/v1/retrieve", wantStatus: http.StatusUnauthorized},
		{name: "wrong token", path: "/v1/ingest", authorization: "Bearer wrong", wantStatus: http.StatusUnauthorized},
		{name: "valid token", path: "/v1/operations/op-1", authorization: "Bearer " + token, wantStatus: http.StatusNoContent},
		{name: "mcp requires token", path: "/mcp", wantStatus: http.StatusUnauthorized},
		{name: "trusted local workflow", path: "/v1/ingest", trusted: true, wantStatus: http.StatusNoContent},
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
	request := httptest.NewRequest(http.MethodGet, "http://knowl/v1/retrieve", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}
