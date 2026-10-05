package app_test

import (
	"encoding/json"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestPublicOperationCorrectionPreservesHistoricalAndUnknownFacts(t *testing.T) {
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	for _, outcome := range []knowl.CorrectionOutcome{knowl.CorrectionAccepted, knowl.CorrectionUnavailable} {
		turns, corrections, used := 2, 1, 512
		report := knowl.OperationCorrectionReport{Version: 1, WorkAttempt: 1, MaxCorrections: limits.MaxCorrections, MaxOutputBytes: limits.MaxOutputBytes, DeadlineNanos: limits.DeadlineNanos, Outcome: outcome}
		if outcome == knowl.CorrectionAccepted {
			report.Turns, report.Corrections, report.OutputBytes, report.ValidationCode = &turns, &corrections, &used, knowl.SourcePlanInvalid
		}
		projected := app.PublicOperationDetails(knowl.Operation{WorkAttempt: 2, Correction: &report})
		turns = 0 // The response must be detached from mutable adapter-owned pointers.
		payload, err := json.Marshal(projected)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Correction *knowl.OperationCorrectionReport `json:"correction"`
		}
		if err := json.Unmarshal(payload, &wire); err != nil || wire.Correction == nil || wire.Correction.WorkAttempt != 1 || wire.Correction.Outcome != outcome {
			t.Fatalf("historical evidence unavailable: %+v %v", wire.Correction, err)
		}
		if outcome == knowl.CorrectionAccepted && (wire.Correction.Turns == nil || *wire.Correction.Turns != 2 || *wire.Correction.Corrections != 1 || *wire.Correction.OutputBytes != used) {
			t.Fatalf("physical evidence altered: %+v", wire.Correction)
		}
		if outcome == knowl.CorrectionUnavailable && (wire.Correction.Turns != nil || wire.Correction.Corrections != nil || wire.Correction.OutputBytes != nil) {
			t.Fatal("unavailable evidence became measured zero")
		}
	}
}

func TestPublicOperationCorrectionWithholdsInvalidAndFutureFacts(t *testing.T) {
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	for _, report := range []*knowl.OperationCorrectionReport{nil, {Version: 1, WorkAttempt: 2, MaxOutputBytes: limits.MaxOutputBytes, DeadlineNanos: limits.DeadlineNanos, Outcome: knowl.CorrectionUnavailable}, {Version: 1, WorkAttempt: 1, MaxOutputBytes: limits.MaxOutputBytes, DeadlineNanos: limits.DeadlineNanos, Outcome: knowl.CorrectionOutcome("private-provider-error")}} {
		payload, err := json.Marshal(app.PublicOperationDetails(knowl.Operation{WorkAttempt: 1, Correction: report}))
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(payload, &wire); err != nil {
			t.Fatal(err)
		}
		if _, ok := wire["correction"]; ok {
			t.Fatal("unsafe or future correction report was published")
		}
	}
}
