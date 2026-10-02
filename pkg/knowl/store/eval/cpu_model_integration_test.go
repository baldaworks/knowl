//go:build integration

package eval_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/internal/knowledgetest"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/contextpolicy"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	"github.com/baldaworks/knowl/pkg/knowl/store/postgres"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	referenceModel    = "intfloat/multilingual-e5-small"
	referenceRevision = "614241f622f53c4eeff9890bdc4f31cfecc418b3"
)

type qualityIndex interface {
	app.SearchIndex
	app.ReportedSearchIndex
	app.OperationStore
	io.Closer
}

type measuredProvider struct {
	app.EmbeddingProvider
	mu        sync.Mutex
	vectors   map[string][]float32
	durations []float64
}

func (p *measuredProvider) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	start := time.Now()
	vectors, err := p.EmbeddingProvider.Embed(ctx, inputs)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.durations = append(p.durations, float64(time.Since(start).Microseconds())/1000)
	if err == nil {
		for i, input := range inputs {
			p.vectors[input] = slices.Clone(vectors[i])
		}
	}
	return vectors, err
}
func (p *measuredProvider) score(t *testing.T, query string, page knowl.PageSnapshot, space app.EmbeddingSpace) float64 {
	t.Helper()
	q, err := hybrid.PrepareQuery(t.Context(), query, space)
	if err != nil {
		t.Fatal(err)
	}
	passage, err := hybrid.PreparePage(t.Context(), page, space)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	score := -math.MaxFloat64
	for _, left := range q.Inputs {
		for _, right := range passage.Inputs {
			if len(p.vectors[left]) == 0 || len(p.vectors[right]) == 0 {
				t.Fatal("missing actual inference measurement")
			}
			value, err := hybrid.Cosine(p.vectors[left], p.vectors[right])
			if err != nil {
				t.Fatal(err)
			}
			score = max(score, value)
		}
	}
	return score
}

type caseMeasurement struct {
	Case                                string         `json:"case"`
	Expected                            knowl.PageID   `json:"expected"`
	Lexical                             []knowl.PageID `json:"lexical"`
	Hybrid                              []knowl.PageID `json:"hybrid"`
	DirectSeeds                         []knowl.PageID `json:"direct_seeds"`
	LexicalRank, HybridRank, SourceRank int
	ExpectedCosine                      float64               `json:"expected_cosine"`
	DistractorCosine                    *float64              `json:"distractor_cosine,omitempty"`
	RelevantFullRank                    int                   `json:"relevant_full_rank,omitempty"`
	DistractorFullRank                  int                   `json:"distractor_full_rank,omitempty"`
	QueryMS                             float64               `json:"query_ms"`
	Report                              knowl.RetrievalReport `json:"report"`
}
type runMeasurement struct {
	Backend   string                `json:"backend"`
	Pass      int                   `json:"pass"`
	RebuildMS float64               `json:"rebuild_ms"`
	Cases     []caseMeasurement     `json:"cases"`
	Golden    knowledgetest.Metrics `json:"golden"`
}
type modelInfo struct {
	Model        string `json:"model_id"`
	Revision     string `json:"model_sha"`
	Version      string `json:"version"`
	DType        string `json:"model_dtype"`
	AutoTruncate *bool  `json:"auto_truncate"`
}

