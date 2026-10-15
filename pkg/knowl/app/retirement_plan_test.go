package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
)

const retirementTestScope knowl.ScopeRef = "retirement-test"

type retirementContentFixture struct {
	page           knowl.PageSnapshot
	catalogs       map[knowl.PageID]knowl.PageSnapshot
	accepted       map[string]knowl.AcceptedSource
	resolveErr     map[string]error
	resolveRefs    []string
	resolveBatches [][]string
	raw            map[string][]byte
	readErr        map[string]error
	readRefs       []string
	readPages      int
	readLimits     []knowl.ReadLimits
	afterResolve   func()
	afterRead      func()
}

func (fixture *retirementContentFixture) ResolveAcceptedSources(ctx context.Context, scope knowl.ScopeRef, refs []string) (map[string]knowl.AcceptedSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fixture.resolveRefs = append(fixture.resolveRefs, refs...)
	fixture.resolveBatches = append(fixture.resolveBatches, slices.Clone(refs))
	if scope != retirementTestScope {
		return nil, app.ErrSourceInvalid
	}
	result := make(map[string]knowl.AcceptedSource, len(refs))
	for _, ref := range refs {
		if err := fixture.resolveErr[ref]; err != nil {
			return nil, err
		}
		if source, ok := fixture.accepted[ref]; ok {
			result[ref] = source
		}
	}
	if fixture.afterResolve != nil {
		fixture.afterResolve()
	}
	return result, nil
}

func (fixture *retirementContentFixture) ReadPages(ctx context.Context, scope knowl.ScopeRef, ids []knowl.PageID, limits knowl.ReadLimits) ([]knowl.PageSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fixture.readPages++
	fixture.readLimits = append(fixture.readLimits, limits)
	if scope != retirementTestScope {
		return nil, app.ErrPlanInvalid
	}
	pages := make([]knowl.PageSnapshot, 0, len(ids))
	for _, id := range ids {
		if id == fixture.page.ID {
			pages = append(pages, cloneRetirementPage(fixture.page))
			continue
		}
		catalog, ok := fixture.catalogs[id]
		if !ok {
			return nil, app.ErrPlanInvalid
		}
		pages = append(pages, cloneRetirementPage(catalog))
	}
	return pages, nil
}

func (fixture *retirementContentFixture) ReadSource(ctx context.Context, source knowl.AcceptedSource, _ knowl.ReadLimits) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ref := app.SourceRefKey(source)
	fixture.readRefs = append(fixture.readRefs, ref)
	if err := fixture.readErr[ref]; err != nil {
		return nil, err
	}
	content, ok := fixture.raw[ref]
	if !ok {
		return nil, app.ErrSourceNotFound
	}
	if fixture.afterRead != nil {
		fixture.afterRead()
	}
	return slices.Clone(content), nil
}

type retirementLifecycleFixture struct {
	authority      knowl.SourceLifecycleAuthority
	states         map[knowl.SourceOwner]knowl.SourceLifecycle
	afterAuthority func()
	afterLifecycle func()
	lifecycleCalls *int
}

type retirementLiveLifecycleFixture struct{ fixture *retirementFixture }

func (reader retirementLiveLifecycleFixture) SourceLifecycleAuthority(ctx context.Context, scope knowl.ScopeRef) (knowl.SourceLifecycleAuthority, error) {
	if err := ctx.Err(); err != nil {
		return knowl.SourceLifecycleAuthority{}, err
	}
	if scope != retirementTestScope {
		return knowl.SourceLifecycleAuthority{}, app.ErrSourceLifecycleInvalid
	}
	return reader.fixture.authority, nil
}

func (reader retirementLiveLifecycleFixture) SourceLifecycle(ctx context.Context, scope knowl.ScopeRef, owner knowl.SourceOwner) (knowl.SourceLifecycle, error) {
	if err := ctx.Err(); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	state, ok := reader.fixture.states[owner]
	if scope != retirementTestScope || !ok {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleUnowned
	}
	return state, nil
}

func (fixture retirementLifecycleFixture) SourceLifecycleAuthority(ctx context.Context, scope knowl.ScopeRef) (knowl.SourceLifecycleAuthority, error) {
	if err := ctx.Err(); err != nil {
		return knowl.SourceLifecycleAuthority{}, err
	}
	if scope != retirementTestScope {
		return knowl.SourceLifecycleAuthority{}, app.ErrSourceLifecycleInvalid
	}
	if fixture.afterAuthority != nil {
		fixture.afterAuthority()
	}
	return fixture.authority, nil
}

func (fixture retirementLifecycleFixture) SourceLifecycle(ctx context.Context, scope knowl.ScopeRef, owner knowl.SourceOwner) (knowl.SourceLifecycle, error) {
	if fixture.lifecycleCalls != nil {
		*fixture.lifecycleCalls++
	}
	if err := ctx.Err(); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	state, ok := fixture.states[owner]
	if scope != retirementTestScope || !ok {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleUnowned
	}
	if fixture.afterLifecycle != nil {
		fixture.afterLifecycle()
	}
	return state, nil
}

type retirementMaintainerFixture struct {
	result    knowl.RetirementModelPlan
	err       error
	inputs    []knowl.RetirementInput
	size      int
	budget    knowl.MaintenanceRequestBudget
	afterPlan func()
	afterSize func()
	planCalls int
	sizeCalls int
}

func (*retirementMaintainerFixture) Plan(context.Context, knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	return knowl.ModelEditPlan{}, nil
}

func (fixture *retirementMaintainerFixture) PlanRetirement(ctx context.Context, input knowl.RetirementInput) (knowl.RetirementModelPlan, error) {
	fixture.planCalls++
	if err := ctx.Err(); err != nil {
		return knowl.RetirementModelPlan{}, err
	}
	fixture.inputs = append(fixture.inputs, cloneRetirementInput(input))
	if fixture.afterPlan != nil {
		fixture.afterPlan()
	}
	return cloneRetirementModelPlan(fixture.result), fixture.err
}

func (fixture *retirementMaintainerFixture) RetirementRequestBudget() knowl.MaintenanceRequestBudget {
	return fixture.budget
}

func (fixture *retirementMaintainerFixture) RetirementRequestBytes(ctx context.Context, _ knowl.RetirementInput) (int, error) {
	fixture.sizeCalls++
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if fixture.afterSize != nil {
		fixture.afterSize()
	}
	return fixture.size, nil
}

type ordinaryOnlyRetirementMaintainer struct{}

func (ordinaryOnlyRetirementMaintainer) Plan(context.Context, knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	return knowl.ModelEditPlan{}, nil
}

