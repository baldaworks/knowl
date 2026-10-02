package app

import (
	"errors"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
	"strings"
	"testing"
)

const navigationExistingPath = "wiki/entities/existing.md"
const navigationNewCatalogPath = "wiki/new/index.md"

func TestMaintenancePlanRejectsUnseenCatalogReplacement(t *testing.T) {
	input := knowl.MaintenanceInput{Scope: fixtureScope, Schema: knowl.SchemaDocument{Digest: fixtureSchemaDigest}, Source: knowl.AcceptedSource{Source: knowl.SourceRef{Adapter: fixtureAdapter, ID: fixtureOperationSourceID}, Version: knowl.SourceVersion{Version: "1"}}}
	model := knowl.ModelEditPlan{SchemaDigest: input.Schema.Digest, SourceRefs: []string{fixtureSourceRef}, Edits: []knowl.FileEdit{{Path: rootCatalogPath, Content: []byte(catalogTestRoot)}}}
	_, err := ValidateMaintenancePlan(t.Context(), input, model, knowl.WorkspaceInspection{Catalogs: []knowl.PageSnapshot{catalogSnapshot(rootCatalogPath, catalogTestRoot)}}, DefaultCatalogLimits(), DefaultPlanLimits())
	if !errors.Is(err, ErrForbiddenEdit) {
		t.Fatalf("raw catalog replacement=%v", err)
	}
}

func navigationFixture() (knowl.MaintenanceInput, knowl.ModelEditPlan, knowl.WorkspaceInspection) {
	input := knowl.MaintenanceInput{Scope: fixtureScope, Schema: knowl.SchemaDocument{Digest: fixtureSchemaDigest}, Source: knowl.AcceptedSource{Source: knowl.SourceRef{Adapter: fixtureAdapter, ID: fixtureOperationSourceID}, Version: knowl.SourceVersion{Version: "1"}}}
	model := knowl.ModelEditPlan{SchemaDigest: input.Schema.Digest, SourceRefs: []string{fixtureSourceRef}}
	root := catalogSnapshot(rootCatalogPath, "# Navigation\n\nOperator prose stays.\n- [Existing](entities/existing.md)\n- [External](https://example.test/)\n")
	inspection := knowl.WorkspaceInspection{Catalogs: []knowl.PageSnapshot{root}, Snapshot: knowl.WorkspaceSnapshot{Pages: []knowl.PageSnapshot{{Path: navigationExistingPath}}}}
	return input, model, inspection
}

