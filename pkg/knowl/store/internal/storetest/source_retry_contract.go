package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// SourceRetryStore is the minimal recovery surface shared by both backends.
type SourceRetryStore interface {
	RetrySourceMaintenance(ctx context.Context, request app.SourceMaintenanceRetryRequest) (app.SourceMaintenanceRetryResult, error)
	Operation(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.Operation, error)
}

const (
	testProviderFailureClass  = "provider"
	testProviderFailureReason = "provider_run"
	testUnsafeFailureReason   = "provider secret"
	testSourceRevision        = "revision-1"
)

// SourceRetryFixture describes one directly seeded recovery boundary row.
type SourceRetryFixture struct {
	OperationID         knowl.OperationID
	DocumentID          knowl.DocumentID
	Revision            string
	MaintenanceRevision string
	OperationScope      knowl.ScopeRef
	Kind                knowl.WorkKind
	Status              knowl.OperationStatus
	FailureClass        string
	FailureReason       string
	WorkAttempt         int
	RetryAttempt        int
	ManualRetryCount    int
	WorkToken           string
	ApplyToken          string
	LeasesExpired       bool
}

// SourceRetryAudit exposes only operational cleanup fields needed by the contract.
type SourceRetryAudit struct {
	PlanDigest       string
	CommitGeneration string
	WorkToken        string
	ApplyToken       string
}

// SourceRetryHarness supplies backend-specific fixture insertion and audit reads.
type SourceRetryHarness struct {
	Store SourceRetryStore
	Seed  func(t *testing.T, scope knowl.ScopeRef, sourceID knowl.SourceID, fixtures []SourceRetryFixture)
	Audit func(t *testing.T, id knowl.OperationID) SourceRetryAudit
	Scope knowl.ScopeRef
}

