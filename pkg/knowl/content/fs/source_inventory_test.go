package fs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestSourceDigestsInventoriesMarkdownAndAssets(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	ctx := context.Background()
	page := sourcePage("sources/engineering/docs/page", "docs/page.md", "revision-1")
	asset := []byte{0x00, 0x01, 0xfe, 0xff, 'b', 'i', 'n'}
	writeCanonicalFixture(t, workspace, testSourcePagePath, page)
	writeCanonicalFixture(t, workspace, "wiki/sources/engineering/assets/nested/logo.bin", asset)

	inventory, err := workspace.SourceDigests(ctx, testScope, testSourceID, 2048)
	if err != nil {
		t.Fatalf("SourceDigests() error = %v", err)
	}
	if len(inventory) != 2 {
		t.Fatalf("inventory = %#v, want two entries", inventory)
	}
	if inventory[0].Path >= inventory[1].Path {
		t.Fatalf("inventory not sorted: %#v", inventory)
	}
	wantByPath := map[string]string{
		"wiki/sources/engineering/assets/nested/logo.bin": fmt.Sprintf("%x", sha256.Sum256(asset)),
		testSourcePagePath: fmt.Sprintf("%x", sha256.Sum256(page)),
	}
	for _, entry := range inventory {
		if wantByPath[entry.Path] != entry.Digest {
			t.Fatalf("entry %q digest = %q, want %q", entry.Path, entry.Digest, wantByPath[entry.Path])
		}
	}

	absent, err := workspace.SourceDigests(ctx, testScope, "missing-source", 16)
	if err != nil || len(absent) != 0 {
		t.Fatalf("absent namespace = %#v, %v; want empty inventory", absent, err)
	}
	writeCanonicalFixture(t, workspace, "wiki/other/page.md", []byte("# Maintainer\n"))
	crossSource, err := workspace.SourceDigests(ctx, testScope, testSourceID, 16)
	if err != nil || len(crossSource) != 2 {
		t.Fatalf("cross-tree leak = %#v, %v", crossSource, err)
	}
	for _, entry := range crossSource {
		if !strings.HasPrefix(entry.Path, "wiki/sources/engineering/") {
			t.Fatalf("foreign path %q leaked into inventory", entry.Path)
		}
	}
}

func TestSourceDigestsIsolatesEqualPathsAcrossSources(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	ctx := context.Background()
	left := []byte("left content")
	right := []byte("right content")
	writeCanonicalFixture(t, workspace, "wiki/sources/engineering/shared/page.md", left)
	writeCanonicalFixture(t, workspace, "wiki/sources/operations/shared/page.md", right)

	leftInventory, err := workspace.SourceDigests(ctx, testScope, testSourceID, 16)
	if err != nil || len(leftInventory) != 1 || leftInventory[0].Path != "wiki/sources/engineering/shared/page.md" || leftInventory[0].Digest != fmt.Sprintf("%x", sha256.Sum256(left)) {
		t.Fatalf("left inventory = %#v, %v", leftInventory, err)
	}
	rightInventory, err := workspace.SourceDigests(ctx, testScope, "operations", 16)
	if err != nil || len(rightInventory) != 1 || rightInventory[0].Path != "wiki/sources/operations/shared/page.md" || rightInventory[0].Digest != fmt.Sprintf("%x", sha256.Sum256(right)) {
		t.Fatalf("right inventory = %#v, %v", rightInventory, err)
	}
}

func TestSourceDigestsRejectsSymlinksAndNonRegularEntries(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	ctx := context.Background()
	root := workspace.Root()
	writeCanonicalFixture(t, workspace, "wiki/sources/engineering/keep.md", []byte("kept"))
	if err := os.Symlink(filepath.Join(root, "wiki/sources/engineering/keep.md"), filepath.Join(root, "wiki/sources/engineering/link.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	inventory, err := workspace.SourceDigests(ctx, testScope, testSourceID, 16)
	if err == nil || !errors.Is(err, ErrPathRejected) {
		t.Fatalf("symlinked file error = %v, want path rejected", err)
	}
	if inventory != nil {
		t.Fatalf("partial inventory returned on error: %#v", inventory)
	}
	if strings.Contains(fmt.Sprint(err), root) {
		t.Fatalf("error leaks absolute root: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "wiki/sources/engineering/link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "outside"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCanonicalFixture(t, workspace, "wiki/sources/engineering/real.md", []byte("real"))
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, "wiki/sources/engineering/docs")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := workspace.SourceDigests(ctx, testScope, testSourceID, 16); !errors.Is(err, ErrPathRejected) {
		t.Fatalf("symlinked directory error = %v, want path rejected", err)
	}
}

func TestSourceDigestsRejectsOverLimitNamespace(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	ctx := context.Background()
	for index := 0; index < 3; index++ {
		writeCanonicalFixture(t, workspace, fmt.Sprintf("wiki/sources/engineering/docs/file-%d.md", index), []byte(strings.Repeat("x", index+1)))
	}
	inventory, err := workspace.SourceDigests(ctx, testScope, testSourceID, 2)
	if !errors.Is(err, app.ErrSourceMutationLimit) {
		t.Fatalf("over-limit error = %v, want mutation limit", err)
	}
	if inventory != nil {
		t.Fatalf("partial inventory returned on limit: %#v", inventory)
	}
	exact, err := workspace.SourceDigests(ctx, testScope, testSourceID, 3)
	if err != nil || len(exact) != 3 {
		t.Fatalf("exact-bound inventory = %#v, %v", exact, err)
	}
}

func TestSourceDigestsValidatesInputAndStaysDeterministic(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	ctx := context.Background()
	writeCanonicalFixture(t, workspace, "wiki/sources/engineering/docs/page.md", []byte("page"))
	first, err := workspace.SourceDigests(ctx, testScope, testSourceID, 8)
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspace.SourceDigests(ctx, testScope, testSourceID, 8)
	if err != nil || len(first) != 1 || !equalSourceEntries(first, second) {
		t.Fatalf("repeat inventory = %#v vs %#v, %v", second, first, err)
	}
	for _, test := range []struct {
		name     string
		scope    knowl.ScopeRef
		sourceID knowl.SourceID
		limit    int
	}{
		{name: "blank scope", scope: " ", sourceID: testSourceID, limit: 8},
		{name: "invalid source id", scope: testScope, sourceID: "../escape", limit: 8},
		{name: "uppercase source id", scope: testScope, sourceID: "Engineering", limit: 8},
		{name: "zero limit", scope: testScope, sourceID: testSourceID, limit: 0},
		{name: "negative limit", scope: testScope, sourceID: testSourceID, limit: -1},
		{name: "over maximum limit", scope: testScope, sourceID: testSourceID, limit: 2049},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := workspace.SourceDigests(ctx, test.scope, test.sourceID, test.limit); !errors.Is(err, app.ErrSourceInvalid) {
				t.Fatalf("input validation error = %v, want ErrSourceInvalid", err)
			}
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := workspace.SourceDigests(canceled, testScope, testSourceID, 8); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v, want context canceled", err)
	}
}

func equalSourceEntries(left, right []app.SourceDigestEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
