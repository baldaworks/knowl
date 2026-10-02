package wiki

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testSignalTag      = "same"
	testSignalReal     = "Real"
	testSignalActual   = "Actual"
	testSignalBody     = "Body"
	testSignalEvidence = "Evidence"
	testSignalHeading  = "heading"
)

func TestSourceSignalsFenceCloserRequiresASCIIWhitespace(t *testing.T) {
	for _, marker := range []string{"```", "~~~"} {
		t.Run(marker, func(t *testing.T) {
			content := marker + "\n# Hidden\n" + marker + "\u00a0\n# Still code\n" + marker + "\n# Real\nBody"
			got, err := SourceSignals(t.Context(), []byte(content))
			if err != nil {
				t.Fatal(err)
			}
			want := knowl.SourceSummary{Title: testSignalReal, Headings: []string{testSignalReal}, Body: testSignalBody}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("signals=%#v want=%#v", got, want)
			}
		})
	}
}

func TestSourceSignalsMetadataAndMarkdown(t *testing.T) {
	tests := []struct {
		name, text string
		want       knowl.SourceSummary
	}{
		{"metadata", "---\ntitle: Actual title\ntags: [storage, storage, смешанный]\nunknown: value\n---\n# Heading\nEvidence text.", knowl.SourceSummary{Title: "Actual title", Tags: []string{"storage", "смешанный"}, Headings: []string{"Heading"}, Body: "Evidence text."}},
		{"fences", "````markdown\n# Fake\n```\n# Still fake\n````\n~~~\n# Fake tilde\n~~~\n    # Indented fake\n# Real\nEvidence", knowl.SourceSummary{Title: testSignalReal, Headings: []string{testSignalReal}, Body: testSignalEvidence}},
		{"mixed indentation heading", " \t# Fake\n# Real\nBody", knowl.SourceSummary{Title: testSignalReal, Headings: []string{testSignalReal}, Body: testSignalBody}},
		{"mixed indentation fence", " \t```markdown\n# Real\nBody", knowl.SourceSummary{Title: testSignalReal, Headings: []string{testSignalReal}, Body: testSignalBody}},
		{"Unicode space is literal prose", "\u00a0# Fake\n# Real\nBody", knowl.SourceSummary{Title: testSignalReal, Headings: []string{testSignalReal}, Body: "# Fake\nBody"}},
		{"setext", "Setext title\n===\nBody\n## Other\n## Other\nMore", knowl.SourceSummary{Title: "Setext title", Headings: []string{"Setext title", "Other"}, Body: "Body\nMore"}},
		{"malformed", "---\ntitle: [broken\n---\n# Actual\nBody", knowl.SourceSummary{Title: testSignalActual, Headings: []string{testSignalActual}, Body: testSignalBody}},
		{"alias", "---\ntitle: &x Fake\ntags: [*x]\n---\n# Actual\nBody", knowl.SourceSummary{Title: testSignalActual, Headings: []string{testSignalActual}, Body: testSignalBody}},
		{"duplicate", "---\ntitle: Fake\ntitle: Second\n---\n# Actual", knowl.SourceSummary{Title: testSignalActual, Headings: []string{testSignalActual}}},
		{"bad type", "---\ntitle: 123\n---\n# Actual", knowl.SourceSummary{Title: testSignalActual, Headings: []string{testSignalActual}}},
		{"unterminated", "---\ntitle: Plain text\n# Actual\nBody", knowl.SourceSummary{Title: testSignalActual, Headings: []string{testSignalActual}, Body: "title: Plain text\nBody"}},
		{"plain fallback", "\nPlain text\nMore", knowl.SourceSummary{Title: "Plain text", Body: "Plain text\nMore"}},
		{"CRLF", "---\r\ntitle: Metadata\r\n---\r\n## Real\r\nBody\r\n", knowl.SourceSummary{Title: "Metadata", Headings: []string{testSignalReal}, Body: testSignalBody}},
		{"empty", " \n---\n", knowl.SourceSummary{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.text)
			original := bytes.Clone(raw)
			got, err := SourceSignals(t.Context(), raw)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("signals=%#v, err=%v, want=%#v", got, err, tc.want)
			}
			if !bytes.Equal(raw, original) {
				t.Fatal("changed source bytes")
			}
		})
	}
}

func TestSourceSignalsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := SourceSignals(ctx, []byte("# Title")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestSourceSignalsTitleAndBodyBounds(t *testing.T) {
	for _, size := range []int{4096, 4097} {
		t.Run(strings.Repeat("x", size-4095), func(t *testing.T) {
			got, err := SourceSignals(t.Context(), []byte(strings.Repeat("界", size)))
			if err != nil || utf8.RuneCountInString(got.Title) != 256 || utf8.RuneCountInString(got.Body) != 3840 || !utf8.ValidString(got.Body) {
				t.Fatalf("bounded title/body=%d/%d, err=%v", utf8.RuneCountInString(got.Title), utf8.RuneCountInString(got.Body), err)
			}
		})
	}
}

func TestSourceMetadataLimits(t *testing.T) {
	const metadataBytes = 256 << 10
	tests := []struct {
		name, raw string
		valid     bool
	}{
		{"bytes exact", "title: Metadata\n#" + strings.Repeat(" ", metadataBytes-len("title: Metadata\n#\n")) + "\n", true},
		{"bytes over", "title: Metadata\n#" + strings.Repeat(" ", metadataBytes+1-len("title: Metadata\n#\n")) + "\n", false},
		{"nodes exact", "title: Metadata\nunknown: [" + strings.Repeat("x,", 16378) + "x]\n", true},
		{"nodes over", "title: Metadata\nunknown: [" + strings.Repeat("x,", 16379) + "x]\n", false},
		{"depth exact", "title: Metadata\nunknown: " + strings.Repeat("[", 62) + "x" + strings.Repeat("]", 62) + "\n", true},
		{"depth over", "title: Metadata\nunknown: " + strings.Repeat("[", 63) + "x" + strings.Repeat("]", 63) + "\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source := []byte("---\n" + tc.raw + "---\n# Real\nEvidence")
			got, err := SourceSignals(t.Context(), source)
			title := testSignalReal
			if tc.valid {
				title = "Metadata"
			}
			if err != nil || got.Title != title || got.Body != testSignalEvidence {
				t.Fatalf("metadata boundary title=%q body=%q err=%v", got.Title, got.Body, err)
			}
		})
	}
}

func TestBoundSourceSignalsListsAndIdentity(t *testing.T) {
	for _, count := range []int{32, 33} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			original := knowl.SourceSummary{Source: knowl.SourceRef{Adapter: "fixture", ID: "source"}, Version: knowl.SourceVersion{Version: "1", Digest: "original"}, Title: "Title", Body: strings.Repeat("😀", 4096)}
			for i := range count {
				original.Tags = append(original.Tags, fmt.Sprintf("tag%d", i))
				original.Headings = append(original.Headings, fmt.Sprintf("heading%d", i))
			}
			got := BoundSourceSignals(original)
			if got.Source != original.Source || got.Version != original.Version || len(got.Tags) != 32 || len(got.Headings) != 32 || len(original.Tags) != count || len(original.Headings) != count {
				t.Fatalf("identity/list limits=%#v", got)
			}
			parts := append([]string{got.Title, got.Body}, got.Tags...)
			parts = append(parts, got.Headings...)
			runes, bytes := 0, 0
			for _, part := range parts {
				if !utf8.ValidString(part) {
					t.Fatal("invalid clipped UTF-8")
				}
				runes += utf8.RuneCountInString(part)
				bytes += len(part)
			}
			if runes > 4096 || bytes > 16384 {
				t.Fatalf("semantic budget=%d/%d", runes, bytes)
			}
			if !reflect.DeepEqual(got, BoundSourceSignals(original)) {
				t.Fatal("nondeterministic clipping")
			}
		})
	}
	duplicate := knowl.SourceSummary{Tags: []string{testSignalTag, testSignalTag, "next"}, Headings: []string{testSignalHeading, testSignalHeading}}
	got := BoundSourceSignals(duplicate)
	if !reflect.DeepEqual(got.Tags, []string{testSignalTag, "next"}) || !reflect.DeepEqual(got.Headings, []string{testSignalHeading}) {
		t.Fatalf("duplicate signals=%#v", got)
	}
}

func TestSourceSignalsUTF8(t *testing.T) {
	if _, err := SourceSignals(t.Context(), []byte{0xff}); !errors.Is(err, ErrSourceSignalsInvalid) {
		t.Fatalf("invalid UTF-8 error=%v", err)
	}
	got := BoundSourceSignals(knowl.SourceSummary{Body: string([]byte{0xff})})
	if !utf8.ValidString(got.Body) {
		t.Fatal("direct summary emitted invalid UTF-8")
	}
}
