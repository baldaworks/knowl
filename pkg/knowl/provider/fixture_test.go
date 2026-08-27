package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestFixtureDefaultReturnsValidNoOpPlan(t *testing.T) {
	input := knowl.MaintenanceInput{
		Schema: knowl.SchemaDocument{Digest: "schema-digest"},
		Source: knowl.AcceptedSource{
			Source:  knowl.SourceRef{Adapter: "wiki-filesystem", ID: "engineering/page.md"},
			Version: knowl.SourceVersion{Version: "revision"},
		},
	}
	plan, err := (Fixture{}).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("Plan() error: %v", err)
	}
	if plan.SchemaDigest != input.Schema.Digest || len(plan.Edits) != 0 ||
		!reflect.DeepEqual(plan.SourceRefs, []string{app.SourceRefKey(input.Source)}) {
		t.Fatalf("Plan() = %#v", plan)
	}
}

func TestFixturePlanHierarchyReturnsIndependentConfiguredPlan(t *testing.T) {
	_, configured := testHierarchyInputAndPlan()
	fixture := Fixture{HierarchyResult: configured}

	first, err := fixture.PlanHierarchy(context.Background(), knowl.HierarchyInput{})
	if err != nil {
		t.Fatalf("PlanHierarchy() error: %v", err)
	}
	first.Catalogs[0].Children[0] = "wiki/changed.md"
	second, err := fixture.PlanHierarchy(context.Background(), knowl.HierarchyInput{})
	if err != nil {
		t.Fatalf("second PlanHierarchy() error: %v", err)
	}
	if !reflect.DeepEqual(second, configured) {
		t.Fatalf("PlanHierarchy() mutated fixture result: %#v", second)
	}
}

func TestFixturePlanHierarchyRequiresConfigurationAndPropagatesErrors(t *testing.T) {
	if _, err := (Fixture{}).PlanHierarchy(context.Background(), knowl.HierarchyInput{}); err == nil {
		t.Fatal("PlanHierarchy() error = nil for unconfigured fixture")
	}
	want := errors.New("hierarchy fixture failure")
	if _, err := (Fixture{HierarchyError: want}).PlanHierarchy(context.Background(), knowl.HierarchyInput{}); !errors.Is(err, want) {
		t.Fatalf("PlanHierarchy() error = %v, want %v", err, want)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Fixture{HierarchyResult: knowl.HierarchyModelPlan{SchemaDigest: "configured"}}).PlanHierarchy(ctx, knowl.HierarchyInput{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PlanHierarchy() canceled error = %v, want context.Canceled", err)
	}
}