func TestRetirementPlannerSuppliesOnlySortedActiveEvidenceAndValidatesReplacement(t *testing.T) {
	fixture := newRetirementFixture(t)
	activeOne := retirementAccepted("git", "shared", "documents/two", "v2", "text/markdown")
	activeTwo := retirementAccepted("git", "shared", "documents/one", "v1", "text/plain; charset=utf-8")
	retiringOne := retirementAccepted("wiki-filesystem", "shared", "legacy/a", "old-1", "text/markdown")
	retiringTwo := retirementAccepted("wiki-filesystem", "shared", "legacy/b", "old-2", "text/markdown")
	refs := []string{app.SourceRefKey(retiringTwo), app.SourceRefKey(activeOne), app.SourceRefKey(retiringOne), app.SourceRefKey(activeTwo)}
	fixture.page = retirementPage(t, "concepts/shared", refs, "Current mixed facts.")
	fixture.content.raw[app.SourceRefKey(activeOne)] = []byte("active two")
	fixture.content.raw[app.SourceRefKey(activeTwo)] = []byte("active one")
	fixture.content.raw[app.SourceRefKey(retiringOne)] = []byte("must not load one")
	fixture.content.raw[app.SourceRefKey(retiringTwo)] = []byte("must not load two")

	activeOwner := retirementOwner(activeOne)
	retiringOwner := retirementOwner(retiringOne)
	fixture.states[activeOwner] = retirementLifecycle(activeOwner, knowl.SourceLifecycleActive, fixture.authority)
	fixture.states[retiringOwner] = retirementLifecycle(retiringOwner, knowl.SourceLifecycleRetiring, fixture.authority)
	wantRefs := []string{app.SourceRefKey(activeTwo), app.SourceRefKey(activeOne)}
	slices.Sort(wantRefs)
	fixture.maintainer.result = replacementRetirementPlan(fixture.page, fixture.schema.Digest, wantRefs)

	request := fixture.request([]knowl.AcceptedSource{retiringTwo, activeOne, retiringOne, activeTwo}, retiringOwner)
	result, err := fixture.planner().PlanRetirement(t.Context(), request)
	if err != nil {
		t.Fatalf("PlanRetirement() error = %v", err)
	}
	if result.Action != knowl.RetirementReplace || result.Page.ID != fixture.page.ID || result.Page.Path != fixture.page.Path || result.Page.Digest != fixture.page.Digest || !reflect.DeepEqual(result.SourceRefs, wantRefs) || len(result.Diagnostics) != 0 {
		t.Fatalf("PlanRetirement() result = %#v", result)
	}
	if len(fixture.maintainer.inputs) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(fixture.maintainer.inputs))
	}
	input := fixture.maintainer.inputs[0]
	gotRefs := make([]string, len(input.Evidence))
	gotTexts := make([]string, len(input.Evidence))
	for index, evidence := range input.Evidence {
		gotRefs[index], gotTexts[index] = evidence.SourceRef, evidence.Text
	}
	if !reflect.DeepEqual(gotRefs, wantRefs) || !reflect.DeepEqual(gotTexts, []string{"active one", "active two"}) {
		t.Fatalf("provider evidence refs/texts = %#v / %#v", gotRefs, gotTexts)
	}
	if !reflect.DeepEqual(fixture.content.readRefs, wantRefs) {
		t.Fatalf("ReadSource refs = %#v, want active refs %#v", fixture.content.readRefs, wantRefs)
	}
	if input.Page.Content != fixture.page.Content || input.Page.Body != fixture.page.Body || input.ContractVersion != app.RetirementMaintenanceContractVersion {
		t.Fatalf("provider current page/contract = %#v", input)
	}
	metadata, parseErr := knowlwiki.ParseFrontmatter(result.Page.Content)
	if parseErr != nil || metadata.ID != string(fixture.page.ID) || !reflect.DeepEqual(metadata.SourceRefs, wantRefs) {
		t.Fatalf("replacement metadata = %#v, error = %v", metadata, parseErr)
	}

	firstInput := cloneRetirementInput(input)
	firstResult := result
	fixture.maintainer.inputs = nil
	fixture.content.readRefs = nil
	request.SourceRefs = []string{app.SourceRefKey(activeTwo), app.SourceRefKey(retiringOne), app.SourceRefKey(activeOne), app.SourceRefKey(retiringTwo)}
	repeated, err := fixture.planner().PlanRetirement(t.Context(), request)
	if err != nil {
		t.Fatalf("PlanRetirement() permuted error = %v", err)
	}
	if !reflect.DeepEqual(fixture.maintainer.inputs, []knowl.RetirementInput{firstInput}) || !reflect.DeepEqual(repeated, firstResult) {
		t.Fatalf("permuted evidence changed request/result: input=%#v result=%#v", fixture.maintainer.inputs, repeated)
	}
}

