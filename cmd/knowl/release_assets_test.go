package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	releaseContainerSmokeJob = "container-smoke"
	releaseIntegrationJob    = "integration"
	releaseVerifyJob         = "verify"
	writePermission          = "write"
)

func TestReleaseWorkflowIsPinnedAndGated(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	if !reflect.DeepEqual(workflow.On.Push.Tags, []string{"v*.*.*"}) {
		t.Fatalf("release tags = %v, want semantic-version tags", workflow.On.Push.Tags)
	}
	wantJobs := []string{releaseVerifyJob, releaseIntegrationJob, releaseContainerSmokeJob, "npm-verify", "publish"}
	for _, name := range wantJobs {
		if _, ok := workflow.Jobs[name]; !ok {
			t.Errorf("release workflow has no %q job", name)
		}
	}
	for jobName, stepNames := range map[string][]string{
		releaseVerifyJob:         {"Verify release tag and main ancestry", "Test with race detector", "Vulnerability scan", "Architecture lint"},
		releaseIntegrationJob:    {"PostgreSQL integration contract"},
		releaseContainerSmokeJob: {"Build release-shaped image", "Smoke empty and reused persistent volumes"},
		"publish":                {"Build and publish image", "Attest published image provenance", "Publish GitHub Release"},
	} {
		for _, stepName := range stepNames {
			if _, ok := workflowStep(workflow.Jobs[jobName].Steps, stepName); !ok {
				t.Errorf("job %q has no step %q", jobName, stepName)
			}
		}
	}

	publish := workflow.Jobs["publish"]
	wantPermissions := map[string]string{
		"contents": writePermission, "packages": writePermission, "id-token": writePermission, "attestations": writePermission,
	}
	if !reflect.DeepEqual(publish.Permissions, wantPermissions) {
		t.Fatalf("publish permissions = %v, want %v", publish.Permissions, wantPermissions)
	}
	build, _ := workflowStep(publish.Steps, "Build and publish image")
	if build.With["platforms"] != "linux/amd64,linux/arm64" || build.With["push"] != true ||
		build.With["sbom"] != true || build.With["provenance"] != "mode=max" {
		t.Fatalf("unexpected container publish inputs: %#v", build.With)
	}
	attest, _ := workflowStep(publish.Steps, "Attest published image provenance")
	if attest.With["push-to-registry"] != true {
		t.Fatalf("unexpected attestation inputs: %#v", attest.With)
	}

	shaPattern := regexp.MustCompile(`^[0-9a-f]{40}$`)
	usesCount := 0
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Uses == "" {
				continue
			}
			usesCount++
			separator := strings.LastIndex(step.Uses, "@")
			if separator < 0 || !shaPattern.MatchString(step.Uses[separator+1:]) {
				t.Errorf("release action ref %q is not an immutable commit SHA", step.Uses)
			}
		}
	}
	if usesCount < 8 {
		t.Fatalf("pinned release actions = %d, want at least 8", usesCount)
	}
}

func TestReleaseWorkflowGatesVerifiedNPMArtifacts(t *testing.T) {
	workflow := readReleaseWorkflow(t)

	npmVerify, ok := workflow.Jobs["npm-verify"]
	if !ok {
		t.Fatal("release workflow has no npm-verify job")
	}
	wantVerification := map[string]string{
		"Install Omnidist":       "npm install --global '@omnidist/omnidist@0.1.36'",
		"Build native artifacts": "omnidist --profile default build",
		"Stage npm artifacts":    "omnidist --profile default stage --only npm",
		"Verify npm artifacts":   "omnidist --profile default verify --only npm",
	}
	for name, wantRun := range wantVerification {
		step, found := workflowStep(npmVerify.Steps, name)
		if !found || step.Run != wantRun {
			t.Fatalf("npm verification step %q = %#v, want run %q", name, step, wantRun)
		}
	}

	publish, ok := workflow.Jobs["publish"]
	if !ok {
		t.Fatal("release workflow has no publish job")
	}
	wantNeeds := []string{releaseVerifyJob, releaseIntegrationJob, releaseContainerSmokeJob, "npm-verify"}
	if !reflect.DeepEqual(publish.Needs, wantNeeds) {
		t.Fatalf("publish needs = %v, want %v", publish.Needs, wantNeeds)
	}
	publishStep, found := workflowStep(publish.Steps, "Publish npm artifacts")
	if !found || publishStep.Run != "omnidist --profile default npm publish" ||
		publishStep.Env["NPM_PUBLISH_TOKEN"] != "${{ secrets.NPM_PUBLISH_TOKEN }}" {
		t.Fatalf("unexpected npm publish step: %#v", publishStep)
	}
}

