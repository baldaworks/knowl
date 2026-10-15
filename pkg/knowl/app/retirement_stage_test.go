package app_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	retirementTestLegacySourceID = "legacy"
	retirementTestRootTitle      = "Knowledge"
	retirementTestTeamCatalog    = "wiki/catalogs/team/index.md"
	retirementTestTeamTarget     = "catalogs/team/index.md"
	retirementTestKeptPage       = "wiki/concepts/kept.md"
	retirementTestKeptTarget     = "../../concepts/kept.md"
	retirementTestExternalTarget = "https://example.invalid/guide?q=1#part"
	retirementTestDuplicateLabel = "Duplicate"
)

func TestBuildRetirementEditPlanDeletesOnePageAndOnlyItsDirectCatalogClosure(t *testing.T) {
	fixture := newRetirementFixture(t)
	retiring := retirementAccepted("wiki-filesystem", "old", retirementTestLegacySourceID, "v1", "text/markdown")
	fixture.page = retirementPage(t, "concepts/old-only", []string{app.SourceRefKey(retiring)}, "Old facts.")
	owner := retirementOwner(retiring)
	fixture.states[owner] = retirementLifecycle(owner, knowl.SourceLifecycleRetiring, fixture.authority)
	request := fixture.request([]knowl.AcceptedSource{retiring}, owner)
	root := retirementCatalogSnapshot(testRootCatalogPath, retirementTestRootTitle, []retirementCatalogLink{{label: "Team catalog", destination: retirementTestTeamTarget}})
	team := retirementCatalogSnapshot(retirementTestTeamCatalog, "Team", []retirementCatalogLink{
		{label: "Human Readable", destination: retirementTestKeptTarget},
		{label: "External reference", destination: retirementTestExternalTarget},
		{label: "Retiring page", destination: "../../concepts/old-only.md"},
	})
	unrelated := retirementCatalogSnapshot("wiki/catalogs/unrelated/index.md", "Unrelated", []retirementCatalogLink{{label: "Unrelated page", destination: "../../concepts/unrelated.md"}})
	fixture.content.catalogs = map[knowl.PageID]knowl.PageSnapshot{root.ID: root, team.ID: team, unrelated.ID: unrelated}
	request.Catalogs = []knowl.HierarchyCatalog{
		{Path: root.Path, Digest: root.Digest, Title: root.Title, Children: []string{team.Path}},
		{Path: team.Path, Digest: team.Digest, Title: team.Title, Children: []string{fixture.page.Path, retirementTestKeptPage}},
		{Path: unrelated.Path, Digest: unrelated.Digest, Title: unrelated.Title, Children: []string{"wiki/concepts/unrelated.md"}},
	}
	planner := fixture.planner()
	result, err := planner.PlanRetirement(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	concurrentUnrelated := fixture.content.catalogs[unrelated.ID]
	concurrentUnrelated.Content = "concurrent unrelated edit"
	fixture.content.catalogs[unrelated.ID] = concurrentUnrelated

	plan, err := planner.BuildRetirementEditPlan(t.Context(), "retire-one", request, result)
	if err != nil {
		t.Fatalf("BuildRetirementEditPlan() error = %v", err)
	}
	if plan.OperationID != "retire-one" || plan.Scope != retirementTestScope || plan.SchemaDigest != fixture.schema.Digest || len(plan.SourceRefs) != 0 || len(plan.Edits) != 2 {
		t.Fatalf("plan identity/bounds = %#v", plan)
	}
	pageEdit := retirementEditByPath(t, plan.Edits, fixture.page.Path)
	if pageEdit.Action != knowl.SourceMutationDelete || pageEdit.ExpectedDigest != fixture.page.Digest || len(pageEdit.Content) != 0 {
		t.Fatalf("page delete = %#v", pageEdit)
	}
	catalogEdit := retirementEditByPath(t, plan.Edits, retirementTestTeamCatalog)
	if catalogEdit.Action != knowl.SourceMutationWrite || catalogEdit.ExpectedDigest != team.Digest {
		t.Fatalf("catalog closure = %#v", catalogEdit)
	}
	wantLinks := []retirementCatalogLink{
		{label: "Human Readable", destination: retirementTestKeptTarget},
		{label: "External reference", destination: retirementTestExternalTarget},
	}
	if links := parsedRetirementCatalogLinks(catalogEdit.Content); !reflect.DeepEqual(links, wantLinks) {
		t.Fatalf("preserved catalog links = %#v, want %#v", links, wantLinks)
	}
	if slices.ContainsFunc(plan.Edits, func(edit knowl.FileEdit) bool {
		return edit.Path == "wiki/catalogs/unrelated/index.md" || edit.Path == testRootCatalogPath
	}) {
		t.Fatalf("unrelated catalog entered closure: %#v", plan.Edits)
	}
	wantContext := &knowl.RetirementPlanContext{
		Scope: retirementTestScope, PageID: fixture.page.ID, PagePath: fixture.page.Path, PageDigest: fixture.page.Digest,
		AuthorityGeneration: fixture.authority.Generation, AuthorityEpoch: fixture.authority.Epoch,
		Owners: []knowl.RetirementOwnerAuthority{{Owner: owner, ConfigDigest: fixture.states[owner].ConfigDigest, RetirementGeneration: fixture.states[owner].RetirementGeneration, RetirementEpoch: fixture.states[owner].RetirementEpoch}},
	}
	if !reflect.DeepEqual(plan.Retirement, wantContext) {
		t.Fatalf("retirement context = %#v, want %#v", plan.Retirement, wantContext)
	}
}

func TestRevalidateRetirementStageRequiresCurrentExactAuthorityAndOwners(t *testing.T) {
	fixture := newRetirementFixture(t)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: retirementTestLegacySourceID}
	state := retirementLifecycle(owner, knowl.SourceLifecycleRetiring, fixture.authority)
	fixture.states[owner] = state
	staged := retirementStagedChange(fixture, state)
	if err := app.RevalidateRetirementStage(t.Context(), fixture.lifecycle(), staged); err != nil {
		t.Fatalf("RevalidateRetirementStage() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*retirementFixture)
	}{
		{name: "authority generation", mutate: func(f *retirementFixture) { f.authority.Generation = strings.Repeat("e", 64) }},
		{name: "authority epoch", mutate: func(f *retirementFixture) { f.authority.Epoch++ }},
		{name: "owner active", mutate: func(f *retirementFixture) {
			changed := f.states[owner]
			changed.State = knowl.SourceLifecycleActive
			f.states[owner] = changed
		}},
		{name: "retirement generation", mutate: func(f *retirementFixture) {
			changed := f.states[owner]
			changed.RetirementGeneration = strings.Repeat("e", 64)
			f.states[owner] = changed
		}},
		{name: "retirement epoch", mutate: func(f *retirementFixture) {
			changed := f.states[owner]
			changed.RetirementEpoch++
			f.states[owner] = changed
		}},
		{name: "config digest", mutate: func(f *retirementFixture) {
			changed := f.states[owner]
			changed.ConfigDigest = strings.Repeat("e", 64)
			f.states[owner] = changed
		}},
		{name: "replacement readiness", mutate: func(f *retirementFixture) {
			changed := f.states[owner]
			changed.ReplacementPending = 1
			f.states[owner] = changed
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copyFixture := *fixture
			copyFixture.states = map[knowl.SourceOwner]knowl.SourceLifecycle{owner: fixture.states[owner]}
			test.mutate(&copyFixture)
			err := app.RevalidateRetirementStage(t.Context(), copyFixture.lifecycle(), staged)
			if !errors.Is(err, app.ErrRetirementLifecycleConflict) {
				t.Fatalf("RevalidateRetirementStage() error = %v, want lifecycle conflict", err)
			}
		})
	}
}

func retirementStagedChange(fixture *retirementFixture, state knowl.SourceLifecycle) knowl.StagedChange {
	return knowl.StagedChange{OperationID: "retire-one", Digest: strings.Repeat("f", 64), Files: []string{fixture.page.Path}, Retirement: &knowl.RetirementPlanContext{
		Scope: retirementTestScope, PageID: "concepts/one", PagePath: "wiki/concepts/one.md", PageDigest: strings.Repeat("1", 64),
		AuthorityGeneration: fixture.authority.Generation, AuthorityEpoch: fixture.authority.Epoch,
		Owners: []knowl.RetirementOwnerAuthority{{Owner: state.Owner, ConfigDigest: state.ConfigDigest, RetirementGeneration: state.RetirementGeneration, RetirementEpoch: state.RetirementEpoch}},
	}}
}

func TestBuildRetirementEditPlanStagesMixedReplacementWithoutCatalogMutation(t *testing.T) {
	fixture, _, _, request := mixedRetirementFixture(t)
	planner := fixture.planner()
	result, err := planner.PlanRetirement(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.BuildRetirementEditPlan(t.Context(), "replace-one", request, result)
	if err != nil {
		t.Fatalf("BuildRetirementEditPlan() error = %v", err)
	}
	if len(plan.Edits) != 1 || plan.Edits[0].Action != knowl.SourceMutationWrite || plan.Edits[0].Path != fixture.page.Path || plan.Edits[0].ExpectedDigest != fixture.page.Digest || !slices.Equal(plan.SourceRefs, result.SourceRefs) {
		t.Fatalf("mixed replacement plan = %#v", plan)
	}
}

func TestBuildRetirementEditPlanRejectsPageDriftAfterPlanning(t *testing.T) {
	fixture := newRetirementFixture(t)
	retiring := retirementAccepted("wiki-filesystem", "old", retirementTestLegacySourceID, "v1", "text/markdown")
	fixture.page = retirementPage(t, "concepts/stale", []string{app.SourceRefKey(retiring)}, "Old facts.")
	owner := retirementOwner(retiring)
	fixture.states[owner] = retirementLifecycle(owner, knowl.SourceLifecycleRetiring, fixture.authority)
	request := fixture.request([]knowl.AcceptedSource{retiring}, owner)
	planner := fixture.planner()
	result, err := planner.PlanRetirement(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.content.page = retirementPage(t, "concepts/stale", []string{app.SourceRefKey(retiring)}, "Concurrent facts.")

	plan, err := planner.BuildRetirementEditPlan(t.Context(), "retire-stale", request, result)
	if !errors.Is(err, app.ErrRetirementPageStale) || len(plan.Edits) != 0 {
		t.Fatalf("BuildRetirementEditPlan() = %#v, %v; want stale page and no plan", plan, err)
	}
}

func TestBuildRetirementEditPlanFreshlyRejectsPageAndLifecycleDrift(t *testing.T) {
	pageTests := []struct {
		name   string
		mutate func(*retirementFixture)
	}{
		{name: "content", mutate: func(f *retirementFixture) { f.content.page.Content += "changed" }},
		{name: "digest", mutate: func(f *retirementFixture) { f.content.page.Digest = strings.Repeat("e", 64) }},
		{name: "path", mutate: func(f *retirementFixture) { f.content.page.Path = "wiki/concepts/moved.md" }},
		{name: "source refs", mutate: func(f *retirementFixture) {
			f.content.page.SourceRefs = append(f.content.page.SourceRefs, "git:other/page@v1")
		}},
	}
	for _, test := range pageTests {
		t.Run("page "+test.name, func(t *testing.T) {
			fixture, planner, request, result, _ := plannedOldOnlyRetirement(t)
			test.mutate(fixture)
			plan, err := planner.BuildRetirementEditPlan(t.Context(), "fresh-page", request, result)
			if !errors.Is(err, app.ErrRetirementPageStale) || len(plan.Edits) != 0 {
				t.Fatalf("BuildRetirementEditPlan() = %#v, %v; want page stale", plan, err)
			}
		})
	}

	lifecycleTests := []struct {
		name   string
		mutate func(*retirementFixture, knowl.SourceOwner)
	}{
		{name: "authority generation", mutate: func(f *retirementFixture, _ knowl.SourceOwner) { f.authority.Generation = strings.Repeat("e", 64) }},
		{name: "authority epoch", mutate: func(f *retirementFixture, _ knowl.SourceOwner) { f.authority.Epoch++ }},
		{name: "state", mutate: func(f *retirementFixture, owner knowl.SourceOwner) {
			state := f.states[owner]
			state.State = knowl.SourceLifecycleActive
			f.states[owner] = state
		}},
		{name: "config", mutate: func(f *retirementFixture, owner knowl.SourceOwner) {
			state := f.states[owner]
			state.ConfigDigest = strings.Repeat("e", 64)
			f.states[owner] = state
		}},
		{name: "retirement generation", mutate: func(f *retirementFixture, owner knowl.SourceOwner) {
			state := f.states[owner]
			state.RetirementGeneration = strings.Repeat("e", 64)
			f.states[owner] = state
		}},
		{name: "retirement epoch", mutate: func(f *retirementFixture, owner knowl.SourceOwner) {
			state := f.states[owner]
			state.RetirementEpoch++
			f.states[owner] = state
		}},
		{name: "readiness", mutate: func(f *retirementFixture, owner knowl.SourceOwner) {
			state := f.states[owner]
			state.Ready = true
			f.states[owner] = state
		}},
		{name: "pending", mutate: func(f *retirementFixture, owner knowl.SourceOwner) {
			state := f.states[owner]
			state.ReplacementPending = 1
			f.states[owner] = state
		}},
		{name: "blocker", mutate: func(f *retirementFixture, owner knowl.SourceOwner) {
			state := f.states[owner]
			state.BlockerCode = "source.blocked"
			f.states[owner] = state
		}},
	}
	for _, test := range lifecycleTests {
		t.Run("lifecycle "+test.name, func(t *testing.T) {
			fixture, planner, request, result, owner := plannedOldOnlyRetirement(t)
			test.mutate(fixture, owner)
			plan, err := planner.BuildRetirementEditPlan(t.Context(), "fresh-lifecycle", request, result)
			if !errors.Is(err, app.ErrRetirementLifecycleConflict) || len(plan.Edits) != 0 {
				t.Fatalf("BuildRetirementEditPlan() = %#v, %v; want lifecycle conflict", plan, err)
			}
		})
	}
}

func plannedOldOnlyRetirement(t *testing.T) (*retirementFixture, *app.RetirementPlanner, app.RetirementPlanningRequest, knowl.RetirementPlanResult, knowl.SourceOwner) {
	t.Helper()
	fixture := newRetirementFixture(t)
	retiring := retirementAccepted("wiki-filesystem", "old", retirementTestLegacySourceID, "v1", "text/markdown")
	fixture.page = retirementPage(t, "concepts/fresh", []string{app.SourceRefKey(retiring)}, "Old facts.")
	owner := retirementOwner(retiring)
	fixture.states[owner] = retirementLifecycle(owner, knowl.SourceLifecycleRetiring, fixture.authority)
	request := fixture.request([]knowl.AcceptedSource{retiring}, owner)
	planner, err := app.NewRetirementPlanner(&fixture.content, retirementLiveLifecycleFixture{fixture: fixture}, &fixture.maintainer, app.RetirementPlannerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := planner.PlanRetirement(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, planner, request, result, owner
}

func TestBuildRetirementEditPlanPreservesCatalogASTOutsideExactRetiringTargets(t *testing.T) {
	fixture, planner, request, result, _ := plannedOldOnlyRetirement(t)
	root := retirementCatalogSnapshot(testRootCatalogPath, retirementTestRootTitle, []retirementCatalogLink{
		{label: "Nested", destination: retirementTestTeamTarget},
		{label: "Root external", destination: "https://example.invalid/root?q=1#part"},
	})
	team := retirementCatalogSnapshot(retirementTestTeamCatalog, "Team", []retirementCatalogLink{
		{label: retirementTestDuplicateLabel, destination: retirementTestKeptTarget},
		{label: retirementTestDuplicateLabel, destination: "../../concepts/kept.md#section"},
		{label: "External query", destination: retirementTestExternalTarget},
		{label: "Remove plain", destination: "../../concepts/fresh.md"},
		{label: "Remove normalized", destination: "../../concepts/./fresh.md?q=1#part"},
	})
	team = retirementRawCatalogSnapshot(team.Path, team.Title, team.Content+"* [Diagram ![Architecture diagram](https://example.invalid/diagram.svg?q=1#overview)](../../concepts/kept.md)\n")
	empty := retirementCatalogSnapshot("wiki/catalogs/empty/index.md", "Empty", []retirementCatalogLink{
		{label: "Remove only", destination: "../../concepts/fresh.md"},
	})
	fixture.content.catalogs = map[knowl.PageID]knowl.PageSnapshot{root.ID: root, team.ID: team, empty.ID: empty}
	request.Catalogs = []knowl.HierarchyCatalog{
		{Path: root.Path, Digest: root.Digest, Title: root.Title, Children: []string{team.Path}},
		{Path: team.Path, Digest: team.Digest, Title: team.Title, Children: []string{fixture.page.Path, retirementTestKeptPage}},
		{Path: empty.Path, Digest: empty.Digest, Title: empty.Title, Children: []string{fixture.page.Path}},
	}
	plan, err := planner.BuildRetirementEditPlan(t.Context(), "catalog-preserve", request, result)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Edits) != 3 {
		t.Fatalf("edits = %#v, want page and two direct catalogs", plan.Edits)
	}
	teamEdit := retirementEditByPath(t, plan.Edits, team.Path)
	wantTeam := []retirementCatalogLink{
		{label: retirementTestDuplicateLabel, destination: retirementTestKeptTarget},
		{label: retirementTestDuplicateLabel, destination: "../../concepts/kept.md#section"},
		{label: "External query", destination: retirementTestExternalTarget},
		{label: "Diagram ", destination: retirementTestKeptTarget},
	}
	if links := parsedRetirementCatalogLinks(teamEdit.Content); !reflect.DeepEqual(links, wantTeam) {
		t.Fatalf("team links = %#v, want %#v", links, wantTeam)
	}
	wantImages := []retirementCatalogLink{{label: "Architecture diagram", destination: "https://example.invalid/diagram.svg?q=1#overview"}}
	if images := parsedRetirementCatalogImages(teamEdit.Content); !reflect.DeepEqual(images, wantImages) {
		t.Fatalf("team images = %#v, want %#v", images, wantImages)
	}
	emptyEdit := retirementEditByPath(t, plan.Edits, empty.Path)
	if links := parsedRetirementCatalogLinks(emptyEdit.Content); len(links) != 0 {
		t.Fatalf("empty catalog links = %#v, want none", links)
	}
	if slices.ContainsFunc(plan.Edits, func(edit knowl.FileEdit) bool { return edit.Path == root.Path }) {
		t.Fatalf("root without a direct retiring target entered closure: %#v", plan.Edits)
	}
}

func TestBuildRetirementEditPlanRejectsMixedRetiringCatalogItemsWithoutAPlan(t *testing.T) {
	tests := []struct {
		name     string
		entries  string
		children []string
	}{
		{name: "trailing text", entries: "* [Remove](../../concepts/fresh.md) — curated metadata\n"},
		{name: "unrelated link", entries: "* [Remove](../../concepts/fresh.md) and [Keep](../../concepts/kept.md)\n", children: []string{retirementTestKeptPage}},
		{name: "multiple target links", entries: "* [Remove one](../../concepts/fresh.md) [Remove two](../../concepts/fresh.md#details)\n"},
		{name: "target parent with nested sibling", entries: "* [Remove](../../concepts/fresh.md)\n  * [Keep](../../concepts/kept.md)\n", children: []string{retirementTestKeptPage}},
		{name: "whitespace-only label", entries: "* [   ](../../concepts/fresh.md)\n"},
		{name: "target image inside kept link", entries: "* [Keep ![retiring image](../../concepts/fresh.md)](../../concepts/kept.md)\n", children: []string{retirementTestKeptPage}},
		{name: "safe link plus opaque wiki target", entries: "* [Keep](../../concepts/kept.md)\n  [[../../concepts/fresh.md]]\n* [Remove](../../concepts/fresh.md)\n", children: []string{retirementTestKeptPage}},
		{name: "emphasis label", entries: "* [**Remove**](../../concepts/fresh.md)\n"},
		{name: "code label", entries: "* [`Remove`](../../concepts/fresh.md)\n"},
		{name: "reference link", entries: "* [Remove][retiring]\n\n[retiring]: ../../concepts/fresh.md\n"},
		{name: "title metadata", entries: "* [Remove](../../concepts/fresh.md \"curated\")\n"},
		{name: "html label", entries: "* [<span>Remove</span>](../../concepts/fresh.md)\n"},
		{name: "autolink label", entries: "* [<https://example.invalid/remove>](../../concepts/fresh.md)\n"},
		{name: "indented code", entries: "* Kept context\n\n      [Remove](../../concepts/fresh.md)\n"},
		{name: "task marker", entries: "* [ ] [Remove](../../concepts/fresh.md)\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, planner, request, result, _ := plannedOldOnlyRetirement(t)
			root := retirementCatalogSnapshot(testRootCatalogPath, retirementTestRootTitle, []retirementCatalogLink{{label: "Team", destination: retirementTestTeamTarget}})
			team := retirementRawCatalogSnapshot(retirementTestTeamCatalog, "Team", "# Team\n"+test.entries)
			fixture.content.catalogs = map[knowl.PageID]knowl.PageSnapshot{root.ID: root, team.ID: team}
			request.Catalogs = []knowl.HierarchyCatalog{
				{Path: root.Path, Digest: root.Digest, Title: root.Title, Children: []string{team.Path}},
				{Path: team.Path, Digest: team.Digest, Title: team.Title, Children: append([]string{fixture.page.Path}, test.children...)},
			}

			plan, err := planner.BuildRetirementEditPlan(t.Context(), "catalog-mixed", request, result)
			if !errors.Is(err, app.ErrRetirementCatalogMixedItem) || !reflect.DeepEqual(plan, knowl.ValidatedEditPlan{}) {
				t.Fatalf("BuildRetirementEditPlan() = %#v, %v; want mixed-item rejection", plan, err)
			}
			var invalid *app.RetirementUnitError
			want := []knowl.MaintenanceDiagnostic{{Code: app.RetirementCatalogMixedItemCode, Path: team.Path, Target: fixture.page.Path}}
			if !errors.As(err, &invalid) || !reflect.DeepEqual(invalid.Diagnostics, want) {
				t.Fatalf("mixed-item error = %#v, want diagnostics %#v", invalid, want)
			}
		})
	}
}

func TestBuildRetirementEditPlanRemovesExactNestedLeafAndPreservesParentAndSibling(t *testing.T) {
	fixture, planner, request, result, _ := plannedOldOnlyRetirement(t)
	root := retirementCatalogSnapshot(testRootCatalogPath, retirementTestRootTitle, []retirementCatalogLink{{label: "Team", destination: retirementTestTeamTarget}})
	team := retirementRawCatalogSnapshot(retirementTestTeamCatalog, "Team", "# Team\n"+
		"* [Parent](../../concepts/kept.md)\n"+
		"  * [Remove](../../concepts/fresh.md?q=1#details)\n"+
		"  * [Sibling](../../concepts/sibling.md)\n")
	fixture.content.catalogs = map[knowl.PageID]knowl.PageSnapshot{root.ID: root, team.ID: team}
	request.Catalogs = []knowl.HierarchyCatalog{
		{Path: root.Path, Digest: root.Digest, Title: root.Title, Children: []string{team.Path}},
		{Path: team.Path, Digest: team.Digest, Title: team.Title, Children: []string{fixture.page.Path, retirementTestKeptPage, "wiki/concepts/sibling.md"}},
	}

	plan, err := planner.BuildRetirementEditPlan(t.Context(), "catalog-nested-leaf", request, result)
	if err != nil {
		t.Fatal(err)
	}
	edit := retirementEditByPath(t, plan.Edits, team.Path)
	want := []retirementCatalogLink{
		{label: "Parent", destination: retirementTestKeptTarget},
		{label: "Sibling", destination: "../../concepts/sibling.md"},
	}
	if links := parsedRetirementCatalogLinks(edit.Content); !reflect.DeepEqual(links, want) {
		t.Fatalf("nested catalog links = %#v, want %#v", links, want)
	}
}

func TestBuildRetirementEditPlanAllowsEmptyRootAfterExactLeafRemoval(t *testing.T) {
	_, planner, request, result, _ := plannedOldOnlyRetirement(t)
	plan, err := planner.BuildRetirementEditPlan(t.Context(), "catalog-empty-root", request, result)
	if err != nil {
		t.Fatal(err)
	}
	root := retirementEditByPath(t, plan.Edits, testRootCatalogPath)
	index, err := okf.ValidateIndex("index.md", root.Content, okf.DefaultLimits())
	if err != nil {
		t.Fatalf("empty root index validation error = %v", err)
	}
	if index.ObservedVersion == "" || len(parsedRetirementCatalogLinks(root.Content)) != 0 {
		t.Fatalf("empty root index = %#v, links=%#v", index, parsedRetirementCatalogLinks(root.Content))
	}
}

func retirementRawCatalogSnapshot(catalogPath, title, content string) knowl.PageSnapshot {
	digest := sha256.Sum256([]byte(content))
	id := strings.TrimSuffix(strings.TrimPrefix(catalogPath, "wiki/"), ".md")
	return knowl.PageSnapshot{
		ID: knowl.PageID(id), Path: catalogPath, Digest: hex.EncodeToString(digest[:]), Title: title,
		Content: content, Body: content, Untrusted: true,
	}
}

func TestBuildRetirementEditPlanRejectsFreshCatalogDrift(t *testing.T) {
	fixture, planner, request, result, _ := plannedOldOnlyRetirement(t)
	catalogID := knowl.PageID(strings.TrimSuffix(strings.TrimPrefix(request.Catalogs[0].Path, "wiki/"), ".md"))
	current := fixture.content.catalogs[catalogID]
	current.Content += "* [Concurrent](concepts/concurrent.md)\n"
	digest := sha256.Sum256([]byte(current.Content))
	current.Digest = hex.EncodeToString(digest[:])
	fixture.content.catalogs[catalogID] = current

	plan, err := planner.BuildRetirementEditPlan(t.Context(), "catalog-stale", request, result)
	if !errors.Is(err, app.ErrRetirementPlanInvalid) || len(plan.Edits) != 0 {
		t.Fatalf("BuildRetirementEditPlan() = %#v, %v; want stale catalog rejection", plan, err)
	}
}

func TestBuildRetirementEditPlanRejectsCatalogClosureOverPlanLimitWithTypedDiagnostic(t *testing.T) {
	fixture := newRetirementFixture(t)
	retiring := retirementAccepted("wiki-filesystem", "old", retirementTestLegacySourceID, "v1", "text/markdown")
	fixture.page = retirementPage(t, "concepts/old-only", []string{app.SourceRefKey(retiring)}, "Old facts.")
	owner := retirementOwner(retiring)
	fixture.states[owner] = retirementLifecycle(owner, knowl.SourceLifecycleRetiring, fixture.authority)
	request := fixture.request([]knowl.AcceptedSource{retiring}, owner)
	root := retirementCatalogSnapshot(testRootCatalogPath, retirementTestRootTitle, []retirementCatalogLink{{label: "Old root", destination: "concepts/old-only.md"}})
	team := retirementCatalogSnapshot(retirementTestTeamCatalog, "Team", []retirementCatalogLink{{label: "Old team", destination: "../../concepts/old-only.md"}})
	fixture.content.catalogs = map[knowl.PageID]knowl.PageSnapshot{root.ID: root, team.ID: team}
	request.Catalogs = []knowl.HierarchyCatalog{
		{Path: root.Path, Digest: root.Digest, Title: root.Title, Children: []string{fixture.page.Path}},
		{Path: team.Path, Digest: team.Digest, Title: team.Title, Children: []string{fixture.page.Path}},
	}
	planner := fixture.planner()
	result, err := planner.PlanRetirement(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	limits := app.DefaultPlanLimits()
	limits.MaxFiles = 2

	planner, err = app.NewRetirementPlanner(&fixture.content, fixture.lifecycle(), &fixture.maintainer, app.RetirementPlannerOptions{PlanLimits: limits})
	if err != nil {
		t.Fatal(err)
	}
	_, err = planner.BuildRetirementEditPlan(t.Context(), "retire-limit", request, result)
	if !errors.Is(err, app.ErrRetirementCatalogClosureLimit) {
		t.Fatalf("BuildRetirementEditPlan() error = %v, want closure limit", err)
	}
	var invalid *app.RetirementUnitError
	if !errors.As(err, &invalid) {
		t.Fatalf("error type = %T, want *RetirementUnitError", err)
	}
	want := []knowl.MaintenanceDiagnostic{{Code: app.RetirementCatalogClosureLimitCode, Path: fixture.page.Path, Target: testRootCatalogPath}}
	if !reflect.DeepEqual(invalid.Diagnostics, want) {
		t.Fatalf("diagnostics = %#v, want %#v", invalid.Diagnostics, want)
	}

	limits = app.DefaultPlanLimits()
	limits.MaxFileBytes = 1
	planner, err = app.NewRetirementPlanner(&fixture.content, fixture.lifecycle(), &fixture.maintainer, app.RetirementPlannerOptions{PlanLimits: limits})
	if err != nil {
		t.Fatal(err)
	}
	_, err = planner.BuildRetirementEditPlan(t.Context(), "retire-byte-limit", request, result)
	if !errors.Is(err, app.ErrRetirementCatalogClosureLimit) {
		t.Fatalf("BuildRetirementEditPlan() byte-limit error = %v, want closure limit", err)
	}
	invalid = nil
	if !errors.As(err, &invalid) || !reflect.DeepEqual(invalid.Diagnostics, []knowl.MaintenanceDiagnostic{{Code: app.RetirementCatalogClosureLimitCode, Path: fixture.page.Path, Target: retirementTestTeamCatalog}}) {
		t.Fatalf("byte-limit error = %#v", invalid)
	}
}

func retirementEditByPath(t *testing.T, edits []knowl.FileEdit, path string) knowl.FileEdit {
	t.Helper()
	for _, edit := range edits {
		if edit.Path == path {
			return edit
		}
	}
	t.Fatalf("edit %q missing from %#v", path, edits)
	return knowl.FileEdit{}
}
