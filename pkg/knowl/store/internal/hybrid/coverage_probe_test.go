package hybrid

import (
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testLongPageID    = "long"
	testLongPagePath  = "wiki/long.md"
	testPassagePrefix = "passage: "
)

func TestCoverageProbeLongPageTail(t *testing.T) {
	page := knowl.PageSnapshot{ID: testLongPageID, Path: testLongPagePath, Title: "Long", Body: strings.Repeat("leading context ", 500) + "terminal evidence"}
	prepared, err := PreparePage(t.Context(), page, app.EmbeddingSpace{PassagePrefix: testPassagePrefix})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.OmittedChunks != 0 || prepared.OmittedRunes != 0 {
		t.Fatalf("published page incomplete: chunks=%d runes=%d", prepared.OmittedChunks, prepared.OmittedRunes)
	}
	found := false
	for _, input := range prepared.Inputs {
		if strings.Contains(input, "terminal evidence") {
			found = true
		}
	}
	if !found {
		t.Fatal("terminal evidence never reaches embeddings")
	}
}