func TestRetirementPlannerDeletesZeroActiveSupportWithoutProviderOrRawReads(t *testing.T) {
	fixture := newRetirementFixture(t)
	first := retirementAccepted("wiki-filesystem", "legacy-a", "a", "one", "text/markdown")
	second := retirementAccepted("wiki-filesystem", "legacy-b", "b", "two", "text/markdown")
	fixture.page = retirementPage(t, "concepts/old-only", []string{app.SourceRefKey(first), app.SourceRefKey(second)}, "Old facts.")
	firstOwner, secondOwner := retirementOwner(first), retirementOwner(second)
	fixture.states[firstOwner] = retirementLifecycle(firstOwner, knowl.SourceLifecycleRetiring, fixture.authority)
	fixture.states[secondOwner] = retirementLifecycle(secondOwner, knowl.SourceLifecycleRetiring, fixture.authority)

	planner, err := app.NewRetirementPlanner(&fixture.content, fixture.lifecycle(), ordinaryOnlyRetirementMaintainer{}, app.RetirementPlannerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := planner.PlanRetirement(t.Context(), fixture.requestFor([]knowl.AcceptedSource{second, first}, []knowl.SourceOwner{secondOwner, firstOwner}))
	if err != nil {
		t.Fatalf("PlanRetirement() error = %v", err)
	}
	if result.Action != knowl.RetirementDelete || result.Page.ID != fixture.page.ID || result.Page.Path != fixture.page.Path || result.Page.Digest != fixture.page.Digest || len(result.SourceRefs) != 0 || len(result.Page.Content) != 0 || len(fixture.content.readRefs) != 0 {
		t.Fatalf("delete result/read refs = %#v / %#v", result, fixture.content.readRefs)
	}
}

func TestRetirementPlannerReportsUnavailableProviderOnlyForMixedPage(t *testing.T) {
	fixture := newRetirementFixture(t)
	active := retirementAccepted("git", "new", "a", "one", "text/markdown")
	retiring := retirementAccepted("wiki-filesystem", "old", "b", "two", "text/markdown")
	fixture.page = retirementPage(t, "concepts/mixed", []string{app.SourceRefKey(retiring), app.SourceRefKey(active)}, "Mixed facts.")
	activeOwner, retiringOwner := retirementOwner(active), retirementOwner(retiring)
	fixture.states[activeOwner] = retirementLifecycle(activeOwner, knowl.SourceLifecycleActive, fixture.authority)
	fixture.states[retiringOwner] = retirementLifecycle(retiringOwner, knowl.SourceLifecycleRetiring, fixture.authority)
	planner, err := app.NewRetirementPlanner(&fixture.content, fixture.lifecycle(), ordinaryOnlyRetirementMaintainer{}, app.RetirementPlannerOptions{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := planner.PlanRetirement(t.Context(), fixture.request([]knowl.AcceptedSource{active, retiring}, retiringOwner))
	assertRetirementFailure(t, result, err, app.ErrRetirementMaintainerUnavailable, app.RetirementMaintainerUnavailableCode, fixture.page.Path, "")
	if len(fixture.content.readRefs) != 0 {
		t.Fatalf("provider absence still read evidence: %#v", fixture.content.readRefs)
	}
}

func TestRetirementPlannerAcceptsExplicitProviderDelete(t *testing.T) {
	fixture, _, _, request := mixedRetirementFixture(t)
	fixture.maintainer.result = knowl.RetirementModelPlan{SchemaDigest: fixture.schema.Digest, Delete: true, SourceRefs: []string{}, Pages: []knowl.FileEdit{}}

	result, err := fixture.planner().PlanRetirement(t.Context(), request)
	if err != nil {
		t.Fatalf("PlanRetirement() error = %v", err)
	}
	if result.Action != knowl.RetirementDelete || len(result.Page.Content) != 0 || len(result.SourceRefs) != 0 || len(result.Diagnostics) != 0 || len(fixture.maintainer.inputs) != 1 {
		t.Fatalf("provider delete result/calls = %#v / %d", result, len(fixture.maintainer.inputs))
	}
}

func TestRetirementPlannerFailsClosedForEvidenceAndLifecycleProblems(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*retirementFixture, *app.RetirementPlanningRequest, knowl.AcceptedSource, knowl.AcceptedSource)
		wantError  error
		wantCode   string
		wantTarget func(knowl.AcceptedSource, knowl.AcceptedSource) string
	}{
		{
			name: "unknown accepted source", wantError: app.ErrRetirementPlanInvalid, wantCode: knowl.DiagnosticCitationUnknownSource,
			mutate: func(_ *retirementFixture, request *app.RetirementPlanningRequest, active, _ knowl.AcceptedSource) {
				request.SourceRefs = slices.DeleteFunc(request.SourceRefs, func(ref string) bool { return ref == app.SourceRefKey(active) })
			},
			wantTarget: func(active, _ knowl.AcceptedSource) string { return app.SourceRefKey(active) },
		},
		{
			name: "inactive evidence", wantError: app.ErrRetirementPlanInvalid, wantCode: app.CitationSourceRetiringCode,
			mutate: func(fixture *retirementFixture, _ *app.RetirementPlanningRequest, active, _ knowl.AcceptedSource) {
				state := fixture.states[retirementOwner(active)]
				state.State = knowl.SourceLifecycleDeleted
				state.DeletedAt = state.UpdatedAt
				fixture.states[retirementOwner(active)] = state
			},
			wantTarget: func(active, _ knowl.AcceptedSource) string { return app.SourceRefKey(active) },
		},
		{
			name: "missing raw evidence", wantError: app.ErrRetirementEvidenceUnavailable, wantCode: app.RetirementEvidenceUnavailableCode,
			mutate: func(fixture *retirementFixture, _ *app.RetirementPlanningRequest, active, _ knowl.AcceptedSource) {
				fixture.content.readErr[app.SourceRefKey(active)] = app.ErrSourceNotFound
			},
			wantTarget: func(active, _ knowl.AcceptedSource) string { return app.SourceRefKey(active) },
		},
		{
			name: "unsupported raw evidence", wantError: app.ErrRetirementEvidenceUnsupported, wantCode: app.RetirementEvidenceUnsupportedCode,
			mutate: func(fixture *retirementFixture, _ *app.RetirementPlanningRequest, active, _ knowl.AcceptedSource) {
				source := fixture.content.accepted[app.SourceRefKey(active)]
				source.MediaType = "application/octet-stream"
				fixture.content.accepted[app.SourceRefKey(active)] = source
			},
			wantTarget: func(active, _ knowl.AcceptedSource) string { return app.SourceRefKey(active) },
		},
		{
			name: "stale authority", wantError: app.ErrRetirementLifecycleConflict, wantCode: app.RetirementLifecycleConflictCode,
			mutate: func(_ *retirementFixture, request *app.RetirementPlanningRequest, _, _ knowl.AcceptedSource) {
				request.Authority.Epoch--
			},
			wantTarget: func(_, _ knowl.AcceptedSource) string { return "" },
		},
		{
			name: "stale retirement generation", wantError: app.ErrRetirementLifecycleConflict, wantCode: app.RetirementLifecycleConflictCode,
			mutate: func(_ *retirementFixture, request *app.RetirementPlanningRequest, _, _ knowl.AcceptedSource) {
				request.Retiring[0].RetirementGeneration = strings.Repeat("9", 64)
			},
			wantTarget: func(_, retiring knowl.AcceptedSource) string { return app.SourceRefKey(retiring) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRetirementFixture(t)
			active := retirementAccepted("git", "active", "doc", "one", "text/markdown")
			retiring := retirementAccepted("wiki-filesystem", "old", "doc", "old", "text/markdown")
			fixture.page = retirementPage(t, "concepts/mixed", []string{app.SourceRefKey(retiring), app.SourceRefKey(active)}, "Mixed facts.")
			fixture.content.raw[app.SourceRefKey(active)] = []byte("active")
			activeOwner, retiringOwner := retirementOwner(active), retirementOwner(retiring)
			fixture.states[activeOwner] = retirementLifecycle(activeOwner, knowl.SourceLifecycleActive, fixture.authority)
			fixture.states[retiringOwner] = retirementLifecycle(retiringOwner, knowl.SourceLifecycleRetiring, fixture.authority)
			fixture.maintainer.result = replacementRetirementPlan(fixture.page, fixture.schema.Digest, []string{app.SourceRefKey(active)})
			request := fixture.request([]knowl.AcceptedSource{active, retiring}, retiringOwner)
			test.mutate(fixture, &request, active, retiring)

			result, err := fixture.planner().PlanRetirement(t.Context(), request)
			assertRetirementFailure(t, result, err, test.wantError, test.wantCode, fixture.page.Path, test.wantTarget(active, retiring))
			if test.name == "unsupported raw evidence" && len(fixture.content.readRefs) != 0 {
				t.Fatalf("unsupported media read raw evidence: %#v", fixture.content.readRefs)
			}
		})
	}
}

func TestRetirementPlannerRejectsSemanticAliasDuplicatesBeforeEvidenceRead(t *testing.T) {
	var wantResult knowl.RetirementPlanResult
	for _, reverse := range []bool{false, true} {
		fixture := newRetirementFixture(t)
		active := retirementAccepted("git", "new", "a@b", "v2", "text/markdown")
		retiring := retirementAccepted("wiki-filesystem", "old", retirementTestLegacySourceID, "v1", "text/markdown")
		canonical := app.SourceRefKey(active)
		legacy := active.Source.Adapter + ":" + active.Source.ID + "@" + active.Version.Version
		refs := []string{app.SourceRefKey(retiring), legacy, canonical}
		if reverse {
			slices.Reverse(refs)
		}
		fixture.page = retirementPage(t, "concepts/alias-duplicate", refs, "Mixed facts.")
		activeOwner, retiringOwner := retirementOwner(active), retirementOwner(retiring)
		fixture.states[activeOwner] = retirementLifecycle(activeOwner, knowl.SourceLifecycleActive, fixture.authority)
		fixture.states[retiringOwner] = retirementLifecycle(retiringOwner, knowl.SourceLifecycleRetiring, fixture.authority)
		request := fixture.request([]knowl.AcceptedSource{retiring, active}, retiringOwner)
		request.SourceRefs = slices.Clone(refs)
		fixture.content.accepted[legacy] = active
		fixture.content.resolveErr[canonical] = &app.SourceRefDuplicateError{Ref: canonical}

		result, err := fixture.planner().PlanRetirement(t.Context(), request)
		assertRetirementFailure(t, result, err, app.ErrRetirementPlanInvalid, knowl.DiagnosticCitationUnknownSource, fixture.page.Path, canonical)
		if reverse && !reflect.DeepEqual(result, wantResult) {
			t.Fatalf("reverse alias order result = %#v; want %#v", result, wantResult)
		}
		wantResult = result
		if !errors.Is(err, app.ErrSourceRefDuplicate) {
			t.Fatalf("PlanRetirement() error = %v; want typed duplicate evidence", err)
		}
		if len(fixture.content.readRefs) != 0 || fixture.maintainer.planCalls != 0 {
			t.Fatalf("duplicate aliases reached raw/provider: reads=%#v calls=%d", fixture.content.readRefs, fixture.maintainer.planCalls)
		}
	}
}

func TestRetirementPlannerAuthenticatesResolvedAcceptedDescriptors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*knowl.AcceptedSource, knowl.AcceptedSource)
	}{
		{name: "forged owner", mutate: func(source *knowl.AcceptedSource, active knowl.AcceptedSource) {
			source.SourceDocument.SourceID = active.SourceDocument.SourceID
		}},
		{name: "blank digest", mutate: func(source *knowl.AcceptedSource, _ knowl.AcceptedSource) { source.Version.Digest = "" }},
		{name: "wrong manifest", mutate: func(source *knowl.AcceptedSource, _ knowl.AcceptedSource) {
			source.ManifestRef = "raw/accepted/version/forged.yaml"
		}},
		{name: "blank media", mutate: func(source *knowl.AcceptedSource, _ knowl.AcceptedSource) { source.MediaType = "" }},
		{name: "wrong raw path", mutate: func(source *knowl.AcceptedSource, _ knowl.AcceptedSource) { source.Source.ID = "old/forged-raw.md" }},
		{name: "wrong document path", mutate: func(source *knowl.AcceptedSource, _ knowl.AcceptedSource) {
			source.SourceDocument.DocumentID = "forged-document.md"
		}},
		{name: "wrong revision", mutate: func(source *knowl.AcceptedSource, _ knowl.AcceptedSource) {
			source.SourceDocument.Revision = "forged-revision"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, active, retiring, request := mixedRetirementFixture(t)
			ref := app.SourceRefKey(retiring)
			resolved := fixture.content.accepted[ref]
			test.mutate(&resolved, active)
			fixture.content.accepted[ref] = resolved

			result, err := fixture.planner().PlanRetirement(t.Context(), request)
			assertRetirementFailure(t, result, err, app.ErrRetirementPlanInvalid, knowl.DiagnosticCitationUnknownSource, fixture.page.Path, ref)
			if len(fixture.content.readRefs) != 0 || len(fixture.maintainer.inputs) != 0 {
				t.Fatalf("unauthenticated descriptor reached raw/provider: reads=%#v calls=%d", fixture.content.readRefs, len(fixture.maintainer.inputs))
			}
		})
	}
}

