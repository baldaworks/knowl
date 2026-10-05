package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func correctionFixture() knowl.OperationCorrectionReport {
	return knowl.OperationCorrectionReport{Version: 1, MaxOutputBytes: 1048576, DeadlineNanos: 300000000000, Outcome: knowl.CorrectionProviderFailed, Turns: contextCount(0), Corrections: contextCount(0), OutputBytes: contextCount(0)}
}

func TestCorrectionReportPreservesZeroUnknownAndHistory(t *testing.T) {
	for _, report := range []knowl.OperationCorrectionReport{
		correctionFixture(),
		{Version: 1, WorkAttempt: 2, MaxCorrections: 1, MaxOutputBytes: 1048576, DeadlineNanos: 300000000000, Outcome: knowl.CorrectionUnavailable},
		{Version: 1, WorkAttempt: 2, MaxCorrections: 1, MaxOutputBytes: 1048576, DeadlineNanos: 300000000000, Outcome: knowl.CorrectionAccepted, Turns: contextCount(2), Corrections: contextCount(1), OutputBytes: contextCount(450), ValidationCode: knowl.SourcePlanInvalid},
		{Version: 1, WorkAttempt: 2, MaxCorrections: 1, MaxOutputBytes: 1048576, DeadlineNanos: 300000000000, Outcome: knowl.CorrectionExhausted, Turns: contextCount(2), Corrections: contextCount(1), OutputBytes: contextCount(10), ValidationCode: knowl.StructuredOutputInvalid},
		{Version: 1, MaxOutputBytes: 1048576, DeadlineNanos: 300000000000, Outcome: knowl.CorrectionOutputLimit, Turns: contextCount(1), Corrections: contextCount(0), OutputBytes: contextCount(0)},
	} {
		encoded, err := EncodeOperationCorrectionReport(report)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeOperationCorrectionReport(encoded, 3)
		if err != nil || !reflect.DeepEqual(got, &report) {
			t.Fatalf("evidence lost: %#v %v", got, err)
		}
	}
	for _, empty := range []string{"", " ", "null"} {
		if got, err := DecodeOperationCorrectionReport(empty, 0); err != nil || got != nil {
			t.Fatalf("legacy evidence invented: %+v %v", got, err)
		}
	}
}

func TestCorrectionReportRejectsMissingOrNullMeasuredFields(t *testing.T) {
	valid, err := json.Marshal(correctionFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"work_attempt", "max_corrections", "turns", "corrections", "output_bytes"} {
		for _, missing := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/missing=%t", field, missing), func(t *testing.T) {
				var fields map[string]any
				if err := json.Unmarshal(valid, &fields); err != nil {
					t.Fatal(err)
				}
				if missing {
					delete(fields, field)
				} else {
					fields[field] = nil
				}
				payload, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeOperationCorrectionReport(string(payload), 0); !errors.Is(err, ErrOperationCorrectionReportInvalid) {
					t.Fatalf("missing evidence accepted: %v", err)
				}
			})
		}
	}
}

func TestCorrectionReportRejectsCorruptOrFabricatedFacts(t *testing.T) {
	for _, edit := range []struct {
		name   string
		change func(*knowl.OperationCorrectionReport)
	}{
		{"version", func(r *knowl.OperationCorrectionReport) { r.Version = 2 }},
		{"negative attempt", func(r *knowl.OperationCorrectionReport) { r.WorkAttempt = -1 }},
		{"allowance", func(r *knowl.OperationCorrectionReport) { r.MaxCorrections = 2 }},
		{"byte cap", func(r *knowl.OperationCorrectionReport) { r.MaxOutputBytes = 1048577 }},
		{"deadline", func(r *knowl.OperationCorrectionReport) { r.DeadlineNanos = 300000000001 }},
		{"turn count", func(r *knowl.OperationCorrectionReport) { r.Turns = contextCount(2) }},
		{"correction count", func(r *knowl.OperationCorrectionReport) { r.Corrections = contextCount(1) }},
		{"usage", func(r *knowl.OperationCorrectionReport) { r.OutputBytes = contextCount(1048577) }},
		{"negative usage", func(r *knowl.OperationCorrectionReport) { r.OutputBytes = contextCount(-1) }},
		{"unmeasured accepted", func(r *knowl.OperationCorrectionReport) { r.Outcome = knowl.CorrectionAccepted }},
		{"exhaustion without rejection", func(r *knowl.OperationCorrectionReport) { r.Outcome = knowl.CorrectionExhausted }},
		{"unavailable with zero", func(r *knowl.OperationCorrectionReport) { r.Outcome = knowl.CorrectionUnavailable }},
		{"partial counters", func(r *knowl.OperationCorrectionReport) { r.Turns = nil }},
		{"private code", func(r *knowl.OperationCorrectionReport) { r.ValidationCode = "https://secret.example/key" }},
	} {
		t.Run(edit.name, func(t *testing.T) {
			r := correctionFixture()
			edit.change(&r)
			if _, err := EncodeOperationCorrectionReport(r); !errors.Is(err, ErrOperationCorrectionReportInvalid) {
				t.Fatalf("fabricated evidence accepted: %v", err)
			}
		})
	}
	for _, payload := range []string{`{}`, `{"version":1,"work_attempt":0,"max_corrections":0,"extra":1}`, `{"outcome":"\ud800"}`, strings.Repeat(" ", 1025)} {
		if _, err := DecodeOperationCorrectionReport(payload, 0); !errors.Is(err, ErrOperationCorrectionReportInvalid) {
			t.Fatalf("corrupt report accepted: %v", err)
		}
	}
	r := correctionFixture()
	valid, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeOperationCorrectionReport(string(valid)+` {}`, 0); !errors.Is(err, ErrOperationCorrectionReportInvalid) {
		t.Fatalf("trailing object accepted: %v", err)
	}
	for _, field := range []string{"version", "extra"} {
		var fields map[string]any
		if err := json.Unmarshal(valid, &fields); err != nil {
			t.Fatal(err)
		}
		fields[field] = nil
		payload, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeOperationCorrectionReport(string(payload), 0); !errors.Is(err, ErrOperationCorrectionReportInvalid) {
			t.Fatalf("unsafe wire accepted: %v", err)
		}
	}
	r.WorkAttempt = 1
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeOperationCorrectionReport(string(encoded), 0); !errors.Is(err, ErrOperationCorrectionReportInvalid) {
		t.Fatalf("future evidence accepted: %v", err)
	}
}
