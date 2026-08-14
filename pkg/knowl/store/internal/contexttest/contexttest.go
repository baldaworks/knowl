// Package contexttest defines the shared maintenance context contract for
// rebuildable search adapters.
package contexttest

import (
	"context"
	"slices"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	Scope        knowl.ScopeRef = "context-contract"
	ForeignScope knowl.ScopeRef = "context-contract-foreign"
	relevantID   knowl.PageID   = "decisions/badger"
	outgoingID   knowl.PageID   = "context/a-outgoing"
	incomingID   knowl.PageID   = "context/b-incoming"
	wikiRelation                = "wiki"
)

var baseTime = time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)

// Index is the narrow adapter surface exercised by the context contract.
type Index interface {
	SelectContext(ctx context.Context, scope knowl.ScopeRef, source knowl.SourceSummary, limits knowl.ReadLimits) ([]knowl.PageID, error)
	Rebuild(ctx context.Context, snapshot knowl.WorkspaceSnapshot) error
}

// Snapshot returns a fresh fixed maintenance context fixture.
func Snapshot() knowl.WorkspaceSnapshot {
	pages := []knowl.PageSnapshot{
		page(relevantID, "wiki/decisions/badger.md", "Badger Session Decision 42 Fixture", "Badger session memory decision 42 fixture is durable.", -48),
		page("notes/badger", "wiki/notes/badger.md", "Badger Note", "Badger storage details.", -12),
		page("notes/session", "wiki/notes/session.md", "Session Note", "Session lifecycle details.", -11),
		page(outgoingID, "wiki/context/a-outgoing.md", "Outgoing Context", "Operational constraints linked from the selected record.", -30),
		page(incomingID, "wiki/context/b-incoming.md", "Incoming Context", "Investigation that references the selected record.", -29),
		page("context/second-hop", "wiki/context/second-hop.md", "Second Hop", "Must not be expanded recursively.", -28),
		page("recent/new", "wiki/recent/new.md", "Newest Unrelated", "Current unrelated release notes.", -1),
		page("recent/old", "wiki/recent/old.md", "Older Unrelated", "Earlier unrelated release notes.", -2),
		page("recent/third", "wiki/recent/third.md", "Third Unrelated", "Additional unrelated notes.", -3),
	}
	pageDigests := make(map[string]string, len(pages))
	for _, fixturePage := range pages {
		pageDigests[fixturePage.Path] = fixturePage.Digest
	}
	return knowl.WorkspaceSnapshot{
		Scope: Scope, SchemaDigest: "schema-context-contract-v1", PageDigests: pageDigests,
		Pages: pages,
		Links: []knowl.LinkReference{
			{From: relevantID, To: outgoingID, Relation: wikiRelation},
			{From: incomingID, To: relevantID, Relation: wikiRelation},
			{From: outgoingID, To: "context/second-hop", Relation: wikiRelation},
			{From: relevantID, To: "missing/page", Relation: wikiRelation},
			{From: relevantID, To: relevantID, Relation: wikiRelation},
		},
		CapturedAt: baseTime,
	}
}

// Run exercises source relevance, one-hop expansion, control reservation,
// recency fallback, scope isolation, bounds, and rebuild equivalence.
func Run(t *testing.T, index Index) {
	t.Helper()
	ctx := context.Background()
	if err := index.Rebuild(ctx, Snapshot()); err != nil {
		t.Fatalf("Rebuild() context fixture: %v", err)
	}
	foreign := Snapshot()
	foreign.Scope = ForeignScope
	foreign.Pages = []knowl.PageSnapshot{page("foreign", "wiki/foreign.md", "Badger Session Decision 42 Fixture", "foreign-only evidence", 0)}
	foreign.PageDigests = map[string]string{foreign.Pages[0].Path: foreign.Pages[0].Digest}
	foreign.Links = nil
	if err := index.Rebuild(ctx, foreign); err != nil {
		t.Fatalf("Rebuild() foreign context fixture: %v", err)
	}

	summary := knowl.SourceSummary{
		Source: knowl.SourceRef{Adapter: "fixture", ID: "decision-42"},
		Title:  "Badger session",
	}
	t.Run("one page preserves relevance", func(t *testing.T) {
		got := selectContext(t, index, Scope, summary, 1)
		assertIDs(t, got, relevantID)
	})
	t.Run("relevant links control and recency", func(t *testing.T) {
		got := selectContext(t, index, Scope, summary, 8)
		if len(got) != 8 || got[0] != relevantID {
			t.Fatalf("context = %q, want eight IDs beginning with %q", got, relevantID)
		}
		if !containsAll(got, outgoingID, incomingID, "index") {
			t.Fatalf("context = %q, want incoming/outgoing neighbors and index", got)
		}
		if slices.Contains(got, knowl.PageID("context/second-hop")) || slices.Contains(got, knowl.PageID("missing/page")) {
			t.Fatalf("context contains broken or recursive neighbor: %q", got)
		}
		if slices.Contains(got, knowl.PageID("foreign")) {
			t.Fatalf("context contains another scope: %q", got)
		}
		assertUnique(t, got)
	})
	t.Run("no hit uses control before recent", func(t *testing.T) {
		noHit := knowl.SourceSummary{Source: knowl.SourceRef{Adapter: "nomatchadapter", ID: "nomatchid"}, Title: "Zephyr quasar"}
		got := selectContext(t, index, Scope, noHit, 4)
		assertIDs(t, got, "index", "recent/new", "recent/old", "recent/third")
	})
	t.Run("deterministic rebuild", func(t *testing.T) {
		before := selectContext(t, index, Scope, summary, 8)
		if err := index.Rebuild(ctx, Snapshot()); err != nil {
			t.Fatalf("Rebuild() replay: %v", err)
		}
		after := selectContext(t, index, Scope, summary, 8)
		if !slices.Equal(before, after) {
			t.Fatalf("context changed after rebuild:\nbefore: %q\nafter:  %q", before, after)
		}
	})
}

func page(id knowl.PageID, path, title, content string, ageHours int) knowl.PageSnapshot {
	return knowl.PageSnapshot{
		ID: id, Path: path, Digest: "digest-" + string(id), Title: title, Content: content,
		SourceRefs: []string{"source:" + string(id)}, Untrusted: true,
		UpdatedAt: baseTime.Add(time.Duration(ageHours) * time.Hour),
	}
}

func selectContext(t *testing.T, index Index, scope knowl.ScopeRef, summary knowl.SourceSummary, pages int) []knowl.PageID {
	t.Helper()
	got, err := index.SelectContext(context.Background(), scope, summary, knowl.ReadLimits{Pages: pages})
	if err != nil {
		t.Fatalf("SelectContext(%#v): %v", summary, err)
	}
	if len(got) > pages {
		t.Fatalf("SelectContext() returned %d IDs, limit %d", len(got), pages)
	}
	return got
}

func assertIDs(t *testing.T, got []knowl.PageID, want ...knowl.PageID) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("context IDs = %q, want %q", got, want)
	}
}

func assertUnique(t *testing.T, ids []knowl.PageID) {
	t.Helper()
	seen := make(map[knowl.PageID]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate context ID %q in %q", id, ids)
		}
		seen[id] = struct{}{}
	}
}

func containsAll(ids []knowl.PageID, want ...knowl.PageID) bool {
	for _, id := range want {
		if !slices.Contains(ids, id) {
			return false
		}
	}
	return true
}
