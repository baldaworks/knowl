package storetest

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type CorrectionReportHarness struct {
	Store                  app.OperationStore
	OpenPeer               func(*testing.T) app.OperationStore
	Payload                func(*testing.T, knowl.OperationID, *string)
	Scope                  knowl.ScopeRef
	Conflict, InvalidState error
}

// RunCorrectionReports exercises the same durability and corruption contract on both backends.
func RunCorrectionReports(t *testing.T, h CorrectionReportHarness) {
	t.Helper()
	ctx := t.Context()
	writer, ok := h.Store.(app.OperationCorrectionReportStore)
	if !ok {
		t.Fatal("store cannot persist correction reports")
	}
	key, meta := Fixture(h.Scope, "correction", time.Unix(1, 0).UTC())
	reserved, err := h.Store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatal(err)
	}
	id := reserved.ID
	if reserved.Correction != nil {
		t.Fatal("fabricated legacy correction")
	}
	zero := 0
	report := knowl.OperationCorrectionReport{Version: 1, MaxOutputBytes: app.MaxCorrectionOutputBytes, DeadlineNanos: int64(app.MaxCorrectionDeadline), Outcome: knowl.CorrectionProviderFailed, Turns: &zero, Corrections: &zero, OutputBytes: &zero}
	if err := writer.SaveOperationCorrectionReport(ctx, "foreign", id, 0, report); !errors.Is(err, app.ErrOperationNotFound) {
		t.Fatalf("scope guard: %v", err)
	}
	future := report
	future.WorkAttempt = 1
	if err := writer.SaveOperationCorrectionReport(ctx, key.Scope, id, 1, future); !errors.Is(err, app.ErrApplyLeaseConflict) {
		t.Fatalf("future write: %v", err)
	}
	if err := writer.SaveOperationCorrectionReport(ctx, key.Scope, id, 0, report); err != nil {
		t.Fatal(err)
	}
	peer := h.OpenPeer(t)
	peerWriter := peer.(app.OperationCorrectionReportStore)
	if err := peerWriter.SaveOperationCorrectionReport(ctx, key.Scope, id, 0, report); err != nil {
		t.Fatal(err)
	}
	operation, err := peer.Operation(ctx, key.Scope, id)
	if err != nil || !reflect.DeepEqual(operation.Correction, &report) {
		t.Fatalf("durable measured zeros: %+v %v", operation.Correction, err)
	}
	changed := report
	changed.Outcome = knowl.CorrectionCanceled
	if err := peerWriter.SaveOperationCorrectionReport(ctx, key.Scope, id, 0, changed); !errors.Is(err, h.Conflict) {
		t.Fatalf("immutable report: %v", err)
	}
	claim, err := h.Store.ClaimOperation(ctx, key.Scope, id, futureLease("correction-targeted"))
	if err != nil || claim.Operation.WorkAttempt != 1 || !reflect.DeepEqual(claim.Operation.Correction, &report) {
		t.Fatalf("targeted historical evidence: %+v %v", claim.Operation, err)
	}
	if err := writer.SaveOperationCorrectionReport(ctx, key.Scope, id, 0, report); !errors.Is(err, app.ErrApplyLeaseConflict) {
		t.Fatalf("stale write: %v", err)
	}
	changed.WorkAttempt = 1
	if err := writer.SaveOperationCorrectionReport(ctx, key.Scope, id, 0, changed); !errors.Is(err, app.ErrApplyLeaseConflict) {
		t.Fatalf("payload attempt mismatch: %v", err)
	}
	if err := h.Store.ReleaseClaim(ctx, key.Scope, id, claim.Lease.Token); err != nil {
		t.Fatal(err)
	}
	ready, err := peer.ClaimReady(ctx, key.Scope, futureLease("correction-ready"))
	if err != nil || ready.Operation.WorkAttempt != 2 || !reflect.DeepEqual(ready.Operation.Correction, &report) {
		t.Fatalf("ready historical evidence: %+v %v", ready.Operation, err)
	}
	changed.WorkAttempt = 2
	if err := writer.SaveOperationCorrectionReport(ctx, key.Scope, id, 2, changed); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.Fail(ctx, id, knowl.Failure{Class: "correction-fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := peerWriter.SaveOperationCorrectionReport(ctx, key.Scope, id, 2, changed); err != nil {
		t.Fatalf("terminal identical replay: %v", err)
	}
	other := changed
	other.Outcome = report.Outcome
	if err := peerWriter.SaveOperationCorrectionReport(ctx, key.Scope, id, 2, other); !errors.Is(err, h.Conflict) {
		t.Fatalf("terminal replacement: %v", err)
	}
	h.Payload(t, id, nil)
	if err := writer.SaveOperationCorrectionReport(ctx, key.Scope, id, 2, changed); !errors.Is(err, h.InvalidState) {
		t.Fatalf("terminal absent write: %v", err)
	}
	encoded, err := app.EncodeOperationCorrectionReport(changed)
	if err != nil {
		t.Fatal(err)
	}
	var valid map[string]any
	if err := json.Unmarshal([]byte(encoded), &valid); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"work_attempt", "max_corrections", "turns", "corrections", "output_bytes"} {
		for _, null := range []bool{false, true} {
			fields := make(map[string]any, len(valid))
			for name, value := range valid {
				fields[name] = value
			}
			if null {
				fields[field] = nil
			} else {
				delete(fields, field)
			}
			payload, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			assertCorruptCorrection(t, h, id, string(payload), changed)
		}
	}
	future = changed
	future.WorkAttempt = 3
	futurePayload, err := app.EncodeOperationCorrectionReport(future)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{futurePayload, `{"version":99}`, `{"secret":"private"}`, strings.Repeat("x", app.MaxOperationCorrectionReportBytes+1)} {
		assertCorruptCorrection(t, h, id, payload, changed)
	}
	h.Payload(t, id, &encoded)
	reopened := h.OpenPeer(t)
	operation, err = reopened.Operation(ctx, key.Scope, id)
	if err != nil || !reflect.DeepEqual(operation.Correction, &changed) || operation.Status != knowl.StatusFailed {
		t.Fatalf("terminal reopen: %+v %v", operation, err)
	}
	replayed, err := reopened.Reserve(ctx, key, meta)
	if err != nil || replayed.New || !reflect.DeepEqual(replayed.Correction, &changed) {
		t.Fatalf("terminal reserve replay: %+v %v", replayed, err)
	}
	runCorrectionAllocationBounds(t, h, report)
	runCorrectionTerminalAndConcurrent(t, h)
	runCorrectionFutureClaimGuards(t, h, report)
}

