package app

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestExecutionDescriptorFromMeta(t *testing.T) {
	t.Parallel()
	schemaContent := []byte("# Schema\n")
	schemaDigest := digestForTest(schemaContent)
	key := knowl.OperationKey{
		Scope:   fixtureScope,
		Source:  knowl.SourceRef{Adapter: fixtureAdapter, ID: fixtureDecisionID},
		Version: knowl.SourceVersion{Version: "1", Digest: strings.Repeat("a", 64)},
	}
	meta := knowl.OperationMeta{
		Key: key,
		AcceptedSource: knowl.AcceptedSource{
			Scope: key.Scope, Source: key.Source, Version: key.Version,
			MediaType: "text/markdown", ManifestRef: "raw/source/version/manifest.yaml",
		},
		Schema:       knowl.SchemaDocument{Scope: key.Scope, Digest: schemaDigest, Version: "1", Content: schemaContent},
		SchemaDigest: schemaDigest,
	}

	descriptor, err := ExecutionDescriptorFromMeta(fixtureOperationID, key, meta)
	if err != nil {
		t.Fatalf("ExecutionDescriptorFromMeta() error = %v", err)
	}
	if descriptor.OperationID != fixtureOperationID || descriptor.Source != meta.AcceptedSource || descriptor.Schema.Digest != schemaDigest {
		t.Fatalf("descriptor = %#v", descriptor)
	}
}

func TestExecutionDescriptorValidationRejectsInvalidInputsWithoutDisclosure(t *testing.T) {
	t.Parallel()
	schemaContent := []byte("secret schema policy")
	key := knowl.OperationKey{
		Scope:   fixtureScope,
		Source:  knowl.SourceRef{Adapter: fixtureAdapter, ID: fixtureDecisionID},
		Version: knowl.SourceVersion{Version: "1", Digest: strings.Repeat("a", 64)},
	}
	valid := knowl.ExecutionDescriptor{
		OperationID: fixtureOperationID,
		Source: knowl.AcceptedSource{
			Scope: key.Scope, Source: key.Source, Version: key.Version,
			MediaType: "text/markdown", ManifestRef: "raw/source/version/manifest.yaml",
		},
		Schema: knowl.SchemaDocument{Scope: key.Scope, Digest: digestForTest(schemaContent), Content: schemaContent},
	}

	tests := []struct {
		name string
		edit func(*knowl.ExecutionDescriptor)
	}{
		{name: "scope mismatch", edit: func(value *knowl.ExecutionDescriptor) { value.Source.Scope = "other" }},
		{name: "missing media type", edit: func(value *knowl.ExecutionDescriptor) { value.Source.MediaType = "" }},
		{name: "absolute manifest", edit: func(value *knowl.ExecutionDescriptor) { value.Source.ManifestRef = "/raw/manifest.yaml" }},
		{name: "escaping manifest", edit: func(value *knowl.ExecutionDescriptor) { value.Source.ManifestRef = "../manifest.yaml" }},
		{name: "schema digest mismatch", edit: func(value *knowl.ExecutionDescriptor) { value.Schema.Digest = strings.Repeat("b", 64) }},
		{name: "empty schema", edit: func(value *knowl.ExecutionDescriptor) { value.Schema.Content = nil }},
		{name: "oversized schema", edit: func(value *knowl.ExecutionDescriptor) { value.Schema.Content = make([]byte, maxExecutionSchemaBytes+1) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor := valid
			test.edit(&descriptor)
			err := ValidateExecutionDescriptor(key, descriptor)
			if !errors.Is(err, ErrExecutionDescriptorUnavailable) {
				t.Fatalf("ValidateExecutionDescriptor() error = %v", err)
			}
			if strings.Contains(err.Error(), string(schemaContent)) || strings.Contains(err.Error(), valid.Source.ManifestRef) {
				t.Fatalf("error disclosed descriptor content: %q", err)
			}
		})
	}
}

func TestHierarchyOperationIdentityAndDescriptorAreDeterministicAndBounded(t *testing.T) {
	t.Parallel()
	schemaContent := []byte("# Hierarchy schema\n")
	schemaDigest := digestForTest(schemaContent)
	identity := knowl.OperationIdentity{
		Scope: fixtureScope, Kind: knowl.WorkHierarchy, Subject: "hierarchy-v1",
		Revision: schemaDigest, Digest: strings.Repeat("b", 64),
	}
	id, err := OperationIDForIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := OperationIDForIdentity(identity); err != nil || replay != id {
		t.Fatalf("OperationIDForIdentity() replay = %q, %v; want %q", replay, err, id)
	}
	descriptor := knowl.ExecutionDescriptor{
		OperationID: id, Kind: knowl.WorkHierarchy,
		Hierarchy: &knowl.HierarchyExecutionDescriptor{SnapshotDigest: identity.Digest, PlannerVersion: identity.Subject},
		Schema:    knowl.SchemaDocument{Scope: fixtureScope, Digest: schemaDigest, Version: "1", Content: schemaContent},
	}
	if err := ValidateOperationDescriptor(identity, descriptor); err != nil {
		t.Fatalf("ValidateOperationDescriptor() error: %v", err)
	}
	if err := ValidateGenericExecutionDescriptor(fixtureScope, descriptor); err != nil {
		t.Fatalf("ValidateGenericExecutionDescriptor() error: %v", err)
	}

	invalid := descriptor
	invalid.Hierarchy = &knowl.HierarchyExecutionDescriptor{SnapshotDigest: strings.Repeat("c", 64), PlannerVersion: identity.Subject}
	if err := ValidateGenericExecutionDescriptor(fixtureScope, invalid); !errors.Is(err, ErrExecutionDescriptorUnavailable) {
		t.Fatalf("stale hierarchy descriptor error = %v", err)
	}
	oversized := identity
	oversized.Subject = strings.Repeat("x", maxPlannerVersionBytes+1)
	if _, err := OperationIDForIdentity(oversized); !errors.Is(err, ErrExecutionDescriptorUnavailable) {
		t.Fatalf("oversized planner version error = %v", err)
	}
}

func TestOperationJSONRemainsRedacted(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(knowl.Operation{
		ID: fixtureOperationID,
		Key: knowl.OperationKey{
			Scope:   fixtureScope,
			Source:  knowl.SourceRef{Adapter: fixtureAdapter, ID: fixtureDecisionID},
			Version: knowl.SourceVersion{Version: "1", Digest: strings.Repeat("a", 64)},
		},
		Status: knowl.StatusReceived,
	})
	if err != nil {
		t.Fatalf("json.Marshal(Operation) error = %v", err)
	}
	for _, forbidden := range []string{fixtureSchema, "descriptor", "manifest_ref", "lease", "token"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Operation JSON %q contains %q", encoded, forbidden)
		}
	}
}

func digestForTest(content []byte) string {
	return fmtDigest(sha256.Sum256(content))
}

func fmtDigest(digest [sha256.Size]byte) string {
	const hex = "0123456789abcdef"
	encoded := make([]byte, len(digest)*2)
	for i, value := range digest {
		encoded[i*2] = hex[value>>4]
		encoded[i*2+1] = hex[value&0x0f]
	}
	return string(encoded)
}