func TestMaintenancePlanPreservesOriginalAndAddsNestedNavigation(t *testing.T) {
	input, model, inspection := navigationFixture()
	const nested = "wiki/topic/index.md"
	const newPage = "wiki/entities/odd `name [draft].md"
	model.Edits = []knowl.FileEdit{{Path: newPage, Content: []byte("new factual content")}}
	model.CatalogAdditions = []knowl.CatalogAddition{
		{Path: nested, Title: "Topic `draft [A] & B", Children: []string{newPage}},
		{Path: rootCatalogPath, ExpectedDigest: inspection.Catalogs[0].Digest, Children: []string{nested, navigationExistingPath, nested}},
	}
	validated, err := ValidateMaintenancePlan(t.Context(), input, model, inspection, DefaultCatalogLimits(), DefaultPlanLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(validated.Edits) != 3 {
		t.Fatalf("combined edits=%d", len(validated.Edits))
	}
	byPath := map[string]knowl.FileEdit{}
	for _, edit := range validated.Edits {
		byPath[edit.Path] = edit
	}
	root := byPath[rootCatalogPath]
	if !strings.HasPrefix(string(root.Content), inspection.Catalogs[0].Content) || root.ExpectedDigest != inspection.Catalogs[0].Digest {
		t.Fatal("original bytes/digest lost")
	}
	destinations, malformed := wiki.IndexDestinations(string(byPath[nested].Content), 8)
	if malformed || len(destinations) != 1 {
		t.Fatalf("new catalog links=%v malformed=%v", destinations, malformed)
	}
	target, external, valid := wiki.ResolveIndexDestination("topic/index.md", destinations[0])
	if !valid || external || "wiki/"+target != newPage {
		t.Fatalf("escaped target=%q", target)
	}
	// New page bytes are passed through unchanged, and the caller's model is not rewritten.
	if string(byPath[newPage].Content) != "new factual content" || len(model.Edits) != 1 {
		t.Fatal("factual content or caller mutated")
	}
	limits := DefaultPlanLimits()
	limits.MaxFiles = 2
	if _, err := ValidateMaintenancePlan(t.Context(), input, model, inspection, DefaultCatalogLimits(), limits); !errors.Is(err, ErrPlanLimitExceeded) {
		t.Fatalf("combined file bound=%v", err)
	}
}

func TestMaintenancePlanExistingMembershipIsNoOp(t *testing.T) {
	input, model, inspection := navigationFixture()
	model.CatalogAdditions = []knowl.CatalogAddition{{Path: rootCatalogPath, ExpectedDigest: inspection.Catalogs[0].Digest, Children: []string{navigationExistingPath, navigationExistingPath}}}
	result, err := ValidateMaintenancePlan(t.Context(), input, model, inspection, DefaultCatalogLimits(), DefaultPlanLimits())
	if err != nil || len(result.Edits) != 0 {
		t.Fatalf("existing addition=%v %v", result.Edits, err)
	}
}

func TestMaintenancePlanRejectsInvalidIntents(t *testing.T) {
	for _, tc := range []struct {
		name      string
		additions func(knowl.WorkspaceInspection) []knowl.CatalogAddition
		want      error
	}{
		{"stale digest", func(i knowl.WorkspaceInspection) []knowl.CatalogAddition {
			return []knowl.CatalogAddition{{Path: rootCatalogPath, ExpectedDigest: strings.Repeat("f", 64), Children: []string{navigationExistingPath}}}
		}, ErrPlanInvalid},
		{"missing target", func(i knowl.WorkspaceInspection) []knowl.CatalogAddition {
			return []knowl.CatalogAddition{{Path: rootCatalogPath, ExpectedDigest: i.Catalogs[0].Digest, Children: []string{"wiki/entities/missing.md"}}}
		}, ErrPlanInvalid},
		{"cycle", func(i knowl.WorkspaceInspection) []knowl.CatalogAddition {
			return []knowl.CatalogAddition{{Path: rootCatalogPath, ExpectedDigest: i.Catalogs[0].Digest, Children: []string{rootCatalogPath}}}
		}, ErrPlanInvalid},
		{"escape", func(i knowl.WorkspaceInspection) []knowl.CatalogAddition {
			return []knowl.CatalogAddition{{Path: rootCatalogPath, ExpectedDigest: i.Catalogs[0].Digest, Children: []string{"wiki/../outside.md"}}}
		}, ErrForbiddenEdit},
		{"rename", func(i knowl.WorkspaceInspection) []knowl.CatalogAddition {
			return []knowl.CatalogAddition{{Path: rootCatalogPath, ExpectedDigest: i.Catalogs[0].Digest, Title: "Replacement", Children: []string{navigationExistingPath}}}
		}, ErrPlanInvalid},
		{"duplicate entry", func(i knowl.WorkspaceInspection) []knowl.CatalogAddition {
			a := knowl.CatalogAddition{Path: rootCatalogPath, ExpectedDigest: i.Catalogs[0].Digest}
			return []knowl.CatalogAddition{a, a}
		}, ErrPlanInvalid},
		{"new without title", func(i knowl.WorkspaceInspection) []knowl.CatalogAddition {
			return []knowl.CatalogAddition{{Path: navigationNewCatalogPath, Children: []string{navigationExistingPath}}}
		}, ErrPlanInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, model, inspection := navigationFixture()
			model.CatalogAdditions = tc.additions(inspection)
			if _, err := ValidateMaintenancePlan(t.Context(), input, model, inspection, DefaultCatalogLimits(), DefaultPlanLimits()); !errors.Is(err, tc.want) {
				t.Fatalf("invalid intent=%v", err)
			}
		})
	}
}

func TestMaintenancePlanEnforcesFinalCatalogBounds(t *testing.T) {
	input, model, inspection := navigationFixture()
	model.CatalogAdditions = []knowl.CatalogAddition{{Path: rootCatalogPath, ExpectedDigest: inspection.Catalogs[0].Digest, Children: []string{navigationNewCatalogPath}}, {Path: navigationNewCatalogPath, Title: "New", Children: []string{navigationExistingPath}}}
	for _, limit := range []func(*knowl.CatalogLimits){
		func(l *knowl.CatalogLimits) { l.MaxCatalogs = 1 },
		func(l *knowl.CatalogLimits) { l.MaxEdges = 1 },
		func(l *knowl.CatalogLimits) { l.MaxDepth = 1 },
		func(l *knowl.CatalogLimits) { l.MaxCatalogBytes = len(inspection.Catalogs[0].Content) },
		func(l *knowl.CatalogLimits) { l.MaxSnapshotBytes = len(inspection.Catalogs[0].Content) },
	} {
		bounds := DefaultCatalogLimits()
		limit(&bounds)
		if _, err := ValidateMaintenancePlan(t.Context(), input, model, inspection, bounds, DefaultPlanLimits()); !errors.Is(err, ErrPlanLimitExceeded) {
			t.Fatalf("final bounds=%v", err)
		}
	}
}

func TestMaintenancePlanRejectsUnreachableNewCatalog(t *testing.T) {
	input, model, inspection := navigationFixture()
	model.CatalogAdditions = []knowl.CatalogAddition{{Path: navigationNewCatalogPath, Title: "New", Children: []string{navigationExistingPath}}}
	if _, err := ValidateMaintenancePlan(t.Context(), input, model, inspection, DefaultCatalogLimits(), DefaultPlanLimits()); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("unreachable catalog=%v", err)
	}
}
