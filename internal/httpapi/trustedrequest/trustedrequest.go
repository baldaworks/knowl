package trustedrequest

import (
	"context"
	"net/http"
)

type contextKey struct{}

// Mark annotates one in-process HTTP request as trusted local workflow traffic.
func Mark(request *http.Request) *http.Request {
	if request == nil {
		return nil
	}
	return request.WithContext(context.WithValue(request.Context(), contextKey{}, true))
}

// IsMarked reports whether ctx carries the trusted local workflow marker.
func IsMarked(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	trusted, _ := ctx.Value(contextKey{}).(bool)
	return trusted
}
