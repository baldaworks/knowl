package knowl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestNormalizeSourcesAcceptsMaximumAndRejectsOverflow(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	sources := make([]domain.Source, maximumSources, maximumSources+1)
	for index := range maximumSources {
		sources[index] = filesystemSource(domain.SourceID(fmt.Sprintf("source-%03d", index)), filepath.Join(root, fmt.Sprintf("source-%03d", index)), nil)
	}
	if normalized, err := NormalizeSources(workspace, root, sources); err != nil || len(normalized) != maximumSources {
		t.Fatalf("maximum sources = %d, %v", len(normalized), err)
	}
	sources = append(sources, filesystemSource("overflow", filepath.Join(root, "overflow"), nil))
	if _, err := NormalizeSources(workspace, root, sources); err == nil {
		t.Fatal("source overflow unexpectedly accepted")
	}
}

func TestConfigValidateAllowsServiceBindAddress(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.ListenAddr = "0.0.0.0:8080"

	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error: %v", err)
	}
}

func TestConfigValidateRejectsNamedHostBindAddress(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.ListenAddr = "knowl:8080"

	if err := config.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want invalid listen host")
	}
}

func TestNormalizeSourcesIsDeterministicForTwoNamedSources(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	sources := []domain.Source{
		filesystemSource("engineering", filepath.Join(root, "engineering"), []string{"docs/*.md", "**/*.md", "docs/*.md"}),
		filesystemSource("operations", filepath.Join(root, "operations"), nil),
	}

	first, err := NormalizeSources(workspace, root, sources)
	if err != nil {
		t.Fatalf("NormalizeSources() error: %v", err)
	}
	second, err := NormalizeSources(workspace, root, sources)
	if err != nil {
		t.Fatalf("NormalizeSources() second error: %v", err)
	}
	if first[0].ConfigDigest == "" || first[0].ConfigDigest != second[0].ConfigDigest {
		t.Fatalf("config digest = %q then %q, want stable non-empty values", first[0].ConfigDigest, second[0].ConfigDigest)
	}
	if got := strings.Join(first[0].Config.Filesystem.Include, ","); got != "**/*.md,docs/*.md" {
		t.Fatalf("normalized include = %q", got)
	}
	if got := first[1].Config.Filesystem.Include; len(got) != 1 || got[0] != defaultSourceInclude {
		t.Fatalf("default include = %#v", got)
	}
	if first[0].Config.Filesystem.Flavor != domain.SourceFlavorMarkdown {
		t.Fatalf("default flavor = %q", first[0].Config.Filesystem.Flavor)
	}
}

func TestNormalizeSourcesRejectsInvalidConfiguration(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	tests := []struct {
		name    string
		sources []domain.Source
	}{
		{name: "duplicate id", sources: []domain.Source{filesystemSource("wiki", filepath.Join(root, "one"), nil), filesystemSource("wiki", filepath.Join(root, "two"), nil)}},
		{name: "unsupported type", sources: []domain.Source{{ID: "wiki", Type: "git", Config: domain.SourceConfig{Filesystem: &domain.FilesystemSourceConfig{Root: filepath.Join(root, "one")}}}}},
		{name: "missing union", sources: []domain.Source{{ID: "wiki", Type: domain.SourceTypeFilesystem}}},
		{name: "invalid include", sources: []domain.Source{filesystemSource("wiki", filepath.Join(root, "one"), []string{"../*.md"})}},
		{name: "workspace child", sources: []domain.Source{filesystemSource("wiki", filepath.Join(workspace, "sources"), nil)}},
		{name: "workspace parent", sources: []domain.Source{filesystemSource("wiki", root, nil)}},
		{name: "negative interval", sources: []domain.Source{sourceWithSync(root, domain.SourceSyncPolicy{Interval: -time.Second})}},
		{name: "interval too large", sources: []domain.Source{sourceWithSync(root, domain.SourceSyncPolicy{Interval: 25 * time.Hour})}},
		{name: "retry order", sources: []domain.Source{sourceWithSync(root, domain.SourceSyncPolicy{RetryInitial: 2 * time.Minute, RetryMaximum: time.Minute})}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeSources(workspace, root, test.sources); err == nil {
				t.Fatal("NormalizeSources() error = nil, want rejection")
			}
		})
	}
}

func TestNormalizedSourceStatusIdentityRedactsSecretShapedConfig(t *testing.T) {
	root := t.TempDir()
	secret := "bearer-secret-material"
	source := filesystemSource("wiki", filepath.Join(root, secret), nil)
	source.Config.Filesystem.URIBase = "https://wiki.example.test/" + secret
	normalized, err := NormalizeSources(filepath.Join(root, "workspace"), root, []domain.Source{source})
	if err != nil {
		t.Fatal(err)
	}
	status, err := json.Marshal(domain.SourceStatus{SourceID: source.ID, Type: source.Type, ConfigDigest: normalized[0].ConfigDigest})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(status), secret) || strings.Contains(normalized[0].ConfigDigest, secret) {
		t.Fatalf("redacted identity contains secret-shaped configuration: %s", status)
	}
}

func TestNormalizeSourcesRejectsSymlinkWorkspaceOverlap(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	_, err := NormalizeSources(workspace, root, []domain.Source{filesystemSource("wiki", filepath.Join(alias, "docs"), nil)})
	if err == nil {
		t.Fatal("NormalizeSources() error = nil, want symlink overlap rejection")
	}
}

func TestConfigValidateKeepsNilSourcesBackwardCompatible(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.Sources = nil
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error: %v", err)
	}
}

func filesystemSource(id domain.SourceID, root string, include []string) domain.Source {
	return domain.Source{
		ID: id, Type: domain.SourceTypeFilesystem, Enabled: true,
		Config: domain.SourceConfig{Filesystem: &domain.FilesystemSourceConfig{Root: root, Include: include}},
	}
}

func sourceWithSync(root string, syncPolicy domain.SourceSyncPolicy) domain.Source {
	source := filesystemSource("wiki", filepath.Join(root, "source"), nil)
	source.Sync = syncPolicy
	return source
}
