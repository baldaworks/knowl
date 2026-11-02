package storetest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type HierarchyRetryStore interface {
	app.OperationStore
	app.HierarchyRetryStore
	HierarchyRetryHistory(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, limit int) ([]app.HierarchyRetryAttempt, error)
}

// RunHierarchyRetryContract exercises durable recovery, including two competing connections.
func RunHierarchyRetryContract(t *testing.T, store, peer HierarchyRetryStore) {
	t.Helper()
	scope := knowl.ScopeRef("hierarchy_retry")
	schema := []byte("# Retry schema\n")
	digest := fmt.Sprintf("%x", sha256.Sum256(schema))
	identity := knowl.OperationIdentity{Scope: scope, Kind: knowl.WorkHierarchy, Subject: "hierarchy-retry-test", Revision: digest, Digest: strings.Repeat("a", 64)}
	id, err := app.OperationIDForIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := knowl.ExecutionDescriptor{OperationID: id, Kind: knowl.WorkHierarchy, Schema: knowl.SchemaDocument{Scope: scope, Digest: digest, Version: "1", Content: schema}, Hierarchy: &knowl.HierarchyExecutionDescriptor{SnapshotDigest: identity.Digest, PlannerVersion: identity.Subject}}
	if _, err := store.ReserveOperation(t.Context(), identity, descriptor); err != nil {
		t.Fatal(err)
	}
	request := app.HierarchyRetryRequest{Descriptor: descriptor, WorkAttempt: 1}
	if _, err := store.RetryHierarchy(t.Context(), request); !errors.Is(err, app.ErrHierarchyRetryConflict) {
		t.Fatalf("unfailed retry: %v", err)
	}
	failure := knowl.Failure{Class: "provider", Reason: "provider_output_exhausted", OperationID: string(id)}
	fail := func(token string) {
		t.Helper()
		claim, err := store.ClaimOperation(t.Context(), scope, id, knowl.WorkLease{Token: token, ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peer.RetryHierarchy(t.Context(), request); !errors.Is(err, app.ErrHierarchyRetryConflict) {
			t.Fatalf("leased retry: %v", err)
		}
		if err := store.FailClaim(t.Context(), scope, id, claim.Lease.Token, failure); err != nil {
			t.Fatal(err)
		}
	}
	fail("hierarchy-first")
	request.DryRun = true
	preview, err := peer.RetryHierarchy(t.Context(), request)
	if err != nil || preview.Requeued || preview.ManualRetryCount != 0 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	history, err := peer.HierarchyRetryHistory(t.Context(), scope, id, 10)
	if err != nil || len(history) != 0 {
		t.Fatalf("preview audit: %+v %v", history, err)
	}
	request.DryRun = false
	stale := request
	stale.WorkAttempt = 2
	if _, err := peer.RetryHierarchy(t.Context(), stale); !errors.Is(err, app.ErrHierarchyRetryConflict) {
		t.Fatalf("stale attempt: %v", err)
	}
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	for _, candidate := range []HierarchyRetryStore{store, peer} {
		go func() { <-start; _, err := candidate.RetryHierarchy(t.Context(), request); outcomes <- err }()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-outcomes
		switch {
		case err == nil:
			successes++
		case errors.Is(err, app.ErrHierarchyRetryConflict):
			conflicts++
		default:
			t.Fatalf("concurrent retry: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("retry winners=%d conflicts=%d", successes, conflicts)
	}
	op, err := peer.Operation(t.Context(), scope, id)
	if err != nil || op.Status != knowl.StatusReceived || op.WorkAttempt != 1 || op.ManualRetryCount != 1 {
		t.Fatalf("requeue: %+v %v", op, err)
	}
	fail("hierarchy-second")
	request.WorkAttempt = 2
	if _, err := peer.RetryHierarchy(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	history, err = peer.HierarchyRetryHistory(t.Context(), scope, id, 10)
	if err != nil || len(history) != 2 || history[0].WorkAttempt != 2 || history[1].WorkAttempt != 1 || history[0].ManualRetryCount != 2 || history[1].Failure != failure || history[0].FailedAt.IsZero() || history[0].RetriedAt.Before(history[0].FailedAt) {
		t.Fatalf("audit: %+v %v", history, err)
	}
	other, err := peer.HierarchyRetryHistory(t.Context(), "another_scope", id, 10)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-scope audit: %+v %v", other, err)
	}
}
