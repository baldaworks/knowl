package storetest

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const retirementLegacySourceID knowl.SourceID = "legacy"

// RunRetirementWorkContract verifies that page-only retirement work reuses the
// generic operation table without changing existing kind semantics.
func RunRetirementWorkContract(t *testing.T, store app.OperationStore, scope knowl.ScopeRef, isConflict func(error) bool, dropOwnerAssociation func(*testing.T, knowl.OperationID)) {
	t.Helper()
	schemaBytes := []byte("# Retirement schema\n")
	schemaDigest := fmt.Sprintf("%x", sha256.Sum256(schemaBytes))
	generation := strings.Repeat("a", 64)
	identity := app.RetirementPageOperationIdentity(scope, generation, "concepts/one")
	id, err := app.OperationIDForIdentity(identity)
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
			Owner:                knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: retirementLegacySourceID},
		},
	}
	first, err := store.ReserveOperation(t.Context(), identity, descriptor)
	if err != nil {
		t.Fatalf("ReserveOperation() error = %v", err)
	}
	dropOwnerAssociation(t, first.ID)
	replay, err := store.ReserveOperation(t.Context(), identity, descriptor)
	if err != nil || replay.New || replay.ID != first.ID || !reflect.DeepEqual(replay.Descriptor, descriptor) {
		t.Fatalf("ReserveOperation() replay = %#v, %v", replay, err)
	}
	if !first.New || first.Kind != knowl.WorkSourceRetirementPage || first.Key != (knowl.OperationKey{}) {
		t.Fatalf("first retirement reservation = %#v", first)
	}

	changed := descriptor
	changedPayload := *descriptor.RetirementPage
	changedPayload.Owner = knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "replacement"}
	changed.RetirementPage = &changedPayload
	if _, err := store.ReserveOperation(t.Context(), identity, changed); !isConflict(err) {
		t.Fatalf("same identity with changed payload error = %v", err)
	}

	lease := knowl.WorkLease{Token: "retirement-worker", ExpiresAt: time.Now().UTC().Add(50 * time.Millisecond)}
	claim, err := store.ClaimReady(t.Context(), scope, lease)
	if err != nil || claim.Operation.ID != first.ID || claim.Operation.Kind != knowl.WorkSourceRetirementPage || !reflect.DeepEqual(claim.Descriptor, descriptor) {
		t.Fatalf("ClaimReady() = %#v, %v", claim, err)
	}
	time.Sleep(75 * time.Millisecond)
	reclaimed, err := store.ClaimOperation(t.Context(), scope, first.ID, knowl.WorkLease{Token: "retirement-replay", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil || reclaimed.Operation.ID != first.ID || reclaimed.Operation.WorkAttempt != claim.Operation.WorkAttempt+1 || !reflect.DeepEqual(reclaimed.Descriptor, descriptor) {
		t.Fatalf("ClaimOperation() after lease expiry = %#v, %v", reclaimed, err)
	}
	if err := store.ReleaseClaim(t.Context(), scope, first.ID, reclaimed.Lease.Token); err != nil {
		t.Fatalf("ReleaseClaim() error = %v", err)
	}

	retirementStore, ok := store.(app.RetirementOperationStore)
	if !ok {
		t.Fatal("store does not implement RetirementOperationStore")
	}
	associator, ok := store.(app.RetirementOperationAssociator)
	if !ok {
		t.Fatal("store does not implement RetirementOperationAssociator")
	}
	page, err := retirementStore.RetirementOperations(t.Context(), app.RetirementOperationReadRequest{
		Scope: scope, Owner: descriptor.RetirementPage.Owner, RetirementGeneration: generation, Limit: 1,
	})
	if err != nil || len(page.Items) != 1 || page.Items[0].Operation.ID != first.ID ||
		page.Items[0].PageID != descriptor.RetirementPage.PageID || page.Items[0].Owner != descriptor.RetirementPage.Owner ||
		page.Items[0].RetirementGeneration != generation {
		t.Fatalf("RetirementOperations() = %#v, %v", page, err)
	}
	secondOwner := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "second-retiring-owner"}
	if err := associator.AssociateRetirementOperation(t.Context(), scope, first.ID, secondOwner, generation, descriptor.RetirementPage.PageID); err != nil {
		t.Fatalf("AssociateRetirementOperation() error = %v", err)
	}
	shared, err := retirementStore.RetirementOperations(t.Context(), app.RetirementOperationReadRequest{
		Scope: scope, Owner: secondOwner, RetirementGeneration: generation, Limit: 1,
	})
	if err != nil || len(shared.Items) != 1 || shared.Items[0].Operation.ID != first.ID || shared.Items[0].Owner != secondOwner {
		t.Fatalf("shared RetirementOperations() = %#v, %v", shared, err)
	}
	if err := store.Fail(t.Context(), first.ID, knowl.Failure{Class: "retirement_planning", Reason: app.RetirementPageStaleCode, OperationID: string(first.ID)}); err != nil {
		t.Fatalf("Fail() retirement operation: %v", err)
	}
	retriedAt := time.Now().UTC().Add(time.Second)
	requeued, err := retirementStore.RetryRetirementOperation(t.Context(), scope, first.ID, generation, retriedAt)
	if err != nil || !requeued {
		t.Fatalf("RetryRetirementOperation() = %v, %v", requeued, err)
	}
	retried, err := store.Operation(t.Context(), scope, first.ID)
	if err != nil || retried.Status != knowl.StatusReceived || retried.ManualRetryCount != 1 || retried.Failure != nil {
		t.Fatalf("retried operation = %#v, %v", retried, err)
	}
	if changed, err := retirementStore.RetryRetirementOperation(t.Context(), scope, first.ID, generation, retriedAt.Add(time.Second)); err != nil || changed {
		t.Fatalf("non-failed retirement retry = %v, %v", changed, err)
	}
}
