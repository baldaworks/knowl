package searchtome

import (
	"math"
	"sort"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// Metrics uses eligible queries as the denominator. Nil values are undefined.
type Metrics struct {
	Total     int      `json:"total"`
	Processed int      `json:"processed"`
	Eligible  int      `json:"eligible"`
	Errors    int      `json:"errors"`
	Degraded  int      `json:"degraded"`
	Failed    int      `json:"failed"`
	Recall1   *float64 `json:"recall_at_1"`
	Recall3   *float64 `json:"recall_at_3"`
	NDCG3     *float64 `json:"ndcg_at_3"`
	P50NS     *float64 `json:"p50_ns"`
	P95NS     *float64 `json:"p95_ns"`
}
type Aggregate struct {
	Domains map[string]Metrics `json:"domains"`
	Macro   Metrics            `json:"macro"`
	Micro   Metrics            `json:"micro"`
}
type Difference struct {
	Absolute float64  `json:"absolute"`
	Relative *float64 `json:"relative"`
}
type Paired struct {
	Count   int         `json:"count"`
	Lexical Metrics     `json:"lexical"`
	Hybrid  Metrics     `json:"hybrid"`
	Recall1 *Difference `json:"recall_at_1"`
	Recall3 *Difference `json:"recall_at_3"`
	NDCG3   *Difference `json:"ndcg_at_3"`
}

func ratio(v float64) *float64 { return &v }
func percentile(sorted []int64, p float64) *float64 {
	if len(sorted) == 0 {
		return nil
	}
	n := int(math.Ceil(p*float64(len(sorted)))) - 1
	return ratio(float64(sorted[n]))
}

// Summarize uses nearest-rank percentiles and excludes every non-successful mode.
func Summarize(items []Observation, mode knowl.RetrievalMode) Metrics {
	m := Metrics{Total: len(items)}
	var r1, r3, ndcg float64
	var latencies []int64
	for _, o := range items {
		if o.Report.Requested != "" {
			m.Processed++
		}
		if o.Error != "" {
			m.Errors++
		}
		if o.Report.Effective == knowl.RetrievalDegraded {
			m.Degraded++
		}
		if o.Report.Effective == knowl.RetrievalFailed {
			m.Failed++
		}
		if o.Exclusion != "" || o.Error != "" || o.Report.Requested != mode || o.Report.Effective != mode {
			continue
		}
		m.Eligible++
		latencies = append(latencies, o.LatencyNS)
		if o.Rank == 1 {
			r1++
		}
		if o.Rank >= 1 && o.Rank <= 3 {
			r3++
			ndcg += 1 / math.Log2(float64(o.Rank+1))
		}
	}
	if m.Eligible > 0 {
		n := float64(m.Eligible)
		m.Recall1 = ratio(r1 / n)
		m.Recall3 = ratio(r3 / n)
		m.NDCG3 = ratio(ndcg / n)
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		m.P50NS = percentile(latencies, .5)
		m.P95NS = percentile(latencies, .95)
	}
	return m
}

// AggregateMetrics keeps equal-domain macro and query-weighted micro distinct.
func AggregateMetrics(domains []string, items []Observation, mode knowl.RetrievalMode) Aggregate {
	result := Aggregate{Domains: make(map[string]Metrics, len(domains))}
	groups := map[string][]Observation{}
	for _, o := range items {
		groups[o.Domain] = append(groups[o.Domain], o)
	}
	var included []Observation
	for _, domain := range domains {
		included = append(included, groups[domain]...)
	}
	result.Micro = Summarize(included, mode)
	var r1, r3, ndcg, p50, p95 float64
	defined := len(domains) > 0
	for _, domain := range domains {
		m := Summarize(groups[domain], mode)
		result.Domains[domain] = m
		result.Macro.Total += m.Total
		result.Macro.Processed += m.Processed
		result.Macro.Eligible += m.Eligible
		result.Macro.Errors += m.Errors
		result.Macro.Degraded += m.Degraded
		result.Macro.Failed += m.Failed
		if m.Recall1 == nil {
			defined = false
			continue
		}
		r1 += *m.Recall1
		r3 += *m.Recall3
		ndcg += *m.NDCG3
		p50 += *m.P50NS
		p95 += *m.P95NS
	}
	if defined {
		n := float64(len(domains))
		result.Macro.Recall1 = ratio(r1 / n)
		result.Macro.Recall3 = ratio(r3 / n)
		result.Macro.NDCG3 = ratio(ndcg / n)
		result.Macro.P50NS = ratio(p50 / n)
		result.Macro.P95NS = ratio(p95 / n)
	}
	return result
}
func diff(a, b *float64) *Difference {
	if a == nil || b == nil {
		return nil
	}
	d := &Difference{Absolute: *b - *a}
	if *a != 0 {
		d.Relative = ratio(d.Absolute / *a)
	}
	return d
}

// ComparePaired only includes a unique successful pair with identical gold.
func ComparePaired(lexical, hybrid []Observation) Paired {
	type key struct{ domain, id string }
	type entry struct {
		o Observation
		n int
	}
	left := map[key]entry{}
	right := map[key]entry{}
	for _, o := range lexical {
		k := key{o.Domain, o.ID}
		v := left[k]
		v.o = o
		v.n++
		left[k] = v
	}
	for _, o := range hybrid {
		k := key{o.Domain, o.ID}
		v := right[k]
		v.o = o
		v.n++
		right[k] = v
	}
	var a, b []Observation
	keys := make([]key, 0, len(left))
	for k := range left {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].domain != keys[j].domain {
			return keys[i].domain < keys[j].domain
		}
		return keys[i].id < keys[j].id
	})
	for _, k := range keys {
		v := left[k]
		w, ok := right[k]
		if !ok || v.n != 1 || w.n != 1 || v.o.Gold != w.o.Gold {
			continue
		}
		l, h := v.o, w.o
		if l.Exclusion != "" || h.Exclusion != "" || l.Error != "" || h.Error != "" || l.Report.Requested != knowl.RetrievalLexical || l.Report.Effective != knowl.RetrievalLexical || h.Report.Requested != knowl.RetrievalHybrid || h.Report.Effective != knowl.RetrievalHybrid {
			continue
		}
		a = append(a, l)
		b = append(b, h)
	}
	lm, hm := Summarize(a, knowl.RetrievalLexical), Summarize(b, knowl.RetrievalHybrid)
	return Paired{Count: len(a), Lexical: lm, Hybrid: hm, Recall1: diff(lm.Recall1, hm.Recall1), Recall3: diff(lm.Recall3, hm.Recall3), NDCG3: diff(lm.NDCG3, hm.NDCG3)}
}
