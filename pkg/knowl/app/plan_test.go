package app

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const fixtureAdapter = "fixture"
const fixtureDecisionID = "decision"
const fixtureOperationID = "operation-1"
const fixtureScope = "local"
const fixtureSchema = "schema"
const fixtureSourceID = "source"
const fixtureSchemaDigest = "schema-digest"

func TestValidatePlanSortsEditsAndPreservesSourceCitation(t *testing.T) {
	input := knowl.MaintenanceInput{
		Scope:  fixtureScope,
		Schema: knowl.SchemaDocument{Digest: fixtureSchemaDigest},
		Source: knowl.AcceptedSource{Source: knowl.SourceRef{Adapter: fixtureAdapter, ID: "source-1"}, Version: knowl.SourceVersion{Version: "1"}},
	}
	plan, err := ValidatePlan(context.Background(), input, knowl.ModelEditPlan{
		SchemaDigest: fixtureSchemaDigest,
		SourceRefs:   []string{"fixture:source-1@1"},
		Edits: []knowl.FileEdit{
			{Path: "wiki/entities/z.md", Content: []byte("z")},
			{Path: "wiki/entities/a.md", Content: []byte("a")},
		},
	}, DefaultPlanLimits())
	if err != nil {
		t.Fatalf("validate plan: %v", err)
	}
	if got := plan.Edits[0].Path; got != "wiki/entities/a.md" {
		t.Fatalf("first edit path = %q, want sorted path", got)
	}
	if plan.OperationID != "local:fixture:source-1@1" {
		t.Fatalf("operation id = %q", plan.OperationID)
	}
}

func TestValidatePlanAcceptsNoOp(t *testing.T) {
	input := knowl.MaintenanceInput{
		Scope:  fixtureScope,
		Schema: knowl.SchemaDocument{Digest: fixtureSchemaDigest},
		Source: knowl.AcceptedSource{Source: knowl.SourceRef{Adapter: fixtureAdapter, ID: "source-1"}, Version: knowl.SourceVersion{Version: "1"}},
	}
	plan, err := ValidatePlan(context.Background(), input, knowl.ModelEditPlan{
		SchemaDigest: fixtureSchemaDigest,
		SourceRefs:   []string{"fixture:source-1@1"},
		Edits:        []knowl.FileEdit{},
	}, DefaultPlanLimits())
	if err != nil {
		t.Fatalf("validate no-op plan: %v", err)
	}
	if len(plan.Edits) != 0 {
		t.Fatalf("no-op edits = %#v, want empty", plan.Edits)
	}
}

func TestValidatePlanRejectsSchemaAndRawTargets(t *testing.T) {
	input := knowl.MaintenanceInput{Schema: knowl.SchemaDocument{Digest: fixtureSchema}, Source: knowl.AcceptedSource{Source: knowl.SourceRef{Adapter: fixtureAdapter, ID: fixtureSourceID}, Version: knowl.SourceVersion{Version: "1"}}}
	for _, path := range []string{"schema.md", "raw/source", "wiki/../schema.md", "wiki/log.md"} {
		_, err := ValidatePlan(context.Background(), input, knowl.ModelEditPlan{SchemaDigest: fixtureSchema, SourceRefs: []string{"fixture:source@1"}, Edits: []knowl.FileEdit{{Path: path}}}, DefaultPlanLimits())
		if !errors.Is(err, ErrForbiddenEdit) {
			t.Errorf("path %q error = %v, want forbidden edit", path, err)
		}
	}
}

func TestValidatePlanRejectsChangedSchema(t *testing.T) {
	input := knowl.MaintenanceInput{Schema: knowl.SchemaDocument{Digest: "current"}, Source: knowl.AcceptedSource{Source: knowl.SourceRef{Adapter: fixtureAdapter, ID: fixtureSourceID}, Version: knowl.SourceVersion{Version: "1"}}}
	_, err := ValidatePlan(context.Background(), input, knowl.ModelEditPlan{SchemaDigest: "old", SourceRefs: []string{"fixture:source@1"}, Edits: []knowl.FileEdit{{Path: "wiki/topic.md"}}}, DefaultPlanLimits())
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("schema mismatch error = %v", err)
	}
}
