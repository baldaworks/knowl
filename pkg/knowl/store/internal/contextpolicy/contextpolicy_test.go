package contextpolicy

import (
	"reflect"
	"slices"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	candidateID knowl.PageID = "candidate"
	recentID    knowl.PageID = "recent"
	neighborID  knowl.PageID = "neighbor"
)

func TestCandidateLimit(t *testing.T) {
	t.Parallel()
	tests := []struct{ limit, want int }{{0, 0}, {1, 1}, {2, 1}, {3, 2}, {5, 3}, {20, 13}}
	for _, test := range tests {
		if got := CandidateLimit(test.limit); got != test.want {
			t.Errorf("CandidateLimit(%d) = %d, want %d", test.limit, got, test.want)
		}
	}
}

func TestMerge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                          string
		limit                         int
		candidates, neighbors, recent []knowl.PageID
		want                          []knowl.PageID
	}{
		{name: "zero", limit: 0, candidates: []knowl.PageID{candidateID}},
		{name: "one relevant", limit: 1, candidates: []knowl.PageID{candidateID}, recent: []knowl.PageID{recentID}, want: []knowl.PageID{candidateID}},
		{name: "one control fallback", limit: 1, recent: []knowl.PageID{recentID}, want: []knowl.PageID{ControlPageID}},
		{name: "phase order and exact bound", limit: 5, candidates: []knowl.PageID{"c1", "c2", "c3", "c4"}, neighbors: []knowl.PageID{"n1", "n2"}, recent: []knowl.PageID{"r1"}, want: []knowl.PageID{"c1", "c2", "c3", "n1", ControlPageID}},
		{name: "unused candidates precede recent", limit: 6, candidates: []knowl.PageID{"c1", "c2", "c3", "c4", "c5"}, recent: []knowl.PageID{"r1"}, want: []knowl.PageID{"c1", "c2", "c3", "c4", ControlPageID, "c5"}},
		{name: "deduplicates all phases", limit: 5, candidates: []knowl.PageID{"c1", "c1"}, neighbors: []knowl.PageID{"c1", "n1", "n1"}, recent: []knowl.PageID{"n1", "r1", "r1"}, want: []knowl.PageID{"c1", "n1", ControlPageID, "r1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := Merge(test.limit, test.candidates, test.neighbors, test.recent)
			if !slices.Equal(got, test.want) {
				t.Fatalf("Merge() = %q, want %q", got, test.want)
			}
			if len(got) > test.limit {
				t.Fatalf("Merge() length = %d, limit %d", len(got), test.limit)
			}
		})
	}
}

func TestMergeReasonsKeepFirstPhase(t *testing.T) {
	channels := ChannelReasons([]knowl.PageID{"c1", "c3", "c4", "c5"}, []knowl.PageID{"c2", "c3", "c5"})
	wantChannels := map[knowl.PageID]knowl.ContextSelectionReason{"c1": knowl.ContextLexical, "c2": knowl.ContextVector, "c3": knowl.ContextHybrid, "c4": knowl.ContextLexical, "c5": knowl.ContextHybrid}
	if !reflect.DeepEqual(channels, wantChannels) {
		t.Fatalf("channel membership=%v", channels)
	}
	candidates := []knowl.PageID{"c1", "c2", "c3", "c4", "c5"}
	ids, reasons := MergeWithReasons(6, candidates, []knowl.PageID{"c5"}, []knowl.PageID{"c5", recentID}, channels)
	if !slices.Equal(ids, []knowl.PageID{"c1", "c2", "c3", "c4", "c5", ControlPageID}) {
		t.Fatalf("changed original phase order: %v", ids)
	}
	want := map[knowl.PageID]knowl.ContextSelectionReason{"c1": knowl.ContextLexical, "c2": knowl.ContextVector, "c3": knowl.ContextHybrid, "c4": knowl.ContextLexical, "c5": knowl.ContextNeighbor}
	if !reflect.DeepEqual(reasons, want) {
		t.Fatalf("later channel overwrote neighbor or control published: %v", reasons)
	}
	ids, reasons = MergeWithReasons(5, []knowl.PageID{"c1", "c1"}, []knowl.PageID{"c1", neighborID}, []knowl.PageID{neighborID, recentID}, channels)
	if !slices.Equal(ids, []knowl.PageID{"c1", neighborID, ControlPageID, recentID}) || reasons["c1"] != knowl.ContextLexical || reasons[neighborID] != knowl.ContextNeighbor || reasons[recentID] != knowl.ContextRecent {
		t.Fatalf("dedup/order/reason drift: %v %v", ids, reasons)
	}
}
