package app_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type declaredBudgetMaintainer struct {
	wrappedBudgetMaintainer
	declaration knowl.MaintenanceRequestBudget
	getters     int
}

func (m *declaredBudgetMaintainer) RequestBudget() knowl.MaintenanceRequestBudget {
	m.getters++
	return m.declaration
}

func TestInputPolicyCapturesBudgetAndFormatBeforeReservation(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	makeService := func(maxBytes int, format string) (*app.IngestService, *declaredBudgetMaintainer) {
		t.Helper()
		m := &declaredBudgetMaintainer{declaration: knowl.MaintenanceRequestBudget{MaxBytes: maxBytes, FormatVersion: format}}
		service, err := app.NewIngestService(workspace, store, store, m, app.IngestOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return service, m
	}
	original, m := makeService(12000, "fixture-v1")
	changedCap, _ := makeService(11999, "fixture-v1")
	changedFormat, _ := makeService(12000, "fixture-v2")
	envelope := sourceEnvelope([]byte("stable policy evidence"))
	first, err := original.Submit(t.Context(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, workflow := range []*app.IngestService{changedCap, changedFormat} {
		submission, err := workflow.Submit(t.Context(), envelope)
		if err != nil {
			t.Fatal(err)
		}
		if submission.Operation.Key.MaintenanceGeneration == first.Operation.Key.MaintenanceGeneration || submission.Operation.ID == first.Operation.ID {
			t.Fatal("changed cap/format reused old generation")
		}
	}
	m.declaration = knowl.MaintenanceRequestBudget{MaxBytes: 1, FormatVersion: "changed-after-construction"}
	if _, err := original.Execute(t.Context(), first); err != nil || m.getters != 1 || m.input.InputLimits.MaxRequestBytes != 12000 {
		t.Fatalf("captured getters=%d input cap=%d err=%v", m.getters, m.input.InputLimits.MaxRequestBytes, err)
	}
}

func TestIngestRejectsInvalidInputPolicyBeforeAcceptance(t *testing.T) {
	for _, budget := range []knowl.MaintenanceRequestBudget{
		{MaxBytes: 0, FormatVersion: "fixture"}, {MaxBytes: -1, FormatVersion: "fixture"},
		{MaxBytes: 100, FormatVersion: ""}, {MaxBytes: 100, FormatVersion: "hidden\x01format"},
		{MaxBytes: 100, FormatVersion: strings.Repeat("x", 257)},
	} {
		workspace, store, _, _ := newBaselineIngest(t)
		m := &declaredBudgetMaintainer{declaration: budget}
		if _, err := app.NewIngestService(workspace, store, store, m, app.IngestOptions{}); !errors.Is(err, app.ErrMaintenanceInputInvalid) {
			t.Fatalf("declaration=%+v err=%v", budget, err)
		}
		inspection, err := workspace.Inspect(t.Context(), "local")
		if err != nil || len(inspection.RawSources) != 0 {
			t.Fatalf("constructor accepted raw: %v", err)
		}
	}
}
