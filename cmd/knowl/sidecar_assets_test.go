package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
)

const quickstartProviderID = "openai"

func TestSidecarConfigLoadsThroughProductionTypes(t *testing.T) {
	repoRoot := testRepoRoot(t)
	workingDir := t.TempDir()
	configDir := filepath.Join(t.TempDir(), appName)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(repoRoot, "deploy", "sidecar", "knowl.yaml"))
	if err != nil {
		t.Fatalf("read sidecar config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), content, 0o600); err != nil {
		t.Fatalf("write sidecar config copy: %v", err)
	}

	t.Chdir(workingDir)
	clearKnowlEnv(t)
	ctx, err := loadConfig(context.Background(), filepath.Dir(configDir), "")
	if err != nil {
		t.Fatalf("load sidecar config: %v", err)
	}
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatalf("hostConfig() error: %v", err)
	}
	if config.Workspace != "/var/lib/knowl/knowledge" {
		t.Fatalf("workspace = %q, want /var/lib/knowl/knowledge", config.Workspace)
	}
	if config.ListenAddr != "0.0.0.0:8080" {
		t.Fatalf("listen addr = %q, want 0.0.0.0:8080", config.ListenAddr)
	}
	if config.StorePath != "/var/lib/knowl/knowledge/.knowl/knowl.sqlite" {
		t.Fatalf("store path = %q, want /var/lib/knowl/knowledge/.knowl/knowl.sqlite", config.StorePath)
	}
	if loaded, err := configFromContext(ctx); err != nil || loaded.Document.Knowl.Provider != "opencode" {
		t.Fatalf("sidecar provider = %q, %v; want opencode", loaded.Document.Knowl.Provider, err)
	}
	if len(config.Sources) != 2 || config.Sources[0].ID != commandEngineeringSourceID || config.Sources[1].ID != commandOperationsSourceID {
		t.Fatalf("sidecar sources = %#v", config.Sources)
	}
	for _, source := range config.Sources {
		if !source.Enabled || !source.Sync.OnStart || source.Sync.Interval != 5*time.Minute || source.Sync.RetryMaximum != time.Minute {
			t.Fatalf("sidecar source policy = %#v", source)
		}
	}
}

func TestQuickstartConfigLoadsThroughProductionTypes(t *testing.T) {
	repoRoot := testRepoRoot(t)
	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, appName)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(repoRoot, "deploy", "sidecar", "quickstart.yaml"))
	if err != nil {
		t.Fatalf("read quick-start config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), content, 0o600); err != nil {
		t.Fatalf("write quick-start config copy: %v", err)
	}

	clearKnowlEnv(t)
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_MODEL"} {
		value, present := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
	if _, loadErr := loadConfig(context.Background(), configRoot, ""); loadErr == nil ||
		!strings.Contains(loadErr.Error(), "OPENAI_API_KEY") || !strings.Contains(loadErr.Error(), "OPENAI_MODEL") {
		t.Fatalf("load quick-start config without provider environment = %v", loadErr)
	}
	t.Setenv("OPENAI_API_KEY", "test-api-key")
	t.Setenv("OPENAI_MODEL", "test-model")
	t.Setenv(operatorTokenEnvName, "test-operator-token")
	t.Chdir(t.TempDir())
	ctx, err := loadConfig(context.Background(), configRoot, "")
	if err != nil {
		t.Fatalf("load quick-start config: %v", err)
	}
	loaded, err := configFromContext(ctx)
	if err != nil {
		t.Fatalf("configFromContext() error: %v", err)
	}
	provider, ok := loaded.Document.Runtime.Providers[quickstartProviderID]
	if !ok || provider.Type != quickstartProviderID || provider.OpenAI == nil {
		t.Fatalf("quick-start runtime provider = %#v", provider)
	}
	if provider.OpenAI.APIKey != "test-api-key" || provider.OpenAI.Model != "test-model" {
		t.Fatalf("quick-start hosted provider = %#v", provider.OpenAI)
	}
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatalf("hostConfig() error: %v", err)
	}
	if loaded.Document.Knowl.Provider != quickstartProviderID || config.OperatorToken != "test-operator-token" {
		t.Fatalf("quick-start provider/token selection = %q/%q", loaded.Document.Knowl.Provider, config.OperatorToken)
	}
	if config.Workspace != "/var/lib/knowl/knowledge" || config.ListenAddr != "0.0.0.0:8080" ||
		config.StorePath != "/var/lib/knowl/knowledge/.knowl/knowl.sqlite" {
		t.Fatalf("quick-start host config = %#v", config)
	}
	if len(config.Sources) != 1 || config.Sources[0].ID != commandEngineeringSourceID ||
		config.Sources[0].Config.Filesystem == nil || config.Sources[0].Config.Filesystem.Root != "/sources/engineering" ||
		!config.Sources[0].Sync.OnStart {
		t.Fatalf("quick-start source config = %#v", config.Sources)
	}
}

func TestMaintainerConfiguredSidecarRunsSourceCLIAndRetrieval(t *testing.T) {
	repoRoot := testRepoRoot(t)
	content, err := os.ReadFile(filepath.Join(repoRoot, "deploy", "sidecar", "knowl.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	workspaceRoot := t.TempDir()
	workspace, err := contentfs.New(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	engineeringRoot := t.TempDir()
	operationsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(engineeringRoot, "Shared.md"), []byte("# Shared\n\nSidecarengineeringbeacon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(operationsRoot, "Shared.md"), []byte("# Shared\n\nSidecaroperationsbeacon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adjusted := strings.ReplaceAll(string(content), "/var/lib/knowl/knowledge", workspaceRoot)
	adjusted = strings.ReplaceAll(adjusted, "/sources/engineering", engineeringRoot)
	adjusted = strings.ReplaceAll(adjusted, "/sources/operations", operationsRoot)
	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, appName)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(adjusted), 0o600); err != nil {
		t.Fatal(err)
	}

	clearKnowlEnv(t)
	t.Chdir(t.TempDir())
	stdout, stderr, err := executeCLICommand(newRootCommand(), []string{"--config-dir", configRoot, sourceCommandName, sourceSyncCommandName, sourceSyncAllFlag}, nil)
	if err != nil {
		t.Fatalf("sidecar source sync --all: %v, stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, `"source_id":"`+commandEngineeringSourceID+`"`) || !strings.Contains(stdout, `"source_id":"`+commandOperationsSourceID+`"`) {
		t.Fatalf("sidecar sync output = %s", stdout)
	}
	stdout, stderr, err = executeCLICommand(newRootCommand(), []string{"--config-dir", configRoot, retrieveCommandName, retrieveSourceFlag, commandEngineeringSourceID, "Sidecarengineeringbeacon"}, nil)
	if err != nil {
		t.Fatalf("sidecar retrieval: %v, stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, `"evidence":[]`) || strings.Contains(stdout, `"source_id":"`+commandOperationsSourceID+`"`) {
		t.Fatalf("sidecar filtered retrieval = %s", stdout)
	}
	inspection, err := workspace.Inspect(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	foundEngineering, foundOperations := false, false
	for _, record := range inspection.RawSources {
		switch record.Source.SourceDocument.SourceID {
		case commandEngineeringSourceID:
			foundEngineering = true
		case commandOperationsSourceID:
			foundOperations = true
		}
	}
	if !foundEngineering || !foundOperations || len(inspection.Snapshot.Pages) != 0 {
		t.Fatalf("sidecar raw-only sync = engineering:%v operations:%v pages:%d", foundEngineering, foundOperations, len(inspection.Snapshot.Pages))
	}
}
