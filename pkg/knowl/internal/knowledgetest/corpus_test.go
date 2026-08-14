package knowledgetest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const mutatedValue = "mutated"

func TestCorpusShapeAndCopies(t *testing.T) {
	t.Parallel()
	sources := Sources()
	queries := Queries()
	if Version != "v1" || len(sources) != 4 || len(queries) != QueryCount || MinimumHits != 11 {
		t.Fatalf("corpus shape = version %q sources %d queries %d minimum %d", Version, len(sources), len(queries), MinimumHits)
	}
	sources[0].Content = mutatedValue
	queries[0].MatchTerms[0] = mutatedValue
	if Sources()[0].Content == mutatedValue || Queries()[0].MatchTerms[0] == mutatedValue {
		t.Fatal("corpus getters exposed mutable backing storage")
	}
}

func TestMaintainerBuildsTwoPageCorpusAndRequiresUpdateContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	maintainer := &Maintainer{}
	schema := knowl.SchemaDocument{Digest: "schema"}
	sources := Sources()

	initial, err := maintainer.Plan(ctx, maintenanceInput(schema, sources[0], nil))
	if err != nil {
		t.Fatalf("initial Plan(): %v", err)
	}
	decision := pageFromPlan(t, DecisionPageID, "Session Memory Decision", initial)

	if _, err := maintainer.Plan(ctx, maintenanceInput(schema, sources[1], nil)); err == nil {
		t.Fatal("investigation update succeeded without relevant context")
	}
	investigated, err := maintainer.Plan(ctx, maintenanceInput(schema, sources[1], []knowl.PageSnapshot{decision}))
	if err != nil {
		t.Fatalf("investigation Plan(): %v", err)
	}
	decision = pageFromPlan(t, DecisionPageID, "Session Memory Decision", investigated)

	superseded, err := maintainer.Plan(ctx, maintenanceInput(schema, sources[2], []knowl.PageSnapshot{decision}))
	if err != nil {
		t.Fatalf("superseding Plan(): %v", err)
	}
	decision = pageFromPlan(t, DecisionPageID, "Session Memory Decision", superseded)
	runbookPlan, err := maintainer.Plan(ctx, maintenanceInput(schema, sources[3], []knowl.PageSnapshot{decision}))
	if err != nil {
		t.Fatalf("runbook Plan(): %v", err)
	}
	runbook := pageFromPlan(t, RunbookPageID, "Session Recovery Runbook", runbookPlan)

	final := FinalSnapshot("local")
	if decision.Content != final.Pages[0].Content || !slices.Equal(decision.SourceRefs, final.Pages[0].SourceRefs) {
		t.Fatalf("maintained decision differs from final fixture:\nmaintained=%#v\nfinal=%#v", decision, final.Pages[0])
	}
	if runbook.Content != final.Pages[1].Content || !slices.Equal(runbook.SourceRefs, final.Pages[1].SourceRefs) {
		t.Fatalf("maintained runbook differs from final fixture")
	}
	if err := ValidateFinalSnapshot(final); err != nil {
		t.Fatalf("ValidateFinalSnapshot(): %v", err)
	}
	if maintainer.Calls() != 5 || maintainer.CallsFor(sources[1].ExpectedRef) != 2 {
		t.Fatalf("maintainer calls = %d, investigation calls = %d", maintainer.Calls(), maintainer.CallsFor(sources[1].ExpectedRef))
	}
}

func TestEvaluatorMetricsAndEvidenceValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	perfect, err := Evaluate(ctx, fixtureRetriever(nil, false))
	if err != nil || perfect.Hits != QueryCount || !perfect.Passed() {
		t.Fatalf("perfect metrics = %#v, err = %v", perfect, err)
	}
	oneMiss, err := Evaluate(ctx, fixtureRetriever(map[string]struct{}{Queries()[0].Query: {}}, false))
	if err != nil || oneMiss.Hits != MinimumHits || !oneMiss.Passed() {
		t.Fatalf("one-miss metrics = %#v, err = %v", oneMiss, err)
	}
	twoMisses := map[string]struct{}{Queries()[0].Query: {}, Queries()[1].Query: {}}
	failed, err := Evaluate(ctx, fixtureRetriever(twoMisses, false))
	if err != nil || failed.Passed() || len(failed.Misses) != 2 {
		t.Fatalf("two-miss metrics = %#v, err = %v", failed, err)
	}
	if _, err := Evaluate(ctx, fixtureRetriever(nil, true)); err == nil {
		t.Fatal("Evaluate() accepted evidence without required provenance")
	}
}

func TestRestartMatrixIsCompleteAndUnique(t *testing.T) {
	t.Parallel()
	if len(RestartMatrix) != 7 {
		t.Fatalf("restart matrix length = %d, want 7", len(RestartMatrix))
	}
	seen := make(map[string]struct{}, len(RestartMatrix))
	for _, scenario := range RestartMatrix {
		if scenario == "" {
			t.Fatal("restart matrix contains an empty scenario")
		}
		if _, duplicate := seen[scenario]; duplicate {
			t.Fatalf("duplicate restart scenario %q", scenario)
		}
		seen[scenario] = struct{}{}
	}
}

func maintenanceInput(schema knowl.SchemaDocument, source SourceFixture, pages []knowl.PageSnapshot) knowl.MaintenanceInput {
	return knowl.MaintenanceInput{
		Scope: "local", Schema: schema,
		Source: knowl.AcceptedSource{
			Scope: "local", Source: knowl.SourceRef{Adapter: inlineAdapter, ID: source.Origin},
			Version: knowl.SourceVersion{Version: source.Revision, Digest: "digest"}, MediaType: source.MediaType,
		},
		SourceText: source.Content, Pages: pages,
	}
}

func pageFromPlan(t *testing.T, id knowl.PageID, title string, plan knowl.ModelEditPlan) knowl.PageSnapshot {
	t.Helper()
	if len(plan.Edits) != 1 {
		t.Fatalf("plan edits = %d, want 1", len(plan.Edits))
	}
	digest := sha256.Sum256(plan.Edits[0].Content)
	return knowl.PageSnapshot{
		ID: id, Path: plan.Edits[0].Path, Digest: fmt.Sprintf("%x", digest), Title: title,
		Content: string(plan.Edits[0].Content), SourceRefs: append([]string(nil), plan.SourceRefs...),
	}
}

func fixtureRetriever(misses map[string]struct{}, omitProvenance bool) Retriever {
	return func(_ context.Context, query string, _ knowl.ReadLimits) ([]knowl.PageReference, error) {
		if _, missing := misses[query]; missing {
			return nil, nil
		}
		for _, fixture := range Queries() {
			if fixture.Query != query {
				continue
			}
			refs := []string{fixture.ExpectedRef}
			if omitProvenance {
				refs = nil
			}
			return []knowl.PageReference{{
				ID: fixture.ExpectedPage, Path: string(fixture.ExpectedPage) + ".md", Title: "Fixture",
				Snippet: fixture.MatchTerms[0] + " evidence", SourceRefs: refs, Untrusted: true,
			}}, nil
		}
		return nil, fmt.Errorf("unknown query %q", query)
	}
}
