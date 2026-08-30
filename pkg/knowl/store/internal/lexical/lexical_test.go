package lexical

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

const testTerm = "badger"

func TestNormalize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		raw   string
		want  []string
		error bool
	}{
		{name: "case punctuation and framing", raw: "Why WAS Badger selected, for Session-Memory?", want: []string{testTerm, "selected", "for", "session", "memory"}},
		{name: "first seen distinct", raw: "alpha BETA alpha beta gamma", want: []string{"alpha", "beta", "gamma"}},
		{name: "unicode letters numbers and attached marks", raw: "КАК cafe\u0301 версия2?", want: []string{"как", "cafe\u0301", "версия2"}},
		{name: "leading mark does not start token", raw: "\u0301alpha", want: []string{"alpha"}},
		{name: "only exact framing set removed", raw: "can should the decision", want: []string{"can", "should", "the", "decision"}},
		{name: "blank", raw: "  -- ", error: true},
		{name: "framing only", raw: "What is it?", want: []string{"it"}},
		{name: "all framing", raw: "what is why", error: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := Normalize(test.raw)
			if test.error {
				if !errors.Is(err, ErrInvalidQuery) {
					t.Fatalf("Normalize() error = %v, want ErrInvalidQuery", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if strings.Join(got.Terms, "|") != strings.Join(test.want, "|") {
				t.Fatalf("Normalize() terms = %q, want %q", got.Terms, test.want)
			}
		})
	}
}

func TestNormalizeBounds(t *testing.T) {
	t.Parallel()
	tooMany := make([]string, MaxTerms+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("term%d", index)
	}
	if _, err := Normalize(strings.Join(tooMany, " ")); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("too many terms error = %v, want ErrInvalidQuery", err)
	}
	if _, err := Normalize(strings.Repeat("界", MaxTermRunes+1)); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("oversized terms error = %v, want ErrInvalidQuery", err)
	}
}

func TestSummarizeTruncatesInPriorityOrder(t *testing.T) {
	t.Parallel()
	parts := make([]string, MaxTerms+2)
	for index := range parts {
		parts[index] = fmt.Sprintf("term%d", index)
	}
	got := Summarize(strings.Join(parts[:MaxTerms+1], " "), "origin-adapter")
	if len(got.Terms) != MaxTerms || got.Terms[0] != "term0" || got.Terms[MaxTerms-1] != fmt.Sprintf("term%d", MaxTerms-1) {
		t.Fatalf("Summarize() terms = %q", got.Terms)
	}
	if strings.Contains(strings.Join(got.Terms, " "), "origin") {
		t.Fatal("Summarize() displaced higher-priority title terms")
	}
}

func TestSummarizeSharesNormalizationAndAllowsEmpty(t *testing.T) {
	t.Parallel()
	got := Summarize("Why WAS Badger, badger?", "Source-ID", "Adapter")
	want := []string{testTerm, "source", "id", "adapter"}
	if strings.Join(got.Terms, "|") != strings.Join(want, "|") {
		t.Fatalf("Summarize() terms = %q, want %q", got.Terms, want)
	}
	if empty := Summarize("what is why"); len(empty.Terms) != 0 {
		t.Fatalf("Summarize() framing-only terms = %q, want empty", empty.Terms)
	}
	withOversizedTitle := Summarize(strings.Repeat("界", MaxTermRunes+1), "origin")
	if len(withOversizedTitle.Terms) != 1 || withOversizedTitle.Terms[0] != "origin" {
		t.Fatalf("Summarize() after oversized title = %q, want origin fallback", withOversizedTitle.Terms)
	}
}

func TestExcerpt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		native, title     string
		body              string
		terms             []string
		limit             int
		want              string
		wantContains      string
		wantNativeContent bool
	}{
		{name: "native match preferred", native: "chosen fragment around Badger decision", title: "Badger title", body: "other body", terms: []string{testTerm}, limit: 24, wantContains: "Badger"},
		{name: "body fallback centers middle match", native: "irrelevant prefix", title: "Decision", body: strings.Repeat("x", 40) + " badger " + strings.Repeat("y", 40), terms: []string{testTerm}, limit: 20, wantContains: testTerm},
		{name: "title-only fallback", native: "body without it", title: "PostgreSQL Recovery", body: "ordinary text", terms: []string{"postgresql"}, limit: 14, wantContains: "PostgreSQL"},
		{name: "unicode", native: "начало решение Баджер завершение", terms: []string{"баджер"}, limit: 16, wantContains: "Баджер"},
		{name: "tiny bound wins", native: "prefix extraordinarilylongterm suffix", terms: []string{"extraordinarilylongterm"}, limit: 5, want: "extra"},
		{name: "unlimited body compatibility", native: "short fragment", title: "Title", body: "complete body without truncation", terms: []string{"fragment"}, limit: 0, want: "complete body without truncation"},
		{name: "unmatched prefix bounded", native: "abcdefghijk", terms: []string{"missing"}, limit: 5, want: "abcd…"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := Excerpt(test.native, test.title, test.body, test.terms, test.limit)
			if test.want != "" && got != test.want {
				t.Fatalf("Excerpt() = %q, want %q", got, test.want)
			}
			if test.wantContains != "" && !strings.Contains(strings.ToLower(got), strings.ToLower(test.wantContains)) {
				t.Fatalf("Excerpt() = %q, want term %q", got, test.wantContains)
			}
			if test.limit > 0 && utf8.RuneCountInString(got) > test.limit {
				t.Fatalf("Excerpt() rune count = %d, limit %d: %q", utf8.RuneCountInString(got), test.limit, got)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("Excerpt() returned invalid UTF-8: %q", got)
			}
		})
	}
}