func TestRetirementPlannerCanonicalizesInvalidRequestedRefs(t *testing.T) {
	fixture, _, _, request := mixedRetirementFixture(t)
	request.SourceRefs = append(request.SourceRefs, "git:z@v1", "git:a@v1")
	first, firstErr := fixture.planner().PlanRetirement(t.Context(), request)
	slices.Reverse(request.SourceRefs)
	second, secondErr := fixture.planner().PlanRetirement(t.Context(), request)
	firstJSON, firstMarshalErr := json.Marshal(first)
	secondJSON, secondMarshalErr := json.Marshal(second)
	if !errors.Is(firstErr, app.ErrRetirementPlanInvalid) || !errors.Is(secondErr, app.ErrRetirementPlanInvalid) ||
		firstMarshalErr != nil || secondMarshalErr != nil || !slices.Equal(firstJSON, secondJSON) ||
		!reflect.DeepEqual(first, second) || first.Diagnostics[0].Target != "git:a@v1" {
		t.Fatalf("permuted invalid refs changed failure: first=%#v/%v second=%#v/%v", first, firstErr, second, secondErr)
	}
}

func TestRetirementPlannerPropagatesCancellationAtEveryAsyncBoundary(t *testing.T) {
	tests := []struct {
		name              string
		arm               func(*retirementFixture, context.CancelFunc)
		wantProviderCalls int
	}{
		{name: "initial authority", arm: func(fixture *retirementFixture, cancel context.CancelFunc) { fixture.afterAuthority = cancel }},
		{name: "accepted source resolution", arm: func(fixture *retirementFixture, cancel context.CancelFunc) { fixture.content.afterResolve = cancel }},
		{name: "owner lifecycle", arm: func(fixture *retirementFixture, cancel context.CancelFunc) { fixture.afterLifecycle = cancel }},
		{name: "raw evidence read", arm: func(fixture *retirementFixture, cancel context.CancelFunc) { fixture.content.afterRead = cancel }},
		{name: "request sizing", arm: func(fixture *retirementFixture, cancel context.CancelFunc) { fixture.maintainer.afterSize = cancel }},
		{name: "provider completion", wantProviderCalls: 1, arm: func(fixture *retirementFixture, cancel context.CancelFunc) { fixture.maintainer.afterPlan = cancel }},
		{name: "final authority", wantProviderCalls: 1, arm: func(fixture *retirementFixture, cancel context.CancelFunc) {
			calls := 0
			fixture.afterAuthority = func() {
				calls++
				if calls == 2 {
					cancel()
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, _, _, request := mixedRetirementFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			test.arm(fixture, cancel)
			result, err := fixture.planner().PlanRetirement(ctx, request)
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, knowl.RetirementPlanResult{}) || len(fixture.maintainer.inputs) != test.wantProviderCalls {
				t.Fatalf("cancellation normalized: result=%#v error=%v provider calls=%d", result, err, len(fixture.maintainer.inputs))
			}
			switch test.name {
			case "accepted source resolution":
				if fixture.lifecycleCalls != 0 {
					t.Fatalf("resolver cancellation reached lifecycle %d times", fixture.lifecycleCalls)
				}
			case "raw evidence read":
				if fixture.maintainer.sizeCalls != 0 {
					t.Fatalf("raw-read cancellation reached request sizing %d times", fixture.maintainer.sizeCalls)
				}
			case "request sizing":
				if fixture.maintainer.planCalls != 0 {
					t.Fatalf("request-sizing cancellation reached provider %d times", fixture.maintainer.planCalls)
				}
			}
		})
	}
	t.Run("provider deadline", func(t *testing.T) {
		fixture, _, _, request := mixedRetirementFixture(t)
		fixture.maintainer.err = context.DeadlineExceeded
		result, err := fixture.planner().PlanRetirement(t.Context(), request)
		if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(result, knowl.RetirementPlanResult{}) {
			t.Fatalf("deadline normalized: result=%#v error=%v", result, err)
		}
	})
}

func TestRetirementPlannerEnforcesRemainingActiveEvidenceBoundaries(t *testing.T) {
	for _, activeCount := range []int{0, 1, 64, 65} {
		t.Run(fmt.Sprintf("active-%d", activeCount), func(t *testing.T) {
			fixture := newRetirementFixture(t)
			retiring := retirementAccepted("wiki-filesystem", "old", retirementTestLegacySourceID, "old", "text/markdown")
			retiringOwner := retirementOwner(retiring)
			fixture.states[retiringOwner] = retirementLifecycle(retiringOwner, knowl.SourceLifecycleRetiring, fixture.authority)
			sources := []knowl.AcceptedSource{retiring}
			refs := []string{app.SourceRefKey(retiring)}
			activeRefs := make([]string, 0, activeCount)
			for index := 0; index < activeCount; index++ {
				active := retirementAccepted("git", "active", knowl.DocumentID("doc-"+strings.Repeat("x", index+1)), "v1", "text/markdown")
				ref := app.SourceRefKey(active)
				fixture.states[retirementOwner(active)] = retirementLifecycle(retirementOwner(active), knowl.SourceLifecycleActive, fixture.authority)
				fixture.content.raw[ref] = []byte("e")
				sources, refs, activeRefs = append(sources, active), append(refs, ref), append(activeRefs, ref)
			}
			fixture.page = retirementPage(t, "concepts/boundary", refs, "Facts.")
			slices.Sort(activeRefs)
			fixture.maintainer.result = replacementRetirementPlan(fixture.page, fixture.schema.Digest, activeRefs)
			result, err := fixture.planner().PlanRetirement(t.Context(), fixture.request(sources, retiringOwner))
			if activeCount == 65 {
				assertRetirementFailure(t, result, err, app.ErrRetirementEvidenceLimit, app.RetirementEvidenceLimitCode, fixture.page.Path, "")
				return
			}
			if err != nil {
				t.Fatalf("active refs %d: %v", activeCount, err)
			}
			wantAction := knowl.RetirementReplace
			if activeCount == 0 {
				wantAction = knowl.RetirementDelete
			}
			if result.Action != wantAction {
				t.Fatalf("active refs %d action=%q, want %q", activeCount, result.Action, wantAction)
			}
			if len(fixture.content.resolveBatches) != 1 || !slices.Equal(fixture.content.resolveBatches[0], fixture.content.resolveRefs) {
				t.Fatalf("%d current refs used resolver calls %#v; want one aggregate batch", len(refs), fixture.content.resolveBatches)
			}
		})
	}
}

func TestRetirementPlannerEnforcesAggregateEvidenceByteBoundary(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprintf("extra-%d", extra), func(t *testing.T) {
			fixture := newRetirementFixture(t)
			retiring := retirementAccepted("wiki-filesystem", "old", retirementTestLegacySourceID, "old", "text/markdown")
			first := retirementAccepted("git", "active", "one", "v1", "text/plain")
			second := retirementAccepted("git", "active", "two", "v1", "text/plain")
			sources := []knowl.AcceptedSource{retiring, first, second}
			refs := []string{app.SourceRefKey(retiring), app.SourceRefKey(first), app.SourceRefKey(second)}
			fixture.page = retirementPage(t, "concepts/byte-boundary", refs, "Facts.")
			fixture.states[retirementOwner(retiring)] = retirementLifecycle(retirementOwner(retiring), knowl.SourceLifecycleRetiring, fixture.authority)
			fixture.states[retirementOwner(first)] = retirementLifecycle(retirementOwner(first), knowl.SourceLifecycleActive, fixture.authority)
			fixture.content.raw[app.SourceRefKey(first)] = make([]byte, app.MaxRetirementEvidenceBytes-1)
			fixture.content.raw[app.SourceRefKey(second)] = make([]byte, 1+extra)
			activeRefs := []string{app.SourceRefKey(first), app.SourceRefKey(second)}
			slices.Sort(activeRefs)
			fixture.maintainer.result = replacementRetirementPlan(fixture.page, fixture.schema.Digest, activeRefs)
			result, err := fixture.planner().PlanRetirement(t.Context(), fixture.request(sources, retirementOwner(retiring)))
			if extra == 1 {
				assertRetirementFailure(t, result, err, app.ErrRetirementEvidenceLimit, app.RetirementEvidenceLimitCode, fixture.page.Path, activeRefs[1])
				return
			}
			if err != nil || result.Action != knowl.RetirementReplace {
				t.Fatalf("exact aggregate boundary result/error = %#v / %v", result, err)
			}
		})
	}
}

