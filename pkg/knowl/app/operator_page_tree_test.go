package app

import (
	"context"
	"errors"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	wikiPageFixtureAPath = "entities/a.md"
	wikiPageFixtureBID   = "entities/b"
)

type wikiPageFixture struct {
	directoryFixture
	scope   knowl.ScopeRef
	options OperatorReadOptions
}

func (f *wikiPageFixture) WikiPageChildren(_ context.Context, scope knowl.ScopeRef, directory string, options OperatorReadOptions) (OperatorReadPage[knowl.OperatorWikiPage], error) {
	f.scope, f.options = scope, options
	if directory != "entities" {
		return OperatorReadPage[knowl.OperatorWikiPage]{}, ErrOperatorDirectoryNotFound
	}
	result := OperatorReadPage[knowl.OperatorWikiPage]{SnapshotVersion: f.version}
	if options.Continuation.Key == "" {
		result.Items = []knowl.OperatorWikiPage{{PageID: "entities/a", Path: wikiPageFixtureAPath, Title: "A"}}
		result.NextKey = wikiPageFixtureAPath
	} else {
		result.Items = []knowl.OperatorWikiPage{{PageID: wikiPageFixtureBID, Path: wikiPageFixtureBID + ".md", Title: "B"}}
	}
	return result, nil
}

func TestOperatorWikiPagesOptionalCapabilityAndBoundCursor(t *testing.T) {
	without := operatorService(t, OperatorReaders{Directories: &directoryFixture{version: operatorFingerprint("v1")}})
	if _, err := without.WikiPageChildren(t.Context(), "", OperatorListOptions{}); !errors.Is(err, ErrOperatorCapabilityUnavailable) {
		t.Fatalf("optional capability = %v", err)
	}
	f := &wikiPageFixture{directoryFixture: directoryFixture{version: operatorFingerprint("v1")}}
	service := operatorService(t, OperatorReaders{Directories: f})
	first, err := service.WikiPageChildren(t.Context(), "entities", OperatorListOptions{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" || f.scope != service.scope || f.options.ReadLimits != service.limits {
		t.Fatalf("bound read = %#v, %v; fixture %#v", first, err, f)
	}
	second, err := service.WikiPageChildren(t.Context(), "entities", OperatorListOptions{Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Items[0].PageID != wikiPageFixtureBID {
		t.Fatalf("continued read = %#v, %v", second, err)
	}
	for _, directory := range []string{"other", ""} {
		if _, err := service.WikiPageChildren(t.Context(), directory, OperatorListOptions{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrOperatorCursorInvalid) {
			t.Errorf("cross-directory cursor %q = %v", directory, err)
		}
	}
	if _, err := service.WikiDirectoryChildren(t.Context(), "entities", OperatorListOptions{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrOperatorCursorInvalid) {
		t.Fatalf("cross-endpoint cursor = %v", err)
	}
	f.version = operatorFingerprint("v2")
	if _, err := service.WikiPageChildren(t.Context(), "entities", OperatorListOptions{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrOperatorSnapshotChanged) {
		t.Fatalf("changed snapshot = %v", err)
	}
	if _, err := service.WikiPageChildren(t.Context(), "../raw", OperatorListOptions{}); !errors.Is(err, ErrOperatorInvalidRequest) {
		t.Fatalf("path scope = %v", err)
	}
}
