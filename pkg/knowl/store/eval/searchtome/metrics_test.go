package searchtome

import (
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"math"
	"testing"
)

func observation(domain, id string, rank int, mode knowl.RetrievalMode, latency int64) Observation {
	return Observation{Domain: domain, ID: id, Gold: testGold, Rank: rank, LatencyNS: latency, Report: knowl.RetrievalReport{Requested: mode, Effective: mode}}
}
func closeFloat(a, b float64) bool { return math.Abs(a-b) < 1e-12 }
func TestMetricsRanksAndPercentiles(t *testing.T) {
	items := []Observation{observation("a", "1", 1, knowl.RetrievalLexical, 10), observation("a", "2", 2, knowl.RetrievalLexical, 20), observation("a", "3", 3, knowl.RetrievalLexical, 30), observation("a", "4", 0, knowl.RetrievalLexical, 40)}
	m := Summarize(items, knowl.RetrievalLexical)
	want := (1 + 1/math.Log2(3) + .5) / 4
	if m.Eligible != 4 || *m.Recall1 != .25 || *m.Recall3 != .75 || !closeFloat(*m.NDCG3, want) || *m.P50NS != 20 || *m.P95NS != 40 {
		t.Fatalf("metrics: %+v", m)
	}
}
func TestMacroMicroAndFailures(t *testing.T) {
	items := []Observation{observation("a", "1", 1, knowl.RetrievalHybrid, 10), observation("b", "1", 0, knowl.RetrievalHybrid, 20), observation("b", "2", 0, knowl.RetrievalHybrid, 30)}
	degraded := observation("b", "3", 1, knowl.RetrievalHybrid, 40)
	degraded.Report.Effective = knowl.RetrievalDegraded
	degraded.Exclusion = "degraded"
	items = append(items, degraded)
	failed := observation("b", "4", 0, knowl.RetrievalHybrid, 50)
	failed.Report.Effective = knowl.RetrievalFailed
	failed.Error = retrievalError
	items = append(items, failed)
	result := AggregateMetrics([]string{"a", "b"}, items, knowl.RetrievalHybrid)
	if *result.Macro.Recall1 != .5 || !closeFloat(*result.Micro.Recall1, 1.0/3) || result.Micro.Eligible != 3 || result.Micro.Degraded != 1 || result.Micro.Failed != 1 || result.Micro.Errors != 1 {
		t.Fatalf("aggregate: %+v", result)
	}
	if AggregateMetrics([]string{"a", "missing"}, items, knowl.RetrievalHybrid).Macro.Recall1 != nil {
		t.Fatal("undefined macro published")
	}
}
func TestPairedComparison(t *testing.T) {
	l := []Observation{observation("a", "1", 1, knowl.RetrievalLexical, 1), observation("a", "2", 0, knowl.RetrievalLexical, 1)}
	h := []Observation{observation("a", "1", 0, knowl.RetrievalHybrid, 1), observation("a", "2", 1, knowl.RetrievalHybrid, 1)}
	h[1].Report.Effective = knowl.RetrievalDegraded
	pair := ComparePaired(l, h)
	if pair.Count != 1 || pair.Recall1 == nil || pair.Recall1.Absolute != -1 || pair.Recall1.Relative == nil || *pair.Recall1.Relative != -1 {
		t.Fatalf("paired: %+v", pair)
	}
	zero := ComparePaired(l[1:], []Observation{observation("a", "2", 1, knowl.RetrievalHybrid, 1)})
	if zero.Recall1 == nil || zero.Recall1.Relative != nil || zero.Recall1.Absolute != 1 {
		t.Fatalf("zero baseline: %+v", zero)
	}
}
