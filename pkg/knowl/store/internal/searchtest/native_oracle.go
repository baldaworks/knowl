package searchtest

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// NativeScore is one native scalar result, before any selection or aggregation.
type NativeScore struct {
	PageID  knowl.PageID
	Ordinal int
	Score   float64
}

// NativeWinners independently selects page maxima from the exhaustive scalar
// stream. This test utility does not call production ranking or grouping code.
func NativeWinners(scores []NativeScore, limit int) []NativeScore {
	pages := make(map[knowl.PageID]NativeScore)
	for _, score := range scores {
		previous, exists := pages[score.PageID]
		if !exists || score.Score > previous.Score || (score.Score == previous.Score && score.Ordinal < previous.Ordinal) {
			pages[score.PageID] = score
		}
	}
	winners := make([]NativeScore, 0, len(pages))
	for _, score := range pages {
		winners = append(winners, score)
	}
	slices.SortFunc(winners, func(a, b NativeScore) int {
		if a.Score > b.Score {
			return -1
		}
		if a.Score < b.Score {
			return 1
		}
		return strings.Compare(string(a.PageID), string(b.PageID))
	})
	return winners[:min(limit, len(winners))]
}

// CheckNativeCosine compares native arithmetic with true cosine calculated
// independently over the original float32 fixture values in float64.
func CheckNativeCosine(t *testing.T, vector, query []float32, score float64) float64 {
	t.Helper()
	var dot, vectorNorm, queryNorm float64
	for i, v := range vector {
		x, y := float64(v), float64(query[i])
		dot += x * y
		vectorNorm += x * x
		queryNorm += y * y
	}
	want := dot / (math.Sqrt(vectorNorm) * math.Sqrt(queryNorm))
	u := math.Ldexp(1, -24)
	n := float64(len(vector) + 2)
	epsilon := 8*(n*u/(1-n*u)) + 8*u
	delta := math.Abs(score - want)
	if math.IsNaN(score) || math.IsInf(score, 0) || delta > epsilon {
		t.Fatalf("native cosine dimensions=%d native=%.17g true=%.17g error=%g bound=%g", len(vector), score, want, delta, epsilon)
	}
	return delta
}

// NativeOraclePages creates a crowded page and bytewise-sensitive page IDs.
func NativeOraclePages(reverse bool) []knowl.PageSnapshot {
	pages := []knowl.PageSnapshot{
		{ID: "crowd", Body: strings.Repeat("crowded evidence ", 800)},
		{ID: "A", Body: "Original A evidence"},
		{ID: "B", Body: "Original B evidence"},
		{ID: "Z", Body: "Original Z evidence"},
		{ID: "a", Body: "Original a evidence"},
		{ID: "ä", Body: "Original umlaut evidence"},
	}
	for i := range 24 {
		pages = append(pages, knowl.PageSnapshot{ID: knowl.PageID(fmt.Sprintf("o%02d", i)), Body: "Independent evidence"})
	}
	for i := range pages {
		pages[i].Path = "wiki/" + string(pages[i].ID) + ".md"
		pages[i].Digest = string(pages[i].ID)
	}
	if reverse {
		slices.Reverse(pages)
	}
	return pages
}

// NativeOracleVector supplies hand-separated scores, exact ties, negative
// scores, close unequal values and an ordinal won only by the fourth window.
func NativeOracleVector(id knowl.PageID, ordinal int) []float32 {
	switch id {
	case "A":
		return []float32{1, .01}
	case "B":
		return []float32{.9998, 0}
	case "crowd":
		if ordinal == 1 {
			return []float32{0, 1}
		}
		return []float32{.8, .6}
	case "ä":
		return []float32{-.599999, .80000075}
	case "Z", "a":
		return []float32{-.6, .8}
	default:
		return []float32{.6, .8}
	}
}

// NativeOracleProvider excludes inference from arithmetic fixtures and returns
// independent copies of already prepared float32 vectors.
type NativeOracleProvider struct{ Vector []float32 }

// Embed implements the fixed-vector test provider.
func (p NativeOracleProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = slices.Clone(p.Vector)
	}
	return vectors, nil
}

// NativeRoundtripVector deterministically exercises nontrivial float32 values.
func NativeRoundtripVector(dimensions int) []float32 {
	vector := make([]float32, dimensions)
	var norm float64
	for i := range vector {
		vector[i] = float32(math.Sin(float64(i+1) * .731))
		norm += float64(vector[i]) * float64(vector[i])
	}
	for i := range vector {
		vector[i] = float32(float64(vector[i]) / math.Sqrt(norm))
	}
	return vector
}
