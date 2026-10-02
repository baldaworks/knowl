package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const catalogTestRoot = "# Root\n"
const catalogTestTitle = "Topic"

func TestCatalogsDoNotShareFactualPageLimit(t *testing.T) {
	snapshots := []knowl.PageSnapshot{{Path: rootCatalogPath, Content: catalogTestRoot}}
	for i := range 31 {
		snapshots = append(snapshots, knowl.PageSnapshot{Path: fmt.Sprintf("wiki/topic-%02d/index.md", i), Content: "# Topic\n"})
	}
	for i := range snapshots {
		digest := sha256.Sum256([]byte(snapshots[i].Content))
		snapshots[i].Digest = hex.EncodeToString(digest[:])
	}
	if _, err := catalogGraph(snapshots, DefaultCatalogLimits()); err != nil {
		t.Fatalf("32 catalogs with factual Pages=20 must fit: %v", err)
	}
}

func catalogSnapshot(path, content string) knowl.PageSnapshot {
	digest := sha256.Sum256([]byte(content))
	return knowl.PageSnapshot{Path: path, Title: catalogTestTitle, Content: content, Digest: hex.EncodeToString(digest[:])}
}

func TestCatalogGraphBoundaries(t *testing.T) {
	snapshots := []knowl.PageSnapshot{
		catalogSnapshot("wiki/topic/index.md", "# Topic\n- [Page](page.md)\n"),
		catalogSnapshot(rootCatalogPath, "# Root\n- [Topic](topic/index.md)\n- [Again](topic/index.md)\n- [External](https://example.test/)\n"),
	}
	limits := DefaultCatalogLimits()
	limits.MaxCatalogs = 2
	limits.MaxEdges = 2
	limits.MaxDepth = 2
	limits.MaxPathBytes = len("wiki/topic/index.md")
	limits.MaxCatalogBytes = max(len(snapshots[0].Content), len(snapshots[1].Content))
	limits.MaxSnapshotBytes = len(snapshots[0].Content) + len(snapshots[1].Content)
	graph, err := catalogGraph(snapshots, limits)
	if err != nil {
		t.Fatal(err)
	}
	want := []knowl.HierarchyCatalog{
		{Path: rootCatalogPath, Digest: snapshots[1].Digest, Title: catalogTestTitle, Children: []string{"wiki/topic/index.md"}},
		{Path: "wiki/topic/index.md", Digest: snapshots[0].Digest, Title: catalogTestTitle, Children: []string{"wiki/topic/page.md"}},
	}
	if !reflect.DeepEqual(graph, want) {
		t.Fatalf("graph=%v want=%v", graph, want)
	}
	encoded, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	limits.MaxInputBytes = len(encoded)
	if _, err := catalogGraph(snapshots, limits); err != nil {
		t.Fatalf("exact graph bytes: %v", err)
	}
	cases := []struct {
		name   string
		reduce func(*knowl.CatalogLimits)
	}{
		{"catalogs", func(l *knowl.CatalogLimits) { l.MaxCatalogs-- }},
		{"edges", func(l *knowl.CatalogLimits) { l.MaxEdges-- }},
		{"depth", func(l *knowl.CatalogLimits) { l.MaxDepth-- }},
		{"path", func(l *knowl.CatalogLimits) { l.MaxPathBytes-- }},
		{"per-catalog bound", func(l *knowl.CatalogLimits) { l.MaxCatalogBytes-- }},
		{"snapshot bytes", func(l *knowl.CatalogLimits) { l.MaxSnapshotBytes-- }},
		{"graph bytes", func(l *knowl.CatalogLimits) { l.MaxInputBytes-- }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bound := limits
			tc.reduce(&bound)
			if _, err := catalogGraph(snapshots, bound); !errors.Is(err, ErrPlanLimitExceeded) {
				t.Fatalf("overflow=%v", err)
			}
		})
	}
}

func TestCatalogGraphRejectsInvalidNavigation(t *testing.T) {
	for _, snapshots := range [][]knowl.PageSnapshot{
		{catalogSnapshot("wiki/topic/index.md", "# Topic\n")},
		{catalogSnapshot(rootCatalogPath, "# Root\n- [Missing](missing/index.md)\n")},
		{catalogSnapshot(rootCatalogPath, catalogTestRoot), catalogSnapshot(rootCatalogPath, "# Duplicate\n")},
		{catalogSnapshot(rootCatalogPath, "# Root\n- [Escape](../escape.md)\n")},
		{catalogSnapshot(rootCatalogPath, "# Root\n- [Self](index.md)\n")},
		{catalogSnapshot(rootCatalogPath, "# Root\n- [Topic](topic/index.md)\n"), catalogSnapshot("wiki/topic/index.md", "# Topic\n- [Root](../index.md)\n")},
	} {
		if _, err := catalogGraph(snapshots, DefaultCatalogLimits()); !errors.Is(err, ErrPlanInvalid) {
			t.Fatalf("invalid graph=%v", err)
		}
	}
	if _, err := NormalizeCatalogLimits(knowl.CatalogLimits{MaxCatalogs: 32}); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("partial limits=%v", err)
	}
}
