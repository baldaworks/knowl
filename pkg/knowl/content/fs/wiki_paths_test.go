package fs

import (
	"context"
	"errors"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSnapshotCapturesCanonicalWikiPlacements(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = workspace.Init(); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if path == canonicalIndexPath {
			content = "---\nokf_version: \"0.2\"\n---\n" + content
		}
		absolute := filepath.Join(workspace.Root(), path)
		if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("wiki/index.md", "# Wiki\n\n* [A](a/index.md)\n* [B](b/index.md)\n")
	write("wiki/a/index.md", "# Alpha\n\n* [Shared](../entities/item.md)\n")
	write("wiki/b/index.md", "# Beta\n\n* [Shared](../entities/item.md)\n")
	write("wiki/entities/item.md", string(validWorkspacePage("entities/item", "Item", testWorkspaceSourceRef, "")))
	snapshot, err := workspace.Snapshot(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pages) != 1 || !reflect.DeepEqual(snapshot.Pages[0].WikiPaths, []string{"Wiki / Alpha", "Wiki / Beta"}) {
		t.Fatalf("paths: %#v", snapshot.Pages)
	}
	original := snapshot.Pages[0]
	write("wiki/index.md", "# Wiki\n\n* [B](b/index.md)\n* [A](a/index.md)\n")
	again, err := workspace.Snapshot(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original.WikiPaths, again.Pages[0].WikiPaths) {
		t.Fatal("link order changed paths")
	}
	write("wiki/a/index.md", "# Renamed\n\n* [Shared](../entities/item.md)\n")
	renamed, err := workspace.Snapshot(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Pages[0].Digest != original.Digest || !reflect.DeepEqual(renamed.Pages[0].WikiPaths, []string{"Wiki / Beta", "Wiki / Renamed"}) {
		t.Fatalf("rename: %#v", renamed.Pages[0])
	}
	write("wiki/a/index.md", "# Cycle\n\n* [Root](../index.md)\n")
	if _, err = workspace.Snapshot(t.Context(), testScope); !errors.Is(err, ErrContentInvalid) {
		t.Fatalf("cycle error=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = workspace.Snapshot(ctx, testScope); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestWikiPathsUseOnlyCanonicalMembership(t *testing.T) {
	pages := []knowl.PageSnapshot{{Path: "wiki/entities/deep/page.md"}}
	catalogs := []knowl.PageSnapshot{{Path: canonicalIndexPath, Title: "Root", Content: "# Root\n"}, {Path: "wiki/elsewhere/index.md", Title: "Unlinked", Content: "# Unlinked\n* [Page](../entities/deep/page.md)\n"}}
	if err := captureWikiPaths(t.Context(), pages, catalogs, 1024); err != nil {
		t.Fatal(err)
	}
	if len(pages[0].WikiPaths) != 0 {
		t.Fatalf("unreachable placement gained context: %#v", pages[0].WikiPaths)
	}
	catalogs[0].Content = "# Root\n* [Nested](elsewhere/index.md)\n"
	if err := captureWikiPaths(t.Context(), pages, catalogs, 1024); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pages[0].WikiPaths, []string{"Root / Unlinked"}) {
		t.Fatalf("canonical membership=%#v", pages[0].WikiPaths)
	}
	if err := captureWikiPaths(t.Context(), pages, catalogs, 5); !errors.Is(err, ErrContentInvalid) {
		t.Fatalf("limit error=%v", err)
	}
	catalogs[0].Content = "# Root\n* [Missing](missing.md)\n"
	if err := captureWikiPaths(t.Context(), pages, catalogs, 1024); err != nil {
		t.Fatalf("pending link blocked snapshot diagnostics: %v", err)
	}
	if len(pages[0].WikiPaths) != 0 {
		t.Fatalf("missing target fabricated a path: %#v", pages[0].WikiPaths)
	}
}

func TestSnapshotPendingCatalogLinkDoesNotBlockRepair(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace.Root(), "wiki/entities"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(workspace.Root(), path), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(canonicalIndexPath, []byte("---\nokf_version: '0.2'\n---\n# Pending Wiki\n* [Existing](entities/existing.md)\n* [Future](entities/future.md)\n"))
	write("wiki/entities/existing.md", validWorkspacePage("entities/existing", "Existing", testWorkspaceSourceRef, ""))
	snapshot, err := workspace.Snapshot(t.Context(), testScope)
	if err != nil || len(snapshot.Pages) != 1 || !reflect.DeepEqual(snapshot.Pages[0].WikiPaths, []string{"Pending Wiki"}) {
		t.Fatalf("pending snapshot=%#v error=%v", snapshot, err)
	}
	write("wiki/entities/future.md", validWorkspacePage("entities/future", "Future", testWorkspaceSourceRef, ""))
	snapshot, err = workspace.Snapshot(t.Context(), testScope)
	if err != nil || len(snapshot.Pages) != 2 {
		t.Fatalf("published snapshot=%#v error=%v", snapshot, err)
	}
	for _, page := range snapshot.Pages {
		if !reflect.DeepEqual(page.WikiPaths, []string{"Pending Wiki"}) {
			t.Fatalf("published context=%#v", page)
		}
	}
}
