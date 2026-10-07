package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOperatorNamespacesRequireAuthentication(t *testing.T) {
	for _, path := range []string{operatorTestPagesRoute, "/ui/fragments/knowledge", "/ui/fragments/wiki-directory"} {
		t.Run(path, func(t *testing.T) {
			handler := WithOperatorAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), "secret")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", response.Code)
			}
			if response.Header().Get("Cache-Control") != operatorTestNoStore {
				t.Fatal("missing no-store")
			}
		})
	}
}
