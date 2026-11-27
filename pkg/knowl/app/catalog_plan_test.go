package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
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
	newPageContent := maintainedPageMarkdown("topic", "New factual page", "A newly maintained factual page.", []string{"facts"})
	model.Edits = []knowl.FileEdit{{Path: newPage, Content: []byte(newPageContent)}}
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
	if string(byPath[newPage].Content) != newPageContent || len(model.Edits) != 1 {
		t.Fatal("factual content or caller mutated")
	}
	limits := DefaultPlanLimits()
	limits.MaxFiles = 2
	if _, err := ValidateMaintenancePlan(t.Context(), input, model, inspection, DefaultCatalogLimits(), limits); !errors.Is(err, ErrPlanLimitExceeded) {
		t.Fatalf("combined file bound=%v", err)
	}
}

func TestMaintenancePlanCatalogLinksUsePageTitles(t *testing.T) {
	input, model, inspection := navigationFixture()
	inspection.Snapshot.Pages[0].Title = "Existing human title"
	const (
		newPage    = "wiki/entities/generated-page-slug.md"
		newCatalog = "wiki/catalogs/generated/index.md"
	)
	model.Edits = []knowl.FileEdit{{
		Path:    newPage,
		Content: []byte(maintainedPageMarkdown("topic", "Generated human title", "Generated page description.", []string{"generated"})),
	}}
	model.CatalogAdditions = []knowl.CatalogAddition{
		{Path: newCatalog, Title: "Generated", Children: []string{navigationExistingPath, newPage}},
		{Path: rootCatalogPath, ExpectedDigest: inspection.Catalogs[0].Digest, Children: []string{newCatalog}},
	}

	validated, err := ValidateMaintenancePlan(t.Context(), input, model, inspection, DefaultCatalogLimits(), DefaultPlanLimits())
	if err != nil {
		t.Fatal(err)
	}
	var content []byte
	for _, edit := range validated.Edits {
		if edit.Path == newCatalog {
			content = edit.Content
			break
		}
	}
	links := parsedCatalogLinks(content)
	want := []catalogLink{
		{label: "Existing human title", destination: "../../entities/existing.md"},
		{label: "Generated human title", destination: "../../entities/generated-page-slug.md"},
	}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("catalog links = %#v, want %#v", links, want)
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

type catalogLink struct {
	label       string
	destination string
}

func parsedCatalogLinks(content []byte) []catalogLink {
	document := parser.New().Parse(content)
	result := make([]catalogLink, 0)
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		link, ok := node.(*ast.Link)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		var label strings.Builder
		for child := link.FirstChild(); child != nil; child = child.NextSibling() {
			if text, ok := child.(*ast.Text); ok {
				label.Write(text.Value.Bytes(content))
			}
		}
		result = append(result, catalogLink{label: label.String(), destination: link.Destination.Value(content)})
		return ast.WalkSkipChildren, nil
	})
	return result
}
