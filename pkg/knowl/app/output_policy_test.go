package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestMaintenanceGenerationIncludesOutputPolicy(t *testing.T) {
	base := SourceMaintenancePolicy(strings.Repeat("a", 64), DefaultReadLimits(), DefaultPlanLimits())
	legacy, err := MaintenancePolicyGeneration(base)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	fields["Output"] = json.RawMessage(`{"supported":true,"limits":{"max_corrections":1,"max_output_bytes":1048576,"deadline_nanos":300000000000}}`)
	changed, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var policy MaintenancePolicy
	if err := json.Unmarshal(changed, &policy); err != nil {
		t.Fatal(err)
	}
	got, err := MaintenancePolicyGeneration(policy)
	if err != nil || got == legacy {
		t.Fatalf("output policy was ignored: generation=%q legacy=%q err=%v", got, legacy, err)
	}
	fields["Output"] = json.RawMessage(`{"supported":true,"limits":{"max_corrections":2,"max_output_bytes":1048576,"deadline_nanos":300000000000}}`)
	invalid, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(invalid, &policy); err != nil {
		t.Fatal(err)
	}
	if _, err := MaintenancePolicyGeneration(policy); !errors.Is(err, ErrExecutionDescriptorUnavailable) {
		t.Fatalf("unbounded output policy accepted: %v", err)
	}
}

func TestMaintenanceGenerationIncludesReservedFeedback(t *testing.T) {
	base := SourceMaintenancePolicy(strings.Repeat("a", 64), DefaultReadLimits(), DefaultPlanLimits())
	legacy, err := MaintenancePolicyGeneration(base)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	fields["RequestReservedBytes"] = json.RawMessage(`128`)
	encoded, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var changed MaintenancePolicy
	if err := json.Unmarshal(encoded, &changed); err != nil {
		t.Fatal(err)
	}
	got, err := MaintenancePolicyGeneration(changed)
	if err != nil || got == legacy {
		t.Fatalf("reservation ignored: generation=%q err=%v", got, err)
	}
}

func TestOutputPolicyNormalizesDefaultsDisableAndCapability(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value *int
		want  int
	}{
		{"default", nil, 1}, {"disabled", contextCount(0), 0}, {"enabled", contextCount(1), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := knowl.OutputSettings{MaxCorrections: tc.value}
			limits, err := NormalizeOutputSettings(settings)
			if err != nil || limits.MaxCorrections != tc.want || limits.MaxOutputBytes != 1048576 || limits.DeadlineNanos != 300000000000 {
				t.Fatalf("limits=%+v err=%v", limits, err)
			}
			unsupported, err := EffectiveOutputCorrectionPolicy(settings, false)
			if err != nil || unsupported.Supported || unsupported.Limits.MaxCorrections != 0 {
				t.Fatalf("unsupported=%+v err=%v", unsupported, err)
			}
		})
	}
	for _, value := range []int{-1, 2} {
		if _, err := NormalizeOutputSettings(knowl.OutputSettings{MaxCorrections: &value}); !errors.Is(err, ErrOutputCorrectionInvalid) {
			t.Fatalf("invalid allowance accepted: %v", err)
		}
	}
	policy, err := EffectiveOutputCorrectionPolicy(knowl.OutputSettings{}, true)
	if err != nil {
		t.Fatal(err)
	}
	first, err := HierarchyOutputPlannerVersion("semantic-v1", policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.Limits.MaxCorrections = 0
	disabled, err := HierarchyOutputPlannerVersion("semantic-v1", policy)
	if err != nil || disabled == first {
		t.Fatalf("changed allowance reused planner: %q %v", disabled, err)
	}
	policy.Supported = false
	unsupported, err := HierarchyOutputPlannerVersion("semantic-v1", policy)
	if err != nil || unsupported == disabled {
		t.Fatalf("support mode reused planner: %q %v", unsupported, err)
	}
	other, err := HierarchyOutputPlannerVersion("semantic-v2", policy)
	if err != nil || other == unsupported {
		t.Fatalf("base version ignored: %q %v", other, err)
	}
}

func TestCorrectionFailuresRemainPermanentAndDistinct(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{ErrOutputCorrectionExhausted, "provider_output_exhausted"}, {ErrCorrectionOutputLimit, "provider_output_limit"}, {ErrCorrectionDeadline, "provider_output_deadline"},
	} {
		wrapped := fmt.Errorf("planning: %w", tc.err)
		info, ok := ClassifyExecutionFailure(wrapped)
		if !ok || info.Class != "provider" || info.Reason != tc.reason || info.Retryable || !errors.Is(wrapped, tc.err) {
			t.Fatalf("classification=%+v ok=%t", info, ok)
		}
	}
}
