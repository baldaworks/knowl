package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/baldaworks/knowl/internal/httpapi/trustedrequest"
)

const mcpEndpointPath = "/mcp"

// FacadePolicy controls admission and write access for one mounted facade.
type FacadePolicy struct {
	RequireToken bool
	ReadOnly     bool
	ProtocolHTTP bool
}

// WithFacadePolicy enforces one normalized facade policy.
// Trusted in-process workflows retain their existing bypass.
func WithFacadePolicy(next http.Handler, token string, policy FacadePolicy) http.Handler {
	configured := strings.TrimSpace(token)
	configuredDigest := sha256.Sum256([]byte(configured))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		if trustedrequest.IsMarked(request.Context()) {
			next.ServeHTTP(response, request)
			return
		}
		if policy.RequireToken {
			scheme, supplied, ok := strings.Cut(request.Header.Get("Authorization"), " ")
			suppliedDigest := sha256.Sum256([]byte(supplied))
			if configured == "" || !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare(configuredDigest[:], suppliedDigest[:]) != 1 {
				response.Header().Set("WWW-Authenticate", "Bearer")
				writeHTTPError(response, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		if policy.ReadOnly && request.Method != http.MethodGet && request.Method != http.MethodHead {
			protocolMethod := request.Method == http.MethodPost || request.Method == http.MethodDelete
			if !policy.ProtocolHTTP || !protocolMethod || request.URL.Path != mcpEndpointPath {
				writeHTTPError(response, http.StatusForbidden, "read_only")
				return
			}
		}
		next.ServeHTTP(response, request)
	})
}

// WithOperatorAuth preserves the existing token-only wrapper for callers that
// do not own a normalized facade policy.
func WithOperatorAuth(next http.Handler, token string) http.Handler {
	protected := WithFacadePolicy(next, token, FacadePolicy{RequireToken: strings.TrimSpace(token) != ""})
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !requiresOperatorAuth(request.URL.Path) {
			next.ServeHTTP(response, request)
			return
		}
		protected.ServeHTTP(response, request)
	})
}

func requiresOperatorAuth(path string) bool {
	return strings.HasPrefix(path, "/operator/v1/") || strings.HasPrefix(path, "/ui/fragments/") || strings.HasPrefix(path, "/v1/") || path == mcpEndpointPath || strings.HasPrefix(path, "/mcp/")
}
