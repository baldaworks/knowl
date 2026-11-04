package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const operatorWikiAIIndex = "AI/index"

func TestOperatorWikiPagesNearestRealParentAndOptionalIndex(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	writeCanonicalFixture(t, workspace, "wiki/index.md", nil)
	writeCanonicalFixture(t, workspace, "wiki/AI/index.md", []byte("Additional information without a heading.\n"))
	writeCanonicalFixture(t, workspace, "wiki/AI/Prism.md", validWorkspacePage("AI/Prism", "Prism title", testWorkspaceSourceRef, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/AI/OpenSpec.md", validWorkspacePage("AI/OpenSpec", "OpenSpec title", testWorkspaceSourceRef, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/concepts/ADRs/index.md", nil)
	writeCanonicalFixture(t, workspace, "wiki/concepts/ADRs/one.md", validWorkspacePage("concepts/ADRs/one", "ADR one", testWorkspaceSourceRef, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/entities/loose.md", validWorkspacePage("entities/loose", "Loose title", testWorkspaceSourceRef, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/catalogs/wrapper/deep.md", validWorkspacePage("catalogs/wrapper/deep", "Deep title", testWorkspaceSourceRef, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/entities/log.md", []byte("not a page"))
	writeCanonicalFixture(t, workspace, "wiki/entities/image.png", []byte("asset"))
	writeCanonicalFixture(t, workspace, "wiki/entities/%2e%2e.md", []byte("unsupported alias"))
	// An invalid accepted-source manifest proves tree title reads do not fan
	// out into full Page/provenance reads.
	writeCanonicalFixture(t, workspace, "raw/broken/manifest.yaml", []byte("invalid: ["))
	root, err := workspace.WikiPageChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 100})
	want := []knowl.OperatorWikiPage{
		{Path: "AI/index.md", PageID: operatorWikiAIIndex, Title: "AI", ChildrenDirectory: "AI"},
		{Path: "catalogs/wrapper/deep.md", PageID: "catalogs/wrapper/deep", Title: "Deep title"},
		{Path: "concepts/ADRs/index.md", PageID: "concepts/ADRs/index", Title: "ADRs", ChildrenDirectory: "concepts/ADRs"},
		{Path: "entities/loose.md", PageID: "entities/loose", Title: "Loose title"},
	}
	if err != nil || !reflect.DeepEqual(root.Items, want) {
		t.Fatalf("root pages = %#v, %v; want %#v", root.Items, err, want)
	}
	ai, err := workspace.WikiPageChildren(t.Context(), testScope, "AI", app.OperatorReadOptions{Limit: 100})
	if err != nil || len(ai.Items) != 2 || ai.Items[0].PageID != "AI/OpenSpec" || ai.Items[1].PageID != "AI/Prism" {
		t.Fatalf("AI pages = %#v, %v", ai, err)
	}
	for _, body := range []string{"", "More prose.\n", "# AI\n\n1. [Unrelated](../entities/loose.md)\n\n## Details\nAdditional prose.\n"} {
		writeCanonicalFixture(t, workspace, "wiki/AI/index.md", []byte(body))
		page, err := workspace.Page(t.Context(), testScope, operatorWikiAIIndex, knowl.ReadLimits{})
		if err != nil || page.Markdown != body || page.ID != operatorWikiAIIndex {
			t.Fatalf("optional index page = %#v, %v", page, err)
		}
		children, err := workspace.WikiPageChildren(t.Context(), testScope, "AI", app.OperatorReadOptions{Limit: 100})
		if err != nil || !reflect.DeepEqual(children.Items, ai.Items) {
			t.Fatalf("body-independent children = %#v, %v", children, err)
		}
	}
	page, err := workspace.Page(t.Context(), testScope, operatorRootID, knowl.ReadLimits{})
	if err != nil || page.Title != operatorRootID || page.Markdown != "" {
		t.Fatalf("empty root page = %#v, %v", page, err)
	}
}

func TestOperatorWikiPagesWindowSnapshotAndSelectedTitles(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	writeCanonicalFixture(t, workspace, "wiki/AI/index.md", nil)
	writeCanonicalFixture(t, workspace, "wiki/entities/a.md", validWorkspacePage(operatorTestPageID, "A", testWorkspaceSourceRef, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/entities/z.md", []byte("invalid concept, outside selected title window"))
	first, err := workspace.WikiPageChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].PageID != operatorWikiAIIndex || first.NextKey != "AI/index.md" {
		t.Fatalf("first window = %#v, %v", first, err)
	}
	second, err := workspace.WikiPageChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 1, Continuation: app.OperatorContinuation{Key: first.NextKey, SnapshotVersion: first.SnapshotVersion}})
	if err != nil || len(second.Items) != 1 || second.Items[0].PageID != operatorTestPageID {
		t.Fatalf("second window = %#v, %v", second, err)
	}
	if _, err := workspace.WikiPageChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 1, Continuation: app.OperatorContinuation{Key: second.NextKey, SnapshotVersion: second.SnapshotVersion}}); !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
		t.Fatalf("selected malformed concept = %v", err)
	}
	if _, err := workspace.WikiPageChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 1, Continuation: app.OperatorContinuation{Key: "missing", SnapshotVersion: first.SnapshotVersion}}); !errors.Is(err, app.ErrOperatorCursorInvalid) {
		t.Fatalf("nonexistent cursor key = %v", err)
	}
	writeCanonicalFixture(t, workspace, "wiki/AI/index.md", []byte("Changed optional body"))
	if _, err := workspace.WikiPageChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 1, Continuation: app.OperatorContinuation{Key: first.NextKey, SnapshotVersion: first.SnapshotVersion}}); !errors.Is(err, app.ErrOperatorSnapshotChanged) {
		t.Fatalf("changed index snapshot = %v", err)
	}
	other, err := workspace.WikiPageChildren(t.Context(), "other-scope", "", app.OperatorReadOptions{Limit: 1})
	if err != nil || other.SnapshotVersion == first.SnapshotVersion {
		t.Fatalf("scope snapshot = %#v, %v", other, err)
	}
	// Adding an index changes transparent traversal into one real parent page.
	writeCanonicalFixture(t, workspace, "wiki/entities/index.md", nil)
	current, err := workspace.WikiPageChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 10})
	if err != nil || len(current.Items) != 2 || current.Items[1].PageID != "entities/index" {
		t.Fatalf("new parent stops traversal = %#v, %v", current, err)
	}
}

