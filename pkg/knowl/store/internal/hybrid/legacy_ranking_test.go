package hybrid

import (
	"context"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"sort"
)

// Historical ranking exists only in this explicit test baseline. Production
// adapters use exhaustive native database cosine/page winners instead.
// Rank retains the historical clamped near-unit dot baseline for tests. Rows
// must already be complete/compatible and scope/source-filtered by the adapter.
func Rank(ctx context.Context, queries [][]float32, chunks []Chunk, limit int) ([]knowl.PageID, error) {
	ranked, err := RankWithEvidence(ctx, queries, chunks, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]knowl.PageID, len(ranked))
	for i, match := range ranked {
		ids[i] = match.PageID
	}
	return ids, nil
}

// RankedPage retains the winning chunk while ranking and fusion stay page based.
type RankedPage struct {
	PageID knowl.PageID
	Chunk  Chunk
}

func RankWithEvidence(ctx context.Context, queries [][]float32, chunks []Chunk, limit int) ([]RankedPage, error) {
	scores := make(map[knowl.PageID]float64)
	winners := make(map[knowl.PageID]Chunk)
	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, query := range queries {
			score, err := Cosine(query, chunk.Vector)
			if err != nil {
				return nil, err
			}
			previous, seen := scores[chunk.PageID]
			if !seen || score > previous || (score == previous && chunk.Ordinal < winners[chunk.PageID].Ordinal) {
				scores[chunk.PageID] = score
				winners[chunk.PageID] = chunk
			}
		}
	}
	ids := make([]knowl.PageID, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] != scores[ids[j]] {
			return scores[ids[i]] > scores[ids[j]]
		}
		return ids[i] < ids[j]
	})
	ranked := make([]RankedPage, min(limit, len(ids)))
	for i, id := range ids[:len(ranked)] {
		ranked[i] = RankedPage{PageID: id, Chunk: winners[id]}
	}
	return ranked, nil
}

// Cosine is the legacy validated near-unit dot baseline, not native true cosine.
func Cosine(left, right []float32) (float64, error) {
	if err := validateVector(left, len(left)); err != nil {
		return 0, err
	}
	if err := validateVector(right, len(left)); err != nil {
		return 0, err
	}
	dot := 0.0
	for i, v := range left {
		dot += float64(v) * float64(right[i])
	}
	return max(-1, min(1, dot)), nil
}
