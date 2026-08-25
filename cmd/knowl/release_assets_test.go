package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseWorkflowIsPinnedAndGated(t *testing.T) {
	repoRoot := testRepoRoot(t)
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "release.yml")
	content, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	workflow := string(content)
	for _, required := range []string{
		"tags:\n      - 'v*.*.*'",
		"git cat-file -t",
		"git merge-base --is-ancestor",
		"needs: [verify, integration, container-smoke]",
		"go test -v -race ./...",
		"go tool govulncheck ./...",
		"go test -tags=integration ./pkg/knowl/store/postgres",
		"scripts/smoke-test-sidecar.sh knowl:release-smoke",
		"packages: write",
		"id-token: write",
		"attestations: write",
		"platforms: linux/amd64,linux/arm64",
		"sbom: true",
		"provenance: mode=max",
		"push-to-registry: true",
		"gh release create",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow missing %q", required)
		}
	}
	usesPattern := regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*[^@\s]+@([^\s#]+)`)
	shaPattern := regexp.MustCompile(`^[0-9a-f]{40}$`)
	uses := usesPattern.FindAllStringSubmatch(workflow, -1)
	if len(uses) < 8 {
		t.Fatalf("pinned release actions = %d, want at least 8", len(uses))
	}
	for _, match := range uses {
		if !shaPattern.MatchString(match[1]) {
			t.Errorf("release action ref %q is not an immutable commit SHA", match[1])
		}
	}
}

func TestReleaseNotesPreserveDistributionContract(t *testing.T) {
	repoRoot := testRepoRoot(t)
	content, err := os.ReadFile(filepath.Join(repoRoot, "docs", "releases", "v0.1.0.md"))
	if err != nil {
		t.Fatalf("read v0.1.0 release notes: %v", err)
	}
	notes := strings.Join(strings.Fields(string(content)), " ")
	for _, required := range []string{
		"Crash-safe Knowledge Loop",
		"ghcr.io/baldaworks/knowl:v0.1.0",
		"ghcr.io/baldaworks/knowl@sha256:<published-digest>",
		operatorTokenEnvName,
		"/var/lib/knowl",
		mcpRetrieveToolName,
		mcpIngestToolName,
		mcpOperationToolName,
		"Do not discard pending operations",
	} {
		if !strings.Contains(notes, required) {
			t.Errorf("v0.1.0 release notes missing %q", required)
		}
	}
}

func TestV020ReleaseNotesDescribeMultiSourceCompatibility(t *testing.T) {
	repoRoot := testRepoRoot(t)
	content, err := os.ReadFile(filepath.Join(repoRoot, "docs", "releases", "v0.2.0.md"))
	if err != nil {
		t.Fatalf("read v0.2.0 release notes: %v", err)
	}
	notes := strings.Join(strings.Fields(string(content)), " ")
	for _, required := range []string{
		"Multi-source Wiki Sync",
		"bootstrap-wiki",
		sourceNamespacePattern,
		"source sync --all",
		"maintainer_unavailable",
		mcpRetrieveToolName,
		mcpIngestToolName,
		mcpOperationToolName,
		"legacy `wiki/notes/**`",
		"Never run destructive down migrations",
	} {
		if !strings.Contains(notes, required) {
			t.Errorf("v0.2.0 release notes missing %q", required)
		}
	}
}

func TestMultiSourceDocumentationSurfacesStayAligned(t *testing.T) {
	repoRoot := testRepoRoot(t)
	for _, relative := range []string{"README.md", designDocRelativePath, "docs/workspace.md", "docs/operations.md", "docs/sidecar.md"} {
		content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		text := string(content)
		for _, required := range []string{sourceNamespacePattern, "provider"} {
			if !strings.Contains(text, required) {
				t.Errorf("%s missing %q", relative, required)
			}
		}
		if relative == designDocRelativePath && strings.Contains(text, "internal/bootstrap") {
			t.Errorf("%s still describes a standalone bootstrap package", relative)
		}
	}
}
