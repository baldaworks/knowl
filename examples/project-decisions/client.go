package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultMCPEndpoint  = "http://127.0.0.1:8080/mcp"
	defaultRunTimeout   = 30 * time.Second
	defaultPollInterval = 25 * time.Millisecond
	projectQuestion     = "Why was Badger selected for session memory, and what replaced it?"
	retrieveTool        = "knowl_retrieve"
	ingestTool          = "knowl_ingest"
	operationTool       = "knowl_operation"
	decisionOrigin      = "adr-session-memory"
	decisionVersionOne  = "1"
	decisionVersionTwo  = "2"
)

var errToolCall = errors.New("knowl MCP tool call failed")

//go:embed sources/*.md
var sourceFiles embed.FS

type sourceSpec struct {
	Path     string
	Origin   string
	Revision string
}

var sourceManifest = []sourceSpec{
	{Path: "sources/adr-session-memory-v1.md", Origin: decisionOrigin, Revision: decisionVersionOne},
	{Path: "sources/investigation-session-recovery-v1.md", Origin: "investigation-session-recovery", Revision: "1"},
	{Path: "sources/adr-session-memory-v2.md", Origin: decisionOrigin, Revision: decisionVersionTwo},
	{Path: "sources/runbook-session-recovery-v1.md", Origin: "runbook-session-recovery", Revision: "1"},
}

type clientConfig struct {
	Endpoint      string
	OperatorToken string
	HTTPClient    *http.Client
	PollInterval  time.Duration
}

type operationRecord struct {
	Source      string
	Revision    string
	OperationID string
	Status      string
}

type evidenceItem struct {
	PageID     string   `json:"page_id"`
	Title      string   `json:"title"`
	Snippet    string   `json:"snippet"`
	SourceRefs []string `json:"source_refs"`
	Untrusted  bool     `json:"untrusted"`
}

type runResult struct {
	Operations []operationRecord
	Evidence   []evidenceItem
}

func runProjectDecisions(ctx context.Context, config clientConfig) (runResult, error) {
	if strings.TrimSpace(config.Endpoint) == "" {
		config.Endpoint = defaultMCPEndpoint
	}
	if config.PollInterval <= 0 {
		config.PollInterval = defaultPollInterval
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "knowl-project-decisions-host", Version: "v1"}, nil)
	httpClient := authenticatedHTTPClient(config.HTTPClient, config.OperatorToken)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint: config.Endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return runResult{}, fmt.Errorf("connect to Knowl MCP: %w", err)
	}
	defer func() { _ = session.Close() }()

	if err := requirePublicTools(ctx, session); err != nil {
		return runResult{}, err
	}
	result := runResult{Operations: make([]operationRecord, 0, len(sourceManifest))}
	for _, source := range sourceManifest {
		content, err := sourceFiles.ReadFile(source.Path)
		if err != nil {
			return runResult{}, fmt.Errorf("read source fixture %q: %w", source.Path, err)
		}
		var accepted struct {
			OperationID string `json:"operation_id"`
			Status      string `json:"status"`
		}
		if err := callStructured(ctx, session, ingestTool, map[string]any{
			"content": string(content), "origin": source.Origin,
			"idempotency_key": source.Revision, "media_type": "text/markdown",
		}, &accepted); err != nil {
			return runResult{}, fmt.Errorf("ingest %s@%s: %w", source.Origin, source.Revision, err)
		}
		if accepted.OperationID == "" {
			return runResult{}, fmt.Errorf("ingest %s@%s returned an empty operation ID", source.Origin, source.Revision)
		}
		status, err := pollOperation(ctx, session, accepted.OperationID, config.PollInterval)
		if err != nil {
			return runResult{}, fmt.Errorf("poll %s@%s operation %s: %w", source.Origin, source.Revision, accepted.OperationID, err)
		}
		result.Operations = append(result.Operations, operationRecord{
			Source: source.Origin, Revision: source.Revision, OperationID: accepted.OperationID, Status: status,
		})
	}

	var retrieved struct {
		Evidence []evidenceItem `json:"evidence"`
	}
	if err := callStructured(ctx, session, retrieveTool, map[string]any{"query": projectQuestion}, &retrieved); err != nil {
		return runResult{}, fmt.Errorf("retrieve project decision: %w", err)
	}
	if err := validateEvidence(retrieved.Evidence); err != nil {
		return runResult{}, err
	}
	result.Evidence = retrieved.Evidence
	return result, nil
}

func requirePublicTools(ctx context.Context, session *sdkmcp.ClientSession) error {
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list Knowl MCP tools: %w", err)
	}
	names := make([]string, len(listed.Tools))
	for index, tool := range listed.Tools {
		names[index] = tool.Name
	}
	slices.Sort(names)
	want := []string{ingestTool, operationTool, retrieveTool}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		return fmt.Errorf("knowl MCP tools are %q, want exactly %q", names, want)
	}
	return nil
}

func pollOperation(ctx context.Context, session *sdkmcp.ClientSession, id string, interval time.Duration) (string, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		var operation struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := callStructured(ctx, session, operationTool, map[string]any{"id": id}, &operation); err != nil {
			return "", err
		}
		switch operation.Status {
		case "completed":
			return operation.Status, nil
		case "failed":
			return operation.Status, fmt.Errorf("operation reached failed status")
		case "queued", "running":
		default:
			return operation.Status, fmt.Errorf("operation returned unknown status %q", operation.Status)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func callStructured(ctx context.Context, session *sdkmcp.ClientSession, name string, arguments map[string]any, target any) error {
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return err
	}
	if result.IsError {
		return fmt.Errorf("%s: %w", name, errToolCall)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return fmt.Errorf("marshal %s result: %w", name, err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return fmt.Errorf("decode %s result: %w", name, err)
	}
	return nil
}

func validateEvidence(evidence []evidenceItem) error {
	for _, item := range evidence {
		if item.PageID == "decisions/session-memory" && item.Untrusted && strings.TrimSpace(item.Snippet) != "" && len(item.SourceRefs) > 0 {
			return nil
		}
	}
	return fmt.Errorf("retrieve returned no untrusted session-memory evidence with provenance")
}

func hostAnswer(result runResult) (string, error) {
	for _, item := range result.Evidence {
		if item.PageID == "decisions/session-memory" {
			return fmt.Sprintf("Host answer (verify against %s): %s", strings.Join(item.SourceRefs, ", "), item.Snippet), nil
		}
	}
	return "", fmt.Errorf("host cannot answer without session-memory evidence")
}

func authenticatedHTTPClient(base *http.Client, token string) *http.Client {
	client := &http.Client{}
	if base != nil {
		*client = *base
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if strings.TrimSpace(token) != "" {
		client.Transport = bearerTransport{base: transport, token: token}
	}
	return client
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(clone)
}
