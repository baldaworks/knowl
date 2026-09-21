package releasecheck

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/baldaworks/knowl/internal/releaseinfo"
	"gopkg.in/yaml.v3"
)

const (
	testVersion       = "0.6.0"
	driftedPackagePin = "@baldaworks/knowl@0.6.1"
	driftedVersion    = "0.6.1"
	packageNameKey    = "name"
	packageVersionKey = "version"
)

func TestRepositoryContract(t *testing.T) {
	if err := Check(Options{Root: repositoryRoot(t), Version: testVersion}); err != nil {
		t.Fatalf("Check() error: %v", err)
	}
}

func TestRepositoryContractRejectsVersionDrift(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, string)
		wantErr error
	}{
		{
			name: "plugin manifest",
			mutate: func(t *testing.T, root string) {
				mutateJSONObject(t, filepath.Join(root, "plugins/knowl/.codex-plugin/plugin.json"), func(object map[string]any) {
					object[packageVersionKey] = driftedVersion
				})
			},
			wantErr: errPluginContract,
		},
		{
			name: "MCP package pin",
			mutate: func(t *testing.T, root string) {
				mutateJSONObject(t, filepath.Join(root, "plugins/knowl/.mcp.json"), func(object map[string]any) {
					servers := object["mcpServers"].(map[string]any)
					server := servers["knowl"].(map[string]any)
					args := server["args"].([]any)
					args[1] = driftedPackagePin
				})
			},
			wantErr: errPluginContract,
		},
		{
			name: "mutable MCP package pin",
			mutate: func(t *testing.T, root string) {
				mutateJSONObject(t, filepath.Join(root, "plugins/knowl/.mcp.json"), func(object map[string]any) {
					servers := object["mcpServers"].(map[string]any)
					server := servers["knowl"].(map[string]any)
					args := server["args"].([]any)
					args[1] = "@baldaworks/knowl@latest"
				})
			},
			wantErr: errPluginContract,
		},
		{
			name: "MCP token",
			mutate: func(t *testing.T, root string) {
				addMCPServerField(t, root, "token", "fixture-secret")
			},
			wantErr: errPluginContract,
		},
		{
			name: "MCP URL",
			mutate: func(t *testing.T, root string) {
				addMCPServerField(t, root, "url", "http://127.0.0.1:8080/mcp")
			},
			wantErr: errPluginContract,
		},
		{
			name: "MCP port",
			mutate: func(t *testing.T, root string) {
				addMCPServerField(t, root, "port", float64(8080))
			},
			wantErr: errPluginContract,
		},
		{
			name: "extra skill",
			mutate: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "plugins/knowl/skills/agent-setup"), 0o700); err != nil {
					t.Fatalf("create extra skill: %v", err)
				}
			},
			wantErr: errSkillContract,
		},
		{
			name: "setup skill version",
			mutate: func(t *testing.T, root string) {
				mutateJSONObject(t, filepath.Join(root, "plugins/knowl/skills/setup/contract.json"), func(object map[string]any) {
					object["release_version"] = driftedVersion
				})
			},
			wantErr: errSkillContract,
		},
		{
			name: "run skill version",
			mutate: func(t *testing.T, root string) {
				mutateJSONObject(t, filepath.Join(root, "plugins/knowl/skills/run/contract.json"), func(object map[string]any) {
					object["release_version"] = driftedVersion
				})
			},
			wantErr: errSkillContract,
		},
		{
			name: "release documentation metadata",
			mutate: func(t *testing.T, root string) {
				mutateJSONObject(t, filepath.Join(root, "docs/releases/v0.6.0.json"), func(object map[string]any) {
					object["npm_package"] = "@baldaworks/other"
				})
			},
			wantErr: errDocumentationContract,
		},
		{
			name: "enabled distributions",
			mutate: func(t *testing.T, root string) {
				mutateOmnidistDistributions(t, filepath.Join(root, ".omnidist/omnidist.yaml"))
			},
			wantErr: errOmnidistContract,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := copyContractFixture(t)
			test.mutate(t, root)
			err := Check(Options{Root: root, Version: testVersion})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Check() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestRepositoryContractRejectsInvalidReleaseVersions(t *testing.T) {
	for _, version := range []string{"", "latest", "0.6", "v0.6.0-beta.1"} {
		t.Run(version, func(t *testing.T) {
			err := Check(Options{Root: repositoryRoot(t), Version: version})
			if !errors.Is(err, releaseinfo.ErrInvalidVersion) {
				t.Fatalf("Check() error = %v, want invalid version", err)
			}
		})
	}
}

func TestStagedNPMRejectsNativeVersionDrift(t *testing.T) {
	root := t.TempDir()
	dependencies := make(map[string]string, len(targets))
	for _, expected := range targets {
		name := npmPackage + "-" + expected.suffix
		dependencies[name] = testVersion
		writePackageJSON(t, root, name, map[string]any{
			packageNameKey:    name,
			packageVersionKey: testVersion,
			"os":              []string{expected.npmOS},
			"cpu":             []string{expected.npmCPU},
		})
		binary := filepath.Join(root, filepath.FromSlash(name), "bin", expected.binary)
		if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
			t.Fatalf("create staged binary directory: %v", err)
		}
		if err := os.WriteFile(binary, []byte("fixture"), 0o700); err != nil {
			t.Fatalf("write staged binary: %v", err)
		}
	}
	writePackageJSON(t, root, npmPackage, map[string]any{
		packageNameKey:         npmPackage,
		packageVersionKey:      testVersion,
		"bin":                  map[string]string{binaryName: "knowl.js"},
		"optionalDependencies": dependencies,
	})

	if err := checkStagedNPM(root, testVersion); err != nil {
		t.Fatalf("checkStagedNPM() error: %v", err)
	}
	drifted := npmPackage + "-linux-x64"
	writePackageJSON(t, root, drifted, map[string]any{
		packageNameKey:    drifted,
		packageVersionKey: driftedVersion,
		"os":              []string{"linux"},
		"cpu":             []string{"x64"},
	})
	if err := checkStagedNPM(root, testVersion); !errors.Is(err, errStagedNPMContract) {
		t.Fatalf("checkStagedNPM() error = %v, want native version drift", err)
	}
}

