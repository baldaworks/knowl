package operationpayload

import (
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestHierarchyPayloadRoundTripAndFailClosedBounds(t *testing.T) {
	descriptor := knowl.ExecutionDescriptor{
		Kind: knowl.WorkHierarchy,
		Hierarchy: &knowl.HierarchyExecutionDescriptor{
			SnapshotDigest: strings.Repeat("a", 64), PlannerVersion: "planner-v1",
		},
	}
	encoded, err := EncodeHierarchy(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var decoded knowl.ExecutionDescriptor
	if err := DecodeHierarchy(encoded, &decoded); err != nil || decoded.Hierarchy == nil || decoded.Hierarchy.PlannerVersion != "planner-v1" {
		t.Fatalf("DecodeHierarchy() = %#v, %v", decoded, err)
	}
	for _, raw := range []string{
		`{"version":2,"hierarchy":{"snapshot_digest":"x","planner_version":"v"}}`,
		`{"version":1,"hierarchy":null}`,
		`{"version":1,"unknown":true}`,
		strings.Repeat("x", MaxBytes+1),
	} {
		if err := DecodeHierarchy(raw, &decoded); !errors.Is(err, app.ErrExecutionDescriptorUnavailable) {
			t.Fatalf("DecodeHierarchy(%d bytes) error = %v", len(raw), err)
		}
	}
}

func TestRetirementPagePayloadRoundTripAndRejectsOtherPayloads(t *testing.T) {
	descriptor := knowl.ExecutionDescriptor{
		Kind: knowl.WorkSourceRetirementPage,
		RetirementPage: &knowl.RetirementPageExecutionDescriptor{
			PageID:               "concepts/one",
			RetirementGeneration: strings.Repeat("a", 64),
			Owner: knowl.SourceOwner{
				Adapter:  knowl.SourceAdapterFilesystem,
				SourceID: "legacy",
			},
		},
	}
	encoded, err := EncodeRetirementPage(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var decoded knowl.ExecutionDescriptor
	if err := DecodeRetirementPage(encoded, &decoded); err != nil || decoded.RetirementPage == nil || *decoded.RetirementPage != *descriptor.RetirementPage {
		t.Fatalf("DecodeRetirementPage() = %#v, %v", decoded, err)
	}
	for _, raw := range []string{
		`{"version":2,"retirement_page":{"page_id":"concepts/one","retirement_generation":"` + strings.Repeat("a", 64) + `","owner":{"adapter":"wiki-filesystem","source_id":"legacy"}}}`,
		`{"version":1,"hierarchy":{"snapshot_digest":"` + strings.Repeat("b", 64) + `","planner_version":"v1"}}`,
		`{"version":1,"retirement_page":null}`,
		`{"version":1,"retirement_page":{"page_id":"concepts/one","retirement_generation":"` + strings.Repeat("a", 64) + `","owner":{"adapter":"wiki-filesystem","source_id":"legacy"}},"unknown":true}`,
		strings.Repeat("x", MaxBytes+1),
	} {
		if err := DecodeRetirementPage(raw, &decoded); !errors.Is(err, app.ErrExecutionDescriptorUnavailable) {
			t.Fatalf("DecodeRetirementPage(%d bytes) error = %v", len(raw), err)
		}
	}
}
