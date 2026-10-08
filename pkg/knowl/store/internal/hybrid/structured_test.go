package hybrid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestStructuredSectionsAndEvidence(t *testing.T) {
	fields := SemanticFields{Title: "Manual", Body: "Preamble.\r\n\r\n# Root\r\nAlpha.\r\n\r\n## Child\r\nCafe\u0301.\r\n\r\n```go\r\nprintln(1)\r\n```\r\n\r\n# Other\r\nOmega.\r\n"}
	space := app.EmbeddingSpace{PassagePrefix: testPassagePrefix}
	got, err := PreparePageFields(t.Context(), fields, space)
	if err != nil || len(got.Inputs) != 4 || len(got.Windows) != 4 {
		t.Fatalf("sections=%#v, %v", got, err)
	}
	for i, ancestry := range []string{"", "Root", "Root / Child", "Other"} {
		if ancestry != "" && !strings.Contains(got.Inputs[i], ancestry) {
			t.Fatalf("missing heading ancestry in %q", got.Inputs[i])
		}
		hash := sha256.Sum256([]byte(got.Inputs[i]))
		excerpt, err := OriginalEvidence(t.Context(), fields, space, Chunk{Ordinal: i, ContentHash: hex.EncodeToString(hash[:])}, 1000)
		if err != nil || excerpt == "" {
			t.Fatalf("evidence %d: %q, %v", i, excerpt, err)
		}
	}
	if !strings.Contains(got.Inputs[2], "Café.") || strings.Contains(got.Inputs[2], "Omega.") || strings.Contains(got.Inputs[3], "Alpha.") {
		t.Fatalf("sections crossed: %#v", got.Inputs)
	}
	if _, err := OriginalEvidence(t.Context(), fields, space, Chunk{Ordinal: 2, ContentHash: strings.Repeat("0", 64)}, 1000); err == nil {
		t.Fatal("mismatched input accepted")
	} else {
		var classified *app.EmbeddingError
		if !errors.As(err, &classified) || classified.Code != knowl.RetrievalProjectionDrift {
			t.Fatalf("mismatch classification: %v", err)
		}
	}
}

func TestStructuredOKFSemanticsExcludeTechnicalMetadata(t *testing.T) {
	space := app.EmbeddingSpace{PassagePrefix: testPassagePrefix}
	page := SemanticFields{Title: "Concept", Format: "okf/0.2", OKF: &okf.Metadata{Type: "Decision", Tags: []string{"storage"}, Description: "Chosen path", Resource: "secret-resource", Extensions: map[string]any{"password": "secret"}}, Body: "## Detail\nReason."}
	got, err := PreparePageFields(t.Context(), page, space)
	if err != nil || len(got.Inputs) != 1 {
		t.Fatalf("OKF=%#v, %v", got, err)
	}
	if !strings.Contains(got.Inputs[0], "Decision") || !strings.Contains(got.Inputs[0], "storage") || !strings.Contains(got.Inputs[0], "Chosen path") || strings.Contains(got.Inputs[0], "secret") {
		t.Fatalf("OKF fields=%q", got.Inputs[0])
	}
}

func TestStructuredOversizedBlockHasCompleteBoundedCoverage(t *testing.T) {
	space := app.EmbeddingSpace{PassagePrefix: testPassagePrefix}
	fields := SemanticFields{Title: "Lengthy document", Body: "# Heading\n" + strings.Repeat("界", 1100)}
	got, err := PreparePageFields(context.Background(), fields, space)
	if err != nil || len(got.Inputs) < 3 {
		t.Fatalf("long block=%#v, %v", got, err)
	}
	var covered strings.Builder
	for i, window := range got.Windows {
		if utf8.RuneCountInString(got.Inputs[i]) > ChunkRunes+utf8.RuneCountInString(testPassagePrefix) {
			t.Fatalf("unbounded input %d", i)
		}
		covered.WriteString(fields.Body[window.Start:window.End])
	}
	if covered.String() != strings.Repeat("界", 1100) {
		t.Fatalf("incomplete source coverage: %d bytes", covered.Len())
	}
	again, err := PreparePageFields(context.Background(), fields, space)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatal("nondeterministic preparation")
	}
}

func TestStructuredSectionIdentitySurvivesEarlierInsertion(t *testing.T) {
	space := app.EmbeddingSpace{PassagePrefix: testPassagePrefix}
	before := SemanticFields{Title: "Operations", Body: "# First\nOne.\n\n# Last\nFinal body."}
	after := SemanticFields{Title: "Operations", Body: "# First\nOne.\n\n# Inserted\nNew.\n\n# Last\nFinal body."}
	oldInputs, err := PreparePageFields(t.Context(), before, space)
	if err != nil {
		t.Fatal(err)
	}
	newInputs, err := PreparePageFields(t.Context(), after, space)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldInputs.Inputs) != 2 || len(newInputs.Inputs) != 3 || oldInputs.Inputs[1] != newInputs.Inputs[2] {
		t.Fatalf("later section identity drifted: %#v -> %#v", oldInputs.Inputs, newInputs.Inputs)
	}
}

func TestStructuredEmptyHeadingCitesOriginalHeading(t *testing.T) {
	fields := SemanticFields{Title: "Guide", Body: "# Empty\r\n"}
	space := app.EmbeddingSpace{PassagePrefix: testPassagePrefix}
	prepared, err := PreparePageFields(t.Context(), fields, space)
	if err != nil || len(prepared.Inputs) != 1 {
		t.Fatalf("prepare: %#v %v", prepared, err)
	}
	hash := sha256.Sum256([]byte(prepared.Inputs[0]))
	excerpt, err := OriginalEvidence(t.Context(), fields, space, Chunk{ContentHash: hex.EncodeToString(hash[:])}, 100)
	if err != nil || excerpt != "# Empty" {
		t.Fatalf("evidence=%q, %v", excerpt, err)
	}
}

func TestStructuredIndentedCodePreservesSourceIndentation(t *testing.T) {
	fields := SemanticFields{Title: "Code", Body: "# Example\n\n    code\n    second\n"}
	space := app.EmbeddingSpace{PassagePrefix: testPassagePrefix}
	prepared, err := PreparePageFields(t.Context(), fields, space)
	if err != nil || len(prepared.Inputs) != 1 {
		t.Fatalf("prepare: %#v %v", prepared, err)
	}
	if !strings.Contains(prepared.Inputs[0], "    code") {
		t.Fatalf("lost indentation: %q", prepared.Inputs[0])
	}
	hash := sha256.Sum256([]byte(prepared.Inputs[0]))
	excerpt, err := OriginalEvidence(t.Context(), fields, space, Chunk{ContentHash: hex.EncodeToString(hash[:])}, 100)
	if err != nil || excerpt != "    code\n    second" {
		t.Fatalf("evidence=%q, %v", excerpt, err)
	}
}
