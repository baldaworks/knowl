package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestPublicDocumentationSurface(t *testing.T) {
	repoRoot := testRepoRoot(t)
	canonicalFiles := []string{
		readmeRelativePath,
		"CONTRIBUTING.md",
		filepath.Join("docs", "design.md"),
		filepath.Join("docs", "operations.md"),
		filepath.Join("docs", "workspace.md"),
		filepath.Join("docs", "sidecar.md"),
		filepath.Join("docs", "releases", "v0.1.0.md"),
		filepath.Join("api", "openapi", "knowl.yaml"),
		filepath.Join("deploy", "sidecar", "quickstart.yaml"),
		filepath.Join("deploy", "sidecar", "quickstart.compose.yaml"),
		filepath.Join("examples", "source-to-wiki", "README.md"),
		filepath.Join("examples", "source-to-wiki", "run.sh"),
		filepath.Join("examples", "source-to-wiki", "sources", "architecture-overview.md"),
		filepath.Join("examples", "source-to-wiki", "sources", "authentication-service.md"),
		filepath.Join("examples", "source-to-wiki", "sources", "database-retention-policy.md"),
		filepath.Join("examples", "source-to-wiki", "sources", "incident-response-runbook.md"),
		filepath.Join("examples", "source-to-wiki", "knowledge", "schema.md"),
		filepath.Join("examples", "source-to-wiki", "knowledge", "wiki", "index.md"),
		filepath.Join("examples", "source-to-wiki", "knowledge", "wiki", "log.md"),
		filepath.Join("examples", "source-to-wiki", "knowledge", "wiki", "entities", "acme-cloud-platform.md"),
	}
	for _, relative := range canonicalFiles {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err != nil {
			t.Fatalf("canonical documentation artifact %q: %v", relative, err)
		}
	}

	readme, err := os.ReadFile(filepath.Join(repoRoot, readmeRelativePath))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	readmeText := strings.Join(strings.Fields(string(readme)), " ")
	for _, link := range []string{
		designDocRelativePath,
		operationsDocRelativePath,
		workspaceDocRelativePath,
		sidecarDocRelativePath,
		"api/openapi/knowl.yaml",
		"examples/source-to-wiki/README.md",
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

	example, err := os.ReadFile(filepath.Join(repoRoot, "examples", "source-to-wiki", "README.md"))
	if err != nil {
		t.Fatalf("read source-to-wiki example: %v", err)
	}
	exampleText := strings.Join(strings.Fields(string(example)), " ")
	for _, statement := range []string{
		"sources/ (raw markdown) ──> knowl run ──> knowledge/wiki/",
		"antigravity_acp",
		"knowl run",
	} {
		if !strings.Contains(exampleText, statement) {
			t.Errorf("source-to-wiki example does not preserve statement %q", statement)
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

func TestREADMEReleaseAndQuickStartContracts(t *testing.T) {
	repoRoot := testRepoRoot(t)
	readme, err := os.ReadFile(filepath.Join(repoRoot, readmeRelativePath))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	readmeText := string(readme)
	normalized := strings.Join(strings.Fields(readmeText), " ")

	orderedHeadings := []string{
		"## Release status",
		"## When to use Knowl",
		"## Quick start: stable sidecar",
		"## Core concepts",
		"## Development",
	}
	previous := -1
	for _, heading := range orderedHeadings {
		position := strings.Index(readmeText, heading)
		if position < 0 {
			t.Fatalf("README does not contain required heading %q", heading)
		}
		if position <= previous {
			t.Fatalf("README heading %q is not in the required user-first order", heading)
		}
		previous = position
	}

	for _, marker := range []string{
		"This README documents the current `main` branch",
		"https://github.com/baldaworks/knowl/releases/tag/v0.3.1",
		"deploy/sidecar/quickstart.yaml",
		"deploy/sidecar/quickstart.compose.yaml",
		"OPENAI_API_KEY",
		"OPENAI_MODEL",
		"KNOWL_OPERATOR_TOKEN",
		"stock Knowl image does not include OpenCode",
		"`opencode acp`",
		"non-empty `evidence` array",
		"Engineering shared page",
		"http://127.0.0.1:8080/mcp",
		`"Authorization": "Bearer <operator-token>"`,
	} {
		if !strings.Contains(normalized, marker) {
			t.Errorf("README does not preserve release or quick-start contract %q", marker)
		}
	}

	if !strings.Contains(normalized, "v0.2.0.md` is an unpublished, superseded historical note, not a published Knowl release") {
		t.Error("README does not identify the v0.2.0 note as unpublished and superseded")
	}
	for _, forbidden := range []string{
		"v0.2.0 multi-source release",
		"latest published release is [v0.2.0",
	} {
		if strings.Contains(normalized, forbidden) {
			t.Errorf("README incorrectly presents v0.2.0 as published: found %q", forbidden)
		}
	}
}

func TestREADMELabelsCurrentOnlyCommands(t *testing.T) {
	repoRoot := testRepoRoot(t)
	readme, err := os.ReadFile(filepath.Join(repoRoot, readmeRelativePath))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	readmeText := string(readme)
	releaseStart := strings.Index(readmeText, "## Release status")
	releaseEnd := strings.Index(readmeText, "## When to use Knowl")
	if releaseStart < 0 || releaseEnd <= releaseStart {
		t.Fatal("README does not have a bounded release-status section")
	}
	releaseSection := readmeText[releaseStart:releaseEnd]

	currentOnly := []struct {
		command string
		present bool
	}{
		{command: "knowl run", present: hasCommandPath(newRootCommand(), runCommandName)},
		{command: "knowl hierarchy reconcile", present: hasCommandPath(newRootCommand(), hierarchyCommandName, hierarchyReconcileCommandName)},
		{command: "knowl source retry", present: hasCommandPath(newRootCommand(), sourceCommandName, sourceRetryCommandName)},
	}
	for _, item := range currentOnly {
		if !item.present {
			t.Fatalf("documented current-only command %q is absent from the current command tree", item.command)
		}
		row := "| `" + item.command + "` | No | Yes |"
		if !strings.Contains(releaseSection, row) {
			t.Errorf("README release table does not label %q unavailable in v0.3.1 and available on main", item.command)
		}
	}
}

func hasCommandPath(command *cobra.Command, path ...string) bool {
	for _, name := range path {
		found := false
		for _, child := range command.Commands() {
			if child.Name() == name {
				command = child
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