func TestRetirementPlannerPreservesUniqueLegacySourceRef(t *testing.T) {
	fixture := newRetirementFixture(t)
	retiring := retirementAccepted("wiki-filesystem", "old", retirementTestLegacySourceID, "old", "text/markdown")
	active := retirementAccepted("git", "active", "a@b.md", "v1", "text/markdown")
	legacyActiveRef := active.Source.Adapter + ":" + active.Source.ID + "@" + active.Version.Version
	if legacyActiveRef == app.SourceRefKey(active) {
		t.Fatalf("fixture did not create legacy/canonical distinction: %q", legacyActiveRef)
	}
	retiringOwner := retirementOwner(retiring)
	fixture.states[retiringOwner] = retirementLifecycle(retiringOwner, knowl.SourceLifecycleRetiring, fixture.authority)
	fixture.states[retirementOwner(active)] = retirementLifecycle(retirementOwner(active), knowl.SourceLifecycleActive, fixture.authority)
	fixture.content.raw[app.SourceRefKey(active)] = []byte("active facts")
	fixture.page = retirementPage(t, "concepts/legacy-ref", []string{app.SourceRefKey(retiring), legacyActiveRef}, "Facts.")
	request := fixture.request([]knowl.AcceptedSource{retiring, active}, retiringOwner)
	request.SourceRefs = slices.Clone(fixture.page.SourceRefs)
	fixture.content.accepted[legacyActiveRef] = active
	fixture.maintainer.result = replacementRetirementPlan(fixture.page, fixture.schema.Digest, []string{legacyActiveRef})

	result, err := fixture.planner().PlanRetirement(t.Context(), request)
	if err != nil || result.Action != knowl.RetirementReplace || len(fixture.maintainer.inputs) != 1 ||
		len(fixture.maintainer.inputs[0].Evidence) != 1 || fixture.maintainer.inputs[0].Evidence[0].SourceRef != legacyActiveRef {
		t.Fatalf("PlanRetirement() = %#v, %v; evidence=%#v", result, err, fixture.maintainer.inputs)
	}
}

