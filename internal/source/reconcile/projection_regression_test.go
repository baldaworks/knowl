package reconcile

import (
	"context"
	"fmt"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	sqlitestore "github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type syncProjectionProvider struct{ calls int }

const projectionPageOne = "entities/one"

func (provider *syncProjectionProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	provider.calls += len(inputs)
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func canonicalProjectionPage(id, title, sourceRef, body string) string {
	return fmt.Sprintf("---\nid: %s\ntitle: %s\ntype: entity\nsource_refs:\n  - %s\n---\n# %s\n\n%s\n", id, title, sourceRef, title, body)
}

func TestSourceSyncKeepsStructuredProjectionCurrentWithoutReembedding(t *testing.T) {
	ctx := t.Context()
	env := newVerticalEnv(t)
	provider := &syncProjectionProvider{}
	store, err := sqlitestore.Open(ctx, env.storePath, app.EmbeddingOptions{Provider: provider, Space: app.EmbeddingSpace{Model: "sync-projection", Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	env.search = store
	env.service.search = store
	initial, err := env.service.SyncSource(ctx, env.scope, env.primary())
	requireSyncSuccess(t, initial, err)
	if !initial.Changed || provider.calls != 0 {
		t.Fatalf("initial sync changed=%v embedding calls=%d", initial.Changed, provider.calls)
	}
	one, err := env.state.DocumentState(ctx, env.scope, testEngineeringSourceID, "docs/one.md")
	if err != nil {
		t.Fatal(err)
	}
	two, err := env.state.DocumentState(ctx, env.scope, testEngineeringSourceID, "docs/two.md")
	if err != nil {
		t.Fatal(err)
	}
	oneRef := app.SourceRefKey(one.AcceptedSource)
	twoRef := app.SourceRefKey(two.AcceptedSource)
	writeVerticalFile(t, env.workspace.Root(), "wiki/entities/one.md", canonicalProjectionPage(projectionPageOne, "One", oneRef, "First section. [Two](two.md)"))
	writeVerticalFile(t, env.workspace.Root(), "wiki/entities/two.md", canonicalProjectionPage("entities/two", "Two", twoRef, "Second section."))
	snapshot, err := env.workspace.Snapshot(ctx, env.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Project(ctx, knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	baseline := provider.calls
	if baseline != 2 {
		t.Fatalf("initial semantic inputs=%d, want 2", baseline)
	}
	unchanged, err := env.service.SyncSource(ctx, env.scope, env.primary())
	requireSyncSuccess(t, unchanged, err)
	if unchanged.Changed || provider.calls != baseline {
		t.Fatalf("unchanged sync changed=%v added embeddings=%d", unchanged.Changed, provider.calls-baseline)
	}
	if err := store.CheckProjection(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	links, err := store.Links(ctx, env.scope, projectionPageOne, knowl.ReadLimits{Pages: 5})
	if err != nil || len(links) != 1 || links[0].From != projectionPageOne || links[0].To != "entities/two" {
		t.Fatalf("current links=%+v err=%v", links, err)
	}
	refs, report, err := store.SearchWithReport(ctx, env.scope, "First", knowl.ReadLimits{Pages: 2, Characters: 200}, nil)
	if err != nil || report.Effective != knowl.RetrievalHybrid || len(refs) == 0 {
		t.Fatalf("current search refs=%+v report=%+v err=%v", refs, report, err)
	}
	found := false
	for _, ref := range refs {
		if ref.ID == projectionPageOne {
			found = true
			if len(ref.SourceDocuments) != 1 || ref.SourceDocuments[0].Revision != one.Revision || ref.Snippet == "" {
				t.Fatalf("source evidence=%+v", ref)
			}
		}
	}
	if !found {
		t.Fatalf("semantic page absent: refs=%+v", refs)
	}
	beforeUpdate := provider.calls
	writeVerticalFile(t, env.sourceRoot, "docs/one.md", "# One revised\n")
	writeVerticalFile(t, env.sourceRoot, "docs/two.md", "# Two revised\n")
	updated, err := env.service.SyncSource(ctx, env.scope, env.primary())
	requireSyncSuccess(t, updated, err)
	if !updated.Changed || provider.calls != beforeUpdate {
		t.Fatalf("source-only sync changed=%v added embeddings=%d", updated.Changed, provider.calls-beforeUpdate)
	}
	current, err := env.workspace.Snapshot(ctx, env.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckProjection(ctx, current); err != nil {
		t.Fatal(err)
	}
	newOne, err := env.state.DocumentState(ctx, env.scope, testEngineeringSourceID, "docs/one.md")
	if err != nil {
		t.Fatal(err)
	}
	oneRef = app.SourceRefKey(newOne.AcceptedSource)
	beforeProvenance := provider.calls
	writeVerticalFile(t, env.workspace.Root(), "wiki/entities/one.md", canonicalProjectionPage(projectionPageOne, "One", oneRef, "First section. [Two](two.md)"))
	provenance, err := env.workspace.Snapshot(ctx, env.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Project(ctx, knowl.ContentCommit{Snapshot: provenance}); err != nil {
		t.Fatal(err)
	}
	if provider.calls != beforeProvenance {
		t.Fatalf("provenance-only projection embedded %d inputs", provider.calls-beforeProvenance)
	}
	refs, report, err = store.SearchWithReport(ctx, env.scope, "First", knowl.ReadLimits{Pages: 2, Characters: 200}, nil)
	if err != nil || report.Effective != knowl.RetrievalHybrid {
		t.Fatalf("provenance search refs=%+v report=%+v err=%v", refs, report, err)
	}
	found = false
	for _, ref := range refs {
		if ref.ID == projectionPageOne {
			found = true
			if len(ref.SourceDocuments) != 1 || ref.SourceDocuments[0].Revision != newOne.Revision {
				t.Fatalf("current source evidence=%+v", ref)
			}
		}
	}
	if !found {
		t.Fatalf("semantic page absent after provenance update: refs=%+v", refs)
	}
	beforeEdits := provider.calls
	writeVerticalFile(t, env.workspace.Root(), "wiki/entities/one.md", canonicalProjectionPage(projectionPageOne, "One", oneRef, "First section. [Two](two.md)\n\n## Added\nNew section."))
	writeVerticalFile(t, env.workspace.Root(), "wiki/entities/two.md", canonicalProjectionPage("entities/two", "Two", twoRef, "Changed second section."))
	edited, err := env.workspace.Snapshot(ctx, env.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Project(ctx, knowl.ContentCommit{Files: []string{"wiki/entities/one.md", "wiki/entities/two.md"}, Snapshot: edited}); err != nil {
		t.Fatal(err)
	}
	if got := provider.calls - beforeEdits; got != 2 {
		t.Fatalf("two-page canonical edit embedded %d inputs, want 2", got)
	}
	if err := store.CheckProjection(ctx, edited); err != nil {
		t.Fatal(err)
	}
	beforeRepair := provider.calls
	if err := store.Rebuild(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if got := provider.calls - beforeRepair; got != 3 {
		t.Fatalf("explicit Rebuild embedded %d inputs, want 3", got)
	}
	if err := store.Project(ctx, knowl.ContentCommit{Snapshot: edited}); err != nil {
		t.Fatal(err)
	}
	if provider.calls != beforeRepair+3 {
		t.Fatalf("unchanged Project after Rebuild embedded %d inputs", provider.calls-beforeRepair-3)
	}
}
