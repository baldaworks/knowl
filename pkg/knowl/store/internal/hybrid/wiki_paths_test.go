package hybrid

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestWikiPathsAreEmbeddingContextNotEvidence(t *testing.T) {
	const lastWikiPath = "Wiki / Z"
	page := knowl.PageSnapshot{ID: "page", Title: "Manual", Body: "## Detail\nOriginal body.", WikiPaths: []string{lastWikiPath, "Wiki / A", lastWikiPath}}
	space := app.EmbeddingSpace{PassagePrefix: testPassagePrefix}
	got, err := PreparePage(t.Context(), page, space)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Inputs, []string{testPassagePrefix + "Wiki / A\nWiki / Z\nManual\n\n\nDetail\nOriginal body."}) {
		t.Fatalf("prepared inputs: %#v", got.Inputs)
	}
	hash := sha256.Sum256([]byte(got.Inputs[0]))
	fields := SemanticFields{Title: page.Title, Body: page.Body, WikiPaths: page.WikiPaths}
	evidence, err := OriginalEvidence(t.Context(), fields, space, Chunk{Ordinal: 0, ContentHash: hex.EncodeToString(hash[:])}, 1000)
	if err != nil || evidence != "Manual\n\n## Detail\n\nOriginal body." {
		t.Fatalf("evidence=%q err=%v", evidence, err)
	}
	page.WikiPaths = []string{"Wiki / A", lastWikiPath}
	again, err := PreparePage(t.Context(), page, space)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("path ordering changed input: %#v %v", again, err)
	}
	fields.WikiPaths = []string{strings.Repeat("x", ChunkRunes)}
	_, err = PreparePageFields(t.Context(), fields, space)
	var classified *app.EmbeddingError
	if !errors.As(err, &classified) || classified.Code != knowl.RetrievalInputLimit {
		t.Fatalf("oversized prefix error=%v", err)
	}
}

func TestWikiPathNormalizationAndLimits(t *testing.T) {
	paths, err := NormalizeWikiPaths([]string{" Cafe\u0301 /  Child ", "Café / Child", ""})
	if err != nil || !reflect.DeepEqual(paths, []string{"Café / Child"}) {
		t.Fatalf("paths=%#v err=%v", paths, err)
	}
	for _, test := range []struct {
		path string
		code knowl.RetrievalFailure
	}{{string([]byte{0xff}), knowl.RetrievalInvalidInput}, {strings.Repeat("a", MaxSemanticBytes+1), knowl.RetrievalInputLimit}} {
		_, err := NormalizeWikiPaths([]string{test.path})
		var classified *app.EmbeddingError
		if !errors.As(err, &classified) || classified.Code != test.code {
			t.Fatalf("error=%v expected=%v", err, test.code)
		}
	}
}