func TestRetirementPlannerEnforcesEvidenceRequestOutputAndCancellationBounds(t *testing.T) {
	t.Run("evidence refs", func(t *testing.T) {
		fixture := newRetirementFixture(t)
		retiring := retirementAccepted("wiki-filesystem", "old", retirementTestLegacySourceID, "old", "text/markdown")
		retiringOwner := retirementOwner(retiring)
		fixture.states[retiringOwner] = retirementLifecycle(retiringOwner, knowl.SourceLifecycleRetiring, fixture.authority)
		sources := []knowl.AcceptedSource{retiring}
		refs := []string{app.SourceRefKey(retiring)}
		for index := 0; index < 65; index++ {
			active := retirementAccepted("git", "active", knowl.DocumentID("doc-"+string(rune('a'+index%26))+strings.Repeat("x", index/26)), "v1", "text/markdown")
			owner := retirementOwner(active)
			fixture.states[owner] = retirementLifecycle(owner, knowl.SourceLifecycleActive, fixture.authority)
			fixture.content.raw[app.SourceRefKey(active)] = []byte("evidence")
			sources, refs = append(sources, active), append(refs, app.SourceRefKey(active))
		}
		fixture.page = retirementPage(t, "concepts/too-many", refs, "Facts.")
		result, err := fixture.planner().PlanRetirement(t.Context(), fixture.request(sources, retiringOwner))
		assertRetirementFailure(t, result, err, app.ErrRetirementEvidenceLimit, app.RetirementEvidenceLimitCode, fixture.page.Path, "")
		if len(fixture.content.readRefs) != 0 || len(fixture.maintainer.inputs) != 0 {
			t.Fatalf("limit performed reads/provider calls: %#v / %d", fixture.content.readRefs, len(fixture.maintainer.inputs))
		}
	})

	t.Run("evidence bytes", func(t *testing.T) {
		fixture, active, retiring, request := mixedRetirementFixture(t)
		fixture.content.raw[app.SourceRefKey(active)] = make([]byte, app.MaxRetirementEvidenceBytes+1)
		result, err := fixture.planner().PlanRetirement(t.Context(), request)
		assertRetirementFailure(t, result, err, app.ErrRetirementEvidenceLimit, app.RetirementEvidenceLimitCode, fixture.page.Path, app.SourceRefKey(active))
		if len(fixture.maintainer.inputs) != 0 {
			t.Fatal("provider called after evidence byte limit")
		}
		_ = retiring
	})

	t.Run("request", func(t *testing.T) {
		fixture, _, _, request := mixedRetirementFixture(t)
		fixture.maintainer.size = 101
		fixture.maintainer.budget = knowl.MaintenanceRequestBudget{MaxBytes: 100, FormatVersion: "retirement-test-v1"}
		result, err := fixture.planner().PlanRetirement(t.Context(), request)
		assertRetirementFailure(t, result, err, app.ErrRetirementRequestLimit, app.RetirementRequestLimitCode, fixture.page.Path, "")
		if len(fixture.maintainer.inputs) != 0 {
			t.Fatal("provider called after request limit")
		}
	})

	t.Run("output", func(t *testing.T) {
		fixture, active, _, request := mixedRetirementFixture(t)
		fixture.maintainer.result = replacementRetirementPlan(fixture.page, fixture.schema.Digest, []string{app.SourceRefKey(active)})
		fixture.maintainer.result.Pages[0].Content = make([]byte, app.DefaultPlanLimits().MaxFileBytes+1)
		result, err := fixture.planner().PlanRetirement(t.Context(), request)
		assertRetirementFailure(t, result, err, app.ErrRetirementOutputLimit, app.RetirementOutputLimitCode, fixture.page.Path, "")
	})

	t.Run("cancellation", func(t *testing.T) {
		fixture, _, _, request := mixedRetirementFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		result, err := fixture.planner().PlanRetirement(ctx, request)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, knowl.RetirementPlanResult{}) || fixture.content.readPages != 0 || len(fixture.maintainer.inputs) != 0 {
			t.Fatalf("canceled result/error/reads/calls = %#v / %v / %d / %d", result, err, fixture.content.readPages, len(fixture.maintainer.inputs))
		}
	})
}

func TestRetirementPlannerRejectsMalformedChangingAndUnsupportedProviderResults(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*knowl.RetirementModelPlan, knowl.AcceptedSource, knowl.AcceptedSource)
		wantError  error
		wantCode   string
		wantTarget func(knowl.AcceptedSource, knowl.AcceptedSource) string
	}{
		{
			name: "malformed page", wantError: app.ErrRetirementPlanInvalid, wantCode: app.RetirementPageMalformedCode,
			mutate: func(plan *knowl.RetirementModelPlan, _, _ knowl.AcceptedSource) {
				plan.Pages[0].Content = []byte("not frontmatter")
			},
		},
		{
			name: "changed path", wantError: app.ErrRetirementPlanInvalid, wantCode: app.RetirementPlanInvalidCode,
			mutate: func(plan *knowl.RetirementModelPlan, _, _ knowl.AcceptedSource) {
				plan.Pages[0].Path = "wiki/concepts/other.md"
			},
		},
		{
			name: "multiple pages", wantError: app.ErrRetirementPlanInvalid, wantCode: app.RetirementPlanInvalidCode,
			mutate: func(plan *knowl.RetirementModelPlan, _, _ knowl.AcceptedSource) {
				plan.Pages = append(plan.Pages, plan.Pages[0])
			},
		},
		{
			name: "undeclared used ref", wantError: app.ErrRetirementPlanInvalid, wantCode: app.CitationPlanMissingCode,
			mutate:     func(plan *knowl.RetirementModelPlan, _, _ knowl.AcceptedSource) { plan.SourceRefs = nil },
			wantTarget: func(active, _ knowl.AcceptedSource) string { return app.SourceRefKey(active) },
		},
		{
			name: "retiring ref", wantError: app.ErrRetirementPlanInvalid, wantCode: app.CitationSourceRetiringCode,
			mutate: func(plan *knowl.RetirementModelPlan, active, retiring knowl.AcceptedSource) {
				plan.SourceRefs = []string{app.SourceRefKey(active), app.SourceRefKey(retiring)}
				plan.Pages[0].Content = []byte(retirementMarkdown("concepts/mixed", plan.SourceRefs, "unsupported old fact"))
			},
			wantTarget: func(_, retiring knowl.AcceptedSource) string { return app.SourceRefKey(retiring) },
		},
		{
			name: "unsupported ref", wantError: app.ErrRetirementPlanInvalid, wantCode: knowl.DiagnosticCitationUnknownSource,
			mutate: func(plan *knowl.RetirementModelPlan, active, _ knowl.AcceptedSource) {
				unknown := "git:other@v9"
				plan.SourceRefs = []string{app.SourceRefKey(active), unknown}
				plan.Pages[0].Content = []byte(retirementMarkdown("concepts/mixed", plan.SourceRefs, "unsupported fact"))
			},
			wantTarget: func(_, _ knowl.AcceptedSource) string { return "git:other@v9" },
		},
		{
			name: "same adapter unknown ref is not retiring", wantError: app.ErrRetirementPlanInvalid, wantCode: knowl.DiagnosticCitationUnknownSource,
			mutate: func(plan *knowl.RetirementModelPlan, active, _ knowl.AcceptedSource) {
				unknown := "wiki-filesystem:not-the-retiring-owner@v9"
				plan.SourceRefs = []string{app.SourceRefKey(active), unknown}
				plan.Pages[0].Content = []byte(retirementMarkdown("concepts/mixed", plan.SourceRefs, "unsupported fact"))
			},
			wantTarget: func(_, _ knowl.AcceptedSource) string { return "wiki-filesystem:not-the-retiring-owner@v9" },
		},
		{
			name: "duplicate content ref", wantError: app.ErrRetirementPlanInvalid, wantCode: app.RetirementPageMalformedCode,
			mutate: func(plan *knowl.RetirementModelPlan, active, _ knowl.AcceptedSource) {
				ref := app.SourceRefKey(active)
				plan.Pages[0].Content = []byte(retirementMarkdown("concepts/mixed", []string{ref, ref}, "duplicated provenance"))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, active, retiring, request := mixedRetirementFixture(t)
			plan := replacementRetirementPlan(fixture.page, fixture.schema.Digest, []string{app.SourceRefKey(active)})
			test.mutate(&plan, active, retiring)
			fixture.maintainer.result = plan
			result, err := fixture.planner().PlanRetirement(t.Context(), request)
			target := ""
			if test.wantTarget != nil {
				target = test.wantTarget(active, retiring)
			}
			assertRetirementFailure(t, result, err, test.wantError, test.wantCode, fixture.page.Path, target)
		})
	}
}

