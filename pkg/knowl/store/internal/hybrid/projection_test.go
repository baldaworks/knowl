package hybrid

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testPageDigest     = "digest"
	testMetadataPageID = "metadata-page"
)

func TestProjectionMetadataWithoutVectors(t *testing.T) {
	pages := []PageCoverage{{PageID: "empty", PageDigest: "empty-digest", Chunks: 0}, {PageID: testMetadataPageID, PageDigest: testPageDigest, Chunks: 1}}
	coverage, err := EncodeCoverage(pages)
	if err != nil {
		t.Fatal(err)
	}
	state := ProjectionState{Space: strings.Repeat("a", 64), SnapshotDigest: strings.Repeat("b", 64), Dimensions: 1, Mode: knowl.RetrievalHybrid, ChunkCount: 1, Coverage: coverage}
	got, err := ValidateProjectionMetadata(t.Context(), state)
	if err != nil || !reflect.DeepEqual(got, pages) {
		t.Fatalf("coverage=%+v error=%v", got, err)
	}
	degraded := state
	degraded.Mode, degraded.Reason = knowl.RetrievalDegraded, knowl.RetrievalUnavailable
	degraded.ChunkCount, degraded.Coverage = 0, ""
	degraded.OmittedChunks, degraded.OmittedRunes = 2, 10
	got, err = ValidateProjectionMetadata(t.Context(), degraded)
	if err != nil || len(got) != 0 {
		t.Fatalf("degraded coverage=%+v error=%v", got, err)
	}
	cases := []struct {
		name   string
		base   ProjectionState
		change func(*ProjectionState)
		want   knowl.RetrievalFailure
	}{
		{"capacity", state, func(s *ProjectionState) { s.ChunkCount = MaxChunks + 1 }, knowl.RetrievalProjectionCapacity},
		{"negative count", state, func(s *ProjectionState) { s.ChunkCount = -1 }, knowl.RetrievalProjectionDrift},
		{"space", state, func(s *ProjectionState) { s.Space = strings.Repeat("A", 64) }, knowl.RetrievalProjectionDrift},
		{"snapshot", state, func(s *ProjectionState) { s.SnapshotDigest = "bad" }, knowl.RetrievalProjectionDrift},
		{"dimensions", state, func(s *ProjectionState) { s.Dimensions = 4097 }, knowl.RetrievalProjectionDrift},
		{"mode", state, func(s *ProjectionState) { s.Mode = knowl.RetrievalLexical }, knowl.RetrievalProjectionDrift},
		{"ready reason", state, func(s *ProjectionState) { s.Reason = knowl.RetrievalUnavailable }, knowl.RetrievalProjectionDrift},
		{"ready omission", state, func(s *ProjectionState) { s.OmittedChunks = 1 }, knowl.RetrievalProjectionDrift},
		{"degraded reason", degraded, func(s *ProjectionState) { s.Reason = "" }, knowl.RetrievalProjectionDrift},
		{"degraded coverage", degraded, func(s *ProjectionState) { s.Coverage = "[]" }, knowl.RetrievalProjectionDrift},
		{"degraded count", degraded, func(s *ProjectionState) { s.ChunkCount = 1 }, knowl.RetrievalProjectionDrift},
		{"negative omission", degraded, func(s *ProjectionState) { s.OmittedRunes = -1 }, knowl.RetrievalProjectionDrift},
		{"null coverage", state, func(s *ProjectionState) { s.Coverage = "null" }, knowl.RetrievalProjectionDrift},
		{"oversized coverage", state, func(s *ProjectionState) { s.Coverage = strings.Repeat(" ", MaxCoverageBytes+1) }, knowl.RetrievalProjectionDrift},
		{"count mismatch", state, func(s *ProjectionState) { s.ChunkCount = 0 }, knowl.RetrievalProjectionDrift},
		{"duplicate page", state, func(s *ProjectionState) { s.Coverage, _ = EncodeCoverage([]PageCoverage{pages[1], pages[1]}) }, knowl.RetrievalProjectionDrift},
		{"negative coverage count", state, func(s *ProjectionState) {
			s.Coverage, _ = EncodeCoverage([]PageCoverage{{PageID: testMetadataPageID, PageDigest: testPageDigest, Chunks: -1}})
		}, knowl.RetrievalProjectionDrift},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.base
			tc.change(&s)
			_, err := ValidateProjectionMetadata(t.Context(), s)
			var classified *app.EmbeddingError
			if !errors.As(err, &classified) || classified.Code != tc.want {
				t.Fatalf("validation=%v want=%s", err, tc.want)
			}
		})
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ValidateProjectionMetadata(canceled, state); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	// Metadata validity must never authorize publication with missing or corrupt vectors.
	for _, vector := range [][]float32{nil, {0}, {float32(math.NaN())}, {float32(math.Inf(1))}, {1, 0}} {
		chunk := Chunk{PageID: testMetadataPageID, PageDigest: testPageDigest, ContentHash: strings.Repeat("c", 64), Vector: vector}
		var classified *app.EmbeddingError
		if err := ValidateProjection(t.Context(), state, []Chunk{chunk}); !errors.As(err, &classified) {
			t.Fatalf("invalid publication vector accepted: %v", err)
		}
	}
}

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
