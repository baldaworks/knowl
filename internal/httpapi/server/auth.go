package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/baldaworks/knowl/internal/httpapi/trustedrequest"
)

// WithOperatorAuth protects public business endpoints when token is configured.
// Health probes remain public, and trusted in-process workflows bypass HTTP auth.
func WithOperatorAuth(next http.Handler, token string) http.Handler {
	configured := strings.TrimSpace(token)

	configuredDigest := sha256.Sum256([]byte(configured))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if requiresOperatorAuth(request.URL.Path) {
			response.Header().Set("Cache-Control", "no-store")
		}
		if configured == "" || !requiresOperatorAuth(request.URL.Path) || trustedrequest.IsMarked(request.Context()) {
			next.ServeHTTP(response, request)
			return
		}
		scheme, supplied, ok := strings.Cut(request.Header.Get("Authorization"), " ")
		suppliedDigest := sha256.Sum256([]byte(supplied))
		if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare(configuredDigest[:], suppliedDigest[:]) != 1 {
			response.Header().Set("WWW-Authenticate", "Bearer")
			writeHTTPError(response, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(response, request)
	})
}

func requiresOperatorAuth(path string) bool {
	return strings.HasPrefix(path, "/operator/v1/") || strings.HasPrefix(path, "/ui/fragments/") || strings.HasPrefix(path, "/v1/") || path == "/mcp" || strings.HasPrefix(path, "/mcp/")
}
