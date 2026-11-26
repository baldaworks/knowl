package searchtome

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type Environment struct {
	Commit               string             `json:"commit"`
	TreeState            string             `json:"tree_state"`
	GoVersion            string             `json:"go_version"`
	OS                   string             `json:"os"`
	Arch                 string             `json:"arch"`
	CPU                  string             `json:"cpu"`
	Backend              string             `json:"backend"`
	Model                app.EmbeddingSpace `json:"model"`
	TEI                  TEIInfo            `json:"tei"`
	FailurePolicy        string             `json:"failure_policy"`
	K                    int                `json:"k"`
	EmbeddingBatchInputs int                `json:"embedding_batch_inputs"`
	Characters           int                `json:"characters"`
	SourceFilter         string             `json:"source_filter"`
	QueryLimit           int                `json:"query_limit"`
	RunKind              string             `json:"run_kind"`
	Warmup               string             `json:"warmup"`
	Commands             []string           `json:"commands"`
}
type PairExclusion struct {
	Domain string `json:"domain"`
	ID     string `json:"query_id"`
	Reason string `json:"reason"`
}
type Report struct {
	Environment    Environment                      `json:"environment"`
	Dataset        Dataset                          `json:"dataset"`
	Projections    map[string]map[string]Projection `json:"projections"`
	Lexical        []Observation                    `json:"lexical"`
	Hybrid         []Observation                    `json:"hybrid"`
	LexicalMetrics Aggregate                        `json:"lexical_metrics"`
	HybridMetrics  Aggregate                        `json:"hybrid_metrics"`
	Paired         Paired                           `json:"paired"`
	PairExclusions []PairExclusion                  `json:"pair_exclusions"`
	Limitations    []string                         `json:"limitations"`
}

type pairKey struct{ domain, id string }

