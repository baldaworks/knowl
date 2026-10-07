package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const operatorTestWindowsOS = "windows"

func TestOperatorWikiDirectoryChildren(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	writeCanonicalFixture(t, workspace, "wiki/entities/orphan.md", validWorkspacePage("entities/orphan", "Orphan", testWorkspaceSourceRef, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/entities/index.md", []byte("# Entities\n"))
	writeCanonicalFixture(t, workspace, "wiki/entities/log.md", []byte("# Log\n"))
	writeCanonicalFixture(t, workspace, "wiki/entities/image.png", []byte("image"))
	root, err := workspace.WikiDirectoryChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 10})
	if err != nil || len(root.Items) != 4 || root.Items[1].Path != "entities" || root.Items[1].Kind != operatorFolderKind || root.Items[2].Path != okfIndexFilename || root.Items[2].PageID != operatorRootID {
		t.Fatalf("root directory = %#v, %v", root, err)
	}
	first, err := workspace.WikiDirectoryChildren(t.Context(), testScope, "entities", app.OperatorReadOptions{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].Path != "entities/"+okfIndexFilename || first.Items[0].PageID != "entities/index" || first.NextKey != "entities/"+okfIndexFilename {
		t.Fatalf("first nested page = %#v, %v", first, err)
	}
	second, err := workspace.WikiDirectoryChildren(t.Context(), testScope, "entities", app.OperatorReadOptions{Limit: 1, Continuation: app.OperatorContinuation{Key: first.NextKey, SnapshotVersion: first.SnapshotVersion}})
	if err != nil || len(second.Items) != 1 || second.Items[0].Path != "entities/orphan.md" || second.Items[0].PageID != "entities/orphan" || second.NextKey != "" {
		t.Fatalf("second nested page = %#v, %v", second, err)
	}
	writeCanonicalFixture(t, workspace, "wiki/entities/new.md", validWorkspacePage("entities/new", "New", testWorkspaceSourceRef, "Body"))
	if _, err := workspace.WikiDirectoryChildren(t.Context(), testScope, "entities", app.OperatorReadOptions{Limit: 1, Continuation: app.OperatorContinuation{Key: first.NextKey, SnapshotVersion: first.SnapshotVersion}}); !errors.Is(err, app.ErrOperatorSnapshotChanged) {
		t.Fatalf("changed branch = %v", err)
	}
	other, err := workspace.WikiDirectoryChildren(t.Context(), knowl.ScopeRef("other"), "entities", app.OperatorReadOptions{Limit: 10})
	if err != nil || other.SnapshotVersion == first.SnapshotVersion {
		t.Fatalf("scope-bound snapshot = %#v, %v", other, err)
	}
}

func TestOperatorWikiDirectoryRejectsAliases(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	for _, dir := range []string{"../raw", "entities/../entities", "entities%2Forphan", "entities\\orphan", "."} {
		if _, err := workspace.WikiDirectoryChildren(t.Context(), testScope, dir, app.OperatorReadOptions{Limit: 10}); !errors.Is(err, app.ErrOperatorInvalidRequest) {
			t.Errorf("directory %q: %v", dir, err)
		}
	}
}

func TestOperatorWikiDirectoryEmptyAndSymlink(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	writeCanonicalFixture(t, workspace, "wiki/empty/image.png", []byte("asset"))
	empty, err := workspace.WikiDirectoryChildren(t.Context(), testScope, "empty", app.OperatorReadOptions{Limit: 10})
	if err != nil || len(empty.Items) != 0 || len(empty.SnapshotVersion) != 64 {
		t.Fatalf("empty branch = %#v, %v", empty, err)
	}
	if _, err := workspace.WikiDirectoryChildren(t.Context(), testScope, "missing", app.OperatorReadOptions{Limit: 10}); !errors.Is(err, app.ErrOperatorDirectoryNotFound) {
		t.Fatalf("missing branch = %v", err)
	}
	if err := os.Symlink(filepath.Join(workspace.root, "raw"), filepath.Join(workspace.root, "wiki", "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := workspace.WikiDirectoryChildren(t.Context(), testScope, "linked", app.OperatorReadOptions{Limit: 10}); !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
		t.Fatalf("linked branch = %v", err)
	}
	root, err := workspace.WikiDirectoryChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 10})
	if err != nil {
		t.Fatalf("linked root child = %v", err)
	}
	for _, item := range root.Items {
		if item.Path == "linked" {
			t.Fatal("symlink advertised as wiki folder")
		}
	}
}

