package knowledgetest

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// Retriever is the narrow evidence operation evaluated by the golden corpus.
type Retriever func(context.Context, string, knowl.ReadLimits) ([]knowl.PageReference, error)

// Metrics records the measured top-five result.
type Metrics struct {
	Hits   int
	Total  int
	Misses []string
}

// Passed reports whether the fixed v1 threshold is met.
func (metrics Metrics) Passed() bool {
	return metrics.Total == QueryCount && metrics.Hits >= MinimumHits
}

// Evaluate runs all measured queries and validates evidence properties.
func Evaluate(ctx context.Context, retrieve Retriever) (Metrics, error) {
	metrics := Metrics{Total: len(queryFixtures)}
	for _, query := range queryFixtures {
		references, err := retrieve(ctx, query.Query, knowl.ReadLimits{Pages: EvidencePages, Characters: EvidenceCharacters})
		if err != nil {
			return metrics, fmt.Errorf("retrieve %q: %w", query.Query, err)
		}
		var expected *knowl.PageReference
		for index := range references {
			if references[index].ID == query.ExpectedPage {
				expected = &references[index]
				break
			}
		}
		if expected == nil {
			metrics.Misses = append(metrics.Misses, query.Query)
			continue
		}
		if !expected.Untrusted || !utf8.ValidString(expected.Snippet) || utf8.RuneCountInString(expected.Snippet) > EvidenceCharacters {
			return metrics, fmt.Errorf("query %q returned invalid evidence %#v", query.Query, *expected)
		}
		if !containsAnyToken(expected.Snippet, query.MatchTerms) {
			return metrics, fmt.Errorf("query %q snippet %q contains none of %q", query.Query, expected.Snippet, query.MatchTerms)
		}
		if !containsString(expected.SourceRefs, query.ExpectedRef) {
			return metrics, fmt.Errorf("query %q source refs %q omit %q", query.Query, expected.SourceRefs, query.ExpectedRef)
		}
		metrics.Hits++
	}
	return metrics, nil
}

// ValidateFinalSnapshot checks the final semantic-page and provenance contract.
func ValidateFinalSnapshot(snapshot knowl.WorkspaceSnapshot) error {
	if len(snapshot.Pages) != 2 {
		return fmt.Errorf("ordinary page count %d, want 2", len(snapshot.Pages))
	}
	decision := findPage(snapshot.Pages, DecisionPageID)
	runbook := findPage(snapshot.Pages, RunbookPageID)
	if decision == nil || decision.Path != DecisionPagePath || runbook == nil || runbook.Path != RunbookPagePath {
		return fmt.Errorf("final page identities are invalid: %#v", snapshot.Pages)
	}
	for _, phrase := range []string{"Badger was selected", "crash recovery investigation", "SQLite superseded Badger", "historical Badger rationale"} {
		if !strings.Contains(decision.Content, phrase) {
			return fmt.Errorf("decision omits %q", phrase)
		}
	}
	if len(decision.SourceRefs) != 3 || len(runbook.SourceRefs) != 1 {
		return fmt.Errorf("final provenance counts are decision=%d runbook=%d", len(decision.SourceRefs), len(runbook.SourceRefs))
	}
	if !strings.Contains(runbook.Content, "[[decisions/session-memory]]") {
		return fmt.Errorf("runbook omits decision link")
	}
	return nil
}

func containsAnyToken(text string, terms []string) bool {
	wanted := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		wanted[strings.ToLower(term)] = struct{}{}
	}
	for _, token := range tokens(text) {
		if _, ok := wanted[token]; ok {
			return true
		}
	}
	return false
}

func tokens(text string) []string {
	var result []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			result = append(result, strings.ToLower(string(current)))
			current = nil
		}
	}
	for _, character := range text {
		switch {
		case unicode.IsLetter(character) || unicode.IsNumber(character):
			current = append(current, character)
		case unicode.IsMark(character) && len(current) > 0:
			current = append(current, character)
		default:
			flush()
		}
	}
	flush()
	return result
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
