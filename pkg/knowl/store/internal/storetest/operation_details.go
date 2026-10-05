package storetest

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// OperationDetailsHarness supplies persistence and deliberate legacy/corruption controls.
type OperationDetailsHarness struct {
	Store          app.OperationStore
	OpenPeer       func(*testing.T) app.OperationStore
	ContextPayload func(*testing.T, knowl.OperationID, *string)
	LegacyPlan     func(*testing.T, knowl.OperationID)
	Conflict       error
	InvalidState   error
	Scope          knowl.ScopeRef
}

// RunOperationDetails verifies both stores through their real public interfaces.
func RunOperationDetails(t *testing.T, h OperationDetailsHarness) {
	t.Helper()
	ctx := t.Context()
	writer, ok := h.Store.(app.OperationContextReportStore)
	if !ok {
		t.Fatal("store lacks operation context persistence")
	}
	key, meta := Fixture(h.Scope, "context", time.Unix(1, 0).UTC())
	reserved, err := h.Store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Context != nil || reserved.Plan != nil {
		t.Fatal("invented legacy facts")
	}
	id := reserved.ID
	report := knowl.OperationContextReport{Version: 1, Outcome: knowl.ContextSelectionFailed, VectorProjection: &knowl.VectorProjectionStatus{State: knowl.VectorInvalid, Reason: knowl.RetrievalProjectionNotReady}}
	if err := writer.SaveOperationContextReport(ctx, "foreign", id, 0, report); !errors.Is(err, app.ErrOperationNotFound) {
		t.Fatalf("scope: %v", err)
	}
	future := report
	future.WorkAttempt = 1
	if err := writer.SaveOperationContextReport(ctx, key.Scope, id, 1, future); !errors.Is(err, app.ErrApplyLeaseConflict) {
		t.Fatalf("attempt: %v", err)
	}
	if err := writer.SaveOperationContextReport(ctx, key.Scope, id, 0, report); err != nil {
		t.Fatal(err)
	}
	peer := h.OpenPeer(t)
	peerWriter, ok := peer.(app.OperationContextReportStore)
	if !ok {
		t.Fatal("peer lacks context persistence")
	}
	if err := peerWriter.SaveOperationContextReport(ctx, key.Scope, id, 0, report); err != nil {
		t.Fatalf("identical peer write: %v", err)
	}
	operation, err := peer.Operation(ctx, key.Scope, id)
	if err != nil || !reflect.DeepEqual(operation.Context, &report) {
		t.Fatalf("durable context=%+v %v", operation.Context, err)
	}
	changed := report
	changed.VectorProjection = &knowl.VectorProjectionStatus{State: knowl.VectorInvalid, Reason: knowl.RetrievalDeadline}
	if err := peerWriter.SaveOperationContextReport(ctx, key.Scope, id, 0, changed); !errors.Is(err, h.Conflict) {
		t.Fatalf("same attempt rewrite: %v", err)
	}
	claim, err := h.Store.ClaimOperation(ctx, key.Scope, id, knowl.WorkLease{Token: "context-worker", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Operation.WorkAttempt != 1 || !reflect.DeepEqual(claim.Operation.Context, &report) {
		t.Fatalf("historical context lost: %+v", claim.Operation)
	}
	if err := writer.SaveOperationContextReport(ctx, key.Scope, id, 0, report); !errors.Is(err, app.ErrApplyLeaseConflict) {
		t.Fatalf("stale write: %v", err)
	}
	changed.WorkAttempt = 1
	if err := writer.SaveOperationContextReport(ctx, key.Scope, id, 0, changed); !errors.Is(err, app.ErrApplyLeaseConflict) {
		t.Fatalf("mismatched payload attempt: %v", err)
	}
	if err := writer.SaveOperationContextReport(ctx, key.Scope, id, 1, changed); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.Fail(ctx, id, knowl.Failure{Class: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := peerWriter.SaveOperationContextReport(ctx, key.Scope, id, 1, changed); err != nil {
		t.Fatalf("terminal identical replay: %v", err)
	}
	other := changed
	other.VectorProjection = report.VectorProjection
	if err := writer.SaveOperationContextReport(ctx, key.Scope, id, 1, other); !errors.Is(err, h.Conflict) {
		t.Fatalf("terminal rewrite: %v", err)
	}
	h.ContextPayload(t, id, nil)
	if err := writer.SaveOperationContextReport(ctx, key.Scope, id, 1, changed); !errors.Is(err, h.InvalidState) {
		t.Fatalf("terminal absent write: %v", err)
	}
	for _, payload := range []string{
		`{"version":99}`, `{"version":1,"work_attempt":2,"outcome":"selection_failed","entries_omitted":0}`,
		`{"version":1,"outcome":"selection_failed","entries_omitted":0}`,
		`{"version":1,"work_attempt":null,"outcome":"selection_failed","entries_omitted":0}`,
		`{"version":1,"work_attempt":1,"outcome":"selection_failed"}`,
		`{"version":1,"work_attempt":1,"outcome":"selection_failed","entries_omitted":null}`,
		`{"version":1,"work_attempt":1,"outcome":"assembly_failed","candidate_count":0,"entries_omitted":0,"budget":{"max_bytes":4096,"omitted_count":0}}`,
		`{"version":1,"work_attempt":1,"outcome":"assembly_failed","candidate_count":0,"entries_omitted":0,"budget":{"max_bytes":4096,"included_count":null,"omitted_count":0}}`,
		`{"version":1,"work_attempt":1,"outcome":"assembly_failed","candidate_count":0,"entries_omitted":0,"budget":{"max_bytes":4096,"included_count":0}}`,
		`{"version":1,"work_attempt":1,"outcome":"assembly_failed","candidate_count":0,"entries_omitted":0,"budget":{"max_bytes":4096,"included_count":0,"omitted_count":null}}`,
	} {
		h.ContextPayload(t, id, &payload)
		if _, err := peer.Operation(ctx, key.Scope, id); !errors.Is(err, app.ErrOperationContextReportInvalid) {
			t.Fatalf("corruption: %v", err)
		}
		if err := peerWriter.SaveOperationContextReport(ctx, key.Scope, id, 1, changed); !errors.Is(err, app.ErrOperationContextReportInvalid) {
			t.Fatalf("corrupt state write accepted: %v", err)
		}
	}
	payload, err := app.EncodeOperationContextReport(changed)
	if err != nil {
		t.Fatal(err)
	}
	h.ContextPayload(t, id, &payload)
	operation, err = h.OpenPeer(t).Operation(ctx, key.Scope, id)
	if err != nil || !reflect.DeepEqual(operation.Context, &changed) || operation.Status != knowl.StatusFailed {
		t.Fatalf("terminal restart: %+v %v", operation, err)
	}
	replayed, err := peer.Reserve(ctx, key, meta)
	if err != nil || replayed.New || !reflect.DeepEqual(replayed.Context, &changed) {
		t.Fatalf("terminal reserve replay: %+v %v", replayed, err)
	}

	key, meta = Fixture(h.Scope, "plan", time.Unix(1, 0).UTC())
	reserved, err = h.Store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatal(err)
	}
	summary := knowl.PlanSummary{Digest: strings.Repeat("d", 64), FileCount: 3}
	if err := h.Store.SavePlan(ctx, reserved.ID, summary); err != nil {
		t.Fatal(err)
	}
	operation, err = peer.Operation(ctx, key.Scope, reserved.ID)
	if err != nil || operation.Plan == nil || operation.Plan.Digest != summary.Digest || operation.Plan.FileCount == nil || *operation.Plan.FileCount != 3 {
		t.Fatalf("stored plan: %+v %v", operation.Plan, err)
	}
	if err := peer.SavePlan(ctx, reserved.ID, summary); err != nil {
		t.Fatal(err)
	}
	summary.FileCount = 4
	if err := peer.SavePlan(ctx, reserved.ID, summary); !errors.Is(err, h.Conflict) {
		t.Fatalf("count rewrite: %v", err)
	}
	h.LegacyPlan(t, reserved.ID)
	if err := peer.SavePlan(ctx, reserved.ID, summary); err != nil {
		t.Fatalf("legacy repeat: %v", err)
	}
	operation, err = peer.Operation(ctx, key.Scope, reserved.ID)
	if err != nil || operation.Plan == nil || operation.Plan.FileCount != nil {
		t.Fatalf("legacy count backfilled: %+v %v", operation.Plan, err)
	}
	key, meta = Fixture(h.Scope, "opaque", time.Unix(1, 0).UTC())
	reserved, err = h.Store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Store.SavePlan(ctx, reserved.ID, knowl.PlanSummary{Digest: "opaque-provider-secret"}); err != nil {
		t.Fatalf("historical digest contract tightened: %v", err)
	}
	operation, err = peer.Operation(ctx, key.Scope, reserved.ID)
	if err != nil || app.PublicOperationDetails(operation).Plan != nil {
		t.Fatalf("opaque optional digest exposed: %v", err)
	}

	key, meta = Fixture(h.Scope, "concurrent", time.Unix(1, 0).UTC())
	reserved, err = h.Store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, participant := range []struct {
		writer app.OperationContextReportStore
		report knowl.OperationContextReport
	}{{writer, report}, {peerWriter, changed}} {
		participant.report.WorkAttempt = 0
		go func() {
			<-start
			results <- participant.writer.SaveOperationContextReport(ctx, key.Scope, reserved.ID, 0, participant.report)
		}()
	}
	close(start)
	first, second := <-results, <-results
	won := (first == nil && errors.Is(second, h.Conflict)) || (second == nil && errors.Is(first, h.Conflict))
	if !won {
		t.Fatalf("concurrent rewrite guard: %v %v", first, second)
	}
	operation, err = peer.Operation(ctx, key.Scope, reserved.ID)
	if err != nil || operation.Context == nil {
		t.Fatalf("concurrent durable report: %v", err)
	}
	if err := peerWriter.SaveOperationContextReport(ctx, key.Scope, reserved.ID, 0, *operation.Context); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.SavePlan(ctx, reserved.ID, knowl.PlanSummary{Digest: strings.Repeat("e", 64), FileCount: -1}); !errors.Is(err, h.Conflict) {
		t.Fatalf("negative plan count: %v", err)
	}
	if err := h.Store.SavePlan(ctx, reserved.ID, knowl.PlanSummary{Digest: strings.Repeat("e", 64)}); err != nil {
		t.Fatal(err)
	}
	operation, err = peer.Operation(ctx, key.Scope, reserved.ID)
	if err != nil || operation.Plan == nil || operation.Plan.FileCount == nil || *operation.Plan.FileCount != 0 {
		t.Fatalf("known zero count: %+v %v", operation.Plan, err)
	}
	if err := h.Store.MarkApplying(ctx, reserved.ID, knowl.Lease{Token: "details-commit", ExpiresAt: time.Now().UTC().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.CommitOutcome(ctx, reserved.ID, knowl.ContentCommit{Generation: "details-committed"}); err != nil {
		t.Fatal(err)
	}
	if err := peerWriter.SaveOperationContextReport(ctx, key.Scope, reserved.ID, 0, *operation.Context); err != nil {
		t.Fatal(err)
	}
	h.ContextPayload(t, reserved.ID, nil)
	if err := writer.SaveOperationContextReport(ctx, key.Scope, reserved.ID, 0, report); !errors.Is(err, h.InvalidState) {
		t.Fatalf("committed missing report changed: %v", err)
	}
	runBoundedOperationDetails(t, h)
}

// Measures actual reader/writer allocations; loading an oversized field before rejecting it fails.
func runBoundedOperationDetails(t *testing.T, h OperationDetailsHarness) {
	t.Helper()
	ctx := t.Context()
	scope := childScope(h.Scope, "oversized")
	key, meta := Fixture(scope, "large", time.Unix(1, 0).UTC())
	reserved, err := h.Store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatal(err)
	}
	writer := h.Store.(app.OperationContextReportStore)
	report := knowl.OperationContextReport{Version: 1, Outcome: knowl.ContextSelectionFailed}
	large := strings.Repeat("x", 2<<20)
	h.ContextPayload(t, reserved.ID, &large)
	boundedAllocations(t, "corrupt context read", func(b *testing.T) {
		if _, err := h.Store.Operation(ctx, scope, reserved.ID); !errors.Is(err, app.ErrOperationContextReportInvalid) {
			b.Fatalf("oversized context: %v", err)
		}
	})
	boundedAllocations(t, "corrupt stored context write", func(b *testing.T) {
		if err := writer.SaveOperationContextReport(ctx, scope, reserved.ID, 0, report); !errors.Is(err, app.ErrOperationContextReportInvalid) {
			b.Fatalf("oversized prior context: %v", err)
		}
	})
	h.ContextPayload(t, reserved.ID, nil)
	summary := knowl.PlanSummary{Digest: large}
	if err := h.Store.SavePlan(ctx, reserved.ID, summary); err != nil {
		t.Fatalf("legacy arbitrary digest contract: %v", err)
	}
	if err := h.Store.SavePlan(ctx, reserved.ID, summary); err != nil {
		t.Fatalf("legacy arbitrary digest replay: %v", err)
	}
	boundedAllocations(t, "opaque digest read", func(b *testing.T) {
		op, err := h.Store.Operation(ctx, scope, reserved.ID)
		if err != nil || op.Plan != nil {
			b.Fatalf("oversized optional plan: %+v %v", op.Plan, err)
		}
	})
	boundedAllocations(t, "context write beside opaque digest", func(b *testing.T) {
		if err := writer.SaveOperationContextReport(ctx, scope, reserved.ID, 0, report); err != nil {
			b.Fatal(err)
		}
	})
	h.ContextPayload(t, reserved.ID, nil)
	boundedAllocations(t, "claim beside opaque digest", func(b *testing.T) {
		claim, err := h.Store.ClaimOperation(ctx, scope, reserved.ID, futureLease("bounded-claim"))
		if err != nil || claim.Operation.Plan != nil {
			b.Fatalf("bounded claim: %+v %v", claim.Operation.Plan, err)
		}
		if err := h.Store.ReleaseClaim(ctx, scope, reserved.ID, claim.Lease.Token); err != nil {
			b.Fatal(err)
		}
	})
	boundedAllocations(t, "ready claim beside opaque digest", func(b *testing.T) {
		claim, err := h.Store.ClaimReady(ctx, scope, futureLease("bounded-ready"))
		if err != nil || claim.Operation.ID != reserved.ID || claim.Operation.Plan != nil {
			b.Fatalf("bounded ready claim: %+v %v", claim.Operation.Plan, err)
		}
		if err := h.Store.ReleaseClaim(ctx, scope, reserved.ID, claim.Lease.Token); err != nil {
			b.Fatal(err)
		}
	})
}

func boundedAllocations(t *testing.T, name string, call func(*testing.T)) {
	t.Helper()
	const samples = 5
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range samples {
		call(t)
	}
	runtime.ReadMemStats(&after)
	allocated := (after.TotalAlloc - before.TotalAlloc) / samples
	if allocated > 512<<10 {
		t.Fatalf("%s allocated %d bytes for optional data; want <=512KiB", name, allocated)
	}
}
