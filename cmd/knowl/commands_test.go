package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/appconfig"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const knownProviderID = "known"

func TestInitWorkspaceIsIdempotent(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := initWorkspace(workspace); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	if err := initWorkspace(workspace); err != nil {
		t.Fatalf("re-init workspace: %v", err)
	}
	if err := validateWorkspace(workspace); err != nil {
		t.Fatalf("validate initialized workspace: %v", err)
	}
	for _, relative := range []string{schemaFile, indexFile, logFile} {
		if _, err := os.Stat(filepath.Join(workspace, relative)); err != nil {
			t.Errorf("expected initialized file %q: %v", relative, err)
		}
	}
}

func TestStoreDriverSelection(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "default", want: defaultStore},
		{name: postgresStore, value: postgresStore, want: postgresStore},
		{name: "unsupported", value: "mysql", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), loadedConfigContextKey{}, loadedConfig{
				WorkingDir: t.TempDir(),
				Document: knowlConfigDocument{Knowl: AppConfig{Storage: StorageConfig{
					Type:   test.value,
					SQLite: &SQLiteConfig{},
				}}},
			})
			if test.value == postgresStore {
				loaded := ctx.Value(loadedConfigContextKey{}).(loadedConfig)
				loaded.Document.Knowl.Storage.SQLite = nil
				loaded.Document.Knowl.Storage.Postgres = &PostgresConfig{DSN: "postgres://localhost/knowl"}
				ctx = context.WithValue(context.Background(), loadedConfigContextKey{}, loaded)
			}
			got, err := storeDriver(ctx)
			if test.wantErr {
				if err == nil {
					t.Fatal("storeDriver() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("storeDriver() error: %v", err)
			}
			if got != test.want {
				t.Fatalf("storeDriver() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRootExposesCurrentLifecycleCommands(t *testing.T) {
	root := newRootCommand()
	want := map[string]bool{
		initCommandName:     true,
		validateCommandName: true,
		startCommandName:    true,
		ingestCommandName:   true,
		lintCommandName:     true,
	}
	for _, command := range root.Commands() {
		delete(want, command.Name())
	}
	if len(want) != 0 {
		t.Fatalf("missing lifecycle commands: %v", want)
	}
}

func TestRootHelpExplainsSupportedLocalWorkflow(t *testing.T) {
	t.Parallel()

	output := commandHelpOutput(t, newRootCommand())
	for _, want := range []string{
		"Supported local workflow:",
		"knowl init",
		"knowl validate",
		startCommandUsage,
		loopbackHTTPAPIText,
		"not the supported local workflow today",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("root help missing %q in output:\n%s", want, output)
		}
	}
}

func TestPlaceholderCommandHelpMarksCommandsUnsupported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cmd       *cobra.Command
		wantShort string
		wantHelp  []string
	}{
		{
			name:      ingestCommandName,
			cmd:       newIngestCommand(),
			wantShort: placeholderCommandShort,
			wantHelp: []string{
				unsupportedWorkflowToday,
				startCommandUsage,
				loopbackHTTPAPIText,
			},
		},
		{
			name:      lintCommandName,
			cmd:       newLintCommand(),
			wantShort: placeholderCommandShort,
			wantHelp: []string{
				unsupportedWorkflowToday,
				startCommandUsage,
				loopbackHTTPAPIText,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.cmd.Short; got != test.wantShort {
				t.Fatalf("%s short = %q, want %q", test.name, got, test.wantShort)
			}

			output := commandHelpOutput(t, test.cmd)
			for _, want := range test.wantHelp {
				if !strings.Contains(output, want) {
					t.Fatalf("%s help missing %q in output:\n%s", test.name, want, output)
				}
			}
		})
	}
}

func TestPlaceholderCommandsReturnSupportedWorkflowGuidance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cmd         *cobra.Command
		wantMessage string
	}{
		{
			name:        ingestCommandName,
			cmd:         newIngestCommand(),
			wantMessage: unsupportedWorkflowError(ingestCommandName).Error(),
		},
		{
			name:        lintCommandName,
			cmd:         newLintCommand(),
			wantMessage: unsupportedWorkflowError(lintCommandName).Error(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if !test.cmd.SilenceErrors {
				t.Fatalf("%s SilenceErrors = false, want true", test.name)
			}
			if !test.cmd.SilenceUsage {
				t.Fatalf("%s SilenceUsage = false, want true", test.name)
			}

			var output bytes.Buffer
			test.cmd.SetOut(&output)
			test.cmd.SetErr(&output)

			err := test.cmd.Execute()
			if err == nil {
				t.Fatalf("%s Execute() error = nil, want guidance error", test.name)
			}
			if got := err.Error(); got != test.wantMessage {
				t.Fatalf("%s Execute() error = %q, want %q", test.name, got, test.wantMessage)
			}
			if got := strings.TrimSpace(output.String()); got != "" {
				t.Fatalf("%s Execute() wrote %q, want no Cobra usage/error output", test.name, got)
			}
		})
	}
}

func TestRootCommandLoadsConfigIntoRunContext(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)

	root := newRootCommand()
	root.SetArgs([]string{initCommandName})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".config", appName, "config.yaml")); err != nil {
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

	configPath := filepath.Join(repoRoot, ".config", appName, "config.yaml")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read checked-in config: %v", err)
	}
	assertNoRetiredConfigKeys(t, content)

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
	if token := strings.TrimSpace(loaded.Document.Knowl.Operator.Token); token != "" && !strings.HasPrefix(token, "replace-") {
		t.Fatalf("operator token = %q, want empty or placeholder", token)
	}
}

func TestEmbeddedDefaultConfigArtifactLoadsThroughProductionTypes(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	clearKnowlEnv(t)
	assertNoRetiredConfigKeys(t, defaultKnowlConfig)

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
	if token := strings.TrimSpace(loaded.Document.Knowl.Operator.Token); token != "" && !strings.HasPrefix(token, "replace-") {
		t.Fatalf("embedded operator token = %q, want empty or placeholder", token)
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

func testRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve commands_test.go path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func clearKnowlEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"KNOWL_PROVIDER",
		"KNOWL_WORKSPACE_PATH",
		"KNOWL_STORAGE_TYPE",
		"KNOWL_STORAGE_SQLITE_PATH",
		"KNOWL_STORAGE_POSTGRES_DSN",
		"KNOWL_SCOPE",
		"KNOWL_SERVER_LISTEN_ADDR",
		"KNOWL_OPERATOR_TOKEN",
		"KNOWL_MAINTENANCE_REVIEW",
		"KNOWL_MAINTENANCE_AUTO_APPLY",
	} {
		t.Setenv(key, "")
	}
}

func assertNoRetiredConfigKeys(t *testing.T, content []byte) {
	t.Helper()
	var document map[string]any
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("decode config artifact: %v", err)
	}
	for _, key := range []string{"workspace", "scope", "store"} {
		if _, exists := document[key]; exists {
			t.Fatalf("config artifact contains retired top-level key %q", key)
		}
	}
	knowlSection, ok := document["knowl"].(map[string]any)
	if !ok {
		t.Fatal("config artifact is missing knowl section")
	}
	storageSection, ok := knowlSection["storage"].(map[string]any)
	if !ok {
		return
	}
	for _, key := range []string{"driver", "path"} {
		if _, exists := storageSection[key]; exists {
			t.Fatalf("config artifact contains retired knowl.storage key %q", key)
		}
	}
}

func commandHelpOutput(t *testing.T, cmd *cobra.Command) string {
	t.Helper()

	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute help for %s: %v", cmd.Name(), err)
	}
	return output.String()
}