func runCorrectionFutureClaimGuards(t *testing.T, h CorrectionReportHarness, report knowl.OperationCorrectionReport) {
	t.Helper()
	for _, targeted := range []bool{true, false} {
		scope := childScope(h.Scope, "future-ready")
		if targeted {
			scope = childScope(h.Scope, "future-targeted")
		}
		key, meta := Fixture(scope, "future-correction", time.Unix(1, 0).UTC())
		reserved, err := h.Store.Reserve(t.Context(), key, meta)
		if err != nil {
			t.Fatal(err)
		}
		report.WorkAttempt = 1
		payload, err := app.EncodeOperationCorrectionReport(report)
		if err != nil {
			t.Fatal(err)
		}
		h.Payload(t, reserved.ID, &payload)
		if targeted {
			_, err = h.Store.ClaimOperation(t.Context(), scope, reserved.ID, futureLease("future-targeted"))
		} else {
			_, err = h.Store.ClaimReady(t.Context(), scope, futureLease("future-ready"))
		}
		if !errors.Is(err, app.ErrOperationCorrectionReportInvalid) {
			t.Fatalf("claim normalized corrupt future evidence (targeted=%t): %v", targeted, err)
		}
		h.Payload(t, reserved.ID, nil)
		operation, err := h.Store.Operation(t.Context(), scope, reserved.ID)
		if err != nil || operation.WorkAttempt != 0 {
			t.Fatalf("future claim changed original attempt: %+v %v", operation, err)
		}
	}
}

