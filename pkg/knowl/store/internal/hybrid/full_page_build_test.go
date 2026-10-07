package hybrid

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type boundedBatchProvider struct {
	sizes     []int
	failAfter int
}

func (p *boundedBatchProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	p.sizes = append(p.sizes, len(inputs))
	if len(inputs) > 16 {
		return nil, errors.New("oversized batch")
	}
	if p.failAfter > 0 && len(p.sizes) >= p.failAfter {
		return nil, &app.EmbeddingError{Code: knowl.RetrievalUnavailable}
	}
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestBuildLongPageUsesBoundedBatchesAndCompleteCoverage(t *testing.T) {
	provider := &boundedBatchProvider{}
	engine := &Engine{Provider: provider, Space: app.EmbeddingSpace{Dimensions: 2, PassagePrefix: testPassagePrefix}, Fingerprint: strings.Repeat("a", 64)}
	page := knowl.PageSnapshot{ID: testLongPageID, Path: testLongPagePath, Digest: strings.Repeat("b", 64), Title: "Long", Body: strings.Repeat("leading context ", 2000) + "terminal evidence"}
	state, chunks, err := engine.Build(t.Context(), knowl.WorkspaceSnapshot{Pages: []knowl.PageSnapshot{page}}, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if state.OmittedChunks != 0 || state.OmittedRunes != 0 || len(chunks) <= 80 || len(provider.sizes) < 2 {
		t.Fatalf("incomplete build state=%+v chunks=%d batches=%v", state, len(chunks), provider.sizes)
	}
	if chunks[len(chunks)-1].Ordinal != len(chunks)-1 {
		t.Fatalf("lost final ordinal: %d", chunks[len(chunks)-1].Ordinal)
	}
}

func TestBuildLateBatchFailureReturnsNoVectors(t *testing.T) {
	provider := &boundedBatchProvider{failAfter: 2}
	engine := &Engine{Provider: provider, Space: app.EmbeddingSpace{Dimensions: 2}, Fingerprint: strings.Repeat("a", 64)}
	page := knowl.PageSnapshot{ID: testLongPageID, Path: testLongPagePath, Digest: strings.Repeat("b", 64), Body: strings.Repeat("leading context ", 500) + "terminal evidence"}
	_, chunks, err := engine.Build(t.Context(), knowl.WorkspaceSnapshot{Pages: []knowl.PageSnapshot{page}}, strings.Repeat("c", 64))
	var classified *app.EmbeddingError
	if !errors.As(err, &classified) || classified.Code != knowl.RetrievalUnavailable || len(chunks) != 0 || len(provider.sizes) != 2 {
		t.Fatalf("partial failure chunks=%d batches=%v err=%v", len(chunks), provider.sizes, err)
	}
}
