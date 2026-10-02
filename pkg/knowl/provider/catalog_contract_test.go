package provider

import (
	"encoding/json"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestRuntimeMaintainerDecodesCatalogAdditions(t *testing.T) {
	output := `{"schema_digest":"schema","source_refs":["fixture:source@1"],"edits":[],"catalog_additions":[{"path":"wiki/index.md","expected_digest":"original","children":["wiki/entities/new.md"]}]}`
	factory := &fakeRuntimeFactory{agent: newOutputAgent(t, output)}
	maintainer, err := newRuntimeMaintainer(factory, "codex", t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := maintainer.Close(); err != nil {
			t.Error(err)
		}
	})
	result, err := maintainer.Plan(t.Context(), testMaintenanceInput())
	if err != nil || len(result.CatalogAdditions) != 1 || len(result.CatalogAdditions[0].Children) != 1 {
		t.Fatalf("source additions lost: %v %v", result.CatalogAdditions, err)
	}
}

func TestRuntimeMaintainerHierarchyRejectsSourceAdditions(t *testing.T) {
	input, plan := testHierarchyInputAndPlan()
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	fields["catalog_additions"] = []knowl.CatalogAddition{}
	encoded, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	factory := &fakeRuntimeFactory{agent: newOutputAgent(t, string(encoded))}
	maintainer, err := newRuntimeMaintainer(factory, "codex", t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := maintainer.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := maintainer.PlanHierarchy(t.Context(), input); err == nil {
		t.Fatal("hierarchy accepted source-only additions")
	}
}

func TestRuntimeMaintainerRejectsOldSourceContractBeforeBuild(t *testing.T) {
	factory := &fakeRuntimeFactory{agent: newOutputAgent(t, testSourcePlanJSON)}
	maintainer, err := newRuntimeMaintainer(factory, "codex", t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := maintainer.Close(); err != nil {
			t.Error(err)
		}
	})
	input := testMaintenanceInput()
	input.ContractVersion = "source-maintenance-v2"
	_, err = maintainer.Plan(t.Context(), input)
	failure, classified := app.ClassifyExecutionFailure(err)
	if !classified || failure.Reason != reasonProviderInput || factory.builds != 0 {
		t.Fatalf("old input=%v, builds=%d", err, factory.builds)
	}
}