func TestOperatorWikiPagesCanonicalPathPagination(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	want := []knowl.PageID{"entities/a-b", operatorTestPageID, "entities/a/index", "entities/z"}
	for _, id := range []string{"entities/a-b", operatorTestPageID, "entities/z"} {
		writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, testWorkspaceSourceRef, "Body"))
	}
	writeCanonicalFixture(t, workspace, "wiki/entities/a/index.md", nil)
	var got []knowl.PageID
	options := app.OperatorReadOptions{Limit: 1}
	for range len(want) + 1 {
		window, err := workspace.WikiPageChildren(t.Context(), testScope, "", options)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range window.Items {
			got = append(got, item.PageID)
		}
		if window.NextKey == "" {
			break
		}
		options.Continuation = app.OperatorContinuation{Key: window.NextKey, SnapshotVersion: window.SnapshotVersion}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical path windows = %#v; want %#v", got, want)
	}
}

func TestOperatorWikiPagesNoFollowAndBounds(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	writeCanonicalFixture(t, workspace, "wiki/entities/a.md", validWorkspacePage(operatorTestPageID, "A", testWorkspaceSourceRef, "Body"))
	for _, directory := range []string{"../raw", "entities/%2e%2e", "entities%2fa", "."} {
		if _, err := workspace.WikiPageChildren(t.Context(), testScope, directory, app.OperatorReadOptions{Limit: 1}); !errors.Is(err, app.ErrOperatorInvalidRequest) {
			t.Errorf("invalid directory %q = %v", directory, err)
		}
	}
	if _, err := workspace.WikiPageChildren(t.Context(), testScope, "missing", app.OperatorReadOptions{Limit: 1}); !errors.Is(err, app.ErrOperatorDirectoryNotFound) {
		t.Fatalf("missing directory = %v", err)
	}
	if _, err := workspace.WikiPageChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 10, ReadLimits: knowl.ReadLimits{Bytes: 1}}); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("selected title byte limit = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := workspace.WikiPageChildren(ctx, testScope, "", app.OperatorReadOptions{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %v", err)
	}
	if err := os.Symlink(filepath.Join(workspace.root, "raw"), filepath.Join(workspace.root, "wiki", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(workspace.root, "wiki", "index.md"), filepath.Join(workspace.root, "wiki", "entities", "index.md")); err != nil {
		t.Fatal(err)
	}
	branch, err := workspace.WikiPageChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 10})
	if err != nil || len(branch.Items) != 1 || branch.Items[0].PageID != operatorTestPageID {
		t.Fatalf("linked entries omitted = %#v, %v", branch, err)
	}
	if _, err := workspace.WikiPageChildren(t.Context(), testScope, "linked", app.OperatorReadOptions{Limit: 10}); !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
		t.Fatalf("linked directory = %v", err)
	}
}

func TestOperatorWikiPagesSharedTransparentDepthAndEntryBudget(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	deep := strings.TrimSuffix(strings.Repeat("nested/", app.DefaultCatalogLimits().MaxDepth+1), "/")
	writeCanonicalFixture(t, workspace, "wiki/"+deep+"/a.md", validWorkspacePage(deep+"/a", "Deep", testWorkspaceSourceRef, "Body"))
	if _, err := workspace.WikiPageChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 1}); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("transparent depth limit = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(workspace.root, "wiki", "nested")); err != nil {
		t.Fatal(err)
	}
	// Reuse the actual aggregate ceiling, split over two transparent branches.
	for i := 0; i <= app.DefaultCatalogLimits().MaxEdges; i++ {
		writeCanonicalFixture(t, workspace, fmt.Sprintf("wiki/technical-%d/asset-%05d.bin", i%2, i), nil)
	}
	if _, err := workspace.WikiPageChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 1}); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("aggregate entry limit = %v", err)
	}
}
