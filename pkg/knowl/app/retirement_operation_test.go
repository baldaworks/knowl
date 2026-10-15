package app

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestRetirementPageOperationIdentityIsPageAndGenerationScoped(t *testing.T) {
	schemaBytes := []byte("# Schema\n")
	schemaDigest := fmt.Sprintf("%x", sha256.Sum256(schemaBytes))
	scope := knowl.ScopeRef("local")
	generation := strings.Repeat("a", 64)
	pageID := knowl.PageID("concepts/decision-records")
	identity := RetirementPageOperationIdentity(scope, generation, pageID)
	id, err := OperationIDForIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := knowl.ExecutionDescriptor{
		OperationID: id,
		Kind:        knowl.WorkSourceRetirementPage,
		Schema:      knowl.SchemaDocument{Scope: scope, Digest: schemaDigest, Version: "1", Content: schemaBytes},
		RetirementPage: &knowl.RetirementPageExecutionDescriptor{
			PageID:               pageID,
			RetirementGeneration: generation,
			Owner: knowl.SourceOwner{
				Adapter:  knowl.SourceAdapterFilesystem,
				SourceID: "legacy",
			},
		},
	}
	if err := ValidateOperationDescriptor(identity, descriptor); err != nil {
		t.Fatalf("ValidateOperationDescriptor() error = %v", err)
	}
	if err := ValidateGenericExecutionDescriptor(scope, descriptor); err != nil {
		t.Fatalf("ValidateGenericExecutionDescriptor() error = %v", err)
	}

	changedOwner := descriptor
	changedPayload := *descriptor.RetirementPage
	changedPayload.Owner = knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "replacement"}
	changedOwner.RetirementPage = &changedPayload
	if changedIdentity := RetirementPageOperationIdentity(scope, generation, pageID); changedIdentity != identity {
		t.Fatalf("triggering owner changed deterministic identity: %#v != %#v", changedIdentity, identity)
	}
	if err := ValidateOperationDescriptor(identity, changedOwner); err != nil {
		t.Fatalf("owner-independent identity rejected valid payload: %v", err)
	}
}

func TestRetirementPageOperationDescriptorRejectsMutableOrUnboundedInputs(t *testing.T) {
	schemaBytes := []byte("# Schema\n")
	schemaDigest := fmt.Sprintf("%x", sha256.Sum256(schemaBytes))
	scope := knowl.ScopeRef("local")
	generation := strings.Repeat("a", 64)
	identity := RetirementPageOperationIdentity(scope, generation, "concepts/one")
	id, err := OperationIDForIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := knowl.ExecutionDescriptor{
		OperationID: id,
		Kind:        knowl.WorkSourceRetirementPage,
		Schema:      knowl.SchemaDocument{Scope: scope, Digest: schemaDigest, Version: "1", Content: schemaBytes},
		RetirementPage: &knowl.RetirementPageExecutionDescriptor{
			PageID:               "concepts/one",
			RetirementGeneration: generation,
			Owner:                knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "legacy"},
		},
	}
	for name, mutate := range map[string]func(*knowl.ExecutionDescriptor){
		"wrong page": func(candidate *knowl.ExecutionDescriptor) { candidate.RetirementPage.PageID = "concepts/two" },
		"wrong generation": func(candidate *knowl.ExecutionDescriptor) {
			candidate.RetirementPage.RetirementGeneration = strings.Repeat("b", 64)
		},
		"body-shaped page id": func(candidate *knowl.ExecutionDescriptor) {
			candidate.RetirementPage.PageID = knowl.PageID(strings.Repeat("x", 4097))
		},
		"extra hierarchy payload": func(candidate *knowl.ExecutionDescriptor) {
			candidate.Hierarchy = &knowl.HierarchyExecutionDescriptor{SnapshotDigest: strings.Repeat("c", 64), PlannerVersion: "v1"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := descriptor
			payload := *descriptor.RetirementPage
			candidate.RetirementPage = &payload
			mutate(&candidate)
			if err := ValidateOperationDescriptor(identity, candidate); err == nil {
				t.Fatalf("ValidateOperationDescriptor() accepted %#v", candidate)
			}
		})
	}
}