func TestContainsTermRequiresCompleteToken(t *testing.T) {
	t.Parallel()
	if ContainsTerm("badgering", []string{testTerm}) {
		t.Fatal("ContainsTerm() matched a substring")
	}
	if !ContainsTerm("A BADGER decision", []string{testTerm}) {
		t.Fatal("ContainsTerm() did not match a complete case-folded token")
	}
}

func TestRelevantRequiresMultipleMatchesForLongQueries(t *testing.T) {
	t.Parallel()
	if Relevant("an unrelated document containing only term", []string{"xyzzy", "valera", "no", "such", "term", "92841"}) {
		t.Fatal("Relevant() accepted one common match from a long OOV query")
	}
	if !Relevant("xyzzy valera useful evidence", []string{"xyzzy", "valera", "no", "such"}) {
		t.Fatal("Relevant() rejected a half-term match")
	}
	if !Relevant("badger evidence", []string{"badger", "session"}) {
		t.Fatal("Relevant() rejected relaxed two-term recall")
	}
}

func TestDocumentFieldsIncludeTagsInRelevanceAndEvidence(t *testing.T) {
	t.Parallel()
	fields := DocumentFields{
		Title:       "Knowledge service",
		Tags:        "architecture\nСистема знаний",
		Description: "Clean public summary",
		Body:        "User-authored content without the query term.",
	}
	terms := []string{"система"}
	if !RelevantFields(fields, terms) {
		t.Fatal("RelevantFields() rejected a tag-only match")
	}
	excerpt := ExcerptFields("irrelevant native body", fields, terms, 48)
	if !strings.Contains(excerpt, "tag: Система знаний") || !ContainsTerm(excerpt, terms) {
		t.Fatalf("ExcerptFields() = %q, want semantic tag evidence", excerpt)
	}
	if utf8.RuneCountInString(excerpt) > 48 || !utf8.ValidString(excerpt) {
		t.Fatalf("ExcerptFields() returned invalid bounded evidence %q", excerpt)
	}
	if RelevantFields(DocumentFields{Body: "unrelated content"}, terms) {
		t.Fatal("RelevantFields() accepted fields without the query term")
	}
}

func TestExcerptFieldsPreservesCleanContentMatches(t *testing.T) {
	t.Parallel()
	fields := DocumentFields{
		Title:       "Decision",
		Tags:        "badger",
		Description: "Public recovery description",
		Body:        "Useful quorumbeacon body evidence.",
	}
	got := ExcerptFields("Useful quorumbeacon body evidence.", fields, []string{"quorumbeacon"}, 24)
	if !ContainsTerm(got, []string{"quorumbeacon"}) || strings.Contains(got, "tag:") {
		t.Fatalf("ExcerptFields() = %q, want existing clean content evidence", got)
	}
}

func TestExcerptPreservesMatchWithinEverySufficientBudget(t *testing.T) {
	t.Parallel()
	const term = testTerm
	for prefixRunes := 0; prefixRunes <= 12; prefixRunes++ {
		for suffixRunes := 0; suffixRunes <= 12; suffixRunes++ {
			text := strings.Repeat("x", prefixRunes) + " " + term + " " + strings.Repeat("y", suffixRunes)
			for limit := utf8.RuneCountInString(term); limit <= utf8.RuneCountInString(text); limit++ {
				got := Excerpt(text, "", "", []string{term}, limit)
				if utf8.RuneCountInString(got) > limit {
					t.Fatalf("prefix=%d suffix=%d limit=%d: oversized excerpt %q", prefixRunes, suffixRunes, limit, got)
				}
				if !ContainsTerm(got, []string{term}) {
					t.Fatalf("prefix=%d suffix=%d limit=%d: excerpt lost complete match %q", prefixRunes, suffixRunes, limit, got)
				}
			}
		}
	}
}
