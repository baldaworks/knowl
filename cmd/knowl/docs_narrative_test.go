package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicDocsMatchServiceFirstKISSNarrative(t *testing.T) {
	repoRoot := testRepoRoot(t)
	tests := []struct {
		path string
		want []string
	}{
		{
			path: filepath.Join(repoRoot, "README.md"),
			want: []string{
				"service-first LLM-wiki knowledge service",
				"knowl_retrieve",
				"/v1/retrieve",
				"sidecar service",
				"not:\n\n- session memory",
			},
		},
		{
			path: filepath.Join(repoRoot, "docs", "operations.md"),
			want: []string{
				"Authoritative contract",
				"/v1/retrieve?query=...",
				"/v1/ingest",
				"/v1/operations/{operation_id}",
				"MCP is the primary agent-facing interface.",
			},
		},
		{
			path: filepath.Join(repoRoot, "docs", "integrations.md"),
			want: []string{
				"Agent-facing data plane:     MCP",
				"Knowl is not:",
				"Neither MCP nor HTTP exposes direct page CRUD",
			},
		},
		{
			path: filepath.Join(repoRoot, "docs", "architecture.md"),
			want: []string{
				"bounded three-tool MCP adapter",
				"same three business operations",
				"Sidecar/service deployment is the baseline runtime shape",
			},
		},
	}

	for _, test := range tests {
		t.Run(filepath.Base(test.path), func(t *testing.T) {
			content, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatalf("read %s: %v", test.path, err)
			}
			text := string(content)
			for _, want := range test.want {
				if !strings.Contains(text, want) {
					t.Fatalf("%s missing %q", test.path, want)
				}
			}
		})
	}
}

func TestPublicDocsDoNotDescribeRetiredPublicSurface(t *testing.T) {
	repoRoot := testRepoRoot(t)
	paths := []string{
		filepath.Join(repoRoot, "README.md"),
		filepath.Join(repoRoot, "docs", "operations.md"),
		filepath.Join(repoRoot, "docs", "integrations.md"),
		filepath.Join(repoRoot, "docs", "architecture.md"),
	}

	unwanted := []string{
		"/v1/ingest/preview",
		"/v1/query/file",
		"/v1/search",
		"/v1/pages/",
		"/v1/lint",
		"Authorization: Bearer",
		"five server-scoped read-only tools",
	}

	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(content)
		for _, bad := range unwanted {
			if strings.Contains(text, bad) {
				t.Fatalf("%s contains retired public-surface reference %q", path, bad)
			}
		}
	}
}
