package knowl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const testInfrastructureScope = "infrastructure"
const testScopeWorkspaceDefaultRef = "durable-default"

func TestScopeWorkspaceResolverPreservesLegacyRootAndCreatesNamedScope(t *testing.T) {
	root := t.TempDir()
	legacy, err := contentfs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Init(); err != nil {
		t.Fatal(err)
	}
	wantSchema := mustReadScopeFile(t, filepath.Join(root, "schema.md"))
	wantLog := mustReadScopeFile(t, filepath.Join(root, "wiki", "log.md"))

	config := explicitScopeWorkspaceConfig(root)
	resolver, err := OpenScopeWorkspaces(config)
	if err != nil {
		t.Fatalf("OpenScopeWorkspaces() error: %v", err)
	}
	workspaces := resolver.All()
	if len(workspaces) != 2 || workspaces[0].Root != root || workspaces[1].Root != filepath.Join(root, "scopes", testInfrastructureScope) {
		t.Fatalf("resolved workspaces = %#v", workspaces)
	}
	if got := mustReadScopeFile(t, filepath.Join(root, "schema.md")); string(got) != string(wantSchema) {
		t.Fatal("legacy schema changed")
	}
	if got := mustReadScopeFile(t, filepath.Join(root, "wiki", "log.md")); string(got) != string(wantLog) {
		t.Fatal("legacy log changed")
	}
	if _, err := os.Stat(filepath.Join(root, ".knowl", scopeLayoutRecordName)); err != nil {
		t.Fatalf("layout record: %v", err)
	}

	restarted, err := OpenScopeWorkspaces(config)
	if err != nil {
		t.Fatalf("restart resolver: %v", err)
	}
	if got := restarted.All(); got[0].Root != root || got[1].Root != workspaces[1].Root {
		t.Fatalf("restart changed roots: %#v", got)
	}
	store, err := NewScopedContentStore(restarted)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "default-export")
	if err := store.ExportOKF(context.Background(), testScopeWorkspaceDefaultRef, destination, contentfs.DefaultExportLimits()); err != nil {
		t.Fatalf("ExportOKF(default) error: %v", err)
	}
	for _, excluded := range []string{"scopes", ".knowl"} {
		if _, statErr := os.Lstat(filepath.Join(destination, excluded)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("default export contains host path %q: %v", excluded, statErr)
		}
	}
}

func TestScopeWorkspaceResolverUsesNestedDefaultForCleanExplicitInstall(t *testing.T) {
	root := t.TempDir()
	resolver, err := OpenScopeWorkspaces(explicitScopeWorkspaceConfig(root))
	if err != nil {
		t.Fatalf("OpenScopeWorkspaces() error: %v", err)
	}
	defaultWorkspace, err := resolver.Resolve(testScopeWorkspaceDefaultRef)
	if err != nil {
		t.Fatal(err)
	}
	wantDefaultRoot := filepath.Join(root, "scopes", string(DefaultPublicScope))
	if defaultWorkspace.Root != wantDefaultRoot {
		t.Fatalf("default root = %q, want %q", defaultWorkspace.Root, wantDefaultRoot)
	}
	for _, marker := range scopeCanonicalMarkerNames() {
		if _, statErr := os.Lstat(filepath.Join(root, marker)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("host root marker %q exists: %v", marker, statErr)
		}
	}
	for _, workspace := range resolver.All() {
		controlRoot := filepath.Join(root, ".knowl", "scopes", scopeControlKey(workspace.Ref))
		for _, directory := range []string{
			filepath.Base("control/staging"),
			filepath.Base("control/recovery"),
			filepath.Base("control/commits"),
		} {
			if info, statErr := os.Stat(filepath.Join(controlRoot, directory)); statErr != nil || !info.IsDir() {
				t.Fatalf("scope %q control %q = %#v, %v", workspace.Name, directory, info, statErr)
			}
		}
		if _, statErr := os.Lstat(filepath.Join(workspace.Root, ".knowl")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("scope %q has nested host control state: %v", workspace.Name, statErr)
		}
	}
}