func TestRetirementPlannerPreservesStableProviderAndStalePageDiagnostics(t *testing.T) {
	t.Run("provider failure", func(t *testing.T) {
		fixture, _, _, request := mixedRetirementFixture(t)
		fixture.maintainer.err = errors.New("provider detail must not become the contract")
		result, err := fixture.planner().PlanRetirement(t.Context(), request)
		assertRetirementFailure(t, result, err, app.ErrRetirementMaintainerFailed, app.RetirementMaintainerFailedCode, fixture.page.Path, "")
	})

	t.Run("stale page", func(t *testing.T) {
		fixture, _, _, request := mixedRetirementFixture(t)
		request.Page.Digest = strings.Repeat("f", 64)
		result, err := fixture.planner().PlanRetirement(t.Context(), request)
		assertRetirementFailure(t, result, err, app.ErrRetirementPageStale, app.RetirementPageStaleCode, fixture.page.Path, "")
		if len(fixture.content.readRefs) != 0 || len(fixture.maintainer.inputs) != 0 {
			t.Fatalf("stale page loaded evidence/provider: %#v / %d", fixture.content.readRefs, len(fixture.maintainer.inputs))
		}
	})
}

type retirementFixture struct {
	content        retirementContentFixture
	maintainer     retirementMaintainerFixture
	authority      knowl.SourceLifecycleAuthority
	states         map[knowl.SourceOwner]knowl.SourceLifecycle
	page           knowl.PageSnapshot
	schema         knowl.SchemaDocument
	afterAuthority func()
	afterLifecycle func()
	lifecycleCalls int
}

func newRetirementFixture(t *testing.T) *retirementFixture {
	t.Helper()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	authority := knowl.SourceLifecycleAuthority{Scope: retirementTestScope, Generation: strings.Repeat("a", 64), Epoch: 7, UpdatedAt: now}
	schemaContent := []byte("# Retirement schema\n")
	schemaDigest := sha256.Sum256(schemaContent)
	fixture := &retirementFixture{
		content: retirementContentFixture{
			accepted: make(map[string]knowl.AcceptedSource), resolveErr: make(map[string]error),
			raw: make(map[string][]byte), readErr: make(map[string]error), catalogs: make(map[knowl.PageID]knowl.PageSnapshot),
		},
		maintainer: retirementMaintainerFixture{size: 1, budget: knowl.MaintenanceRequestBudget{MaxBytes: app.MaxMaintenanceRequestBytes, FormatVersion: "retirement-test-v1"}},
		authority:  authority,
		states:     make(map[knowl.SourceOwner]knowl.SourceLifecycle),
		schema:     knowl.SchemaDocument{Scope: retirementTestScope, Digest: hex.EncodeToString(schemaDigest[:]), Version: "v1", Content: schemaContent, UpdatedAt: now},
	}
	return fixture
}

func mixedRetirementFixture(t *testing.T) (*retirementFixture, knowl.AcceptedSource, knowl.AcceptedSource, app.RetirementPlanningRequest) {
	t.Helper()
	fixture := newRetirementFixture(t)
	active := retirementAccepted("git", "new", "active", "v2", "text/markdown")
	retiring := retirementAccepted("wiki-filesystem", "old", retirementTestLegacySourceID, "v1", "text/markdown")
	fixture.page = retirementPage(t, "concepts/mixed", []string{app.SourceRefKey(retiring), app.SourceRefKey(active)}, "Mixed facts.")
	fixture.content.raw[app.SourceRefKey(active)] = []byte("active evidence")
	activeOwner, retiringOwner := retirementOwner(active), retirementOwner(retiring)
	fixture.states[activeOwner] = retirementLifecycle(activeOwner, knowl.SourceLifecycleActive, fixture.authority)
	fixture.states[retiringOwner] = retirementLifecycle(retiringOwner, knowl.SourceLifecycleRetiring, fixture.authority)
	fixture.maintainer.result = replacementRetirementPlan(fixture.page, fixture.schema.Digest, []string{app.SourceRefKey(active)})
	return fixture, active, retiring, fixture.request([]knowl.AcceptedSource{retiring, active}, retiringOwner)
}

func (fixture *retirementFixture) lifecycle() retirementLifecycleFixture {
	return retirementLifecycleFixture{
		authority: fixture.authority, states: fixture.states,
		afterAuthority: fixture.afterAuthority, afterLifecycle: fixture.afterLifecycle, lifecycleCalls: &fixture.lifecycleCalls,
	}
}

func (fixture *retirementFixture) planner() *app.RetirementPlanner {
	planner, err := app.NewRetirementPlanner(&fixture.content, fixture.lifecycle(), &fixture.maintainer, app.RetirementPlannerOptions{})
	if err != nil {
		panic(err)
	}
	return planner
}

func (fixture *retirementFixture) request(sources []knowl.AcceptedSource, retiring knowl.SourceOwner) app.RetirementPlanningRequest {
	return fixture.requestFor(sources, []knowl.SourceOwner{retiring})
}

func (fixture *retirementFixture) requestFor(sources []knowl.AcceptedSource, retiring []knowl.SourceOwner) app.RetirementPlanningRequest {
	fixture.content.page = cloneRetirementPage(fixture.page)
	refs := make([]string, len(sources))
	for index, source := range sources {
		ref := app.SourceRefKey(source)
		refs[index] = ref
		fixture.content.accepted[ref] = source
	}
	states := make([]knowl.SourceLifecycle, len(retiring))
	for index, owner := range retiring {
		states[index] = fixture.states[owner]
	}
	catalog := retirementCatalogSnapshot(testRootCatalogPath, retirementTestRootTitle, []retirementCatalogLink{{label: fixture.page.Title, destination: strings.TrimPrefix(fixture.page.Path, "wiki/")}})
	fixture.content.catalogs[catalog.ID] = catalog
	return app.RetirementPlanningRequest{
		Scope: fixture.pageScope(), Page: app.CanonicalPageProvenance{ID: fixture.page.ID, Path: fixture.page.Path, Digest: fixture.page.Digest, SourceRefs: slices.Clone(fixture.page.SourceRefs)},
		SourceRefs: refs, Retiring: states, Authority: fixture.authority, Schema: fixture.schema,
		Catalogs: []knowl.HierarchyCatalog{{Path: catalog.Path, Digest: catalog.Digest, Title: catalog.Title, Children: []string{fixture.page.Path}}},
	}
}

func (*retirementFixture) pageScope() knowl.ScopeRef { return retirementTestScope }

