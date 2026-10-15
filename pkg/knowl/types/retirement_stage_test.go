package knowl_test

import (
	"encoding/json"
	"reflect"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const retirementTestPagePath = "wiki/concepts/one.md"

func TestFileEditActionCodecPreservesLegacyWriteAndExplicitDelete(t *testing.T) {
	tests := []struct {
		name       string
		edit       knowl.FileEdit
		wantAction any
	}{
		{name: "legacy write", edit: knowl.FileEdit{Path: retirementTestPagePath, Content: []byte("one")}, wantAction: nil},
		{name: "explicit write", edit: knowl.FileEdit{Action: knowl.SourceMutationWrite, Path: retirementTestPagePath, Content: []byte("one")}, wantAction: string(knowl.SourceMutationWrite)},
		{name: "explicit delete", edit: knowl.FileEdit{Action: knowl.SourceMutationDelete, Path: retirementTestPagePath, ExpectedDigest: "digest"}, wantAction: string(knowl.SourceMutationDelete)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.edit)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if got := fields["action"]; !reflect.DeepEqual(got, test.wantAction) {
				t.Fatalf("action field = %#v, want %#v", got, test.wantAction)
			}
			var decoded knowl.FileEdit
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, test.edit) {
				t.Fatalf("decoded = %#v, want %#v", decoded, test.edit)
			}
		})
	}
}

func TestRetirementPlanContextRoundTripsThroughValidatedAndStagedPlans(t *testing.T) {
	context := knowl.RetirementPlanContext{
		Scope: "scope", PageID: "concepts/one", PagePath: retirementTestPagePath, PageDigest: "page-digest",
		AuthorityGeneration: "authority", AuthorityEpoch: 7,
		Owners: []knowl.RetirementOwnerAuthority{{
			Owner:                knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "legacy"},
			ConfigDigest:         "config",
			RetirementGeneration: "retirement", RetirementEpoch: 7,
		}},
	}
	plan := knowl.ValidatedEditPlan{OperationID: "operation", Retirement: &context}
	staged := knowl.StagedChange{OperationID: "operation", Retirement: &context}
	for name, value := range map[string]any{"plan": plan, "staged": staged} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Retirement *knowl.RetirementPlanContext `json:"retirement"`
			}
			if err := json.Unmarshal(encoded, &envelope); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(envelope.Retirement, &context) {
				t.Fatalf("retirement = %#v, want %#v", envelope.Retirement, context)
			}
		})
	}
}