type workflowTestStep struct {
	Name string            `yaml:"name"`
	Run  string            `yaml:"run"`
	Uses string            `yaml:"uses"`
	Env  map[string]string `yaml:"env"`
	With map[string]any    `yaml:"with"`
}

type workflowTestJob struct {
	Needs       []string           `yaml:"needs"`
	Permissions map[string]string  `yaml:"permissions"`
	Steps       []workflowTestStep `yaml:"steps"`
}

type releaseWorkflow struct {
	On struct {
		Push struct {
			Tags []string `yaml:"tags"`
		} `yaml:"push"`
	} `yaml:"on"`
	Jobs map[string]workflowTestJob `yaml:"jobs"`
}

func readReleaseWorkflow(t *testing.T) releaseWorkflow {
	t.Helper()
	repoRoot := testRepoRoot(t)
	content, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(content, &workflow); err != nil {
		t.Fatalf("decode release workflow: %v", err)
	}
	return workflow
}

func workflowStep(steps []workflowTestStep, name string) (workflowTestStep, bool) {
	for _, step := range steps {
		if step.Name == name {
			return step, true
		}
	}
	return workflowTestStep{}, false
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

func TestV040ReleaseNotesDescribeConnectedPublishingContract(t *testing.T) {
	repoRoot := testRepoRoot(t)
	content, err := os.ReadFile(filepath.Join(repoRoot, "docs", "releases", "v0.4.0.md"))
	if err != nil {
		t.Fatalf("read v0.4.0 release notes: %v", err)
	}
	notes := strings.Join(strings.Fields(string(content)), " ")
	for _, required := range []string{
		"Connected Sources and Publishable Wikis",
		"remote Git sources",
		"`knowl run`",
		"`knowl hierarchy reconcile`",
		"`knowl export okf`",
		"`knowl export llms-txt`",
		"262,144 characters",
		"ghcr.io/baldaworks/knowl:v0.4.0",
	} {
		if !strings.Contains(notes, required) {
			t.Errorf("v0.4.0 release notes missing %q", required)
		}
	}
}

func TestV050ReleaseNotesDescribePolicyAwareMaintenance(t *testing.T) {
	repoRoot := testRepoRoot(t)
	content, err := os.ReadFile(filepath.Join(repoRoot, "docs", "releases", "v0.5.0.md"))
	if err != nil {
		t.Fatalf("read v0.5.0 release notes: %v", err)
	}
	notes := strings.Join(strings.Fields(string(content)), " ")
	for _, required := range []string{
		"Policy-Aware Maintenance and Isolated Validation",
		"policy generation",
		"262,144-character",
		"unresolved original links",
		"citation.unknown_source",
		"SQLite and PostgreSQL",
		"ghcr.io/baldaworks/knowl:v0.5.0",
	} {
		if !strings.Contains(notes, required) {
			t.Errorf("v0.5.0 release notes missing %q", required)
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
		for _, required := range []string{"raw/", "semantic", providerFailureClass} {
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
			"source retry engineering --failure-class provider --dry-run",
			"limited to three total work attempts",
			"maintenance.counts.retrying",
			"provider error text",
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
