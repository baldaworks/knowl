package hybrid

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type reuseProvider struct{ inputs []string }

func (p *reuseProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	p.inputs = append(p.inputs, inputs...)
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func reuseFixture(t *testing.T, body string) (*Engine, *reuseProvider, knowl.WorkspaceSnapshot, ProjectionState, []Chunk) {
	t.Helper()
	provider := &reuseProvider{}
	engine := &Engine{Provider: provider, Space: app.EmbeddingSpace{Dimensions: 2}, Fingerprint: strings.Repeat("a", 64)}
	snapshot := knowl.WorkspaceSnapshot{Pages: []knowl.PageSnapshot{{ID: "page", Path: "wiki/page.md", Digest: strings.Repeat("b", 64), Title: "Page", Body: body}}}
	state, chunks, err := engine.Build(t.Context(), snapshot, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	provider.inputs = nil
	return engine, provider, snapshot, state, chunks
}

func TestBuildWithReusePreservesCurrentOrdinalsAndDigests(t *testing.T) {
	engine, provider, snapshot, previous, old := reuseFixture(t, "# A\nalpha\n\n# B\nbeta\n")
	previousPages := append([]knowl.PageSnapshot(nil), snapshot.Pages...)
	snapshot.Pages[0].Digest = strings.Repeat("d", 64)
	snapshot.Pages[0].Body = "# New\nnew\n\n# B\nbeta\n\n# A\nalpha\n"
	state, chunks, err := engine.BuildWithReuse(t.Context(), snapshot, strings.Repeat("e", 64), &previous, old, previousPages)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.inputs) != 1 || len(chunks) != 3 {
		t.Fatalf("provider inputs=%d chunks=%d", len(provider.inputs), len(chunks))
	}
	if chunks[0].ContentHash == old[0].ContentHash || chunks[1].ContentHash != old[1].ContentHash || chunks[2].ContentHash != old[0].ContentHash {
		t.Fatalf("section identities old=%v new=%v", old, chunks)
	}
	for i, chunk := range chunks {
		if chunk.Ordinal != i || chunk.PageDigest != snapshot.Pages[0].Digest {
			t.Fatalf("chunk %d metadata=%+v", i, chunk)
		}
	}
	if err := ValidateProjection(t.Context(), state, chunks); err != nil {
		t.Fatal(err)
	}
}

func TestBuildWithReuseSkipsUnchangedAndEmbedsChangedContext(t *testing.T) {
	engine, provider, snapshot, previous, old := reuseFixture(t, "# A\nalpha\n\n# B\nbeta\n")
	previousPages := append([]knowl.PageSnapshot(nil), snapshot.Pages...)
	_, same, err := engine.BuildWithReuse(t.Context(), snapshot, strings.Repeat("d", 64), &previous, old, previousPages)
	if err != nil || len(provider.inputs) != 0 || !reflect.DeepEqual(old, same) {
		t.Fatalf("unchanged reuse inputs=%d err=%v", len(provider.inputs), err)
	}
	snapshot.Pages[0].Body = "# Renamed\nalpha\n\n# B\nbeta\n"
	snapshot.Pages[0].Digest = strings.Repeat("e", 64)
	_, changed, err := engine.BuildWithReuse(t.Context(), snapshot, strings.Repeat("f", 64), &previous, old, previousPages)
	if err != nil || len(provider.inputs) != 1 || changed[0].ContentHash == old[0].ContentHash || changed[1].ContentHash != old[1].ContentHash {
		t.Fatalf("heading edit inputs=%d err=%v", len(provider.inputs), err)
	}
}

func TestBuildWithReuseSharesValidDuplicateInput(t *testing.T) {
	engine, provider, snapshot, previous, old := reuseFixture(t, "# Same\ntext\n\n# Same\ntext\n")
	if len(old) != 2 || old[0].ContentHash != old[1].ContentHash {
		t.Fatalf("fixture lacks equal inputs: %v", old)
	}
	_, chunks, err := engine.BuildWithReuse(t.Context(), snapshot, strings.Repeat("d", 64), &previous, old[:1], snapshot.Pages)
	if err != nil || len(provider.inputs) != 2 {
		t.Fatalf("incomplete prior failed full inference: inputs=%d chunks=%d err=%v", len(provider.inputs), len(chunks), err)
	}
	provider.inputs = nil
	_, chunks, err = engine.BuildWithReuse(t.Context(), snapshot, strings.Repeat("e", 64), &previous, old, snapshot.Pages)
	if err != nil || len(provider.inputs) != 0 || len(chunks) != 2 {
		t.Fatalf("duplicate reuse inputs=%d chunks=%d err=%v", len(provider.inputs), len(chunks), err)
	}
}

func TestBuildWithReuseRejectsMismatchedOrCorruptPriorState(t *testing.T) {
	for _, mutate := range []func(*ProjectionState, []Chunk){
		func(s *ProjectionState, _ []Chunk) { s.Space = strings.Repeat("d", 64) },
		func(s *ProjectionState, _ []Chunk) { s.Dimensions = 3 },
		func(_ *ProjectionState, c []Chunk) {
			c[0].ContentHash = strings.Repeat("0", 64)
			c[0].Vector = []float32{0, 0}
		},
		func(s *ProjectionState, _ []Chunk) { s.ChunkCount++ },
	} {
		engine, provider, snapshot, state, chunks := reuseFixture(t, "# A\nalpha\n")
		mutate(&state, chunks)
		_, _, err := engine.BuildWithReuse(t.Context(), snapshot, strings.Repeat("e", 64), &state, chunks, snapshot.Pages)
		if err != nil || len(provider.inputs) != 1 {
			t.Fatalf("unsafe prior reuse inputs=%d err=%v", len(provider.inputs), err)
		}
	}
}

func TestBuildWithReuseRejectsPlausibleButFalsePriorHashes(t *testing.T) {
	engine, provider, snapshot, previous, old := reuseFixture(t, "# A\nalpha\n\n# B\nbeta\n")
	old[0].ContentHash, old[1].ContentHash = old[1].ContentHash, old[0].ContentHash
	if err := ValidateProjection(t.Context(), previous, old); err != nil {
		t.Fatalf("fixture must pass structural validation: %v", err)
	}
	_, _, err := engine.BuildWithReuse(t.Context(), snapshot, strings.Repeat("e", 64), &previous, old, snapshot.Pages)
	if err != nil || len(provider.inputs) != 2 {
		t.Fatalf("false prior hashes reused: inputs=%d err=%v", len(provider.inputs), err)
	}
}

func TestBuildWithReuseRequiresMatchingPriorLexicalPages(t *testing.T) {
	for _, priorPages := range []func([]knowl.PageSnapshot) []knowl.PageSnapshot{
		func([]knowl.PageSnapshot) []knowl.PageSnapshot { return nil },
		func(pages []knowl.PageSnapshot) []knowl.PageSnapshot {
			pages[0].Body = "# A\nchanged\n"
			return pages
		},
		func(pages []knowl.PageSnapshot) []knowl.PageSnapshot {
			pages[0].Digest = strings.Repeat("d", 64)
			return pages
		},
	} {
		engine, provider, snapshot, previous, chunks := reuseFixture(t, "# A\nalpha\n")
		oldPages := priorPages(append([]knowl.PageSnapshot(nil), snapshot.Pages...))
		_, _, err := engine.BuildWithReuse(t.Context(), snapshot, strings.Repeat("e", 64), &previous, chunks, oldPages)
		if err != nil || len(provider.inputs) != 1 {
			t.Fatalf("unverified lexical pages reused: inputs=%d err=%v", len(provider.inputs), err)
		}
	}
}

func TestBuildWithReusePropagatesProviderFailureWithoutPartialChunks(t *testing.T) {
	engine, _, snapshot, state, chunks := reuseFixture(t, "# A\nalpha\n")
	previousPages := append([]knowl.PageSnapshot(nil), snapshot.Pages...)
	engine.Provider = failingReuseProvider{}
	snapshot.Pages[0].Body = "# A\nchanged\n"
	_, result, err := engine.BuildWithReuse(t.Context(), snapshot, strings.Repeat("e", 64), &state, chunks, previousPages)
	var classified *app.EmbeddingError
	if !errors.As(err, &classified) || classified.Code != knowl.RetrievalUnavailable || result != nil {
		t.Fatalf("partial failure chunks=%v err=%v", result, err)
	}
}

type failingReuseProvider struct{}

func (failingReuseProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, &app.EmbeddingError{Code: knowl.RetrievalUnavailable}
}
