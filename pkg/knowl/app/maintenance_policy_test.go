package app

import (
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestMaintenancePolicyGenerationIsDeterministicAndNormalized(t *testing.T) {
	t.Parallel()
	schemaDigest := strings.Repeat("a", 64)
	explicit := SourceMaintenancePolicy(schemaDigest, DefaultReadLimits(), DefaultPlanLimits())
	want, err := MaintenancePolicyGeneration(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 64 || want != strings.ToLower(want) {
		t.Fatalf("generation = %q, want full lowercase SHA-256", want)
	}

	normalized := MaintenancePolicy{
		ContractVersion: "  " + SourceMaintenanceContractVersion + "  ",
		SchemaDigest:    strings.ToUpper(schemaDigest),
	}
	got, err := MaintenancePolicyGeneration(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("normalized generation = %q, want %q", got, want)
	}
}

func TestMaintenanceGenerationPrefixIsBoundedAndValid(t *testing.T) {
	t.Parallel()
	generation := strings.Repeat("a", 64)
	prefix, err := MaintenanceGenerationPrefix(generation)
	if err != nil || prefix != generation[:16] {
		t.Fatalf("MaintenanceGenerationPrefix() = %q, %v", prefix, err)
	}
	if prefix, err := MaintenanceGenerationPrefix(""); err != nil || prefix != "" {
		t.Fatalf("legacy MaintenanceGenerationPrefix() = %q, %v", prefix, err)
	}
	if _, err := MaintenanceGenerationPrefix("secret-shaped-invalid-generation"); err == nil {
		t.Fatal("invalid generation prefix succeeded")
	}
}

func TestMaintenancePolicyGenerationChangesWithEffectivePolicy(t *testing.T) {
	t.Parallel()
	base := SourceMaintenancePolicy(strings.Repeat("a", 64), DefaultReadLimits(), DefaultPlanLimits())
	want, err := MaintenancePolicyGeneration(base)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*MaintenancePolicy)
	}{
		{name: "contract", edit: func(policy *MaintenancePolicy) { policy.ContractVersion = "source-maintenance-v2" }},
		{name: fixtureSchema, edit: func(policy *MaintenancePolicy) { policy.SchemaDigest = strings.Repeat("b", 64) }},
		{name: "read pages", edit: func(policy *MaintenancePolicy) { policy.ReadLimits.Pages++ }},
		{name: "read bytes", edit: func(policy *MaintenancePolicy) { policy.ReadLimits.Bytes++ }},
		{name: "read characters", edit: func(policy *MaintenancePolicy) { policy.ReadLimits.Characters++ }},
		{name: "read depth", edit: func(policy *MaintenancePolicy) { policy.ReadLimits.Depth++ }},
		{name: "read deadline", edit: func(policy *MaintenancePolicy) { policy.ReadLimits.Deadline += time.Second }},
		{name: "plan files", edit: func(policy *MaintenancePolicy) { policy.PlanLimits.MaxFiles++ }},
		{name: "plan file bytes", edit: func(policy *MaintenancePolicy) { policy.PlanLimits.MaxFileBytes++ }},
		{name: "plan source refs", edit: func(policy *MaintenancePolicy) { policy.PlanLimits.MaxSourceRefs++ }},
		{name: "plan rationale", edit: func(policy *MaintenancePolicy) { policy.PlanLimits.MaxRationaleSize++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.edit(&changed)
			got, generationErr := MaintenancePolicyGeneration(changed)
			if generationErr != nil {
				t.Fatal(generationErr)
			}
			if got == want {
				t.Fatalf("changed policy retained generation %q", got)
			}
		})
	}
}

func TestMaintenancePolicyGenerationPreservesNoDeadlineSemantics(t *testing.T) {
	t.Parallel()
	policy := SourceMaintenancePolicy(strings.Repeat("a", 64), DefaultReadLimits(), DefaultPlanLimits())
	policy.ReadLimits.Deadline = 0
	want, err := MaintenancePolicyGeneration(policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.ReadLimits.Deadline = -time.Second
	got, err := MaintenancePolicyGeneration(policy)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("equivalent no-deadline policies differ: %q != %q", got, want)
	}
}

func TestMaintenancePolicyGenerationRejectsInvalidPolicy(t *testing.T) {
	t.Parallel()
	valid := SourceMaintenancePolicy(strings.Repeat("a", 64), DefaultReadLimits(), DefaultPlanLimits())
	tests := []struct {
		name string
		edit func(*MaintenancePolicy)
	}{
		{name: "contract", edit: func(policy *MaintenancePolicy) { policy.ContractVersion = "" }},
		{name: fixtureSchema, edit: func(policy *MaintenancePolicy) { policy.SchemaDigest = "not-a-digest" }},
		{name: "partial read limits", edit: func(policy *MaintenancePolicy) { policy.ReadLimits.Characters = 0 }},
		{name: "partial plan limits", edit: func(policy *MaintenancePolicy) { policy.PlanLimits.MaxFiles = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := valid
			test.edit(&invalid)
			if _, err := MaintenancePolicyGeneration(invalid); err == nil {
				t.Fatal("MaintenancePolicyGeneration() succeeded")
			}
		})
	}
}

func TestSourceOperationIDPreservesLegacyAndDiscriminatesGeneration(t *testing.T) {
	t.Parallel()
	key := knowl.OperationKey{
		Scope: "local", Source: knowl.SourceRef{Adapter: "fixture", ID: fixtureOperationSourceID},
		Version: knowl.SourceVersion{Version: "1", Digest: strings.Repeat("a", 64)},
	}
	legacy, err := SourceOperationID(key)
	if err != nil {
		t.Fatal(err)
	}
	if legacy != "local:fixture:source-1@1#aaaaaaaaaaaaaaaa" {
		t.Fatalf("legacy ID = %q", legacy)
	}

	key.MaintenanceGeneration = strings.Repeat("b", 64)
	current, err := SourceOperationID(key)
	if err != nil {
		t.Fatal(err)
	}
	if current != legacy+"~bbbbbbbbbbbbbbbb" {
		t.Fatalf("generation-bearing ID = %q", current)
	}
	key.MaintenanceGeneration = strings.ToUpper(key.MaintenanceGeneration)
	if _, err := SourceOperationID(key); err == nil {
		t.Fatal("SourceOperationID() accepted noncanonical generation")
	}
}
