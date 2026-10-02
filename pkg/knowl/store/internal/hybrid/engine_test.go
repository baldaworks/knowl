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

func TestPageRankingAggregatesWindowsAndChunks(t *testing.T) {
	chunks := []Chunk{{PageID: "a", Vector: []float32{1, 0}}, {PageID: "a", Vector: []float32{-1, 0}}, {PageID: "b", Vector: []float32{0, 1}}, {PageID: "c", Vector: []float32{0.6, 0.8}}}
	ids, err := Rank(t.Context(), [][]float32{{1, 0}, {0, 1}}, chunks, 5)
	if err != nil || !reflect.DeepEqual(ids, []knowl.PageID{"a", "b", "c"}) {
		t.Fatalf("page aggregation=%v %v", ids, err)
	}
	one, err := Rank(t.Context(), [][]float32{{1, 0}}, chunks, 2)
	if err != nil || !reflect.DeepEqual(one, []knowl.PageID{"a", "c"}) {
		t.Fatalf("window ranking=%v %v", one, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Rank(canceled, [][]float32{{1, 0}}, chunks, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("rank cancellation=%v", err)
	}
}

func TestFusionDeduplicatesChannelsAndBreaksTies(t *testing.T) {
	cases := []struct{ lexical, dense, want []knowl.PageID }{
		{[]knowl.PageID{"a", "b"}, []knowl.PageID{"b", "c"}, []knowl.PageID{"b", "a", "c"}},
		{[]knowl.PageID{"a", "b"}, []knowl.PageID{"b", "a"}, []knowl.PageID{"a", "b"}},
		{[]knowl.PageID{"a", "a", "b"}, []knowl.PageID{"b", "b"}, []knowl.PageID{"b", "a"}},
	}
	for _, tc := range cases {
		if got := Fuse(tc.lexical, tc.dense, 5); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("fused=%v want=%v", got, tc.want)
		}
	}
}

func TestFailurePolicyDoesNotExposeProviderErrors(t *testing.T) {
	for _, policy := range []app.EmbeddingFailurePolicy{app.EmbeddingFallbackLexical, app.EmbeddingStrict} {
		engine := &Engine{FailurePolicy: policy, Fingerprint: "0123456789abcdef"}
		report, err := engine.Failure(t.Context(), engine.Report(), errors.New("secret provider body"))
		if report.Reason != knowl.RetrievalUnavailable {
			t.Fatalf("unallowlisted report=%+v", report)
		}
		if policy == app.EmbeddingStrict {
			var classified *app.EmbeddingError
			if !errors.As(err, &classified) || classified.Code != knowl.RetrievalUnavailable || report.Effective != knowl.RetrievalFailed {
				t.Fatalf("strict error=%v report=%+v", err, report)
			}
		} else if err != nil || report.Effective != knowl.RetrievalDegraded {
			t.Fatalf("fallback=%v report=%+v", err, report)
		}
		_, err = engine.Failure(t.Context(), engine.Report(), &app.EmbeddingError{Code: knowl.RetrievalInvalidInput})
		if err == nil {
			t.Fatal("invalid caller input masked by fallback")
		}
	}
}

func TestMaintenanceGenerationIncludesEffectiveHybridChoices(t *testing.T) {
	const policyChangeSuffix = "-changed"
	baseSpace := app.EmbeddingSpace{Model: "generation-fixture", Revision: "immutable-1", Dimensions: 2, QueryPrefix: "query: ", PassagePrefix: "passages: "}
	makePolicy := func(space app.EmbeddingSpace, failurePolicy app.EmbeddingFailurePolicy) app.MaintenancePolicy {
		t.Helper()
		engine, err := New([]app.EmbeddingOptions{{Provider: policyTestProvider{}, Space: space, FailurePolicy: failurePolicy}})
		if err != nil {
			t.Fatal(err)
		}
		policy := app.SourceMaintenancePolicy(strings.Repeat("a", 64), app.DefaultReadLimits(), app.DefaultPlanLimits())
		policy.Retrieval = engine.MaintenancePolicy()
		return policy
	}
	base := makePolicy(baseSpace, app.EmbeddingFallbackLexical)
	original, err := app.MaintenancePolicyGeneration(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*app.EmbeddingSpace){func(s *app.EmbeddingSpace) { s.Model = "generation-other" }, func(s *app.EmbeddingSpace) { s.Revision = "immutable-2" }, func(s *app.EmbeddingSpace) { s.Dimensions = 3 }, func(s *app.EmbeddingSpace) { s.QueryPrefix = "question: " }, func(s *app.EmbeddingSpace) { s.PassagePrefix = "document: " }} {
		changed := baseSpace
		change(&changed)
		generation, err := app.MaintenancePolicyGeneration(makePolicy(changed, app.EmbeddingFallbackLexical))
		if err != nil || generation == original {
			t.Fatalf("model contract identity unchanged: %v", err)
		}
	}
	for _, change := range []func(*app.MaintenanceRetrievalPolicy){
		func(p *app.MaintenanceRetrievalPolicy) { p.Preprocessing += policyChangeSuffix }, func(p *app.MaintenanceRetrievalPolicy) { p.VectorNormalization += policyChangeSuffix }, func(p *app.MaintenanceRetrievalPolicy) { p.Fusion += policyChangeSuffix }, func(p *app.MaintenanceRetrievalPolicy) { p.RankConstant++ }, func(p *app.MaintenanceRetrievalPolicy) { p.MinimumCandidates++ }, func(p *app.MaintenanceRetrievalPolicy) { p.MaximumCandidates++ }, func(p *app.MaintenanceRetrievalPolicy) { p.CandidateMultiplier++ }, func(p *app.MaintenanceRetrievalPolicy) { p.MaxChunks++ }, func(p *app.MaintenanceRetrievalPolicy) { p.MaxProjectionBytes++ }, func(p *app.MaintenanceRetrievalPolicy) { p.FailurePolicy = app.EmbeddingStrict },
	} {
		changed := base
		copied := *base.Retrieval
		changed.Retrieval = &copied
		change(changed.Retrieval)
		generation, err := app.MaintenancePolicyGeneration(changed)
		if err != nil || generation == original {
			t.Fatalf("retrieval identity unchanged: %v", err)
		}
	}
	disabled := base
	disabled.Retrieval = nil
	generation, err := app.MaintenancePolicyGeneration(disabled)
	if err != nil || generation == original {
		t.Fatalf("disabled identity unchanged: %v", err)
	}
}

type policyTestProvider struct{}

func (policyTestProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("policy must not invoke model")
}