func TestScopeWorkspaceResolverRejectsAmbiguousDefaultWithoutWritingLayout(t *testing.T) {
	root := t.TempDir()
	initializeScopeWorkspace(t, root)
	initializeScopeWorkspace(t, filepath.Join(root, "scopes", string(DefaultPublicScope)))
	recordPath := filepath.Join(root, ".knowl", scopeLayoutRecordName)

	if _, err := OpenScopeWorkspaces(explicitScopeWorkspaceConfig(root)); !errors.Is(err, ErrScopeLayoutAmbiguous) {
		t.Fatalf("OpenScopeWorkspaces() error = %v, want ErrScopeLayoutAmbiguous", err)
	}
	if _, err := os.Lstat(recordPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ambiguous layout wrote record: %v", err)
	}
}

func TestScopeWorkspaceResolverRejectsSymlinkedNamedRootBeforeLayoutWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "scopes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "scopes", testInfrastructureScope)); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	if _, err := OpenScopeWorkspaces(explicitScopeWorkspaceConfig(root)); !errors.Is(err, ErrScopeWorkspaceUnsafe) {
		t.Fatalf("OpenScopeWorkspaces() error = %v, want ErrScopeWorkspaceUnsafe", err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".knowl", scopeLayoutRecordName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe layout wrote record: %v", err)
	}
}

func TestScopeWorkspaceResolverRejectsSymlinkedHostControl(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".knowl")); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	if _, err := OpenScopeWorkspaces(explicitScopeWorkspaceConfig(root)); !errors.Is(err, ErrScopeWorkspaceUnsafe) {
		t.Fatalf("OpenScopeWorkspaces() error = %v, want ErrScopeWorkspaceUnsafe", err)
	}
}

func TestScopeWorkspaceResolverRejectsSymlinkedCanonicalMarkerAfterRegularMarker(t *testing.T) {
	root := t.TempDir()
	defaultRoot := filepath.Join(root, "scopes", string(DefaultPublicScope))
	if err := os.MkdirAll(defaultRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultRoot, "schema.md"), []byte("schema"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(defaultRoot, "raw")); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	if _, err := OpenScopeWorkspaces(explicitScopeWorkspaceConfig(root)); !errors.Is(err, ErrScopeWorkspaceUnsafe) {
		t.Fatalf("OpenScopeWorkspaces() error = %v, want ErrScopeWorkspaceUnsafe", err)
	}
}

func TestScopedContentStoreKeepsEqualSourceIDsIsolated(t *testing.T) {
	resolver, err := OpenScopeWorkspaces(explicitScopeWorkspaceConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewScopedContentStore(resolver)
	if err != nil {
		t.Fatal(err)
	}

	contents := map[domain.ScopeRef]string{
		testScopeWorkspaceDefaultRef:                        "default content",
		domain.ScopeRef("scope:" + testInfrastructureScope): "infrastructure content",
	}
	for ref, content := range contents {
		digest := sha256.Sum256([]byte(content))
		accepted, acceptErr := store.AcceptSource(context.Background(), domain.SourceEnvelope{
			Scope:     ref,
			Source:    domain.SourceRef{Adapter: "scope-fixture", ID: "shared"},
			Version:   domain.SourceVersion{Version: "1", Digest: hex.EncodeToString(digest[:])},
			MediaType: "text/plain",
			Content:   []byte(content),
		})
		if acceptErr != nil {
			t.Fatalf("AcceptSource(%q) error: %v", ref, acceptErr)
		}
		read, readErr := store.ReadSource(context.Background(), accepted, domain.ReadLimits{Bytes: len(content)})
		if readErr != nil || string(read) != content {
			t.Fatalf("ReadSource(%q) = %q, %v", ref, read, readErr)
		}
	}
	if _, err := store.Schema(context.Background(), "unknown"); !errors.Is(err, domain.ErrScopeUnknown) {
		t.Fatalf("Schema(unknown) error = %v", err)
	}
}

func TestScopeWorkspaceResolverKeepsCleanLegacyConfigAtRoot(t *testing.T) {
	root := t.TempDir()
	config := DefaultConfig()
	config.Workspace = root
	resolver, err := OpenScopeWorkspaces(config)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolver.All(); len(got) != 1 || got[0].Root != root {
		t.Fatalf("legacy resolver roots = %#v", got)
	}
}

func explicitScopeWorkspaceConfig(root string) Config {
	config := DefaultConfig()
	config.Workspace = root
	config.Sources = nil
	config.Scopes = []ScopeConfig{
		{Name: DefaultPublicScope, Ref: testScopeWorkspaceDefaultRef},
		{Name: testInfrastructureScope},
	}
	return config
}

func initializeScopeWorkspace(t *testing.T, root string) {
	t.Helper()
	workspace, err := contentfs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
}

func mustReadScopeFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