func PairExclusions(left, right []Observation) []PairExclusion {
	type group struct{ l, r []Observation }
	groups := map[pairKey]*group{}
	for _, o := range left {
		k := pairKey{o.Domain, o.ID}
		if groups[k] == nil {
			groups[k] = &group{}
		}
		groups[k].l = append(groups[k].l, o)
	}
	for _, o := range right {
		k := pairKey{o.Domain, o.ID}
		if groups[k] == nil {
			groups[k] = &group{}
		}
		groups[k].r = append(groups[k].r, o)
	}
	keys := make([]pairKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].domain != keys[j].domain {
			return keys[i].domain < keys[j].domain
		}
		return keys[i].id < keys[j].id
	})
	var exclusions []PairExclusion
	for _, k := range keys {
		g := groups[k]
		reason := ""
		switch {
		case len(g.l) > 1 || len(g.r) > 1:
			reason = exDuplicate
		case len(g.l) == 0 || len(g.r) == 0:
			reason = "missing_pair"
		case g.l[0].Gold != g.r[0].Gold:
			reason = "gold_mismatch"
		case g.l[0].Exclusion != "":
			reason = "lexical:" + g.l[0].Exclusion
		case g.r[0].Exclusion != "":
			reason = "hybrid:" + g.r[0].Exclusion
		case g.l[0].Error != "":
			reason = "lexical:error"
		case g.r[0].Error != "":
			reason = "hybrid:error"
		case g.l[0].Report.Requested != knowl.RetrievalLexical || g.l[0].Report.Effective != knowl.RetrievalLexical:
			reason = "lexical:unexpected_mode"
		case g.r[0].Report.Requested != knowl.RetrievalHybrid || g.r[0].Report.Effective != knowl.RetrievalHybrid:
			reason = "hybrid:unexpected_mode"
		}
		if reason != "" {
			exclusions = append(exclusions, PairExclusion{Domain: k.domain, ID: k.id, Reason: reason})
		}
	}
	return exclusions
}
func NewReport(env Environment, dataset Dataset, projections map[string]map[string]Projection, lexical, hybrid []Observation) Report {
	return Report{Environment: env, Dataset: dataset, Projections: projections, Lexical: lexical, Hybrid: hybrid, LexicalMetrics: AggregateMetrics(Domains, lexical, knowl.RetrievalLexical), HybridMetrics: AggregateMetrics(Domains, hybrid, knowl.RetrievalHybrid), Paired: ComparePaired(lexical, hybrid), PairExclusions: PairExclusions(lexical, hybrid), Limitations: []string{"STAIR 82.6% Recall@1 is not directly comparable: corpus and protocol may differ.", "The embedding endpoint metadata is operator supplied; the API cannot cryptographically attest model weights.", "Latency uses nearest-rank p50 and p95; macro latency averages domain percentiles.", "No warm-up; retrieval metrics may vary with backend and inference implementation."}}
}
func number(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.4f", *v)
}
func milliseconds(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.3f ms", *v/1e6)
}
func (r Report) Markdown() []byte {
	var b strings.Builder
	b.WriteString("# SearchTome benchmark\n\n| Domain | Docs | Test queries | Mode | R@1 | R@3 | nDCG@3 | p50 | p95 | Degraded | Failed |\n| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, domain := range Domains {
		for _, mode := range []string{modeLexical, modeHybrid} {
			agg := r.LexicalMetrics
			if mode == modeHybrid {
				agg = r.HybridMetrics
			}
			m := agg.Domains[domain]
			docs := r.Projections[domain][mode].Pages
			fmt.Fprintf(&b, "| %s | %d | %d | %s | %s | %s | %s | %s | %s | %d | %d |\n", domain, docs, m.Total, mode, number(m.Recall1), number(m.Recall3), number(m.NDCG3), milliseconds(m.P50NS), milliseconds(m.P95NS), m.Degraded, m.Failed)
		}
	}
	for _, name := range []string{"macro", "micro"} {
		for _, mode := range []string{modeLexical, modeHybrid} {
			agg := r.LexicalMetrics
			if mode == modeHybrid {
				agg = r.HybridMetrics
			}
			m := agg.Macro
			if name == "micro" {
				m = agg.Micro
			}
			fmt.Fprintf(&b, "| %s | — | %d | %s | %s | %s | %s | %s | %s | %d | %d |\n", name, m.Total, mode, number(m.Recall1), number(m.Recall3), number(m.NDCG3), milliseconds(m.P50NS), milliseconds(m.P95NS), m.Degraded, m.Failed)
		}
	}
	b.WriteString("\nProcessing counts (processed, eligible, errors):\n\n")
	for _, domain := range Domains {
		for _, mode := range []string{modeLexical, modeHybrid} {
			agg := r.LexicalMetrics
			if mode == modeHybrid {
				agg = r.HybridMetrics
			}
			m := agg.Domains[domain]
			fmt.Fprintf(&b, "- %s / %s: %d processed, %d eligible, %d errors\n", domain, mode, m.Processed, m.Eligible, m.Errors)
		}
	}
	fmt.Fprintf(&b, "\nPaired successful queries: %d.\n\n", r.Paired.Count)
	for _, x := range []struct {
		name string
		d    *Difference
	}{{"Recall@1", r.Paired.Recall1}, {"Recall@3", r.Paired.Recall3}, {"nDCG@3", r.Paired.NDCG3}} {
		if x.d == nil {
			fmt.Fprintf(&b, "- %s difference: undefined\n", x.name)
		} else {
			fmt.Fprintf(&b, "- %s difference (hybrid − lexical): %.4f; relative: %s\n", x.name, x.d.Absolute, number(x.d.Relative))
		}
	}
	b.WriteString("\nIndividual exclusions:\n\n")
	for _, set := range []struct {
		name  string
		items []Observation
	}{{modeLexical, r.Lexical}, {modeHybrid, r.Hybrid}} {
		for _, o := range set.items {
			if o.Exclusion != "" || o.Error != "" {
				fmt.Fprintf(&b, "- %s / %s / %s: %s %s\n", o.Domain, o.ID, set.name, o.Exclusion, o.Error)
			}
		}
	}
	b.WriteString("\nExcluded pairs:\n\n")
	if len(r.PairExclusions) == 0 {
		b.WriteString("None.\n")
	} else {
		for _, e := range r.PairExclusions {
			fmt.Fprintf(&b, "- %s / %s: %s\n", e.Domain, e.ID, e.Reason)
		}
	}
	fmt.Fprintf(&b, "\nCommit: `%s`; tree: %s; Go: %s; platform: %s/%s; CPU: %s; backend: %s.\n\n", r.Environment.Commit, r.Environment.TreeState, r.Environment.GoVersion, r.Environment.OS, r.Environment.Arch, r.Environment.CPU, r.Environment.Backend)
	fmt.Fprintf(&b, "Dataset revision: `%s`. Model: `%s` revision `%s`, dimensions %d, query prefix `%s`, passage prefix `%s`, policy `%s`; k=%d; warm-up: %s.\n\n", r.Dataset.Revision, r.Environment.Model.Model, r.Environment.Model.Revision, r.Environment.Model.Dimensions, r.Environment.Model.QueryPrefix, r.Environment.Model.PassagePrefix, r.Environment.FailurePolicy, r.Environment.K, r.Environment.Warmup)
	fmt.Fprintf(&b, "Embedding request batch size: %d.\n\n", r.Environment.EmbeddingBatchInputs)
	fmt.Fprintf(&b, "Run kind: %s; query limit per domain: %d; characters: %d; source filter: %s.\n\n", r.Environment.RunKind, r.Environment.QueryLimit, r.Environment.Characters, r.Environment.SourceFilter)
	for _, command := range r.Environment.Commands {
		fmt.Fprintf(&b, "Command: `%s`\n\n", command)
	}
	b.WriteString("Dataset files (configuration, split, count, SHA-256):\n\n")
	for _, f := range r.Dataset.Files {
		fmt.Fprintf(&b, "- %s / %s: %d, `%s`\n", f.Configuration, f.Split, f.Count, f.SHA256)
	}
	b.WriteString("\nProjection build (pages, chunks, nanoseconds):\n\n")
	for _, domain := range Domains {
		for _, mode := range []string{modeLexical, modeHybrid} {
			p := r.Projections[domain][mode]
			chunks := "unknown"
			if p.Chunks != nil {
				chunks = fmt.Sprint(*p.Chunks)
			}
			fmt.Fprintf(&b, "- %s / %s: %d pages, %s chunks, %d ns\n", domain, mode, p.Pages, chunks, p.BuildNS)
		}
	}
	b.WriteString("\nLimitations:\n\n")
	for _, x := range r.Limitations {
		fmt.Fprintf(&b, "- %s\n", x)
	}
	return []byte(b.String())
}
func hashHex(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func atomicFile(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, ".partial-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// ValidateComplete prevents a partial run from receiving a completion marker.
func (r Report) ValidateComplete() error {
	e := r.Environment
	if e.Commit == "" || e.TreeState == "" || e.GoVersion == "" || e.OS == "" || e.Arch == "" || e.CPU == "" || e.Backend != "sqlite" || !validServerInfo(e.TEI, e.Model) || e.FailurePolicy != modeLexical || e.K != 3 || e.EmbeddingBatchInputs != 4 || e.Characters != 1024 || e.SourceFilter != "all" || e.Warmup != "none" || len(e.Commands) == 0 || (e.RunKind != runFull && e.RunKind != "smoke") || (e.RunKind == runFull && e.QueryLimit != 0) || (e.RunKind == "smoke" && e.QueryLimit <= 0) {
		return fmt.Errorf("incomplete environment")
	}
	if r.Dataset.Revision != Revision || len(r.Dataset.Files) != 24 || len(r.Projections) != len(Domains) {
		return fmt.Errorf("incomplete dataset or projections")
	}
	for _, domain := range Domains {
		corpus, ok := r.Dataset.Corpus[domain]
		if !ok || len(corpus) == 0 {
			return fmt.Errorf("missing corpus")
		}
		queries, ok := r.Dataset.Queries[domain]
		if !ok || len(queries) == 0 {
			return fmt.Errorf("missing queries")
		}
		for _, mode := range []string{modeLexical, modeHybrid} {
			p, ok := r.Projections[domain][mode]
			if !ok || p.Pages != len(corpus) {
				return fmt.Errorf("incomplete projection")
			}
		}
		for modeIndex, list := range [][]Observation{r.Lexical, r.Hybrid} {
			expectedMode := knowl.RetrievalLexical
			if modeIndex == 1 {
				expectedMode = knowl.RetrievalHybrid
			}
			i := 0
			for _, o := range list {
				if o.Domain != domain {
					continue
				}
				if i >= len(queries) || o.ID != queries[i].ID || o.Gold != queries[i].Gold {
					return fmt.Errorf("query identity mismatch")
				}
				if o.Exclusion == "" && (o.Error != "" || o.Report.Requested != expectedMode || o.Report.Effective != expectedMode) {
					return fmt.Errorf("nonterminal observation")
				}
				if o.Error != "" && o.Error != retrievalError && o.Error != invalidQuery {
					return fmt.Errorf("unsafe error")
				}
				if len(o.Pages) > 3 || o.Rank < 0 || o.Rank > 3 {
					return fmt.Errorf("invalid result")
				}
				rank := 0
				for n, page := range o.Pages {
					if page.Title == o.Gold {
						rank = n + 1
					}
				}
				if rank != o.Rank {
					return fmt.Errorf("rank mismatch")
				}
				i++
			}
			if i != len(queries) {
				return fmt.Errorf("incomplete query outcomes")
			}
		}
	}
	return nil
}

// WriteReport writes the completion marker last, after both report hashes exist.
func WriteReport(dir string, r Report) error { return writeReport(dir, r, atomicFile) }

func writeReport(dir string, r Report, write func(string, string, []byte) error) error {
	if err := r.ValidateComplete(); err != nil {
		return err
	}
	for _, name := range []string{reportJSON, reportMarkdown, "complete.json"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("report destination exists or unavailable")
		}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	md := r.Markdown()
	if err = write(dir, reportJSON, data); err != nil {
		return err
	}
	if err = write(dir, reportMarkdown, md); err != nil {
		return err
	}
	marker, err := json.MarshalIndent(map[string]string{reportJSON: hashHex(data), reportMarkdown: hashHex(md)}, "", "  ")
	if err != nil {
		return err
	}
	return write(dir, "complete.json", append(marker, '\n'))
}
