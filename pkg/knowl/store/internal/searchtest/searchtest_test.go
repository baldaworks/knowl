package searchtest

import "testing"

func TestFixtureContract(t *testing.T) {
	t.Parallel()
	if len(MeasuredQueries) < 12 {
		t.Fatalf("measured query count = %d, want at least 12", len(MeasuredQueries))
	}
	fixture := Snapshot()
	if fixture.Scope != Scope || len(fixture.Pages) < 12 {
		t.Fatalf("incomplete search fixture: scope=%q pages=%d", fixture.Scope, len(fixture.Pages))
	}
	seenIDs := make(map[string]struct{}, len(fixture.Pages))
	seenPaths := make(map[string]struct{}, len(fixture.Pages))
	for _, page := range fixture.Pages {
		if _, exists := seenIDs[string(page.ID)]; exists {
			t.Fatalf("duplicate page ID %q", page.ID)
		}
		if _, exists := seenPaths[page.Path]; exists {
			t.Fatalf("duplicate page path %q", page.Path)
		}
		seenIDs[string(page.ID)] = struct{}{}
		seenPaths[page.Path] = struct{}{}
	}
	for _, measured := range MeasuredQueries {
		if _, exists := seenIDs[string(measured.Expected)]; !exists {
			t.Fatalf("query %q expects missing page %q", measured.Query, measured.Expected)
		}
	}
}
