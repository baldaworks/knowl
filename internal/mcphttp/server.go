// Package mcphttp exposes Knowl's transport-neutral MCP registry over the
// standard Streamable HTTP transport.
package mcphttp

import (
	"fmt"
	"net/http"

	"github.com/baldaworks/knowl/internal/mcpsdk"
	knowlmcp "github.com/baldaworks/knowl/pkg/knowl/mcp"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewHandler adapts one trusted-scope Knowl MCP registry to Streamable HTTP.
func NewHandler(registry *knowlmcp.Server, ready func() bool) (http.Handler, error) {
	if registry == nil {
		return nil, fmt.Errorf("knowl MCP registry is required")
	}
	if ready == nil {
		ready = func() bool { return true }
	}

	server, err := mcpsdk.NewServer(registry)
	if err != nil {
		return nil, err
	}

	transport := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server },
		&sdkmcp.StreamableHTTPOptions{},
	)
	return &handler{
		next:  transport,
		ready: ready,
	}, nil
}

type handler struct {
	next  http.Handler
	ready func() bool
}

func (handler *handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if !handler.ready() {
		http.Error(response, "knowl MCP service is not ready", http.StatusServiceUnavailable)
		return
	}
	handler.next.ServeHTTP(response, request)
}
