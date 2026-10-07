package app

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type directoryFixture struct{ version string }

const operatorDirectoryPageKind = "page"

func (f *directoryFixture) WikiDirectoryChildren(_ context.Context, _ knowl.ScopeRef, directory string, options OperatorReadOptions) (OperatorReadPage[knowl.OperatorWikiEntry], error) {
	if directory != "entities" {
		return OperatorReadPage[knowl.OperatorWikiEntry]{}, ErrOperatorDirectoryNotFound
	}
	if options.Continuation.Key == "" {
		return OperatorReadPage[knowl.OperatorWikiEntry]{Items: []knowl.OperatorWikiEntry{{Path: "entities/a.md", Name: "a.md", Kind: operatorDirectoryPageKind, PageID: "entities/a"}}, NextKey: "entities/a.md", SnapshotVersion: f.version}, nil
	}
	return OperatorReadPage[knowl.OperatorWikiEntry]{Items: []knowl.OperatorWikiEntry{{Path: "entities/b.md", Name: "b.md", Kind: operatorDirectoryPageKind, PageID: "entities/b"}}, SnapshotVersion: f.version}, nil
}

func TestOperatorDirectoryCursorScopeAndSnapshot(t *testing.T) {
	f := &directoryFixture{version: operatorFingerprint("v1")}
	service := operatorService(t, OperatorReaders{Directories: f})
	first, err := service.WikiDirectoryChildren(t.Context(), "entities", OperatorListOptions{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first = %#v, %v", first, err)
	}
	second, err := service.WikiDirectoryChildren(t.Context(), "entities", OperatorListOptions{Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Items[0].Path != "entities/b.md" {
		t.Fatalf("second = %#v, %v", second, err)
	}
	if _, err := service.WikiDirectoryChildren(t.Context(), "other", OperatorListOptions{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrOperatorCursorInvalid) {
		t.Fatalf("cross-branch cursor = %v", err)
	}
	f.version = operatorFingerprint("v2")
	if _, err := service.WikiDirectoryChildren(t.Context(), "entities", OperatorListOptions{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrOperatorSnapshotChanged) {
		t.Fatalf("changed branch = %v", err)
	}
	for _, directory := range []string{"../raw", "entities/../entities", "entities%2Fa", "entities%252Fa", "entities/%2e%2e", "entities/%252e%252e", ".", "entities\\a"} {
		if _, err := service.WikiDirectoryChildren(t.Context(), directory, OperatorListOptions{}); !errors.Is(err, ErrOperatorInvalidRequest) {
			t.Errorf("directory %q: %v", directory, err)
		}
	}
	tooDeep := strings.Repeat("folder/", DefaultCatalogLimits().MaxDepth) + "child"
	if _, err := service.WikiDirectoryChildren(t.Context(), tooDeep, OperatorListOptions{}); !errors.Is(err, ErrOperatorReadLimitExceeded) {
		t.Fatalf("deep directory = %v", err)
	}
	for _, directory := range []string{"release%2026", "100%-ready", "version%2e1"} {
		if _, err := service.WikiDirectoryChildren(t.Context(), directory, OperatorListOptions{}); !errors.Is(err, ErrOperatorDirectoryNotFound) {
			t.Errorf("accepted directory %q = %v", directory, err)
		}
	}
	if runtime.GOOS != operatorWindowsOS {
		if _, err := service.WikiDirectoryChildren(t.Context(), "ops:team", OperatorListOptions{}); !errors.Is(err, ErrOperatorDirectoryNotFound) {
			t.Errorf("accepted colon directory = %v", err)
		}
	}
}
