package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/appconfig"
)

func TestRootCommandLoadsConfigIntoRunContext(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)

	root := newRootCommand()
	root.SetArgs([]string{initCommandName})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute init: %v", err)
	}
	configPath := filepath.Join(workspace, ".config", appName, "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("generated config: %v", err)
	}
}

func TestLoadConfigDefaultsToSQLiteWithoutBackendSection(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)

	ctx, err := loadConfig(context.Background(), "", "")
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	storage, err := storageSettings(ctx)
	if err != nil {
		t.Fatalf("normalize default storage: %v", err)
	}
	wantPath := filepath.Join(workingDir, ".knowl", "knowl.sqlite")
	if storage.Driver != knowl.StoreSQLite || storage.Path != wantPath {
		t.Fatalf("default storage = %#v, want sqlite path %q", storage, wantPath)
	}
}

func TestCheckedInConfigArtifactUsesTypedBaldaCompatibleShape(t *testing.T) {
	repoRoot := testRepoRoot(t)
	t.Chdir(repoRoot)
	clearKnowlEnv(t)

	ctx, err := loadConfig(context.Background(), "", "")
	if err != nil {
		t.Fatalf("load checked-in config: %v", err)
	}
	loaded, err := configFromContext(ctx)
	if err != nil {
		t.Fatalf("read loaded config: %v", err)
	}
	if _, ok := loaded.Document.Runtime.Providers[loaded.Document.Knowl.Provider]; !ok {
		t.Fatalf("knowl.provider %q not present in runtime.providers", loaded.Document.Knowl.Provider)
	}
	storage, err := storageSettings(ctx)
	if err != nil {
		t.Fatalf("normalize checked-in storage: %v", err)
	}
	wantPath := filepath.Join(repoRoot, "knowledge", ".knowl", "knowl.sqlite")
	if storage.Driver != knowl.StoreSQLite || storage.Path != wantPath {
		t.Fatalf("checked-in storage = %#v, want sqlite path %q", storage, wantPath)
	}
}

func TestEmbeddedDefaultConfigArtifactLoadsThroughProductionTypes(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	clearKnowlEnv(t)
	ctx, err := loadConfig(context.Background(), "", "")
	if err != nil {
		t.Fatalf("load embedded config: %v", err)
	}
	loaded, err := configFromContext(ctx)
	if err != nil {
		t.Fatalf("read loaded config: %v", err)
	}
	if _, ok := loaded.Document.Runtime.Providers[loaded.Document.Knowl.Provider]; !ok {
		t.Fatalf("embedded knowl.provider %q not present in runtime.providers", loaded.Document.Knowl.Provider)
	}
	storage, err := storageSettings(ctx)
	if err != nil {
		t.Fatalf("normalize embedded storage: %v", err)
	}
	wantPath := filepath.Join(workingDir, ".knowl", "knowl.sqlite")
	if storage.Driver != knowl.StoreSQLite || storage.Path != wantPath {
		t.Fatalf("embedded storage = %#v, want sqlite path %q", storage, wantPath)
	}
}

func TestOperatorDocsUseCanonicalIngestConfigShape(t *testing.T) {
	t.Parallel()

	repoRoot := testRepoRoot(t)
	tests := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "readme",
			path: filepath.Join(repoRoot, "README.md"),
			want: []string{"knowl.ingest.auto_apply", "auto_apply: false"},
		},
		{
			name: "operations",
			path: filepath.Join(repoRoot, "docs", "operations.md"),
			want: []string{"KNOWL_INGEST_AUTO_APPLY", "knowl.ingest.auto_apply"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatalf("read %s: %v", test.path, err)
			}
			text := string(content)
			for _, want := range test.want {
				if !strings.Contains(text, want) {
					t.Fatalf("%s missing canonical ingest-policy reference %q", test.path, want)
				}
			}
		})
	}
}