// RunSourceRetryContract verifies atomic filtering, reset semantics,
// idempotency, and bounded results for explicit recovery.
func RunSourceRetryContract(t *testing.T, harness SourceRetryHarness) {
	t.Helper()
	if harness.Store == nil || harness.Seed == nil || harness.Audit == nil || harness.Scope == "" {
		t.Fatal("source retry harness is incomplete")
	}
	ctx := context.Background()

	t.Run("preview_filter_mutation_and_idempotency", func(t *testing.T) {
		scope := knowl.ScopeRef(fmt.Sprintf("%s_valid", harness.Scope))
		const sourceID = knowl.SourceID("retry-valid")
		fixtures := []SourceRetryFixture{
			retryFixture(scope, "provider-operation", testProviderFailureClass, testProviderFailureReason),
			retryFixture(scope, "source-operation", "source", ""),
			retryFixture(scope, "staging-operation", "staging", ""),
		}
		fixtures[0].WorkAttempt = 5
		fixtures[0].RetryAttempt = 3
		fixtures[0].ManualRetryCount = 2
		harness.Seed(t, scope, sourceID, fixtures)

		request := app.SourceMaintenanceRetryRequest{Scope: scope, SourceID: sourceID, FailureClasses: []string{testProviderFailureClass}, DryRun: true}
		preview, err := harness.Store.RetrySourceMaintenance(ctx, request)
		if err != nil || preview.Matched != 1 || preview.Requeued != 0 || preview.Rejected != 0 || preview.Truncated ||
			len(preview.OperationIDs) != 1 || preview.OperationIDs[0] != fixtures[0].OperationID {
			t.Fatalf("retry preview = %#v, err = %v", preview, err)
		}
		before, err := harness.Store.Operation(ctx, scope, fixtures[0].OperationID)
		if err != nil || before.Status != knowl.StatusFailed || before.ManualRetryCount != 2 {
			t.Fatalf("operation after preview = %#v, err = %v", before, err)
		}

		request.DryRun = false
		applied, err := harness.Store.RetrySourceMaintenance(ctx, request)
		if err != nil || applied.Matched != 1 || applied.Requeued != 1 || applied.Rejected != 0 {
			t.Fatalf("retry mutation = %#v, err = %v", applied, err)
		}
		after, err := harness.Store.Operation(ctx, scope, fixtures[0].OperationID)
		if err != nil || after.ID != fixtures[0].OperationID || after.Status != knowl.StatusReceived || after.Failure != nil ||
			after.WorkAttempt != 5 || after.RetryAttempt != 0 || after.ManualRetryCount != 3 {
			t.Fatalf("requeued operation = %#v, err = %v", after, err)
		}
		if audit := harness.Audit(t, fixtures[0].OperationID); audit != (SourceRetryAudit{}) {
			t.Fatalf("requeued cleanup = %#v", audit)
		}
		for _, untouched := range fixtures[1:] {
			operation, readErr := harness.Store.Operation(ctx, scope, untouched.OperationID)
			if readErr != nil || operation.Status != knowl.StatusFailed || operation.Failure == nil || operation.Failure.Class != untouched.FailureClass {
				t.Fatalf("filtered operation = %#v, err = %v", operation, readErr)
			}
		}

		request.FailureClasses = []string{"source", "unsafe secret"}
		if _, err := harness.Store.RetrySourceMaintenance(ctx, request); !errors.Is(err, app.ErrSourceInvalid) {
			t.Fatalf("mixed invalid class error = %v", err)
		}
		sourceOperation, err := harness.Store.Operation(ctx, scope, fixtures[1].OperationID)
		if err != nil || sourceOperation.Status != knowl.StatusFailed {
			t.Fatalf("operation after invalid request = %#v, err = %v", sourceOperation, err)
		}

		request.FailureClasses = []string{testProviderFailureClass}
		replay, err := harness.Store.RetrySourceMaintenance(ctx, request)
		if err != nil || replay.Matched != 0 || replay.Requeued != 0 || len(replay.OperationIDs) != 0 {
			t.Fatalf("retry replay = %#v, err = %v", replay, err)
		}
	})

	boundaries := []struct {
		name   string
		mutate func(*SourceRetryFixture)
	}{
		{name: "committed", mutate: func(fixture *SourceRetryFixture) { fixture.Status = knowl.StatusCommitted }},
		{name: "hierarchy", mutate: func(fixture *SourceRetryFixture) { fixture.Kind = knowl.WorkHierarchy }},
		{name: "stale_revision", mutate: func(fixture *SourceRetryFixture) { fixture.MaintenanceRevision = "stale" }},
		{name: "cross_scope", mutate: func(fixture *SourceRetryFixture) { fixture.OperationScope += "_other" }},
		{name: "work_leased", mutate: func(fixture *SourceRetryFixture) { fixture.WorkToken = "active-work-owner" }},
		{name: "apply_leased", mutate: func(fixture *SourceRetryFixture) { fixture.ApplyToken = "active-apply-owner" }},
	}
	for _, boundary := range boundaries {
		t.Run("atomic_rejection_"+boundary.name, func(t *testing.T) {
			scope := knowl.ScopeRef(fmt.Sprintf("%s_%s", harness.Scope, boundary.name))
			sourceID := knowl.SourceID("retry-" + boundary.name)
			valid := retryFixture(scope, boundary.name+"-valid", testProviderFailureClass, testProviderFailureReason)
			invalid := retryFixture(scope, boundary.name+"-invalid", testProviderFailureClass, testProviderFailureReason)
			boundary.mutate(&invalid)
			harness.Seed(t, scope, sourceID, []SourceRetryFixture{valid, invalid})
			result, err := harness.Store.RetrySourceMaintenance(ctx, app.SourceMaintenanceRetryRequest{
				Scope: scope, SourceID: sourceID, FailureClasses: []string{testProviderFailureClass},
			})
			if !errors.Is(err, app.ErrSourceRetryConflict) || result.Matched != 2 || result.Requeued != 0 || result.Rejected != 1 {
				t.Fatalf("boundary retry = %#v, err = %v", result, err)
			}
			operation, readErr := harness.Store.Operation(ctx, scope, valid.OperationID)
			if readErr != nil || operation.Status != knowl.StatusFailed || operation.Failure == nil {
				t.Fatalf("valid peer after atomic rejection = %#v, err = %v", operation, readErr)
			}
		})
	}

	t.Run("expired_legacy_leases_are_recoverable", func(t *testing.T) {
		scope := knowl.ScopeRef(fmt.Sprintf("%s_expired_leases", harness.Scope))
		const sourceID = knowl.SourceID("retry-expired-leases")
		fixture := retryFixture(scope, "expired-legacy-leases", testProviderFailureClass, testProviderFailureReason)
		fixture.WorkToken = "expired-work-owner"
		fixture.ApplyToken = "expired-apply-owner"
		fixture.LeasesExpired = true
		harness.Seed(t, scope, sourceID, []SourceRetryFixture{fixture})

		result, err := harness.Store.RetrySourceMaintenance(ctx, app.SourceMaintenanceRetryRequest{
			Scope: scope, SourceID: sourceID, FailureClasses: []string{testProviderFailureClass},
		})
		if err != nil || result.Matched != 1 || result.Requeued != 1 || result.Rejected != 0 {
			t.Fatalf("expired legacy lease retry = %#v, err = %v", result, err)
		}
		operation, readErr := harness.Store.Operation(ctx, scope, fixture.OperationID)
		if readErr != nil || operation.Status != knowl.StatusReceived || operation.ManualRetryCount != 1 {
			t.Fatalf("operation after expired legacy lease retry = %#v, err = %v", operation, readErr)
		}
		if audit := harness.Audit(t, fixture.OperationID); audit != (SourceRetryAudit{}) {
			t.Fatalf("expired legacy lease cleanup = %#v", audit)
		}
	})

	t.Run("bounded_preview", func(t *testing.T) {
		scope := knowl.ScopeRef(fmt.Sprintf("%s_bounded", harness.Scope))
		const sourceID = knowl.SourceID("retry-bounded")
		fixtures := make([]SourceRetryFixture, app.MaxSourceMaintenanceRetryResultIDs()+1)
		for index := range fixtures {
			fixtures[index] = retryFixture(scope, fmt.Sprintf("bounded-%03d", index), testProviderFailureClass, testProviderFailureReason)
		}
		harness.Seed(t, scope, sourceID, fixtures)
		result, err := harness.Store.RetrySourceMaintenance(ctx, app.SourceMaintenanceRetryRequest{
			Scope: scope, SourceID: sourceID, FailureClasses: []string{testProviderFailureClass}, DryRun: true,
		})
		if err != nil || result.Matched != int64(len(fixtures)) || len(result.OperationIDs) != app.MaxSourceMaintenanceRetryResultIDs() || !result.Truncated {
			t.Fatalf("bounded retry preview = %#v, err = %v", result, err)
		}
	})
}

func retryFixture(scope knowl.ScopeRef, id, class, reason string) SourceRetryFixture {
	return SourceRetryFixture{
		OperationID: knowl.OperationID(id), DocumentID: knowl.DocumentID("docs/" + id + ".md"),
		Revision: testSourceRevision, MaintenanceRevision: testSourceRevision, OperationScope: scope,
		Kind: knowl.WorkSourceMaintenance, Status: knowl.StatusFailed, FailureClass: class, FailureReason: reason,
		WorkAttempt: 1, RetryAttempt: 1,
	}
}
