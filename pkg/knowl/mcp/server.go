// Package mcp exposes bounded, read-only Knowl tools.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

var (
	ErrInvalidArguments = errors.New("invalid Knowl MCP arguments")
	ErrScopeOverride    = errors.New("MCP arguments cannot override the server scope")
	ErrToolNotFound     = errors.New("knowl MCP tool not found")
)

// Tool describes one server-bound, read-only MCP operation.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	ReadOnly    bool           `json:"read_only"`
	InputSchema map[string]any `json:"input_schema"`
}

// Server is a transport-neutral MCP tool registry with a trusted scope.
type Server struct {
	query  *app.QueryService
	lint   *app.LintService
	scope  knowl.ScopeRef
	limits knowl.ReadLimits
	tools  []Tool
}

const schemaTypeKey = "type"

// NewServer constructs the supported read-only tool surface.
func NewServer(query *app.QueryService, lint *app.LintService, scope knowl.ScopeRef, limits knowl.ReadLimits) (*Server, error) {
	if query == nil || lint == nil {
		return nil, fmt.Errorf("query and lint services are required")
	}
	if strings.TrimSpace(string(scope)) == "" {
		return nil, fmt.Errorf("MCP scope is required")
	}
	if limits == (knowl.ReadLimits{}) {
		limits = app.DefaultReadLimits()
	}
	if limits.Pages < 0 || limits.Bytes < 0 || limits.Characters < 0 || limits.Depth < 0 || limits.Deadline < 0 {
		return nil, fmt.Errorf("MCP read limits are invalid: %w", ErrInvalidArguments)
	}
	server := &Server{query: query, lint: lint, scope: scope, limits: limits}
	server.tools = []Tool{
		{Name: "search", Description: "Search the scoped Knowl wiki", ReadOnly: true, InputSchema: objectSchema("query")},
		{Name: "read-page", Description: "Read one bounded scoped Knowl wiki page", ReadOnly: true, InputSchema: objectSchema("id")},
		{Name: "links", Description: "Read one bounded scoped Knowl link neighborhood", ReadOnly: true, InputSchema: objectSchema("id")},
		{Name: "operation-status", Description: "Read one scoped redacted operation status", ReadOnly: true, InputSchema: objectSchema("id")},
		{Name: "lint-results", Description: "Run deterministic and suggestion-only lint for the trusted scope", ReadOnly: true, InputSchema: map[string]any{schemaTypeKey: "object"}},
	}
	return server, nil
}

// Scope returns the trusted server scope. Tool arguments cannot change it.
func (server *Server) Scope() knowl.ScopeRef { return server.scope }

// Tools returns a copy of the read-only tool definitions.
func (server *Server) Tools() []Tool {
	tools := make([]Tool, len(server.tools))
	copy(tools, server.tools)
	for index := range tools {
		tools[index].InputSchema = cloneMap(tools[index].InputSchema)
	}
	return tools
}

// ListTools is an alias suitable for MCP transports that use list terminology.
func (server *Server) ListTools() []Tool { return server.Tools() }

// Call invokes one read-only tool using the server's scope and limits.
func (server *Server) Call(ctx context.Context, name string, arguments map[string]any) (any, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	if _, present := arguments["scope"]; present {
		return nil, ErrScopeOverride
	}
	switch name {
	case "search":
		query, err := argumentString(arguments, "query", "q")
		if err != nil {
			return nil, err
		}
		return server.query.Search(ctx, server.scope, query, server.limits)
	case "read-page":
		id, err := argumentString(arguments, "id", "page_id")
		if err != nil {
			return nil, err
		}
		return server.query.Page(ctx, server.scope, knowl.PageID(id), server.limits)
	case "links":
		id, err := argumentString(arguments, "id", "page_id")
		if err != nil {
			return nil, err
		}
		return server.query.Links(ctx, server.scope, knowl.PageID(id), server.limits)
	case "operation-status":
		id, err := argumentString(arguments, "id", "operation_id")
		if err != nil {
			return nil, err
		}
		return server.query.Operation(ctx, server.scope, knowl.OperationID(id))
	case "lint-results":
		return server.lint.Lint(ctx, server.scope)
	default:
		return nil, fmt.Errorf("%q: %w", name, ErrToolNotFound)
	}
}

// CallTool is an alias suitable for transports that use the MCP term directly.
func (server *Server) CallTool(ctx context.Context, name string, arguments map[string]any) (any, error) {
	return server.Call(ctx, name, arguments)
}

func argumentString(arguments map[string]any, names ...string) (string, error) {
	for _, name := range names {
		value, present := arguments[name]
		if !present {
			continue
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("argument %q must be a non-empty string: %w", name, ErrInvalidArguments)
		}
		return strings.TrimSpace(text), nil
	}
	return "", fmt.Errorf("one of %v is required: %w", names, ErrInvalidArguments)
}

func objectSchema(required string) map[string]any {
	return map[string]any{
		schemaTypeKey: "object",
		"required":    []string{required},
		"properties": map[string]any{
			required: map[string]any{schemaTypeKey: "string"},
		},
	}
}

func cloneMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
