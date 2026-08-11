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

func TestLoadConfigNormalizesCanonicalIngestPolicy(t *testing.T) {
	ctx := loadTestConfig(t, testConfigOptions{
		knowl: "provider: codex\ningest:\n  auto_apply: true\n",
	})
	loaded, err := configFromContext(ctx)
	if err != nil {
		t.Fatalf("configFromContext() error: %v", err)
	}
	if loaded.Document.Knowl.Ingest.AutoApply == nil || !*loaded.Document.Knowl.Ingest.AutoApply {
		t.Fatalf("normalized ingest.auto_apply = %#v, want true", loaded.Document.Knowl.Ingest.AutoApply)
	}
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatalf("hostConfig() error: %v", err)
	}
	if !config.IngestOptions.AutoApply {
		t.Fatal("hostConfig().IngestOptions.AutoApply = false, want true")
	}
}

func TestLoadConfigNormalizesLegacyMaintenancePolicy(t *testing.T) {
	tests := []struct {
		name      string
		knowl     string
		wantApply bool
	}{
		{
			name:      "legacy review false",
			knowl:     "provider: codex\nmaintenance:\n  review: false\n",
			wantApply: true,
		},
		{
			name:      "legacy auto apply",
			knowl:     "provider: codex\nmaintenance:\n  auto_apply: true\n",
			wantApply: true,
		},
		{
			name:      "legacy consistent pair",
			knowl:     "provider: codex\nmaintenance:\n  auto_apply: false\n  review: true\n",
			wantApply: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := loadTestConfig(t, testConfigOptions{knowl: test.knowl})
			loaded, err := configFromContext(ctx)
			if err != nil {
				t.Fatalf("configFromContext() error: %v", err)
			}
			if loaded.Document.Knowl.Ingest.AutoApply == nil || *loaded.Document.Knowl.Ingest.AutoApply != test.wantApply {
				t.Fatalf("normalized ingest.auto_apply = %#v, want %t", loaded.Document.Knowl.Ingest.AutoApply, test.wantApply)
			}
			config, err := hostConfig(ctx)
			if err != nil {
				t.Fatalf("hostConfig() error: %v", err)
			}
			if config.IngestOptions.AutoApply != test.wantApply {
				t.Fatalf("hostConfig().IngestOptions.AutoApply = %t, want %t", config.IngestOptions.AutoApply, test.wantApply)
			}
		})
	}
}

func TestLoadConfigDefaultsToReviewFirstWhenIngestPolicyAbsent(t *testing.T) {
	ctx := loadTestConfig(t, testConfigOptions{knowl: "provider: codex\n"})
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatalf("hostConfig() error: %v", err)
	}
	if config.IngestOptions.AutoApply {
		t.Fatal("hostConfig().IngestOptions.AutoApply = true, want default review-first false")
	}
}

func TestLoadConfigRejectsMixedCanonicalAndLegacyIngestPolicy(t *testing.T) {
	_, err := tryLoadTestConfig(t, testConfigOptions{
		knowl: "provider: codex\ningest:\n  auto_apply: true\nmaintenance:\n  review: false\n",
	})
	if err == nil {
		t.Fatal("loadConfig() error = nil, want mixed-shape rejection")
	}
	if !strings.Contains(err.Error(), "knowl.ingest.auto_apply cannot be combined with legacy knowl.maintenance settings") {
		t.Fatalf("loadConfig() error = %q, want mixed-shape rejection", err)
	}
}

func TestLoadConfigRejectsContradictoryLegacyMaintenancePolicy(t *testing.T) {
	_, err := tryLoadTestConfig(t, testConfigOptions{
		knowl: "provider: codex\nmaintenance:\n  auto_apply: true\n  review: true\n",
	})
	if err == nil {
		t.Fatal("loadConfig() error = nil, want contradictory legacy policy rejection")
	}
	if !strings.Contains(err.Error(), "knowl.maintenance.auto_apply conflicts with knowl.maintenance.review") {
		t.Fatalf("loadConfig() error = %q, want contradictory legacy policy rejection", err)
	}
}

func TestCanonicalIngestConfigStillExposesCompletedPublicIngest(t *testing.T) {
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
		knowl: "provider: codex\nworkspace:\n  path: " + strconvQuote(workspace.Root()) + "\ningest:\n  auto_apply: true\n",
	})
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatalf("hostConfig() error: %v", err)
	}
	if !config.IngestOptions.AutoApply {
		t.Fatal("hostConfig().IngestOptions.AutoApply = false, want true")
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
