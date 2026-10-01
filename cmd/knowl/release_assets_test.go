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
		"Install Omnidist":       "npm install --global '@omnidist/omnidist@0.1.40'",
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