func runCorrectionTerminalAndConcurrent(t *testing.T, h CorrectionReportHarness) {
	t.Helper()
	key, meta := Fixture(childScope(h.Scope, "concurrent"), "correction", time.Unix(1, 0).UTC())
	reserved, err := h.Store.Reserve(t.Context(), key, meta)
	if err != nil {
		t.Fatal(err)
	}
	writer := h.Store.(app.OperationCorrectionReportStore)
	peerWriter := h.OpenPeer(t).(app.OperationCorrectionReportStore)
	report := knowl.OperationCorrectionReport{Version: 1, MaxOutputBytes: app.MaxCorrectionOutputBytes, DeadlineNanos: int64(app.MaxCorrectionDeadline), Outcome: knowl.CorrectionUnavailable}
	changed := report
	changed.MaxCorrections = 1
	start, results := make(chan struct{}), make(chan error, 2)
	for _, participant := range []struct {
		writer app.OperationCorrectionReportStore
		report knowl.OperationCorrectionReport
	}{{writer, report}, {peerWriter, changed}} {
		go func() {
			<-start
			results <- participant.writer.SaveOperationCorrectionReport(t.Context(), key.Scope, reserved.ID, 0, participant.report)
		}()
	}
	close(start)
	first, second := <-results, <-results
	won := (first == nil && errors.Is(second, h.Conflict)) || (second == nil && errors.Is(first, h.Conflict))
	if !won {
		t.Fatalf("concurrent immutable write: %v %v", first, second)
	}
	operation, err := h.Store.Operation(t.Context(), key.Scope, reserved.ID)
	if err != nil || operation.Correction == nil || operation.Correction.Turns != nil {
		t.Fatalf("unavailable counters fabricated: %+v %v", operation.Correction, err)
	}
	if err := h.Store.SavePlan(t.Context(), reserved.ID, knowl.PlanSummary{Digest: strings.Repeat("d", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.MarkApplying(t.Context(), reserved.ID, knowl.Lease{Token: "correction-commit", ExpiresAt: time.Now().UTC().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.CommitOutcome(t.Context(), reserved.ID, knowl.ContentCommit{Generation: "correction-committed"}); err != nil {
		t.Fatal(err)
	}
	if err := peerWriter.SaveOperationCorrectionReport(t.Context(), key.Scope, reserved.ID, 0, *operation.Correction); err != nil {
		t.Fatalf("committed identical replay: %v", err)
	}
	h.Payload(t, reserved.ID, nil)
	if err := writer.SaveOperationCorrectionReport(t.Context(), key.Scope, reserved.ID, 0, report); !errors.Is(err, h.InvalidState) {
		t.Fatalf("committed missing report: %v", err)
	}
}

func assertCorruptCorrection(t *testing.T, h CorrectionReportHarness, id knowl.OperationID, payload string, report knowl.OperationCorrectionReport) {
	t.Helper()
	h.Payload(t, id, &payload)
	if _, err := h.Store.Operation(t.Context(), h.Scope, id); !errors.Is(err, app.ErrOperationCorrectionReportInvalid) {
		t.Fatalf("corrupt read: %v", err)
	}
	if err := h.Store.(app.OperationCorrectionReportStore).SaveOperationCorrectionReport(t.Context(), h.Scope, id, report.WorkAttempt, report); !errors.Is(err, app.ErrOperationCorrectionReportInvalid) {
		t.Fatalf("corrupt rewrite: %v", err)
	}
}

func runCorrectionAllocationBounds(t *testing.T, h CorrectionReportHarness, report knowl.OperationCorrectionReport) {
	t.Helper()
	scope := childScope(h.Scope, "bounded")
	key, meta := Fixture(scope, "large-correction", time.Unix(1, 0).UTC())
	reserved, err := h.Store.Reserve(t.Context(), key, meta)
	if err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("x", 2<<20)
	h.Payload(t, reserved.ID, &large)
	writer := h.Store.(app.OperationCorrectionReportStore)
	for _, check := range []struct {
		name string
		call func() error
	}{
		{"read", func() error { _, err := h.Store.Operation(t.Context(), scope, reserved.ID); return err }},
		{"rewrite", func() error { return writer.SaveOperationCorrectionReport(t.Context(), scope, reserved.ID, 0, report) }},
		{"targeted claim", func() error {
			_, err := h.Store.ClaimOperation(t.Context(), scope, reserved.ID, futureLease("large-targeted"))
			return err
		}},
		{"ready claim", func() error { _, err := h.Store.ClaimReady(t.Context(), scope, futureLease("large-ready")); return err }},
	} {
		boundedAllocations(t, "correction "+check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, app.ErrOperationCorrectionReportInvalid) {
				t.Fatalf("corrupt payload: %v", err)
			}
		})
	}
	h.Payload(t, reserved.ID, nil)
	operation, err := h.Store.Operation(t.Context(), scope, reserved.ID)
	if err != nil || operation.WorkAttempt != 0 || operation.Correction != nil {
		t.Fatalf("failed claims changed attempt: %+v %v", operation, err)
	}
}
