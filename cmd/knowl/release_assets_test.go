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
		`release_notes="docs/releases/${GITHUB_REF_NAME}.md"`,
		`test -f "$release_notes"`,
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

func TestV030ReleaseNotesDescribeSemanticWikiContract(t *testing.T) {
	repoRoot := testRepoRoot(t)
	content, err := os.ReadFile(filepath.Join(repoRoot, "docs", "releases", "v0.3.0.md"))
	if err != nil {
		t.Fatalf("read v0.3.0 release notes: %v", err)
	}
	notes := strings.Join(strings.Fields(string(content)), " ")
	for _, required := range []string{
		"Semantic Source Maintenance",
		"maintainer provider is now required",
		"Bootstrap remains optional",
		"`raw/`",
		"semantic OKF",
		"source_documents",
		"wiki/sources/<source_id>/**",
		"ghcr.io/baldaworks/knowl:v0.3.0",
	} {
		if !strings.Contains(notes, required) {
			t.Errorf("v0.3.0 release notes missing %q", required)
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
	for _, relative := range []string{readmeRelativePath, designDocRelativePath, workspaceDocRelativePath, operationsDocRelativePath, sidecarDocRelativePath} {
		content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		text := string(content)
		for _, required := range []string{"raw/", "semantic", "provider"} {
			if !strings.Contains(text, required) {
				t.Errorf("%s missing %q", relative, required)
			}
		}
		for _, stale := range []string{"provider-free", "active materialized mirror", "writes mirrors below"} {
			if strings.Contains(text, stale) {
				t.Errorf("%s still contains stale contract %q", relative, stale)
			}
		}
		if relative == designDocRelativePath && strings.Contains(text, "internal/bootstrap") {
			t.Errorf("%s still describes a standalone bootstrap package", relative)
		}
	}
}

func TestActiveDocumentationDescribesSemanticWikiMaintenance(t *testing.T) {
	repoRoot := testRepoRoot(t)
	wants := map[string][]string{
		readmeRelativePath: {
			"Source documents are never copied into `wiki/`",
			"Initial bootstrap and automatic `on_start` synchronization are both optional",
		},
		designDocRelativePath: {
			"Bootstrap remains optional",
			"never copies source content into `wiki/`",
		},
		workspaceDocRelativePath: {
			"contains no configured-source copies",
			"source_documents",
		},
		operationsDocRelativePath: {
			"Bootstrap is optional",
			"successful sync reports raw acceptance and maintenance reservation, not LLM completion",
		},
		sidecarDocRelativePath: {
			"Initial bootstrap is optional",
			"reserves durable maintenance work",
		},
	}
	for relative, required := range wants {
		content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		normalized := strings.Join(strings.Fields(string(content)), " ")
		for _, phrase := range required {
			if !strings.Contains(normalized, phrase) {
				t.Errorf("%s missing active contract %q", relative, phrase)
			}
		}
	}
}
