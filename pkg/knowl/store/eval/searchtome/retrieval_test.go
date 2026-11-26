package searchtome

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type fixtureEmbedding struct{ fail bool }

func (p *fixtureEmbedding) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	if p.fail && len(inputs) > 0 && strings.HasPrefix(inputs[0], "query: ") {
		return nil, &app.EmbeddingError{Code: knowl.RetrievalUnavailable}
	}
	out := make([][]float32, len(inputs))
	for i := range out {
		out[i] = make([]float32, 768)
		out[i][0] = 1
	}
	return out, nil
}
func TestRealRetrievalModes(t *testing.T) {
	records := []CorpusRecord{{Title: testApples, Content: "Apples are red fruit."}, {Title: "Pears", Content: "Pears are green fruit."}, {Title: "Clouds", Content: "Clouds contain water."}}
	p, err := Publish(t.Context(), filepath.Join(t.TempDir(), "workspace"), "education", records)
	if err != nil {
		t.Fatal(err)
	}
	queries := []Query{{ID: "1", Query: "red apples", Gold: testApples}, {ID: "2", Query: "missing", Gold: "No title", Exclusion: exUnknownGold}}
	for _, test := range []struct {
		name     string
		provider app.EmbeddingProvider
		mode     knowl.RetrievalMode
	}{{modeLexical, nil, knowl.RetrievalLexical}, {modeHybrid, &fixtureEmbedding{}, knowl.RetrievalHybrid}} {
		t.Run(test.name, func(t *testing.T) {
			projection, got, err := RunMode(t.Context(), filepath.Join(t.TempDir(), "search.sqlite"), "education", p, queries, test.provider)
			if err != nil {
				t.Fatal(err)
			}
			if projection.Pages != 3 || len(got) != 2 || got[0].Report.Effective != test.mode || got[0].Report.Requested != test.mode || got[0].Exclusion != "" || got[1].Exclusion != exUnknownGold {
				t.Fatalf("projection=%+v results=%+v", projection, got)
			}
			if len(got[0].Pages) == 0 || got[0].Pages[0].Title != testApples || got[0].Rank != 1 || got[0].Pages[0].ID == "" || p.Titles[got[0].Pages[0].ID] != testApples || got[0].Report.FusedCandidates == 0 {
				t.Fatalf("invalid result mapping: %+v", got[0])
			}
			if test.mode == knowl.RetrievalHybrid && (projection.Chunks == nil || *projection.Chunks == 0) {
				t.Fatalf("missing chunks %+v", projection)
			}
		})
	}
}

func TestHybridDegradationIsExcluded(t *testing.T) {
	p, err := Publish(t.Context(), filepath.Join(t.TempDir(), "workspace"), "education", []CorpusRecord{{Title: testApples, Content: "Apples are red."}})
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := RunMode(t.Context(), filepath.Join(t.TempDir(), "test.sqlite"), "education", p, []Query{{ID: "q", Query: "apples", Gold: testApples}}, &fixtureEmbedding{fail: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Report.Effective != knowl.RetrievalDegraded || got[0].Report.Reason != knowl.RetrievalUnavailable || got[0].Exclusion != "degraded" {
		t.Fatalf("result=%+v", got)
	}
}

type failingEmbedding struct{}

func (failingEmbedding) Embed(context.Context, []string) ([][]float32, error) {
	return nil, &app.EmbeddingError{Code: knowl.RetrievalUnavailable}
}
func TestProjectionFailurePreventsMeasurements(t *testing.T) {
	p, err := Publish(t.Context(), filepath.Join(t.TempDir(), "workspace"), "education", []CorpusRecord{{Title: testApples, Content: "Red fruit."}})
	if err != nil {
		t.Fatal(err)
	}
	_, observations, err := RunMode(t.Context(), filepath.Join(t.TempDir(), "test.sqlite"), "education", p, []Query{{ID: "q", Query: "apples", Gold: testApples}}, failingEmbedding{})
	if err == nil || len(observations) != 0 {
		t.Fatalf("err=%v observations=%+v", err, observations)
	}
}

func TestRetrievalUsesOnlyDomainWorkspace(t *testing.T) {
	a, err := Publish(t.Context(), filepath.Join(t.TempDir(), "a"), "education", []CorpusRecord{{Title: testIdentical, Content: "shared word apple"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Publish(t.Context(), filepath.Join(t.TempDir(), "b"), "finance", []CorpusRecord{{Title: testIdentical, Content: "shared word apple"}})
	if err != nil {
		t.Fatal(err)
	}
	_, results, err := RunMode(t.Context(), filepath.Join(t.TempDir(), "db.sqlite"), "education", a, []Query{{ID: "q", Query: "apple", Gold: testIdentical}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || len(results[0].Pages) != 1 {
		t.Fatalf("results: %+v", results)
	}
	if _, ok := b.Titles[results[0].Pages[0].ID]; ok {
		t.Fatal("cross-domain result")
	}
}

func TestInvalidQueryIsClassifiedInBothModes(t *testing.T) {
	p, err := Publish(t.Context(), filepath.Join(t.TempDir(), "workspace"), "education", []CorpusRecord{{Title: testApples, Content: "Apples are fruit."}})
	if err != nil {
		t.Fatal(err)
	}
	parts := make([]string, 40)
	for i := range parts {
		parts[i] = fmt.Sprintf("token%d", i)
	}
	query := Query{ID: "long", Query: strings.Join(parts, " "), Gold: testApples}
	for _, provider := range []app.EmbeddingProvider{nil, &fixtureEmbedding{}} {
		_, got, err := RunMode(t.Context(), filepath.Join(t.TempDir(), "test.sqlite"), "education", p, []Query{query}, provider)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Error != invalidQuery || got[0].Exclusion != invalidQuery {
			t.Fatalf("outcome=%+v", got)
		}
	}
}
