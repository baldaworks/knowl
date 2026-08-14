package contextpolicy

import (
	"slices"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const candidateID knowl.PageID = "candidate"

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
		{name: "one relevant", limit: 1, candidates: []knowl.PageID{candidateID}, recent: []knowl.PageID{"recent"}, want: []knowl.PageID{candidateID}},
		{name: "one control fallback", limit: 1, recent: []knowl.PageID{"recent"}, want: []knowl.PageID{ControlPageID}},
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
