package contexttest

import "testing"

func TestFixtureContract(t *testing.T) {
	t.Parallel()
	fixture := Snapshot()
	if fixture.Scope != Scope || len(fixture.Pages) < 8 || len(fixture.Links) < 5 {
		t.Fatalf("incomplete context fixture: scope=%q pages=%d links=%d", fixture.Scope, len(fixture.Pages), len(fixture.Links))
	}
	seenIDs := make(map[string]struct{}, len(fixture.Pages))
	for _, page := range fixture.Pages {
		if _, duplicate := seenIDs[string(page.ID)]; duplicate {
			t.Fatalf("duplicate fixture page ID %q", page.ID)
		}
		seenIDs[string(page.ID)] = struct{}{}
	}
}
