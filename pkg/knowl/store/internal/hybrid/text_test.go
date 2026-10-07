package hybrid

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const fixtureQueryPrefix = "query: "

func TestSemanticTextIsBoundedAndReportsOmission(t *testing.T) {
	prepared, err := PrepareText(context.Background(), strings.Repeat("я", 2000), fixtureQueryPrefix, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Inputs) != 4 || prepared.OmittedRunes <= 0 || prepared.OmittedChunks <= 0 {
		t.Fatalf("unbounded/incomplete coverage hidden: %#v", prepared)
	}
	for _, input := range prepared.Inputs {
		if !utf8.ValidString(input) || utf8.RuneCountInString(strings.TrimPrefix(input, fixtureQueryPrefix)) > 384 || len(input) > 2048 {
			t.Fatal("invalid semantic chunk")
		}
	}
}

func TestSemanticTextPreservesCaseAndCanonicalEquivalence(t *testing.T) {
	got, err := PrepareText(context.Background(), "Cafe\u0301\r\nХРАНИЛИЩЕ Store", fixtureQueryPrefix, 4)
	if err != nil || !reflect.DeepEqual(got.Inputs, []string{"query: Café\nХРАНИЛИЩЕ Store"}) || got.OmittedRunes != 0 {
		t.Fatalf("semantic Unicode=%#v %v", got, err)
	}
	composed, err := PrepareText(context.Background(), "Café\nХРАНИЛИЩЕ Store", fixtureQueryPrefix, 4)
	if err != nil || !reflect.DeepEqual(got, composed) {
		t.Fatalf("canonical-equivalent semantic inputs drifted: %v", err)
	}
}

func TestSemanticTextExactAndOverChunkBound(t *testing.T) {
	for _, test := range []struct{ runes, omitted int }{{384, 0}, {385, 1}} {
		got, err := PrepareText(context.Background(), strings.Repeat("x", test.runes), testPassagePrefix, 1)
		if err != nil || len(got.Inputs) != 1 || utf8.RuneCountInString(got.Inputs[0]) != 393 || got.OmittedRunes != test.omitted {
			t.Fatalf("boundary prepared=%#v %v", got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PrepareText(ctx, "source", "", 4); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation=%v", err)
	}
	if _, err := PrepareText(context.Background(), "\xff", "", 4); !errors.Is(err, app.ErrEmbedding) {
		t.Fatalf("malformed text=%v", err)
	}
	if _, err := PrepareText(context.Background(), strings.Repeat("x", (4<<20)+1), "", 4); !errors.Is(err, app.ErrEmbedding) {
		t.Fatalf("oversized text=%v", err)
	}
}

func TestSemanticSourceUsesTextBeforeIdentityFallback(t *testing.T) {
	space := app.EmbeddingSpace{QueryPrefix: fixtureQueryPrefix}
	source := knowl.SourceSummary{Source: knowl.SourceRef{ID: "technical-id", Adapter: "fixture"}, Title: "OriginalTitle", Tags: []string{"Tag"}, Headings: []string{"Heading"}, Body: "Хранилище Body"}
	got, err := PrepareSource(context.Background(), source, space)
	if err != nil || !reflect.DeepEqual(got.Inputs, []string{"query: OriginalTitle\n\nTag\n\nHeading\n\nХранилище Body"}) {
		t.Fatalf("source semantic input=%#v %v", got, err)
	}
	source.Title = ""
	source.Tags = nil
	source.Headings = nil
	source.Body = ""
	got, err = PrepareSource(context.Background(), source, space)
	if err != nil || !reflect.DeepEqual(got.Inputs, []string{"query: technical-id\n\nfixture"}) {
		t.Fatalf("empty source fallback=%#v %v", got, err)
	}
}

func TestSemanticPageExcludesControlPagesAndUsesOriginalFields(t *testing.T) {
	space := app.EmbeddingSpace{PassagePrefix: testPassagePrefix}
	page := knowl.PageSnapshot{ID: "private-id", Path: "wiki/concepts/one.md", Title: "OriginalTitle", Body: "OriginalBody", SourceRefs: []string{"private-ref"}}
	got, err := PreparePage(context.Background(), page, space)
	if err != nil || !reflect.DeepEqual(got.Inputs, []string{testPassagePrefix + "OriginalTitle\n\n\n\n\n\nOriginalBody"}) {
		t.Fatalf("semantic page fields=%#v %v", got, err)
	}
	for _, path := range []string{"wiki/index.md", "wiki/log.md", "wiki/sources/mirror.md"} {
		page.Path = path
		got, err := PreparePage(context.Background(), page, space)
		if err != nil || len(got.Inputs) != 0 {
			t.Fatalf("control page embedded: %s %v", path, err)
		}
	}
}

func TestSpaceFingerprintChangesWithEveryModelPreprocessingField(t *testing.T) {
	base := app.EmbeddingSpace{Model: "e5", Revision: "fixed-revision", Dimensions: 384, QueryPrefix: fixtureQueryPrefix, PassagePrefix: testPassagePrefix}
	original, err := SpaceFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*app.EmbeddingSpace){func(s *app.EmbeddingSpace) { s.Model = "other" }, func(s *app.EmbeddingSpace) { s.Revision = "other" }, func(s *app.EmbeddingSpace) { s.Dimensions = 385 }, func(s *app.EmbeddingSpace) { s.QueryPrefix = "" }, func(s *app.EmbeddingSpace) { s.PassagePrefix = "" }} {
		changed := base
		edit(&changed)
		fingerprint, err := SpaceFingerprint(changed)
		if err != nil || fingerprint == original {
			t.Fatalf("space mismatch reused fingerprint: %v", err)
		}
	}
	again, err := SpaceFingerprint(base)
	if err != nil || again != original {
		t.Fatal("unstable space fingerprint")
	}
}

func TestDenseSourceDoesNotReplaceNonlexicalSemanticTextWithIdentity(t *testing.T) {
	for _, test := range []struct{ title, body, want string }{
		{title: "🚀", want: "query: 🚀"},
		{body: strings.Repeat("я", 300), want: fixtureQueryPrefix + strings.Repeat("я", 300)},
	} {
		source := knowl.SourceSummary{Source: knowl.SourceRef{ID: "technical-id", Adapter: "fixture"}, Title: test.title, Body: test.body}
		got, err := PrepareSource(context.Background(), source, app.EmbeddingSpace{QueryPrefix: fixtureQueryPrefix})
		if err != nil || !reflect.DeepEqual(got.Inputs, []string{test.want}) {
			t.Fatalf("semantic source replaced with identity: %v", err)
		}
	}
}