func TestRealCPUModelRecallBothStores(t *testing.T) {
	endpoint := os.Getenv("KNOWL_EMBEDDING_EVAL_ENDPOINT")
	if endpoint == "" {
		t.Skip("explicit CPU embedding endpoint required; skip is not model-quality evidence")
	}
	dsn := os.Getenv("KNOWL_EMBEDDING_EVAL_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("explicit PostgreSQL fixture DSN required for the two-backend quality gate")
	}
	info := referenceInfo(t, endpoint)
	space := app.EmbeddingSpace{Model: referenceModel, Revision: referenceRevision, Dimensions: 384, QueryPrefix: "query: ", PassagePrefix: "passage: "}
	client, err := provider.NewEmbeddingClient(provider.EmbeddingClientOptions{Endpoint: endpoint, Space: space})
	if err != nil {
		t.Fatal(err)
	}
	measured := &measuredProvider{EmbeddingProvider: client, vectors: map[string][]float32{}}
	options := app.EmbeddingOptions{Provider: measured, Space: space, FailurePolicy: app.EmbeddingStrict}
	type factory func(*testing.T, ...app.EmbeddingOptions) qualityIndex
	factories := []struct {
		name string
		open factory
	}{
		{"sqlite", func(t *testing.T, options ...app.EmbeddingOptions) qualityIndex {
			t.Helper()
			s, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "quality.sqlite"), options...)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}},
		{"postgres", func(t *testing.T, options ...app.EmbeddingOptions) qualityIndex {
			t.Helper()
			s, err := postgres.Open(t.Context(), dsn, options...)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}},
	}
	var runs []runMeasurement
	for _, backend := range factories {
		t.Run(backend.name, func(t *testing.T) {
			index := backend.open(t, options)
			defer func() { _ = index.Close() }()
			lexical := backend.open(t)
			defer func() { _ = lexical.Close() }()
			// Separate scopes keep enabled PostgreSQL and lexical measurements independent.
			snapshot := qualitySnapshot()
			lexSnapshot := qualitySnapshot()
			lexSnapshot.Scope += "-lexical"
			if err := lexical.Rebuild(t.Context(), lexSnapshot); err != nil {
				t.Fatal(err)
			}
			workspace, err := contentfs.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := workspace.Init(); err != nil {
				t.Fatal(err)
			}
			before, err := workspace.Snapshot(t.Context(), evalScope)
			if err != nil {
				t.Fatal(err)
			}
			service, err := app.NewQueryService(workspace, index, index, nil, app.QueryOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var previous []caseMeasurement
			for pass := range 2 {
				start := time.Now()
				if err := index.Rebuild(t.Context(), snapshot); err != nil {
					t.Fatal(err)
				}
				run := runMeasurement{Backend: backend.name, Pass: pass + 1, RebuildMS: float64(time.Since(start).Microseconds()) / 1000}
				for _, fixture := range qualityCases() {
					original := snapshot.Pages[slices.IndexFunc(snapshot.Pages, func(p knowl.PageSnapshot) bool { return p.ID == fixture.Expected })]
					base, err := lexical.Search(t.Context(), lexSnapshot.Scope, fixture.Query, knowl.ReadLimits{Pages: 5, Characters: 1024}, nil)
					if err != nil {
						t.Fatal(err)
					}
					start := time.Now()
					result, err := service.Query(t.Context(), snapshot.Scope, fixture.Query, knowl.ReadLimits{Pages: 5, Characters: 1024}, nil)
					if err != nil {
						t.Fatalf("case %s: %v", fixture.ID, err)
					}
					latency := float64(time.Since(start).Microseconds()) / 1000
					if result.Retrieval == nil || result.Retrieval.Effective != knowl.RetrievalHybrid {
						t.Fatalf("case %s not hybrid: %+v", fixture.ID, result.Retrieval)
					}
					source, report, err := index.SelectContextWithReport(t.Context(), snapshot.Scope, knowl.SourceSummary{Body: fixture.Query}, knowl.ReadLimits{Pages: 8})
					if err != nil {
						t.Fatal(err)
					}
					if contextpolicy.CandidateLimit(8) != 5 || report.Effective != knowl.RetrievalHybrid || len(source) < 5 {
						t.Fatal("direct relevance budget/profile changed")
					}
					source = source[:5] // excludes root, neighbors and recent fallback by the declared phase quota.
					m := caseMeasurement{Case: fixture.ID, Expected: fixture.Expected, Lexical: referenceIDs(base), Hybrid: referenceIDs(result.Pages), DirectSeeds: slices.Clone(source), QueryMS: latency, Report: *result.Retrieval, ExpectedCosine: measured.score(t, fixture.Query, original, space)}
					m.LexicalRank = rank(m.Lexical, fixture.Expected)
					m.HybridRank = rank(m.Hybrid, fixture.Expected)
					m.SourceRank = rank(source, fixture.Expected)
					run.Cases = append(run.Cases, m)
					if m.HybridRank == 0 || m.SourceRank == 0 {
						t.Errorf("%s expected %s top5: Query=%v source=%v", fixture.ID, fixture.Expected, m.Hybrid, source)
					}
					if fixture.ExactTitle && m.HybridRank != 1 {
						t.Errorf("exact unique title rank=%d", m.HybridRank)
					}
					if fixture.NoOverlap && m.LexicalRank != 0 {
						t.Errorf("no-overlap case has lexical hit: %s", fixture.ID)
					}
					assertOriginalEvidence(t, result, snapshot)
					if fixture.Distractor != "" {
						refs, report, err := index.SearchWithReport(t.Context(), snapshot.Scope, fixture.Query, knowl.ReadLimits{Pages: 100}, nil)
						if err != nil {
							t.Fatal(err)
						}
						ids := referenceIDs(refs)
						a, b := rank(ids, fixture.Expected), rank(ids, fixture.Distractor)
						distractor := snapshot.Pages[slices.IndexFunc(snapshot.Pages, func(p knowl.PageSnapshot) bool { return p.ID == fixture.Distractor })]
						score := measured.score(t, fixture.Query, distractor, space)
						measurement := &run.Cases[len(run.Cases)-1]
						measurement.DistractorCosine, measurement.RelevantFullRank, measurement.DistractorFullRank = &score, a, b
						if report.Effective != knowl.RetrievalHybrid || a == 0 || b == 0 || a >= b {
							t.Errorf("%s relevant/distractor ranks=%d/%d", fixture.ID, a, b)
						}
					}
				}
				if pass == 1 {
					for i, current := range run.Cases {
						prior := previous[i]
						if !slices.Equal(current.Hybrid, prior.Hybrid) || !slices.Equal(current.DirectSeeds, prior.DirectSeeds) || !slices.Equal(current.Lexical, prior.Lexical) {
							t.Errorf("non-deterministic IDs for %s", current.Case)
						}
					}
				}
				previous = run.Cases
				golden, err := knowledgetest.EvaluateProjectionReplay(t.Context(), index, knowl.ScopeRef("cpu-golden-"+backend.name))
				if err != nil {
					t.Errorf("golden: %v", err)
				}
				run.Golden = golden
				runs = append(runs, run)
			}
			assertRealProjectionLifecycle(t, index, snapshot)
			after, err := workspace.Snapshot(t.Context(), evalScope)
			if err != nil || !reflect.DeepEqual(before.PageDigests, after.PageDigests) {
				t.Fatalf("read-only queries changed canonical workspace: %v", err)
			}
		})
	}
	encodedCorpus, _ := json.Marshal(struct {
		Snapshot knowl.WorkspaceSnapshot
		Cases    []qualityCase
	}{qualitySnapshot(), qualityCases()})
	hash := sha256.Sum256(encodedCorpus)
	results := struct {
		Model     modelInfo        `json:"model"`
		CorpusSHA string           `json:"corpus_sha256"`
		Runs      []runMeasurement `json:"runs"`
		APIMS     []float64        `json:"api_request_ms"`
	}{info, fmt.Sprintf("%x", hash), runs, measured.durations}
	encoded, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 128<<10 {
		t.Fatal("evaluation artifact exceeds bound")
	}
	if output := os.Getenv("KNOWL_EMBEDDING_EVAL_OUTPUT"); output != "" {
		if err := os.WriteFile(output, append(encoded, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(encoded))
}

func referenceInfo(t *testing.T, endpoint string) modelInfo {
	t.Helper()
	address, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	address.Path = "/info"
	address.RawQuery = ""
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var info modelInfo
	if response.StatusCode != http.StatusOK {
		t.Fatal("reference runtime info unavailable")
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Model != referenceModel || info.Revision != referenceRevision || info.Version != "1.9.0" || info.DType != "float32" || info.AutoTruncate == nil || *info.AutoTruncate {
		t.Fatalf("reference runtime identity mismatch: %+v", info)
	}
	return info
}
func rank(ids []knowl.PageID, id knowl.PageID) int { return slices.Index(ids, id) + 1 }
func referenceIDs(refs []knowl.PageReference) []knowl.PageID {
	ids := make([]knowl.PageID, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ID
	}
	return ids
}
func assertOriginalEvidence(t *testing.T, result app.QueryResult, snapshot knowl.WorkspaceSnapshot) {
	t.Helper()
	for _, ref := range result.Pages {
		i := slices.IndexFunc(snapshot.Pages, func(p knowl.PageSnapshot) bool { return p.ID == ref.ID })
		if i < 0 {
			t.Fatal("foreign page evidence")
		}
		p := snapshot.Pages[i]
		body := p.Body
		if body == "" {
			body = p.Content
		}
		if ref.Title != p.Title || ref.Path != p.Path || !ref.Untrusted || !slices.Equal(ref.SourceRefs, p.SourceRefs) || ref.Snippet == "" || !strings.Contains(p.Title+"\n\n"+body, ref.Snippet) {
			t.Fatalf("original evidence mismatch: %s", ref.ID)
		}
		for _, source := range p.SourceRefs {
			if !slices.ContainsFunc(result.Citations, func(c app.Citation) bool {
				return c.PageID == p.ID && c.Kind == "raw" && c.SourceRef == source && c.Untrusted
			}) {
				t.Fatal("original citation missing")
			}
		}
	}
}
func assertRealProjectionLifecycle(t *testing.T, index qualityIndex, snapshot knowl.WorkspaceSnapshot) {
	t.Helper()
	refs, report, err := index.SearchWithReport(t.Context(), snapshot.Scope, "Conversations surviving process reboot", knowl.ReadLimits{Pages: 5}, []knowl.SourceID{"eligible"})
	if err != nil || report.Effective != knowl.RetrievalHybrid || !slices.Equal(referenceIDs(refs), []knowl.PageID{dialogueID}) {
		t.Fatalf("real source-filter retrieval: %v %v", referenceIDs(refs), err)
	}
	refs, _, err = index.SearchWithReport(t.Context(), snapshot.Scope, "Conversations surviving process reboot", knowl.ReadLimits{Pages: 5}, []knowl.SourceID{"missing"})
	if err != nil || len(refs) != 0 {
		t.Fatal("unknown source filter bypass")
	}
	foreign := qualitySnapshot()
	foreign.Scope += "-foreign"
	foreign.Pages = foreign.Pages[:1]
	foreign.Pages[0].ID = "foreign-evidence"
	if err := index.Rebuild(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	updated := qualitySnapshot()
	i := slices.IndexFunc(updated.Pages, func(p knowl.PageSnapshot) bool { return p.ID == dialogueID })
	updated.Pages[i].Body += " Added literal updatebeacon."
	updated.Pages[i].Content = updated.Pages[i].Body
	updated.Pages[i].Digest = strings.Repeat("c", 64)
	updated.PageDigests[updated.Pages[i].Path] = updated.Pages[i].Digest
	if err := index.Project(t.Context(), knowl.ContentCommit{Snapshot: updated}); err != nil {
		t.Fatal(err)
	}
	refs, report, err = index.SearchWithReport(t.Context(), updated.Scope, "updatebeacon", knowl.ReadLimits{Pages: 1}, nil)
	if err != nil || report.Effective != knowl.RetrievalHybrid || !slices.Equal(referenceIDs(refs), []knowl.PageID{dialogueID}) {
		t.Fatal("updated evidence not searchable")
	}
	delete(updated.PageDigests, updated.Pages[i].Path)
	updated.Pages = slices.Delete(updated.Pages, i, i+1)
	if err := index.Project(t.Context(), knowl.ContentCommit{Snapshot: updated}); err != nil {
		t.Fatal(err)
	}
	refs, _, err = index.SearchWithReport(t.Context(), updated.Scope, "Conversations surviving process reboot", knowl.ReadLimits{Pages: 100}, nil)
	if err != nil || slices.Contains(referenceIDs(refs), dialogueID) || slices.Contains(referenceIDs(refs), "foreign-evidence") {
		t.Fatal("deleted or foreign evidence returned")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = index.SearchWithReport(cancelled, updated.Scope, "archives", knowl.ReadLimits{Pages: 5}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("caller cancellation lost")
	}
}
