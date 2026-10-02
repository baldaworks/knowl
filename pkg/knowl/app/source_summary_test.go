package app

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSourceTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, content, want string
	}{
		{name: "first heading wins", content: "metadata preamble\n\n##  Badger session memory  \nbody", want: "Badger session memory"},
		{name: "first nonempty fallback", content: "\n  Incident report  \nordinary body", want: "Incident report"},
		{name: "unicode", content: "# Решение о хранилище\n", want: "Решение о хранилище"},
		{name: "seven hashes are not ATX", content: "####### literal\nbody", want: "####### literal"},
		{name: "hash without whitespace is not ATX", content: "#literal\n# Actual", want: "Actual"},
		{name: "empty", content: " \n\t\n", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := sourceTitle([]byte(test.content)); got != test.want {
				t.Fatalf("sourceTitle() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestContextBaselineSourceTitles(t *testing.T) {
	// Keep these fixtures local: the golden helper depends on app and cannot be
	// imported from an internal app test. Epic REQ-SOURCE-001; Story .3.
	fixtures := []struct{ id, content, expected string }{
		{"frontmatter", "---\ntitle: Quasarretention\ntags: [quasarretention]\n---\nA maintenance note.", "Quasarretention"},
		{"fenced-heading", "```markdown\n# Decoy\n```\n\n# Quasarretention\n\nStorage requirements.", "Quasarretention"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.id, func(t *testing.T) {
			observed := sourceTitle([]byte(fixture.content))
			outcome := "gap"
			if observed == fixture.expected {
				outcome = "met"
			}
			// Titles here are fixed public fixture labels, never operator source data.
			encoded, err := json.Marshal(struct {
				CaseID   string `json:"case_id"`
				Expected string `json:"expected_title"`
				Observed string `json:"observed_title"`
				Outcome  string `json:"outcome"`
			}{fixture.id + "-title", fixture.expected, observed, outcome})
			if err != nil {
				t.Fatal(err)
			}
			t.Log(string(encoded))
		})
	}
}

func TestSourceTitleRuneBound(t *testing.T) {
	t.Parallel()
	got := sourceTitle([]byte("# " + strings.Repeat("界", maxSourceTitleRunes+10)))
	if utf8.RuneCountInString(got) != maxSourceTitleRunes {
		t.Fatalf("sourceTitle() rune count = %d, want %d", utf8.RuneCountInString(got), maxSourceTitleRunes)
	}
}
