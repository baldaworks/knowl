package knowl

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const testMissingName = "missing"

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

func TestNormalizeSourcesDefaultsAndValidatesMaxDocuments(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")

	for _, test := range []struct {
		name  string
		value int
		want  int
	}{
		{name: "omitted", want: app.DefaultSourceMaxDocuments},
		{name: "minimum", value: 1, want: 1},
		{name: "maximum", value: app.MaximumSourceDocuments, want: app.MaximumSourceDocuments},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := filesystemSource("wiki", filepath.Join(root, test.name), nil)
			source.Sync.MaxDocuments = test.value
			normalized, err := NormalizeSources(workspace, root, []domain.Source{source})
			if err != nil {
				t.Fatalf("NormalizeSources() error: %v", err)
			}
			if got := normalized[0].Sync.MaxDocuments; got != test.want {
				t.Fatalf("MaxDocuments = %d, want %d", got, test.want)
			}
		})
	}

	for _, value := range []int{-1, app.MaximumSourceDocuments + 1} {
		source := filesystemSource("wiki", filepath.Join(root, fmt.Sprintf("invalid-%d", value)), nil)
		source.Sync.MaxDocuments = value
		if _, err := NormalizeSources(workspace, root, []domain.Source{source}); !errors.Is(err, app.ErrSourceInvalid) {
			t.Fatalf("NormalizeSources(MaxDocuments=%d) error = %v, want ErrSourceInvalid", value, err)
		}
	}
}

func TestNormalizeSourcesMaxDocumentsParticipatesInDigest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	base := filesystemSource("wiki", filepath.Join(root, "source"), nil)
	explicitDefault := base
	explicitDefault.Sync.MaxDocuments = app.DefaultSourceMaxDocuments
	raised := base
	raised.Sync.MaxDocuments = 10000

	normalizeOne := func(source domain.Source) domain.Source {
		t.Helper()
		result, normalizeErr := NormalizeSources(workspace, root, []domain.Source{source})
		if normalizeErr != nil {
			t.Fatalf("NormalizeSources() error: %v", normalizeErr)
		}
		return result[0]
	}
	omitted := normalizeOne(base)
	defaulted := normalizeOne(explicitDefault)
	changed := normalizeOne(raised)
	if omitted.ConfigDigest != defaulted.ConfigDigest {
		t.Fatalf("omitted digest %q differs from explicit default %q", omitted.ConfigDigest, defaulted.ConfigDigest)
	}
	if omitted.ConfigDigest == changed.ConfigDigest {
		t.Fatalf("default digest %q equals raised-limit digest", omitted.ConfigDigest)
	}
}

