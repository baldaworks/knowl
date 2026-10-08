package hybrid

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

func TestOriginalEvidencePreservesOriginalUnicodeAndCRLF(t *testing.T) {
	fields := SemanticFields{Title: "Résumé", Body: "Cafe\u0301\r\nпроверка"}
	space := app.EmbeddingSpace{PassagePrefix: testPassagePrefix}
	prepared, err := PreparePageFields(t.Context(), fields, space)
	if err != nil || len(prepared.Inputs) != 1 {
		t.Fatalf("prepare: %+v %v", prepared, err)
	}
	hash := sha256.Sum256([]byte(prepared.Inputs[0]))
	snippet, err := OriginalEvidence(t.Context(), fields, space, Chunk{Ordinal: 0, ContentHash: hex.EncodeToString(hash[:])}, 100)
	if err != nil || snippet != "Cafe\u0301\r\nпроверка" {
		t.Fatalf("original evidence=%q error=%v", snippet, err)
	}
	_, err = OriginalEvidence(t.Context(), fields, space, Chunk{Ordinal: 0, ContentHash: strings.Repeat("0", 64)}, 100)
	if err == nil {
		t.Fatal("stale window hash accepted")
	}
}

func TestOriginalEvidenceCitesMetadataOnlyPage(t *testing.T) {
	fields := SemanticFields{Title: "Title", Tags: "metatag"}
	space := app.EmbeddingSpace{PassagePrefix: testPassagePrefix}
	prepared, err := PreparePageFields(t.Context(), fields, space)
	if err != nil || len(prepared.Inputs) != 1 {
		t.Fatalf("prepare tags: %+v %v", prepared, err)
	}
	ordinal := 0
	hash := sha256.Sum256([]byte(prepared.Inputs[ordinal]))
	snippet, err := OriginalEvidence(t.Context(), fields, space, Chunk{Ordinal: ordinal, ContentHash: hex.EncodeToString(hash[:])}, 80)
	if err != nil || !strings.Contains(snippet, "metatag") {
		t.Fatalf("tag evidence=%q error=%v", snippet, err)
	}
}
