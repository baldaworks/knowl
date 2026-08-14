package app

import (
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

func TestSourceTitleRuneBound(t *testing.T) {
	t.Parallel()
	got := sourceTitle([]byte("# " + strings.Repeat("界", maxSourceTitleRunes+10)))
	if utf8.RuneCountInString(got) != maxSourceTitleRunes {
		t.Fatalf("sourceTitle() rune count = %d, want %d", utf8.RuneCountInString(got), maxSourceTitleRunes)
	}
}
