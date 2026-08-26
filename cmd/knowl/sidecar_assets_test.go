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

func TestSidecarAssetsMentionCanonicalRuntimePaths(t *testing.T) {
	repoRoot := testRepoRoot(t)
	tests := []struct {
		path string
		want []string
	}{
		{
			path: filepath.Join(repoRoot, "Dockerfile"),
			want: []string{
				"golang:1.26.6@sha256:" +
					"640a234f4bea3e399c056b7b8f9c667c4939befae8db2f14e9785e16eccd4205",
				"org.opencontainers.image.version",
				"USER 65532:65532",
				"HEALTHCHECK",
				"/usr/local/bin/knowl-entrypoint",
				"/etc/knowl/config.yaml",
				"VOLUME [\"/var/lib/knowl\"]",
			},
		},
		{
			path: filepath.Join(repoRoot, "deploy", "sidecar", "entrypoint.sh"),
			want: []string{
				"knowl --config-dir \"$config_dir\" init",
				"knowl --config-dir \"$config_dir\" start",
			},
		},
		{
			path: filepath.Join(repoRoot, "scripts", "smoke-test-sidecar.sh"),
			want: []string{
				operatorTokenEnvName,
				"/v1/retrieve?query=session",
				"$base_url/mcp",
				"docker volume rm \"$volume\"",
				"docker rm -f \"$container\"",
			},
		},
		{
			path: filepath.Join(repoRoot, "deploy", "sidecar", "compose.yaml"),
			want: []string{
				"ghcr.io/baldaworks/knowl:local",
				"127.0.0.1:8080:8080",
				"/var/lib/knowl",
				"/sources/engineering:ro",
				"/sources/operations:ro",
				"/readyz",
			},
		},
		{
			path: filepath.Join(repoRoot, "docs", "sidecar.md"),
			want: []string{
				"/healthz",
				"/readyz",
				"/v1/retrieve",
				publicIngestPath,
				"/v1/operations/{operation_id}",
				"runtime.providers",
				"`raw/`",
				"`wiki/`",
				"Initial bootstrap is optional",
			},
		},
	}

	for _, test := range tests {
		t.Run(filepath.Base(test.path), func(t *testing.T) {
			content, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatalf("read %s: %v", test.path, err)
			}
			text := string(content)
			for _, want := range test.want {
				if !strings.Contains(text, want) {
					t.Fatalf("%s missing %q", test.path, want)
				}
			}
		})
	}
}
