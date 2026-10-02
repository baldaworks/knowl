package knowledgetest

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// RunContextBaseline measures curated quality gaps without requiring them to persist.
// Unexpected errors, invalid evidence and nondeterministic replay still fail.
func RunContextBaseline(t *testing.T, index Projection) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const scope = knowl.ScopeRef("context-baseline-v1")
	snapshot := BaselineSnapshot(scope)
	limits := knowl.ReadLimits{Pages: 5, Characters: 128}
	var previous [][]knowl.PageReference
	for pass := range 2 {
		if err := index.Rebuild(ctx, snapshot); err != nil {
			t.Fatalf("baseline rebuild: %v", err)
		}
		var observed [][]knowl.PageReference
		for _, fixture := range BaselineQueries() {
			references, err := index.Search(ctx, scope, fixture.Query, limits, nil)
			if err != nil {
				t.Fatalf("baseline %s: %v", fixture.ID, err)
			}
			if len(references) > limits.Pages {
				t.Fatalf("baseline %s exceeded page limit", fixture.ID)
			}
			ids := make([]knowl.PageID, 0, len(references))
			for _, ref := range references {
				pageIndex := slices.IndexFunc(snapshot.Pages, func(p knowl.PageSnapshot) bool { return p.ID == ref.ID })
				if pageIndex < 0 || slices.Contains(ids, ref.ID) || !ref.Untrusted || !utf8.ValidString(ref.Snippet) || utf8.RuneCountInString(ref.Snippet) > limits.Characters {
					t.Fatalf("baseline %s returned invalid evidence for %s", fixture.ID, ref.ID)
				}
				page := snapshot.Pages[pageIndex]
				if ref.Path != page.Path || ref.Title != page.Title || ref.Snippet == "" {
					t.Fatalf("baseline %s returned incomplete or incorrect evidence for %s", fixture.ID, ref.ID)
				}
				if !slices.Equal(ref.SourceRefs, page.SourceRefs) {
					t.Fatalf("baseline %s lost provenance for %s", fixture.ID, ref.ID)
				}
				ids = append(ids, ref.ID)
			}
			observed = append(observed, references)
			result := ObserveRecall(fixture.ID, fixture.Expected, ids, limits.Pages)
			if fixture.Control && result.Hits != result.Total {
				t.Fatalf("baseline control %s lost expected evidence: %+v", fixture.ID, result)
			}
			if pass == 0 {
				encoded, marshalErr := json.Marshal(result)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				t.Log(string(encoded))
			}
		}
		if pass == 1 && !reflect.DeepEqual(previous, observed) {
			t.Fatal("baseline evidence changed after fixed-input rebuild")
		}
		previous = observed
	}
}
