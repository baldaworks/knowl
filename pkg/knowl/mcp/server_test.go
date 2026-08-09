package mcp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
)

func TestServerExposesOnlyBoundedReadToolsAndPinsScope(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(workspace.Root(), "state.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	content := []byte("source")
	accepted, err := workspace.AcceptSource(ctx, knowl.SourceEnvelope{
		Scope:   "local",
		Source:  knowl.SourceRef{Adapter: "fixture", ID: "source"},
		Version: knowl.SourceVersion{Version: "1", Digest: digest(content)},
		Content: content,
	})
	if err != nil {
		t.Fatalf("accept source: %v", err)
	}
	page := "---\nid: entities/one\ntitle: One\ntype: entity\nsource_refs:\n  - fixture:source@1\n---\n# One\n"
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "entities", "one.md"), []byte(page), 0o600); err != nil {
		t.Fatalf("write page: %v", err)
	}
	snapshot, err := workspace.Snapshot(ctx, "local")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("rebuild projection: %v", err)
	}
	query, err := app.NewQueryService(workspace, store, store, nil, app.QueryOptions{})
	if err != nil {
		t.Fatalf("new query service: %v", err)
	}
	lint, err := app.NewLintService(workspace, store, app.LintOptions{})
	if err != nil {
		t.Fatalf("new lint service: %v", err)
	}
	server, err := mcp.NewServer(query, lint, accepted.Scope, knowl.ReadLimits{Pages: 1})
	if err != nil {
		t.Fatalf("new MCP server: %v", err)
	}
	tools := server.Tools()
	if len(tools) != 5 {
		t.Fatalf("tool count = %d, want 5", len(tools))
	}
	for _, tool := range tools {
		if !tool.ReadOnly {
			t.Fatalf("tool %q is not read-only", tool.Name)
		}
	}
	value, err := server.Call(ctx, "read-page", map[string]any{"id": "entities/one"})
	if err != nil {
		t.Fatalf("read-page: %v", err)
	}
	pageResult, ok := value.(knowl.PageSnapshot)
	if !ok || !pageResult.Untrusted || pageResult.ID != "entities/one" {
		t.Fatalf("read-page result = %#v", value)
	}
	if _, err := server.Call(ctx, "search", map[string]any{"query": "One", "scope": "other"}); !errors.Is(err, mcp.ErrScopeOverride) {
		t.Fatalf("scope override error = %v, want scope override", err)
	}
	if _, err := server.Call(ctx, "ingest", nil); !errors.Is(err, mcp.ErrToolNotFound) {
		t.Fatalf("write tool error = %v, want not found", err)
	}
}

func digest(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}