func TestLoadConfigMatchesBaldaRuntimeDocumentAndOverrides(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	if err := os.MkdirAll(filepath.Join(workingDir, ".config", appName), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	config := `runtime:
  providers:
    codex:
      type: codex_acp
      codex_acp:
        model: gpt-5-codex
profiles:
  fast:
    knowl:
      provider: codex
      workspace:
        path: profile-workspace
knowl:
  provider: codex
  workspace:
    path: file-workspace
  storage:
    type: sqlite
    sqlite:
      path: .knowl/state.db
`
	if err := os.WriteFile(filepath.Join(workingDir, ".config", appName, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("KNOWL_WORKSPACE_PATH", "env-workspace")

	ctx, err := loadConfig(context.Background(), "", "fast")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	loaded, err := configFromContext(ctx)
	if err != nil {
		t.Fatalf("read loaded config: %v", err)
	}
	if loaded.Profile != "fast" {
		t.Fatalf("profile = %q, want fast", loaded.Profile)
	}
	provider, ok := loaded.Document.Runtime.Providers["codex"]
	if !ok || provider.CodexACP == nil || provider.CodexACP.Model != "gpt-5-codex" {
		t.Fatalf("runtime provider = %#v, want Balda-compatible codex provider", provider)
	}
	if loaded.Document.Knowl.Provider != "codex" {
		t.Fatalf("knowl.provider = %q, want codex", loaded.Document.Knowl.Provider)
	}
	if loaded.Document.Knowl.Workspace.Path != "env-workspace" {
		t.Fatalf("workspace.path = %q, want env-workspace", loaded.Document.Knowl.Workspace.Path)
	}
	if loaded.Document.Knowl.Storage.SQLite == nil || loaded.Document.Knowl.Storage.SQLite.Path != ".knowl/state.db" {
		t.Fatalf("sqlite config = %#v, want decoded typed section", loaded.Document.Knowl.Storage.SQLite)
	}
}

func TestLoadConfigUsesConfigDirLayout(t *testing.T) {
	workingDir := t.TempDir()
	configDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configDir, appName), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	config := `runtime:
  providers:
    hosted:
      type: openai
      openai:
        model: gpt-5-mini
knowl:
  provider: hosted
  storage:
    type: postgres
    postgres:
      dsn: postgres://user:secret@localhost/knowl
`
	if err := os.WriteFile(filepath.Join(configDir, appName, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(workingDir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	ctx, err := loadConfig(context.Background(), configDir, "")
	if err != nil {
		t.Fatalf("load config from config-dir: %v", err)
	}
	loaded, err := configFromContext(ctx)
	if err != nil {
		t.Fatalf("read loaded config: %v", err)
	}
	if loaded.Document.Knowl.Provider != "hosted" {
		t.Fatalf("knowl.provider = %q, want hosted", loaded.Document.Knowl.Provider)
	}
	if _, err := storageSettings(ctx); err != nil {
		t.Fatalf("normalize postgres storage: %v", err)
	}
	if got := configOutputPath(loaded); !strings.HasPrefix(got, configDir) {
		t.Fatalf("config output path = %q, want under config-dir %q", got, configDir)
	}
}

func TestLoadConfigRejectsInvalidRuntimeProvider(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	if err := os.MkdirAll(filepath.Join(workingDir, ".config", appName), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	config := `runtime:
  providers:
    broken:
      type: codex_acp
      opencode_acp: {}
knowl:
  provider: broken
`
	if err := os.WriteFile(filepath.Join(workingDir, ".config", appName, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := loadConfig(context.Background(), "", "")
	if err == nil {
		t.Fatal("loadConfig() error = nil, want invalid runtime provider error")
	}
	if !strings.Contains(err.Error(), "runtime config") {
		t.Fatalf("loadConfig() error = %q, want shared runtime validation context", err)
	}
}

func TestSelectedRuntimeProviderValidatesSelectorBeforeHostConstruction(t *testing.T) {
	providerConfig := agentconfig.Config{
		Type:   "openai",
		OpenAI: &agentconfig.LocalAPIConfig{Model: "test-model"},
	}
	for _, test := range []struct {
		name       string
		providerID string
		wantError  string
	}{
		{name: "empty", wantError: "knowl.provider is required"},
		{name: "unknown", providerID: "missing", wantError: "validate knowl.provider"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), loadedConfigContextKey{}, loadedConfig{
				Document: knowlConfigDocument{
					Runtime: appconfig.RuntimeConfig{Providers: map[string]agentconfig.Config{knownProviderID: providerConfig}},
					Knowl:   AppConfig{Provider: test.providerID},
				},
			})
			if _, _, err := selectedRuntimeProvider(ctx); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("selectedRuntimeProvider() error = %v, want %q", err, test.wantError)
			}
		})
	}

	ctx := context.WithValue(context.Background(), loadedConfigContextKey{}, loadedConfig{
		Document: knowlConfigDocument{
			Runtime: appconfig.RuntimeConfig{Providers: map[string]agentconfig.Config{knownProviderID: providerConfig}},
			Knowl:   AppConfig{Provider: knownProviderID},
		},
	})
	factory, providerID, err := selectedRuntimeProvider(ctx)
	if err != nil {
		t.Fatalf("selectedRuntimeProvider() error: %v", err)
	}
	if factory == nil || providerID != knownProviderID {
		t.Fatalf("selectedRuntimeProvider() = (%#v, %q), want factory and %s", factory, providerID, knownProviderID)
	}
}
