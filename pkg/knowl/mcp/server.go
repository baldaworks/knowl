// Package mcp exposes bounded Knowl MCP tools over a trusted scope.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

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

const schemaTypeKey = "type"
const schemaStringType = "string"
const inlineSourceAdapter = "inline"

// RetrieveResult is the bounded evidence payload returned by knowl_retrieve.
type RetrieveResult struct {
	Query     string         `json:"query"`
	Evidence  []EvidenceItem `json:"evidence"`
	Citations []app.Citation `json:"citations,omitempty"`
}

// EvidenceItem is one bounded page-derived evidence record.
type EvidenceItem struct {
	PageID     knowl.PageID `json:"page_id"`
	Title      string       `json:"title"`
	Snippet    string       `json:"snippet"`
	SourceRefs []string     `json:"source_refs,omitempty"`
	Untrusted  bool         `json:"untrusted"`
}

// IngestResult is the simplified MCP-facing write response.
type IngestResult struct {
	OperationID knowl.OperationID `json:"operation_id"`
	Status      string            `json:"status"`
}

// OperationResult is the simplified MCP-facing durable operation model.
type OperationResult struct {
	ID        knowl.OperationID `json:"id"`
	Status    string            `json:"status"`
	UpdatedAt time.Time         `json:"updated_at"`
	Failure   *knowl.Failure    `json:"failure,omitempty"`
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
			required: map[string]any{schemaTypeKey: schemaStringType},
		},
	}
}

func ingestSchema() map[string]any {
	return map[string]any{
		schemaTypeKey: "object",
		"properties": map[string]any{
			"content":         map[string]any{schemaTypeKey: schemaStringType},
			"uri":             map[string]any{schemaTypeKey: schemaStringType},
			"media_type":      map[string]any{schemaTypeKey: schemaStringType},
			"origin":          map[string]any{schemaTypeKey: schemaStringType},
			"idempotency_key": map[string]any{schemaTypeKey: schemaStringType},
		},
	}
}

func retrieveResult(result app.QueryResult) RetrieveResult {
	evidence := make([]EvidenceItem, 0, len(result.Pages))
	for _, page := range result.Pages {
		evidence = append(evidence, EvidenceItem{
			PageID:     page.ID,
			Title:      page.Title,
			Snippet:    page.Snippet,
			SourceRefs: append([]string(nil), page.SourceRefs...),
			Untrusted:  page.Untrusted,
		})
	}
	citations := make([]app.Citation, len(result.Citations))
	copy(citations, result.Citations)
	return RetrieveResult{Query: result.Query, Evidence: evidence, Citations: citations}
}

func operationResult(operation knowl.Operation) OperationResult {
	return OperationResult{
		ID:        operation.ID,
		Status:    publicOperationStatus(operation.Status),
		UpdatedAt: operation.UpdatedAt,
		Failure:   operation.Failure,
	}
}

func publicOperationStatus(status knowl.OperationStatus) string {
	switch status {
	case knowl.StatusApplying:
		return "running"
	case knowl.StatusCommitted:
		return "completed"
	case knowl.StatusFailed:
		return "failed"
	default:
		return "queued"
	}
}

func ingestEnvelope(scope knowl.ScopeRef, arguments map[string]any) (knowl.SourceEnvelope, error) {
	content, _ := optionalString(arguments, "content")
	uri, _ := optionalString(arguments, "uri")
	mediaType, _ := optionalString(arguments, "media_type")
	origin, _ := optionalString(arguments, "origin")
	idempotencyKey, _ := optionalString(arguments, "idempotency_key")

	switch {
	case content == "" && uri == "":
		return knowl.SourceEnvelope{}, fmt.Errorf("one of %q or %q is required: %w", "content", "uri", ErrInvalidArguments)
	case content != "" && uri != "":
		return knowl.SourceEnvelope{}, fmt.Errorf("only one of %q or %q is allowed: %w", "content", "uri", ErrInvalidArguments)
	}

	payload := []byte(content)
	adapter := inlineSourceAdapter
	sourceHint := origin
	if uri != "" {
		payload = []byte(uri)
		adapter = "uri"
		sourceHint = uri
		if mediaType == "" {
			mediaType = "text/uri-list"
		}
	}
	if mediaType == "" {
		mediaType = "text/plain"
	}
	digest := sha256.Sum256(payload)
	digestText := hex.EncodeToString(digest[:])
	sourceID := stableSourceID(sourceHint, idempotencyKey, digestText)
	version := stableSourceVersion(idempotencyKey, digestText)
	return knowl.SourceEnvelope{
		Scope:     scope,
		Source:    knowl.SourceRef{Adapter: adapter, ID: sourceID},
		Version:   knowl.SourceVersion{Version: version, Digest: digestText},
		MediaType: mediaType,
		Content:   payload,
	}, nil
}

func stableSourceID(origin, idempotencyKey, digest string) string {
	switch {
	case origin != "":
		return strings.TrimSpace(origin)
	case idempotencyKey != "":
		return strings.TrimSpace(idempotencyKey)
	default:
		return "source-" + digest[:16]
	}
}

func stableSourceVersion(idempotencyKey, digest string) string {
	if idempotencyKey != "" {
		return strings.TrimSpace(idempotencyKey)
	}
	return "sha256-" + digest[:16]
}

func optionalString(arguments map[string]any, name string) (string, error) {
	value, present := arguments[name]
	if !present {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string: %w", name, ErrInvalidArguments)
	}
	return strings.TrimSpace(text), nil
}

func cloneMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