func TestNormalizeSourcesAcceptsOKFFlavor(t *testing.T) {
	root := t.TempDir()
	source := filesystemSource("catalog", filepath.Join(root, "catalog"), nil)
	source.Config.Filesystem.Flavor = domain.SourceFlavorOKF
	normalized, err := NormalizeSources(filepath.Join(root, "workspace"), root, []domain.Source{source})
	if err != nil || len(normalized) != 1 || normalized[0].Config.Filesystem.Flavor != domain.SourceFlavorOKF || normalized[0].ConfigDigest == "" {
		t.Fatalf("NormalizeSources(OKF) = %#v, %v", normalized, err)
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

func TestScopeRegistryNormalizesLegacyDefault(t *testing.T) {
	const legacyScopeRef = "legacy-team"

	root := t.TempDir()
	config := DefaultConfig()
	config.Workspace = filepath.Join(root, "workspace")
	config.Scope = legacyScopeRef
	config.Sources = []domain.Source{filesystemSource("docs", filepath.Join(root, "source"), nil)}

	registry, err := config.ScopeRegistry()
	if err != nil {
		t.Fatalf("ScopeRegistry() error: %v", err)
	}
	context := registry.Default()
	if context.Name != DefaultPublicScope || context.Ref != legacyScopeRef || len(context.Sources) != 1 {
		t.Fatalf("default context = %#v", context)
	}
	if resolved, err := registry.Resolve(""); err != nil || resolved.Ref != context.Ref {
		t.Fatalf("Resolve(empty) = %#v, %v", resolved, err)
	}
}

func TestScopeRegistryNormalizesExplicitScopesDeterministically(t *testing.T) {
	const (
		legacyScopeRef = "legacy-team"
		sharedSourceID = "shared"
	)

	root := t.TempDir()
	config := DefaultConfig()
	config.Workspace = filepath.Join(root, "workspace")
	config.Sources = nil
	config.DefaultScopeName = DefaultPublicScope
	config.Scopes = []ScopeConfig{
		{Name: "operations", Sources: []domain.Source{filesystemSource(sharedSourceID, filepath.Join(root, "operations"), nil)}},
		{Name: DefaultPublicScope, Ref: legacyScopeRef, Sources: []domain.Source{filesystemSource(sharedSourceID, filepath.Join(root, "default"), nil)}},
	}

	registry, err := config.ScopeRegistry()
	if err != nil {
		t.Fatalf("ScopeRegistry() error: %v", err)
	}
	contexts := registry.All()
	if len(contexts) != 2 || contexts[0].Name != DefaultPublicScope || contexts[1].Name != "operations" {
		t.Fatalf("ordered contexts = %#v", contexts)
	}
	if contexts[0].Ref != legacyScopeRef || contexts[1].Ref != "scope:operations" {
		t.Fatalf("refs = %q, %q", contexts[0].Ref, contexts[1].Ref)
	}
	contexts[0].Sources[0].ID = "mutated"
	if registry.Default().Sources[0].ID != sharedSourceID {
		t.Fatal("registry context was mutated through All result")
	}
	if registry.DefaultName() != DefaultPublicScope {
		t.Fatalf("default name = %q", registry.DefaultName())
	}

	first, err := config.normalized()
	if err != nil {
		t.Fatalf("normalized() error: %v", err)
	}
	second, err := first.normalized()
	if err != nil {
		t.Fatalf("normalized() second error: %v", err)
	}
	if got := second.scopeRegistry.All(); len(got) != 2 || got[0].Ref != legacyScopeRef || got[1].Ref != "scope:operations" {
		t.Fatalf("idempotent contexts = %#v", got)
	}
}

func TestScopeRegistryRejectsInvalidConfigurations(t *testing.T) {
	root := t.TempDir()
	newConfig := func() Config {
		config := DefaultConfig()
		config.Workspace = filepath.Join(root, "workspace")
		config.Sources = nil
		return config
	}
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "invalid name", change: func(config *Config) {
			config.Scopes = []ScopeConfig{{Name: "Invalid"}}
		}},
		{name: "missing default", change: func(config *Config) {
			config.Scopes = []ScopeConfig{{Name: "other"}}
		}},
		{name: "disabled default", change: func(config *Config) {
			config.Scopes = []ScopeConfig{{Name: DefaultPublicScope, Disabled: true}}
		}},
		{name: "duplicate ref", change: func(config *Config) {
			config.Scopes = []ScopeConfig{{Name: DefaultPublicScope, Ref: "same"}, {Name: "other", Ref: "same"}}
		}},
		{name: "duplicate name", change: func(config *Config) {
			config.Scopes = []ScopeConfig{{Name: DefaultPublicScope}, {Name: DefaultPublicScope}}
		}},
		{name: "legacy sources conflict", change: func(config *Config) {
			config.Sources = []domain.Source{filesystemSource("legacy", filepath.Join(root, "legacy"), nil)}
			config.Scopes = []ScopeConfig{{Name: DefaultPublicScope}}
		}},
		{name: "legacy ref conflict", change: func(config *Config) {
			config.Scope = "legacy-team"
			config.Scopes = []ScopeConfig{{Name: DefaultPublicScope, Ref: "different"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := newConfig()
			test.change(&config)
			if _, err := config.ScopeRegistry(); !errors.Is(err, ErrScopeConfigInvalid) {
				t.Fatalf("ScopeRegistry() error = %v, want ErrScopeConfigInvalid", err)
			}
		})
	}
}

func TestScopeRegistryResolveRejectsInvalidUnknownAndDisabledNames(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.Sources = nil
	config.Scopes = []ScopeConfig{
		{Name: DefaultPublicScope},
		{Name: "archive", Disabled: true},
	}
	registry, err := config.ScopeRegistry()
	if err != nil {
		t.Fatalf("ScopeRegistry() error: %v", err)
	}

	tests := []struct {
		name string
		want error
	}{
		{name: "Invalid", want: domain.ErrScopeNameInvalid},
		{name: testMissingName, want: domain.ErrScopeUnknown},
		{name: "archive", want: domain.ErrScopeDisabled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, resolveErr := registry.Resolve(domain.ScopeName(test.name)); !errors.Is(resolveErr, test.want) {
				t.Fatalf("Resolve(%q) error = %v, want %v", test.name, resolveErr, test.want)
			}
		})
	}
}

func filesystemSource(id domain.SourceID, root string, include []string) domain.Source {
	return domain.Source{
		ID: id, Type: domain.SourceTypeFilesystem, Enabled: true,
		Config: domain.SourceConfig{Filesystem: &domain.FilesystemSourceConfig{Root: root, Include: include}},
	}
}

func gitSource(id domain.SourceID, remote, ref string, include []string) domain.Source {
	return domain.Source{
		ID: id, Type: domain.SourceTypeGit, Enabled: true,
		Config: domain.SourceConfig{
			Git: &domain.GitSourceConfig{
				Remote: remote, Ref: ref, Include: include,
			},
		},
	}
}

func TestNormalizeGitSourcesAcceptsValidConfigs(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	tests := []struct {
		name     string
		remote   string
		ref      string
		refKind  string
		wantRef  string
		wantKind string
	}{
		{name: "https default branch", remote: "https://github.com/org/repo.git", ref: "main", wantRef: "refs/heads/main", wantKind: domain.GitRefKindBranch},
		{name: "https explicit branch ref", remote: "https://github.com/org/repo", ref: "refs/heads/feature/docs", wantRef: "refs/heads/feature/docs", wantKind: domain.GitRefKindBranch},
		{name: "ssh git user", remote: "git@github.com:org/repo.git", ref: "v1.2.0", refKind: domain.GitRefKindTag, wantRef: "refs/tags/v1.2.0", wantKind: domain.GitRefKindTag},
		{name: "ssh url scheme", remote: "ssh://git@github.com/org/repo.git", ref: "refs/tags/release-1.0", wantRef: "refs/tags/release-1.0", wantKind: domain.GitRefKindTag},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := gitSource("git-source", tc.remote, tc.ref, nil)
			s.Config.Git.RefKind = tc.refKind
			if strings.HasPrefix(tc.remote, "ssh://") || strings.Contains(tc.remote, "@") {
				s.Config.Git.Auth.SecretEnv = "TEST_GIT_SSH_KEY"
				s.Config.Git.KnownHosts = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEexample"}
			}
			normalized, err := NormalizeSources(workspace, root, []domain.Source{s})
			if err != nil {
				t.Fatalf("NormalizeSources() error: %v", err)
			}
			if len(normalized) != 1 {
				t.Fatalf("expected 1 normalized source, got %d", len(normalized))
			}
			git := normalized[0].Config.Git
			if git.Ref != tc.wantRef || git.RefKind != tc.wantKind {
				t.Errorf("ref = %q, kind = %q; want %q, %q", git.Ref, git.RefKind, tc.wantRef, tc.wantKind)
			}
			if normalized[0].ConfigDigest == "" {
				t.Errorf("expected non-empty config digest")
			}
			if git.MaxTransferBytes != 500<<20 || git.MaxCacheBytes != 512<<20 {
				t.Errorf("resource defaults = %d/%d, want 500MiB/512MiB", git.MaxTransferBytes, git.MaxCacheBytes)
			}
		})
	}
}