func retirementAccepted(adapter string, sourceID string, documentID knowl.DocumentID, revision, mediaType string) knowl.AcceptedSource {
	digest := sha256.Sum256([]byte(adapter + ":" + sourceID + ":" + string(documentID) + "@" + revision))
	return knowl.AcceptedSource{
		Scope:          retirementTestScope,
		Source:         knowl.SourceRef{Adapter: adapter, ID: sourceID + "/" + string(documentID)},
		Version:        knowl.SourceVersion{Version: revision, Digest: hex.EncodeToString(digest[:])},
		MediaType:      mediaType,
		SourceDocument: knowl.SourceDocument{SourceID: knowl.SourceID(sourceID), DocumentID: documentID, Revision: revision, URI: "https://example.invalid/" + string(documentID)},
		ManifestRef:    "raw/accepted/version/manifest.yaml",
	}
}

func retirementOwner(source knowl.AcceptedSource) knowl.SourceOwner {
	return knowl.SourceOwner{Adapter: knowl.SourceAdapterID(source.Source.Adapter), SourceID: source.SourceDocument.SourceID}
}

func retirementLifecycle(owner knowl.SourceOwner, state knowl.SourceLifecycleState, authority knowl.SourceLifecycleAuthority) knowl.SourceLifecycle {
	result := knowl.SourceLifecycle{
		Scope: retirementTestScope, Owner: owner, State: state, ConfigDigest: strings.Repeat("b", 64),
		AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch,
		ActivatedGeneration: strings.Repeat("c", 64), ActivatedEpoch: 2, Ready: state == knowl.SourceLifecycleActive,
		ActivatedAt: authority.UpdatedAt.Add(-2 * time.Hour), UpdatedAt: authority.UpdatedAt,
	}
	if state != knowl.SourceLifecycleActive {
		result.RetirementGeneration = strings.Repeat("d", 64)
		result.RetirementEpoch = 6
		result.RetiringAt = authority.UpdatedAt.Add(-time.Hour)
		result.Ready = false
	}
	if state == knowl.SourceLifecycleDeleted {
		result.DeletedAt = authority.UpdatedAt
	}
	return result
}

func retirementPage(t *testing.T, id string, refs []string, body string) knowl.PageSnapshot {
	t.Helper()
	content := retirementMarkdown(id, refs, body)
	digest := sha256.Sum256([]byte(content))
	metadata, err := knowlwiki.ParseFrontmatter(content)
	if err != nil {
		t.Fatal(err)
	}
	return knowl.PageSnapshot{
		ID: knowl.PageID(id), Path: "wiki/" + id + ".md", Digest: hex.EncodeToString(digest[:]), Title: metadata.Title,
		Content: content, Body: knowlwiki.Body(content), SourceRefs: slices.Clone(refs), Untrusted: true,
	}
}

func retirementMarkdown(id string, refs []string, body string) string {
	var builder strings.Builder
	builder.WriteString("---\ntype: topic\ntitle: Retirement fixture\nknowl:\n  id: " + id + "\n  source_refs:\n")
	for _, ref := range refs {
		builder.WriteString("    - " + ref + "\n")
	}
	builder.WriteString("---\n# Retirement fixture\n\n" + body + "\n")
	return builder.String()
}

type retirementCatalogLink struct {
	label       string
	destination string
}

func retirementCatalogSnapshot(catalogPath, title string, links []retirementCatalogLink) knowl.PageSnapshot {
	var builder strings.Builder
	if catalogPath == testRootCatalogPath {
		builder.WriteString("---\nokf_version: v0.2\n---\n")
	}
	builder.WriteString("# " + title + "\n")
	for _, link := range links {
		builder.WriteString("* [" + link.label + "](" + link.destination + ")\n")
	}
	content := builder.String()
	digest := sha256.Sum256([]byte(content))
	id := strings.TrimSuffix(strings.TrimPrefix(catalogPath, "wiki/"), ".md")
	return knowl.PageSnapshot{ID: knowl.PageID(id), Path: catalogPath, Digest: hex.EncodeToString(digest[:]), Title: title, Content: content, Body: content, Untrusted: true}
}

func parsedRetirementCatalogLinks(content []byte) []retirementCatalogLink {
	return parsedRetirementCatalogNodes(content, false)
}

func parsedRetirementCatalogImages(content []byte) []retirementCatalogLink {
	return parsedRetirementCatalogNodes(content, true)
}

func parsedRetirementCatalogNodes(content []byte, images bool) []retirementCatalogLink {
	document := parser.New().Parse(content)
	result := make([]retirementCatalogLink, 0)
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var firstChild ast.Node
		var destination string
		switch typed := node.(type) {
		case *ast.Link:
			if images {
				return ast.WalkContinue, nil
			}
			firstChild, destination = typed.FirstChild(), typed.Destination.Value(content)
		case *ast.Image:
			if !images {
				return ast.WalkContinue, nil
			}
			firstChild, destination = typed.FirstChild(), typed.Destination.Value(content)
		default:
			return ast.WalkContinue, nil
		}
		var label strings.Builder
		for child := firstChild; child != nil; child = child.NextSibling() {
			if text, ok := child.(*ast.Text); ok {
				label.WriteString(text.Value.Value(content))
			}
		}
		result = append(result, retirementCatalogLink{label: label.String(), destination: destination})
		return ast.WalkSkipChildren, nil
	})
	return result
}

func replacementRetirementPlan(page knowl.PageSnapshot, schemaDigest string, refs []string) knowl.RetirementModelPlan {
	return knowl.RetirementModelPlan{
		SchemaDigest: schemaDigest, SourceRefs: slices.Clone(refs),
		Pages:     []knowl.FileEdit{{Path: page.Path, ExpectedDigest: page.Digest, Content: []byte(retirementMarkdown(string(page.ID), refs, "Active facts only."))}},
		Rationale: "remove retired evidence",
	}
}

func assertRetirementFailure(t *testing.T, result knowl.RetirementPlanResult, err, wantError error, code, path, target string) {
	t.Helper()
	if !errors.Is(err, wantError) {
		t.Fatalf("PlanRetirement() error = %v, want %v", err, wantError)
	}
	want := []knowl.MaintenanceDiagnostic{{Code: code, Path: path, Target: target}}
	if !reflect.DeepEqual(result.Diagnostics, want) || result.Action != "" || !reflect.DeepEqual(result.Page, knowl.PageSnapshot{}) || len(result.SourceRefs) != 0 {
		t.Fatalf("PlanRetirement() failure result = %#v, want diagnostics %#v", result, want)
	}
}

func cloneRetirementPage(page knowl.PageSnapshot) knowl.PageSnapshot {
	page.SourceRefs = slices.Clone(page.SourceRefs)
	page.SourceDocuments = slices.Clone(page.SourceDocuments)
	return page
}

func cloneRetirementInput(input knowl.RetirementInput) knowl.RetirementInput {
	input.Page = cloneRetirementPage(input.Page)
	input.Evidence = slices.Clone(input.Evidence)
	for index := range input.Evidence {
		input.Evidence[index].Text = strings.Clone(input.Evidence[index].Text)
	}
	input.Catalogs = slices.Clone(input.Catalogs)
	return input
}

func cloneRetirementModelPlan(plan knowl.RetirementModelPlan) knowl.RetirementModelPlan {
	plan.SourceRefs = slices.Clone(plan.SourceRefs)
	plan.Pages = slices.Clone(plan.Pages)
	for index := range plan.Pages {
		plan.Pages[index].Content = slices.Clone(plan.Pages[index].Content)
	}
	return plan
}