func copyContractFixture(t *testing.T) string {
	t.Helper()
	sourceRoot := repositoryRoot(t)
	targetRoot := t.TempDir()
	paths := []string{
		".omnidist/omnidist.yaml",
		".agents/plugins/marketplace.json",
		"docs/releases/v0.6.0.json",
		"plugins/knowl/.codex-plugin/plugin.json",
		"plugins/knowl/.mcp.json",
		"plugins/knowl/skills/run/contract.json",
		"plugins/knowl/skills/setup/contract.json",
	}
	for _, relative := range paths {
		source := filepath.Join(sourceRoot, filepath.FromSlash(relative))
		target := filepath.Join(targetRoot, filepath.FromSlash(relative))
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read %s: %v", source, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatalf("create fixture directory: %v", err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			t.Fatalf("write %s: %v", target, err)
		}
	}
	return targetRoot
}

func addMCPServerField(t *testing.T, root, key string, value any) {
	t.Helper()
	mutateJSONObject(t, filepath.Join(root, "plugins/knowl/.mcp.json"), func(object map[string]any) {
		servers := object["mcpServers"].(map[string]any)
		server := servers["knowl"].(map[string]any)
		server[key] = value
	})
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve releasecheck test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func writePackageJSON(t *testing.T, root, name string, value any) {
	t.Helper()
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	path := packageJSONPath(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create package directory: %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mutateJSONObject(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var object map[string]any
	if err := json.Unmarshal(content, &object); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	mutate(object)
	updated, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mutateOmnidistDistributions(t *testing.T, path string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	profiles := document["profiles"].(map[string]any)
	profile := profiles["default"].(map[string]any)
	profile["enabled-distributions"] = []string{"npm", "uv"}
	updated, err := yaml.Marshal(document)
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