func TestNormalizeGitSourcesRejectsInvalidConfigs(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	tests := []struct {
		name   string
		source domain.Source
	}{
		{name: "file transport", source: gitSource("git-src", "file:///path/to/repo", "main", nil)},
		{name: "local relative path", source: gitSource("git-src", "./relative/repo", "main", nil)},
		{name: "local absolute path", source: gitSource("git-src", "/absolute/repo", "main", nil)},
		{name: "unencrypted http", source: gitSource("git-src", "http://github.com/org/repo", "main", nil)},
		{name: "credentials in https url", source: gitSource("git-src", "https://token:secret@github.com/org/repo", "main", nil)},
		{name: "ext transport helper", source: gitSource("git-src", "ext::sh -c evil", "main", nil)},
		{name: "commit sha 40 hex", source: gitSource("git-src", "https://github.com/org/repo", "0123456789abcdef0123456789abcdef01234567", nil)},
		{name: "commit sha 64 hex", source: gitSource("git-src", "https://github.com/org/repo", "0123456789abcdef0123456789abcdef012345670123456789abcdef01234567", nil)},
		{name: "invalid ref traversal", source: gitSource("git-src", "https://github.com/org/repo", "refs/heads/../evil", nil)},
		{name: "missing remote", source: gitSource("git-src", "", "main", nil)},
		{name: "missing ref", source: gitSource("git-src", "https://github.com/org/repo", "", nil)},
		{name: "negative transfer limit", source: func() domain.Source {
			s := gitSource("git-src", "https://github.com/org/repo", "main", nil)
			s.Config.Git.MaxTransferBytes = -1
			return s
		}()},
		{name: "oversized cache limit", source: func() domain.Source {
			s := gitSource("git-src", "https://github.com/org/repo", "main", nil)
			s.Config.Git.MaxCacheBytes = 8<<30 + 1
			return s
		}()},
		{name: "ref kind mismatch", source: func() domain.Source {
			s := gitSource("git-src", "https://github.com/org/repo", "refs/heads/main", nil)
			s.Config.Git.RefKind = "tag"
			return s
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NormalizeSources(workspace, root, []domain.Source{tc.source}); err == nil {
				t.Fatalf("NormalizeSources() expected error for %s, got nil", tc.name)
			}
		})
	}
}

func sourceWithSync(root string, syncPolicy domain.SourceSyncPolicy) domain.Source {
	source := filesystemSource("wiki", filepath.Join(root, "source"), nil)
	source.Sync = syncPolicy
	return source
}
