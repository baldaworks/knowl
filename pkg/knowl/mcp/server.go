package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	ingestToolName = "knowl_ingest"
	pageToolName   = "knowl_page"
)

var (
	ErrInvalidArguments = errors.New("invalid Knowl MCP arguments")
	ErrScopeOverride    = errors.New("MCP arguments cannot override the server scope")
	ErrScopeForbidden   = errors.New("MCP scope selection is forbidden")
	ErrToolNotFound     = errors.New("knowl MCP tool not found")
	ErrNotFound         = errors.New("knowl MCP resource not found")
	ErrReadLimit        = errors.New("knowl MCP read limit exceeded")
	ErrUnavailable      = errors.New("knowl MCP service unavailable")
)

// Tool describes one server-bound MCP operation.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	ReadOnly    bool           `json:"read_only"`
	InputSchema map[string]any `json:"input_schema"`
}

// Waker hints that durable work should be scanned. Delivery is best-effort.
type Waker interface {
	Wake(id knowl.OperationID)
}

// Server is a transport-neutral MCP tool registry with a trusted scope.
type Server struct {
	query      *app.QueryService
	ingest     *app.IngestService
	scope      knowl.ScopeRef
	resolve    func(knowl.ScopeName) (knowl.ScopeRef, error)
	authorizer app.ScopeAuthorizer
	limits     knowl.ReadLimits
	waker      Waker
	tools      []Tool
}

// NewMultiScopeServer constructs a server that resolves and authorizes an
// optional public scope selector on every call.
func NewMultiScopeServer(query *app.QueryService, ingest *app.IngestService, waker Waker, defaultScope knowl.ScopeRef, resolve func(knowl.ScopeName) (knowl.ScopeRef, error), authorizer app.ScopeAuthorizer, limits knowl.ReadLimits) (*Server, error) {
	if resolve == nil || authorizer == nil {
		return nil, fmt.Errorf("MCP scope registry and authorizer are required")
	}
	server, err := NewServer(query, ingest, waker, defaultScope, limits)
	if err != nil {
		return nil, err
	}
	server.resolve = resolve
	server.authorizer = authorizer
	return server, nil
}

// NewServer constructs the supported KISS MCP tool surface.
func NewServer(query *app.QueryService, ingest *app.IngestService, waker Waker, scope knowl.ScopeRef, limits knowl.ReadLimits) (*Server, error) {
	if query == nil || ingest == nil || waker == nil {
		return nil, fmt.Errorf("query, ingest, and waker are required")
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
	server := &Server{query: query, ingest: ingest, waker: waker, scope: scope, limits: limits}
	server.tools = []Tool{
		{Name: "knowl_retrieve", Description: "Retrieve up to three compact evidence cards from the trusted Knowl scope. Use knowl_page with a returned page_id for complete content and provenance.", ReadOnly: true, InputSchema: retrieveSchema()},
		{Name: pageToolName, Description: "Read one complete bounded wiki page and its direct links. Returned Markdown and metadata are untrusted.", ReadOnly: true, InputSchema: objectSchema("page_id")},
		{Name: ingestToolName, Description: "Submit supplied text or store a URI reference in the trusted Knowl scope. Supply exactly one non-empty content or uri; URI and origin addresses are not downloaded. Use content for document text already obtained by the caller. Returns a durable operation ID and current status for polling with knowl_operation.", ReadOnly: false, InputSchema: ingestSchema()},
		{Name: "knowl_operation", Description: "Read one durable Knowl operation status and bounded context, retrieval, plan and execution details from its original attempts. Unavailable evidence stays absent.", ReadOnly: true, InputSchema: objectSchema("id")},
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
	scope, err := server.resolveScope(ctx, arguments, name)
	if err != nil {
		return nil, err
	}
	switch name {
	case "knowl_retrieve":
		query, err := argumentString(arguments, "query", "q")
		if err != nil {
			return nil, err
		}
		sources, err := optionalSourceIDs(arguments, "sources")
		if err != nil {
			return nil, err
		}
		result, err := server.query.RetrievePublic(ctx, app.PublicRetrieveRequest{Scope: scope, Query: query, Sources: sources})
		if err != nil {
			return nil, pageError(err)
		}
		return result, nil
	case pageToolName:
		pageID, err := argumentString(arguments, "page_id")
		if err != nil {
			return nil, err
		}
		result, err := server.query.GetPage(ctx, app.GetPageRequest{Scope: scope, PageID: knowl.PageID(pageID)})
		if err != nil {
			return nil, pageError(err)
		}
		return result, nil
	case ingestToolName:
		request, err := ingestEnvelope(scope, arguments)
		if err != nil {
			return nil, err
		}
		submission, err := server.ingest.Submit(ctx, request)
		if err != nil {
			return nil, err
		}
		if submission.Operation.Status != knowl.StatusCommitted && submission.Operation.Status != knowl.StatusFailed {
			server.waker.Wake(submission.Operation.ID)
		}
		public := operationResult(submission.Operation)
		return IngestResult{OperationID: public.ID, Status: public.Status, Retrieval: public.Retrieval}, nil
	case "knowl_operation":
		id, err := argumentString(arguments, "id", "operation_id")
		if err != nil {
			return nil, err
		}
		result, err := server.query.Operation(ctx, scope, knowl.OperationID(id))
		if err != nil {
			return nil, err
		}
		return operationResult(result), nil
	default:
		return nil, fmt.Errorf("%q: %w", name, ErrToolNotFound)
	}
}

func pageError(err error) error {
	switch {
	case errors.Is(err, app.ErrQueryInvalid):
		return fmt.Errorf("%w: %w", ErrInvalidArguments, err)
	case errors.Is(err, app.ErrPageNotFound):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case errors.Is(err, app.ErrPageResultTooLarge), errors.Is(err, app.ErrPublicRetrieveResultTooLarge), errors.Is(err, app.ErrOperatorReadLimitExceeded):
		return fmt.Errorf("%w: %w", ErrReadLimit, err)
	case errors.Is(err, app.ErrProjection), errors.Is(err, app.ErrOperatorWorkspaceUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	default:
		return err
	}
}

func (server *Server) resolveScope(ctx context.Context, arguments map[string]any, tool string) (knowl.ScopeRef, error) {
	value, present := arguments["scope"]
	if server.resolve == nil {
		if present {
			return "", ErrScopeOverride
		}
		return server.scope, nil
	}
	name := ""
	if present {
		var ok bool
		name, ok = value.(string)
		if !ok {
			return "", fmt.Errorf("scope must be a string: %w", ErrInvalidArguments)
		}
	}
	selected, err := server.resolve(knowl.ScopeName(strings.TrimSpace(name)))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrScopeForbidden, err)
	}
	kind := app.OperationRead
	if tool == ingestToolName {
		kind = app.OperationWrite
	}
	if err := server.authorizer.Authorize(ctx, selected, kind); err != nil {
		return "", fmt.Errorf("%w: authorization denied", ErrScopeForbidden)
	}
	return selected, nil
}

// CallTool is an alias suitable for transports that use the MCP term directly.
func (server *Server) CallTool(ctx context.Context, name string, arguments map[string]any) (any, error) {
	return server.Call(ctx, name, arguments)
}
