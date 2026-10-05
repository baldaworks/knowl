package app_test

import (
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type reservedBudgetMaintainer struct{ *wrappedBudgetMaintainer }

func (m reservedBudgetMaintainer) RequestBudget() knowl.MaintenanceRequestBudget {
	budget := m.wrappedBudgetMaintainer.RequestBudget()
	budget.ReservedBytes = app.MaxCorrectionFeedbackBytes
	return budget
}

func TestIngestReservesFeedbackWithoutInventingUsedBytes(t *testing.T) {
	for _, extra := range []int{-1, 0} {
		workspace, store, _, _ := newBaselineIngest(t)
		seedBaselineContext(t, workspace, store)
		index := orderedBudgetIndex{store, []knowl.PageID{budgetStorageID}}
		probe := &wrappedBudgetMaintainer{cap: 12000}
		service, err := app.NewIngestService(workspace, store, index, probe, app.IngestOptions{})
		if err != nil {
			t.Fatal(err)
		}
		envelope := sourceEnvelope([]byte("input evidence"))
		if _, err := service.Ingest(t.Context(), envelope); err != nil {
			t.Fatal(err)
		}
		input := probe.input
		capBytes := 12000
		for range 3 {
			input.InputLimits.MaxRequestBytes = capBytes
			used, err := probe.RequestBytes(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			capBytes = used + app.MaxCorrectionFeedbackBytes + extra
		}
		m := reservedBudgetMaintainer{&wrappedBudgetMaintainer{cap: capBytes}}
		service, err = app.NewIngestService(workspace, store, index, m, app.IngestOptions{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Ingest(t.Context(), envelope)
		if err != nil {
			t.Fatal(err)
		}
		used, err := m.RequestBytes(t.Context(), m.input)
		if err != nil || result.Budget == nil || result.Budget.UsedBytes != used || result.Budget.MaxBytes != capBytes || used+app.MaxCorrectionFeedbackBytes > capBytes {
			t.Fatalf("reserved budget/actual telemetry: %+v measured=%d cap=%d err=%v", result.Budget, used, capBytes, err)
		}
		wantIncluded := 1
		if extra < 0 {
			wantIncluded = 0
		}
		if len(m.input.Pages) != wantIncluded || result.Budget.IncludedCount != wantIncluded || result.Budget.OmittedCount != 1-wantIncluded || m.input.SourceText != input.SourceText {
			t.Fatalf("exact reserve fitting: %+v pages=%d", result.Budget, len(m.input.Pages))
		}
		if wantIncluded == 1 && m.input.Pages[0].Content != input.Pages[0].Content {
			t.Fatal("reserve fitting truncated the page")
		}
	}
}
