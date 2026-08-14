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
		filepath.Join("docs", "releases", "v0.1.0.md"),
		filepath.Join("api", "openapi", "knowl.yaml"),
		filepath.Join("examples", "project-decisions", "README.md"),
		filepath.Join("examples", "project-decisions", "client.go"),
		filepath.Join("examples", "project-decisions", "main.go"),
		filepath.Join("examples", "project-decisions", "sources", "adr-session-memory-v1.md"),
		filepath.Join("examples", "project-decisions", "sources", "investigation-session-recovery-v1.md"),
		filepath.Join("examples", "project-decisions", "sources", "adr-session-memory-v2.md"),
		filepath.Join("examples", "project-decisions", "sources", "runbook-session-recovery-v1.md"),
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
	readmeText := strings.Join(strings.Fields(string(readme)), " ")
	for _, link := range []string{
		"docs/design.md",
		"docs/operations.md",
		"docs/workspace.md",
		"docs/sidecar.md",
		"api/openapi/knowl.yaml",
		"examples/project-decisions/README.md",
	} {
		if !strings.Contains(string(readme), link) {
			t.Errorf("README does not link canonical artifact %q", link)
		}
	}
	for _, statement := range []string{
		"Durable project knowledge for agents",
		"self-hosted knowledge sidecar for agentic applications",
		"bounded, provenance-backed evidence",
		"Host agent or application",
		"Knowl does not answer the user itself",
	} {
		if !strings.Contains(readmeText, statement) {
			t.Errorf("README does not preserve product boundary statement %q", statement)
		}
	}
	if strings.Contains(string(readme), "LLM-wiki") {
		t.Error("README lead still positions Knowl as an LLM-wiki")
	}

	design, err := os.ReadFile(filepath.Join(repoRoot, "docs", "design.md"))
	if err != nil {
		t.Fatalf("read product design: %v", err)
	}
	designText := strings.Join(strings.Fields(string(design)), " ")
	for _, statement := range []string{
		"Durable project knowledge for agents",
		"self-hosted knowledge sidecar for agentic applications",
		"The host decides which events are durable",
		"it does not answer the user itself",
	} {
		if !strings.Contains(designText, statement) {
			t.Errorf("product design does not preserve boundary statement %q", statement)
		}
	}
	if strings.Contains(string(design), "LLM-wiki") {
		t.Error("product-design lead still positions Knowl as an LLM-wiki")
	}
	for _, tool := range []string{
		mcpRetrieveToolName,
		mcpIngestToolName,
		mcpOperationToolName,
	} {
		if !strings.Contains(string(design), tool) {
			t.Errorf("product design does not identify MCP tool %q", tool)
		}
	}

	example, err := os.ReadFile(filepath.Join(repoRoot, "examples", "project-decisions", "README.md"))
	if err != nil {
		t.Fatalf("read project-decisions example: %v", err)
	}
	exampleText := strings.Join(strings.Fields(string(example)), " ")
	for _, statement := range []string{
		mcpIngestToolName,
		mcpOperationToolName,
		mcpRetrieveToolName,
		"http://127.0.0.1:8080/mcp",
		"KNOWL_MCP_ENDPOINT",
		operatorTokenEnvName,
		"source_refs",
		"Host answer",
		"Change a revision only when the corresponding source content changes",
	} {
		if !strings.Contains(exampleText, statement) {
			t.Errorf("project-decisions example does not preserve contract statement %q", statement)
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
