package contextpolicy

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const testFallbackAdapter = "adapter"

func TestSourceQuerySemanticPriorityAndIdentityFallback(t *testing.T) {
	source := knowl.SourceSummary{Source: knowl.SourceRef{ID: "identity", Adapter: testFallbackAdapter}, Title: "Title", Tags: []string{"Tag"}, Headings: []string{"Heading"}, Body: "Body title"}
	if got := SourceQuery(source).Terms; !slices.Equal(got, []string{"title", "tag", "heading", "body"}) {
		t.Fatalf("semantic terms=%v", got)
	}
	source.Title = "why"
	source.Tags = nil
	source.Headings = nil
	source.Body = "how"
	if got := SourceQuery(source).Terms; !slices.Equal(got, []string{"identity", testFallbackAdapter}) {
		t.Fatalf("fallback terms=%v", got)
	}
	source.Body = "nohitsemantic"
	if got := SourceQuery(source).Terms; !slices.Equal(got, []string{"nohitsemantic"}) {
		t.Fatalf("nonempty query polluted by identity=%v", got)
	}
}

func TestSourceQueryBounds(t *testing.T) {
	var terms []string
	for i := range 40 {
		terms = append(terms, fmt.Sprintf("term%d", i))
	}
	query := SourceQuery(knowl.SourceSummary{Body: strings.Join(terms, " ")})
	if len(query.Terms) != 32 {
		t.Fatalf("term cap=%d", len(query.Terms))
	}
	long := strings.Repeat("界", 256)
	query = SourceQuery(knowl.SourceSummary{Title: long, Body: "extra"})
	if !slices.Equal(query.Terms, []string{long}) {
		t.Fatalf("rune cap=%v", query.Terms)
	}
	source := knowl.SourceSummary{Body: strings.Repeat(" ", 16384) + "beyondbudget", Source: knowl.SourceRef{ID: "fallback"}}
	// Leading whitespace is trimmed, so it does not consume a semantic budget.
	if got := SourceQuery(source).Terms; !slices.Equal(got, []string{"beyondbudget"}) {
		t.Fatalf("whitespace semantic=%v", got)
	}
	source = knowl.SourceSummary{Source: knowl.SourceRef{ID: strings.Repeat("界", 257), Adapter: testFallbackAdapter}}
	query = SourceQuery(source)
	if !slices.Equal(query.Terms, []string{long}) {
		t.Fatalf("clipped identity=%v", query.Terms)
	}
	total := 0
	for _, term := range query.Terms {
		total += utf8.RuneCountInString(term)
	}
	if len(query.Terms) == 0 || total > 256 || !utf8.ValidString(query.Terms[0]) {
		t.Fatalf("identity cap=%v", query.Terms)
	}
}
