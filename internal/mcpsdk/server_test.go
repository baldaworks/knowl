package mcpsdk_test

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/internal/mcpsdk"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowlmcp "github.com/baldaworks/knowl/pkg/knowl/mcp"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	sdkPageID       = domain.PageID("concepts/sdk-page")
	sdkLargePageID  = domain.PageID("concepts/sdk-large-page")
	sdkPageMarkdown = "# SDK Page\n\nComplete untrusted Markdown.\n"
	sdkPageTool     = "knowl_page"
	sdkPageArgument = "page_id"
	sdkRetrieveTool = "knowl_retrieve"
	sdkSnippet      = "compact structured evidence"
)

type sdkPageContent struct{ app.ContentStore }

func (sdkPageContent) ReadPages(_ context.Context, _ domain.ScopeRef, ids []domain.PageID, _ domain.ReadLimits) ([]domain.PageSnapshot, error) {
	if len(ids) != 1 {
		return nil, nil
	}
	if ids[0] == sdkLargePageID {
		return []domain.PageSnapshot{{ID: sdkLargePageID, Title: "Large", Content: strings.Repeat("x", app.MaxGetPageResultBytes), Digest: "large-digest"}}, nil
	}
	if ids[0] != sdkPageID {
		return nil, nil
	}
	return []domain.PageSnapshot{{ID: sdkPageID, Title: "SDK Page", Content: sdkPageMarkdown, Digest: "sdk-page-digest"}}, nil
}

type sdkPageIndex struct{ app.SearchIndex }

func (sdkPageIndex) Search(context.Context, domain.ScopeRef, string, domain.ReadLimits, []domain.SourceID) ([]domain.PageReference, error) {
	return []domain.PageReference{{ID: sdkPageID, Title: "SDK Page", Snippet: sdkSnippet, SourceRefs: []string{"raw:sdk@1"}}}, nil
}

func (sdkPageIndex) Links(context.Context, domain.ScopeRef, domain.PageID, domain.ReadLimits) ([]domain.LinkReference, error) {
	return []domain.LinkReference{}, nil
}

type sdkPageOperations struct{ app.OperationStore }
type sdkWaker struct{}

func (sdkWaker) Wake(domain.OperationID) {}

func TestPageToolKeepsMarkdownOnlyInStructuredContent(t *testing.T) {
	query, err := app.NewQueryService(sdkPageContent{}, sdkPageOperations{}, sdkPageIndex{}, nil, app.QueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := knowlmcp.NewServer(query, new(app.IngestService), sdkWaker{}, "local", domain.ReadLimits{})
	if err != nil {
		t.Fatal(err)
	}
	server, err := mcpsdk.NewServerWithOptions(registry, mcpsdk.ServerOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverTransport) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		cancel()
		<-done
	})

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	foundPage := false
	for _, tool := range listed.Tools {
		if tool.Name == "knowl_ingest" {
			t.Fatal("read-only SDK server exposed knowl_ingest")
		}
		if tool.Name == sdkPageTool {
			foundPage = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		}
	}
	if !foundPage {
		t.Fatal("read-only SDK server did not expose read-only knowl_page")
	}

	retrieved, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: sdkRetrieveTool, Arguments: map[string]any{"query": "sdk"}})
	if err != nil || retrieved.IsError || len(retrieved.Content) != 1 {
		t.Fatalf("knowl_retrieve = %#v, %v", retrieved, err)
	}
	retrieveText, ok := retrieved.Content[0].(*sdkmcp.TextContent)
	if !ok || retrieveText.Text == "" || strings.Contains(retrieveText.Text, sdkSnippet) {
		t.Fatalf("knowl_retrieve text duplicated evidence: %#v", retrieved.Content)
	}
	retrieveStructured, ok := retrieved.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("knowl_retrieve structured content type = %T", retrieved.StructuredContent)
	}
	evidence, ok := retrieveStructured["evidence"].([]any)
	if !ok || len(evidence) != 1 || evidence[0].(map[string]any)["snippet"] != sdkSnippet {
		t.Fatalf("knowl_retrieve structured content = %#v", retrieved.StructuredContent)
	}

	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: sdkPageTool, Arguments: map[string]any{sdkPageArgument: string(sdkPageID)}})
	if err != nil || result.IsError {
		t.Fatalf("knowl_page = %#v, %v", result, err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("knowl_page content = %#v", result.Content)
	}
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok || text.Text == "" || strings.Contains(text.Text, sdkPageMarkdown) {
		t.Fatalf("knowl_page text content duplicated Markdown: %#v", result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("knowl_page structured content type = %T", result.StructuredContent)
	}
	page, ok := structured["page"].(map[string]any)
	if !ok || page["page_id"] != string(sdkPageID) || page["markdown"] != sdkPageMarkdown || page["untrusted"] != true {
		t.Fatalf("knowl_page structured content = %#v", result.StructuredContent)
	}

	missing, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: sdkPageTool, Arguments: map[string]any{sdkPageArgument: "concepts/missing"}})
	if err != nil || !missing.IsError {
		t.Fatalf("missing knowl_page = %#v, %v", missing, err)
	}
	failure, ok := missing.StructuredContent.(map[string]any)
	if !ok || failure["error"] != "not_found" {
		t.Fatalf("missing knowl_page failure = %#v", missing.StructuredContent)
	}

	large, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: sdkPageTool, Arguments: map[string]any{sdkPageArgument: string(sdkLargePageID)}})
	if err != nil || !large.IsError {
		t.Fatalf("large knowl_page = %#v, %v", large, err)
	}
	largeFailure, ok := large.StructuredContent.(map[string]any)
	if !ok || largeFailure["error"] != "read_limit_exceeded" {
		t.Fatalf("large knowl_page failure = %#v", large.StructuredContent)
	}
}