func TestOperatorWikiDirectoryOmitsUnrelatedAssets(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	writeCanonicalFixture(t, workspace, "wiki/entities/article.md", validWorkspacePage("entities/article", "Article", testWorkspaceSourceRef, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/entities/diagram%20v2.png", []byte("asset"))
	if err := os.Symlink(filepath.Join(workspace.root, "raw"), filepath.Join(workspace.root, "wiki", "entities", "asset-link.png")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	branch, err := workspace.WikiDirectoryChildren(t.Context(), testScope, "entities", app.OperatorReadOptions{Limit: 10})
	if err != nil || len(branch.Items) != 1 || branch.Items[0].Path != "entities/article.md" {
		t.Fatalf("branch with unrelated assets = %#v, %v", branch, err)
	}
}

func TestOperatorWikiDirectoryDepthBoundary(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	segments := make([]string, app.DefaultCatalogLimits().MaxDepth)
	for i := range segments {
		segments[i] = fmt.Sprintf("d%d", i)
	}
	deep := strings.Join(segments, "/")
	writeCanonicalFixture(t, workspace, "wiki/"+deep+"/article.md", validWorkspacePage(deep+"/article", "Article", testWorkspaceSourceRef, "Body"))
	allowed, err := workspace.WikiDirectoryChildren(t.Context(), testScope, deep, app.OperatorReadOptions{Limit: 10})
	if err != nil || len(allowed.Items) != 1 || allowed.Items[0].PageID != knowl.PageID(deep+"/article") {
		t.Fatalf("last readable depth = %#v, %v", allowed, err)
	}
	writeCanonicalFixture(t, workspace, "wiki/"+deep+"/deeper/child.md", validWorkspacePage(deep+"/deeper/child", "Child", testWorkspaceSourceRef, "Body"))
	if _, err := workspace.WikiDirectoryChildren(t.Context(), testScope, deep, app.OperatorReadOptions{Limit: 10}); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("folder beyond max depth = %v", err)
	}
	if _, err := workspace.WikiDirectoryChildren(t.Context(), testScope, deep+"/deeper", app.OperatorReadOptions{Limit: 10}); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("request beyond max depth = %v", err)
	}
}

func TestOperatorWikiDirectoryAcceptedPercentAndColonPaths(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	cases := []string{"entities/100%-ready", "entities/version%2e1", "release%2026/cpu%20guide"}
	if runtime.GOOS != operatorTestWindowsOS {
		cases = append(cases, "ops:team/cpu:guide")
	}
	for _, id := range cases {
		writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, testWorkspaceSourceRef, "Body"))
		branch, err := workspace.WikiDirectoryChildren(t.Context(), testScope, filepath.ToSlash(filepath.Dir(id)), app.OperatorReadOptions{Limit: 10})
		if err != nil {
			t.Fatalf("directory for %q: %v", id, err)
		}
		found := false
		for _, item := range branch.Items {
			if item.PageID == knowl.PageID(id) {
				found = true
			}
		}
		if !found {
			t.Fatalf("page %q absent from %#v", id, branch.Items)
		}
		page, err := workspace.Page(t.Context(), testScope, knowl.PageID(id), knowl.ReadLimits{})
		if err != nil || page.ID != knowl.PageID(id) {
			t.Fatalf("open %q = %#v, %v", id, page, err)
		}
	}
}

func TestOperatorWikiDirectoryOmitsEncodedAliasPage(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	writeCanonicalFixture(t, workspace, "wiki/entities/good.md", validWorkspacePage("entities/good", "Good", testWorkspaceSourceRef, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/entities/%2e%2e.md", validWorkspacePage("entities/%2e%2e", "Alias", testWorkspaceSourceRef, "Body"))
	branch, err := workspace.WikiDirectoryChildren(t.Context(), testScope, "entities", app.OperatorReadOptions{Limit: 10})
	if err != nil || len(branch.Items) != 2 || len(branch.SnapshotVersion) != 64 || branch.Items[0].Kind != operatorUnsupportedKind || branch.Items[0].PageID != "" || branch.Items[1].PageID != "entities/good" {
		t.Fatalf("encoded alias alongside valid page = %#v, %v", branch, err)
	}
	if _, err := workspace.Page(t.Context(), testScope, "entities/%2e%2e", knowl.ReadLimits{}); !errors.Is(err, app.ErrOperatorInvalidRequest) {
		t.Fatalf("encoded alias open = %v", err)
	}
}

func TestOperatorWikiDirectoryDoesNotOpenUnsupportedFile(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	target := filepath.Join(workspace.root, "wiki", "entities", "%2e%2e.md")
	writeCanonicalFixture(t, workspace, "wiki/entities/%2e%2e.md", validWorkspacePage("entities/%2e%2e", "Alias", testWorkspaceSourceRef, "Body"))
	if err := os.Chmod(target, 0); err != nil {
		t.Skipf("cannot restrict file: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o600) })
	if file, err := os.Open(target); err == nil {
		_ = file.Close()
		t.Skip("runtime can read zero-permission files")
	}
	branch, err := workspace.WikiDirectoryChildren(t.Context(), testScope, "entities", app.OperatorReadOptions{Limit: 10})
	if err != nil || len(branch.Items) != 1 || branch.Items[0].Kind != operatorUnsupportedKind {
		t.Fatalf("unreadable unsupported file = %#v, %v", branch, err)
	}
}

func TestOperatorWikiDirectoryEntryBudget(t *testing.T) {
	directory := t.TempDir()
	for i := 0; i <= app.DefaultCatalogLimits().MaxEdges; i++ {
		name := filepath.Join(directory, fmt.Sprintf("asset-%05d.bin", i))
		file, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	branch, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = branch.Close() }()
	if _, _, err := operatorDirectoryInventory(t.Context(), branch, ""); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("oversized branch = %v", err)
	}
}
