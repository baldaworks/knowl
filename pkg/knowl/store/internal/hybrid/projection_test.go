package hybrid

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const testPageDigest = "digest"

func TestProjectionBoundsAndCompleteness(t *testing.T) {
	state := ProjectionState{Space: strings.Repeat("a", 64), SnapshotDigest: strings.Repeat("b", 64), Dimensions: 1, Mode: knowl.RetrievalHybrid, ChunkCount: MaxChunks}
	chunks := make([]Chunk, MaxChunks)
	for i := range chunks {
		chunks[i] = Chunk{PageID: knowl.PageID(fmt.Sprintf("page-%d", i/EmbeddingBatchChunks)), PageDigest: testPageDigest, Ordinal: i % EmbeddingBatchChunks, ContentHash: strings.Repeat("c", 64), Vector: []float32{1}}
	}
	coverage := make([]PageCoverage, 0, MaxChunks/EmbeddingBatchChunks)
	for i := 0; i < MaxChunks/EmbeddingBatchChunks; i++ {
		coverage = append(coverage, PageCoverage{PageID: knowl.PageID(fmt.Sprintf("page-%d", i)), PageDigest: testPageDigest, Chunks: EmbeddingBatchChunks})
	}
	slices.SortFunc(coverage, func(a, b PageCoverage) int { return strings.Compare(string(a.PageID), string(b.PageID)) })
	state.Coverage, _ = EncodeCoverage(coverage)
	if err := ValidateProjection(t.Context(), state, chunks); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		state  ProjectionState
		chunks []Chunk
		want   knowl.RetrievalFailure
	}{
		{"extra row", state, append(slices.Clone(chunks), chunks[0]), knowl.RetrievalProjectionCapacity},
		{"partial", state, chunks[:MaxChunks-1], knowl.RetrievalProjectionDrift},
	}
	large := make([]float32, 4096)
	large[0] = 1
	oversized := make([]Chunk, 4096)
	for i := range oversized {
		oversized[i] = chunks[i]
		oversized[i].Vector = large
	}
	byteState := state
	byteState.Dimensions = len(large)
	byteState.ChunkCount = len(oversized)
	byteCoverage := make([]PageCoverage, 0, len(oversized)/EmbeddingBatchChunks)
	for i := 0; i < len(oversized)/EmbeddingBatchChunks; i++ {
		byteCoverage = append(byteCoverage, PageCoverage{PageID: knowl.PageID(fmt.Sprintf("page-%d", i)), PageDigest: testPageDigest, Chunks: EmbeddingBatchChunks})
	}
	slices.SortFunc(byteCoverage, func(a, b PageCoverage) int { return strings.Compare(string(a.PageID), string(b.PageID)) })
	byteState.Coverage, _ = EncodeCoverage(byteCoverage)
	cases = append(cases, struct {
		name   string
		state  ProjectionState
		chunks []Chunk
		want   knowl.RetrievalFailure
	}{"bytes", byteState, oversized, knowl.RetrievalProjectionCapacity})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var classified *app.EmbeddingError
			err := ValidateProjection(t.Context(), tc.state, tc.chunks)
			if !errors.As(err, &classified) || classified.Code != tc.want {
				t.Fatalf("validation=%v want=%s", err, tc.want)
			}
		})
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ValidateProjection(canceled, state, chunks); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}
