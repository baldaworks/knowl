package provider

import (
	"context"
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
