package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
			path: filepath.Join(repoRoot, "deploy", "sidecar", "compose.yaml"),
			want: []string{
				"127.0.0.1:8080:8080",
				"/var/lib/knowl",
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
