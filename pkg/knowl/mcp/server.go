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

// Tool describes one server-bound MCP operation.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	ReadOnly    bool           `json:"read_only"`
	InputSchema map[string]any `json:"input_schema"`
}

// Server is a transport-neutral MCP tool registry with a trusted scope.
type Server struct {
	query  *app.QueryService
	ingest *app.IngestService
	scope  knowl.ScopeRef
	limits knowl.ReadLimits
	tools  []Tool
}

// NewServer constructs the supported KISS MCP tool surface.
func NewServer(query *app.QueryService, ingest *app.IngestService, scope knowl.ScopeRef, limits knowl.ReadLimits) (*Server, error) {
	if query == nil || ingest == nil {
		return nil, fmt.Errorf("query and ingest services are required")
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
	server := &Server{query: query, ingest: ingest, scope: scope, limits: limits}
	server.tools = []Tool{
		{Name: "knowl_retrieve", Description: "Retrieve bounded evidence from the trusted Knowl scope", ReadOnly: true, InputSchema: objectSchema("query")},
		{Name: "knowl_ingest", Description: "Submit one bounded source to the trusted Knowl ingest pipeline", ReadOnly: false, InputSchema: ingestSchema()},
		{Name: "knowl_operation", Description: "Read one durable Knowl operation status", ReadOnly: true, InputSchema: objectSchema("id")},
	}
	return server, nil
}

// Scope returns the trusted server scope. Tool arguments cannot change it.
func (server *Server) Scope() knowl.ScopeRef { return server.scope }

// Tools returns a copy of the tool definitions.
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

// Call invokes one MCP tool using the server's scope and limits.
func (server *Server) Call(ctx context.Context, name string, arguments map[string]any) (any, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	if _, present := arguments["scope"]; present {
		return nil, ErrScopeOverride
	}
	switch name {
	case "knowl_retrieve":
		query, err := argumentString(arguments, "query", "q")
		if err != nil {
			return nil, err
		}
		result, err := server.query.Query(ctx, server.scope, query, server.limits)
		if err != nil {
			return nil, err
		}
		return retrieveResult(result), nil
	case "knowl_ingest":
		request, err := ingestEnvelope(server.scope, arguments)
		if err != nil {
			return nil, err
		}
		result, err := server.ingest.Ingest(ctx, request)
		if err != nil {
			return nil, err
		}
		if result.Operation.Status == knowl.StatusAwaitingReview {
			applied, applyErr := server.ingest.Apply(ctx, server.scope, result.Operation.ID)
			if applyErr != nil {
				return nil, applyErr
			}
			result.Operation = applied.Operation
		}
		public := operationResult(result.Operation)
		return IngestResult{OperationID: public.ID, Status: public.Status}, nil
	case "knowl_operation":
		id, err := argumentString(arguments, "id", "operation_id")
		if err != nil {
			return nil, err
		}
		result, err := server.query.Operation(ctx, server.scope, knowl.OperationID(id))
		if err != nil {
			return nil, err
		}
		return operationResult(result), nil
	default:
		return nil, fmt.Errorf("%q: %w", name, ErrToolNotFound)
	}
}

// CallTool is an alias suitable for transports that use the MCP term directly.
func (server *Server) CallTool(ctx context.Context, name string, arguments map[string]any) (any, error) {
	return server.Call(ctx, name, arguments)
}
