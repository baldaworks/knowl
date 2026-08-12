package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicDocumentationSurface(t *testing.T) {
	repoRoot := testRepoRoot(t)
	canonicalFiles := []string{
		"README.md",
		filepath.Join("docs", "design.md"),
		filepath.Join("docs", "operations.md"),
		filepath.Join("docs", "workspace.md"),
		filepath.Join("docs", "sidecar.md"),
		filepath.Join("api", "openapi", "knowl.yaml"),
	}
	for _, relative := range canonicalFiles {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err != nil {
			t.Fatalf("canonical documentation artifact %q: %v", relative, err)
		}
	}

	readme, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	for _, link := range []string{
		"docs/design.md",
		"docs/operations.md",
		"docs/workspace.md",
		"docs/sidecar.md",
		"api/openapi/knowl.yaml",
	} {
		if !strings.Contains(string(readme), link) {
			t.Errorf("README does not link canonical artifact %q", link)
		}
	}

	design, err := os.ReadFile(filepath.Join(repoRoot, "docs", "design.md"))
	if err != nil {
		t.Fatalf("read product design: %v", err)
	}
	for _, tool := range []string{
		"knowl_retrieve",
		"knowl_ingest",
		"knowl_operation",
	} {
		if !strings.Contains(string(design), tool) {
			t.Errorf("product design does not identify MCP tool %q", tool)
		}
	}

	for _, retired := range []string{
		filepath.Join("docs", "requirements.md"),
		filepath.Join("docs", "architecture.md"),
		filepath.Join("docs", "integrations.md"),
	} {
		_, err := os.Stat(filepath.Join(repoRoot, retired))
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("retired conceptual document %q still exists", retired)
		}
	}

	openAPI, err := os.ReadFile(filepath.Join(repoRoot, "api", "openapi", "knowl.yaml"))
	if err != nil {
		t.Fatalf("read OpenAPI schema: %v", err)
	}
	for _, path := range []string{
		"/healthz:",
		"/readyz:",
		"/v1/retrieve:",
		"/v1/ingest:",
		"/v1/operations/{operation_id}:",
	} {
		if !strings.Contains(string(openAPI), path) {
			t.Errorf("OpenAPI schema does not define path %q", path)
		}
	}
}
