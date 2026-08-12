package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestLoadConfigUsesDefaultInternalIngestPolicyWhenConfigOmitsIt(t *testing.T) {
	ctx := loadTestConfig(t, testConfigOptions{knowl: "provider: codex\n"})
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatalf("hostConfig() error: %v", err)
	}
	if config.IngestOptions.AutoApply {
		t.Fatal("hostConfig().IngestOptions.AutoApply = true, want default review-first false")
	}
}

func TestLoadConfigRejectsRemovedIngestConfig(t *testing.T) {
	_, err := tryLoadTestConfig(t, testConfigOptions{
		knowl: "provider: codex\ningest:\n  auto_apply: true\n",
	})
	if err == nil {
		t.Fatal("loadConfig() error = nil, want removed-section rejection")
	}
	if !strings.Contains(err.Error(), "knowl.ingest is not supported") {
		t.Fatalf("loadConfig() error = %q, want ingest rejection", err)
	}
}

func TestLoadConfigRejectsRemovedMaintenanceConfig(t *testing.T) {
	_, err := tryLoadTestConfig(t, testConfigOptions{
		knowl: "provider: codex\nmaintenance:\n  auto_apply: true\n",
	})
	if err == nil {
		t.Fatal("loadConfig() error = nil, want removed-section rejection")
	}
	if !strings.Contains(err.Error(), "knowl.maintenance is not supported") {
		t.Fatalf("loadConfig() error = %q, want maintenance rejection", err)
	}
}

func TestPublicIngestStillExposesCompletedResultWithoutConfigPolicy(t *testing.T) {
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	schema, err := workspace.Schema(context.Background(), "local")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}

	ctx := loadTestConfig(t, testConfigOptions{
		knowl: "provider: codex\nworkspace:\n  path: " + strconvQuote(workspace.Root()) + "\n",
	})
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatalf("hostConfig() error: %v", err)
	}
	if config.IngestOptions.AutoApply {
		t.Fatal("hostConfig().IngestOptions.AutoApply = true, want default false")
	}
	fixture := commandWorkflowFixture{
		config: config,
		schema: schema,
		maintainer: provider.Fixture{Result: domain.ModelEditPlan{
			SchemaDigest: schema.Digest,
			SourceRefs:   []string{smokeSourceRef},
			Edits:        []domain.FileEdit{{Path: smokePagePath, Content: []byte(smokePageContent)}},
		}},
	}
	withLocalWorkflowSessionFactory(t, fixture.newSessionFactory(t))

	inputPath := writeJSONFixture(t, knowlapi.IngestRequest{
		Content:        pointerTo(smokeSourceText),
		Origin:         pointerTo(smokeSourceID),
		IdempotencyKey: pointerTo(smokeSourceVersion),
	})

	stdout, stderr, err := executeCLICommand(newIngestCommand(), []string{workflowInputFlagUsage, inputPath}, nil)
	if err != nil {
		t.Fatalf("ingest Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("ingest stderr = %q, want empty", stderr)
	}
	var result knowlapi.IngestResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode ingest output: %v", err)
	}
	if result.Status != knowlapi.IngestResultStatusCompleted {
		t.Fatalf("ingest status = %q, want %q", result.Status, knowlapi.IngestResultStatusCompleted)
	}
	if _, err := os.Stat(fixture.pagePath(smokePagePath)); err != nil {
		t.Fatalf("committed page stat = %v, want present", err)
	}
}

type testConfigOptions struct {
	knowl string
}

func loadTestConfig(t *testing.T, options testConfigOptions) context.Context {
	t.Helper()

	ctx, err := tryLoadTestConfig(t, options)
	if err != nil {
		t.Fatalf("loadConfig() error: %v", err)
	}
	return ctx
}

func tryLoadTestConfig(t *testing.T, options testConfigOptions) (context.Context, error) {
	t.Helper()

	workingDir := t.TempDir()
	t.Chdir(workingDir)
	clearKnowlEnv(t)
	configDir := filepath.Join(workingDir, ".config", appName)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	config := "runtime:\n  providers:\n    codex:\n      type: codex_acp\n      codex_acp:\n        model: gpt-5-codex\nknowl:\n" + indentYAML(strings.TrimSpace(options.knowl), "  ") + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return loadConfig(context.Background(), "", "")
}

func indentYAML(value, prefix string) string {
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = prefix + lines[index]
	}
	return strings.Join(lines, "\n")
}

func strconvQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}
