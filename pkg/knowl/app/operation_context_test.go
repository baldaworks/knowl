package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func contextCount(value int) *int { return &value }

func assembledContextFixture() knowl.OperationContextReport {
	return knowl.OperationContextReport{
		Version: 1, WorkAttempt: 2, Outcome: knowl.ContextAssembled,
		CandidateCount: contextCount(2), CatalogCount: contextCount(3),
		Pages: []knowl.ContextPage{
			{PageID: "decisions/cache", SelectionReason: knowl.ContextLexical, Disposition: knowl.ContextIncluded},
			{PageID: "runbooks/rollback", SelectionReason: knowl.ContextNeighbor, Disposition: knowl.ContextBudgetOmitted},
		},
		Budget:           &knowl.ContextBudget{MaxBytes: 4096, UsedBytes: contextCount(3056), IncludedCount: 1, OmittedCount: 1},
		VectorProjection: &knowl.VectorProjectionStatus{State: knowl.VectorNotChecked},
	}
}

// Catches lost measured evidence, invented legacy/overflow sizes and attempt reassignment.
func TestOperationContextRoundTripAndUnknownEvidence(t *testing.T) {
	report := assembledContextFixture()
	encoded, err := EncodeOperationContextReport(report)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOperationContextReport(encoded, 3)
	if err != nil || !reflect.DeepEqual(got, &report) {
		t.Fatalf("decoded=%#v err=%v", got, err)
	}
	for _, legacy := range []string{"", nullReportJSON} {
		got, err := DecodeOperationContextReport(legacy, 0)
		if err != nil || got != nil {
			t.Fatalf("legacy=%#v %v", got, err)
		}
	}
	partial := knowl.OperationContextReport{Version: 1, WorkAttempt: 1, Outcome: knowl.ContextAssemblyFailed, CandidateCount: contextCount(0), Budget: &knowl.ContextBudget{MaxBytes: 4096}}
	encoded, err = EncodeOperationContextReport(partial)
	if err != nil {
		t.Fatal(err)
	}
	got, err = DecodeOperationContextReport(encoded, 1)
	if err != nil || got.Budget.UsedBytes != nil {
		t.Fatalf("invented usage: %#v %v", got, err)
	}
	if _, err := DecodeOperationContextReport(encoded, 0); !errors.Is(err, ErrOperationContextReportInvalid) {
		t.Fatalf("future attempt accepted: %v", err)
	}
}

func TestOperationContextRequiresMeasuredZeroFields(t *testing.T) {
	report := knowl.OperationContextReport{Version: 1, Outcome: knowl.ContextAssemblyFailed, CandidateCount: contextCount(0), Budget: &knowl.ContextBudget{MaxBytes: 4096}}
	encoded, err := EncodeOperationContextReport(report)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOperationContextReport(encoded, 0)
	if err != nil || !reflect.DeepEqual(got, &report) {
		t.Fatalf("explicit zero evidence lost: %+v %v", got, err)
	}
	for _, field := range []string{"work_attempt", "entries_omitted", "budget.included_count", "budget.omitted_count"} {
		for _, missing := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/missing=%t", field, missing), func(t *testing.T) {
				var root map[string]any
				if err := json.Unmarshal([]byte(encoded), &root); err != nil {
					t.Fatal(err)
				}
				object := root
				parent, name, nested := strings.Cut(field, ".")
				if nested {
					object = object[parent].(map[string]any)
				} else {
					name = parent
				}
				if missing {
					delete(object, name)
				} else {
					object[name] = nil
				}
				payload, err := json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeOperationContextReport(string(payload), 0); !errors.Is(err, ErrOperationContextReportInvalid) {
					t.Fatalf("unmeasured required field accepted: %v", err)
				}
			})
		}
	}
}

// A hidden diagnostic entry cannot account for two completed candidates beside a pending page.
func TestOperationContextRejectsContradictoryPartialCounts(t *testing.T) {
	report := assembledContextFixture()
	report.Outcome = knowl.ContextAssemblyFailed
	report.Pages = report.Pages[:1]
	report.Pages[0].Disposition = knowl.ContextPending
	report.EntriesOmitted = 1
	report.Budget.IncludedCount, report.Budget.OmittedCount = 2, 0
	if _, err := EncodeOperationContextReport(report); !errors.Is(err, ErrOperationContextReportInvalid) {
		t.Fatal("pending candidate counted as completed")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeOperationContextReport(string(raw), 2); !errors.Is(err, ErrOperationContextReportInvalid) {
		t.Fatal("corrupt partial counts decoded")
	}
	report.Budget.IncludedCount = 1
	if _, err := EncodeOperationContextReport(report); err != nil {
		t.Fatalf("valid hidden included candidate rejected: %v", err)
	}
}

// Corrupt encoded bytes must not silently rename an identifier during JSON decoding.
func TestOperationContextRejectsInvalidUTF8(t *testing.T) {
	report := assembledContextFixture()
	report.Pages[0].PageID = "pages/хранилище"
	encoded, err := EncodeOperationContextReport(report)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOperationContextReport(encoded, 2)
	if err != nil || !reflect.DeepEqual(got, &report) {
		t.Fatalf("Unicode round trip: %v", err)
	}
	corrupt := strings.Replace(encoded, "хранилище", string([]byte{0xff}), 1)
	if _, err := DecodeOperationContextReport(corrupt, 2); !errors.Is(err, ErrOperationContextReportInvalid) {
		t.Fatal("corrupt UTF-8 decoded with changed ID")
	}
	for _, escape := range []string{`\ud800`, `\udfff`, `\ud800\u0041`} {
		corrupt = strings.Replace(encoded, "хранилище", escape, 1)
		if _, err := DecodeOperationContextReport(corrupt, 2); !errors.Is(err, ErrOperationContextReportInvalid) {
			t.Fatal("unpaired surrogate decoded with changed ID")
		}
	}
	paired := strings.Replace(encoded, "хранилище", `\ud83d\ude00`, 1)
	got, err = DecodeOperationContextReport(paired, 2)
	if err != nil || got.Pages[0].PageID != "pages/😀" {
		t.Fatalf("valid surrogate pair rejected: %v", err)
	}
}

// Catches unsafe identifiers/categories, inconsistent counts and corrupt durable payloads.
func TestOperationContextRejectsUnsafeAndInconsistentEvidence(t *testing.T) {
	for name, change := range map[string]func(*knowl.OperationContextReport){
		"future-version":      func(r *knowl.OperationContextReport) { r.Version = 2 },
		"negative-attempt":    func(r *knowl.OperationContextReport) { r.WorkAttempt = -1 },
		"unsafe-page":         func(r *knowl.OperationContextReport) { r.Pages[0].PageID = "../private" },
		"url":                 func(r *knowl.OperationContextReport) { r.Pages[0].PageID = "https://secret.example" },
		"control":             func(r *knowl.OperationContextReport) { r.Pages[0].PageID = "index" },
		"unknown-reason":      func(r *knowl.OperationContextReport) { r.Pages[0].SelectionReason = "provider-secret" },
		"unknown-disposition": func(r *knowl.OperationContextReport) { r.Pages[0].Disposition = "provider-secret" },
		"wrong-total":         func(r *knowl.OperationContextReport) { r.CandidateCount = contextCount(3) },
		"wrong-fitting-count": func(r *knowl.OperationContextReport) { r.Budget.IncludedCount = 2 },
		"duplicate":           func(r *knowl.OperationContextReport) { r.Pages[1].PageID = r.Pages[0].PageID },
		"pending-assembled":   func(r *knowl.OperationContextReport) { r.Pages[0].Disposition = knowl.ContextPending },
		"oversized-request":   func(r *knowl.OperationContextReport) { r.Budget.MaxBytes = (4 << 20) + 1 },
		"false-projection":    func(r *knowl.OperationContextReport) { r.VectorProjection.Reason = knowl.RetrievalUnavailable },
		"unsafe-projection": func(r *knowl.OperationContextReport) {
			r.VectorProjection = &knowl.VectorProjectionStatus{State: knowl.VectorInvalid, Reason: "bearer-secret"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := assembledContextFixture()
			change(&r)
			if _, err := EncodeOperationContextReport(r); !errors.Is(err, ErrOperationContextReportInvalid) {
				t.Fatalf("accepted invalid report: %v", err)
			}
		})
	}
	for _, raw := range []string{`{`, `{} {}`, `{"version":1,"work_attempt":1,"outcome":"selection_failed","secret":"bearer"}`, strings.Repeat(" ", 32769)} {
		if _, err := DecodeOperationContextReport(raw, 1); !errors.Is(err, ErrOperationContextReportInvalid) {
			t.Fatalf("unsafe payload accepted: %v", err)
		}
	}
}

// Catches silent report truncation, wrong omission counts and modified identifiers.
func TestOperationContextBoundsOnlyDiagnosticEntries(t *testing.T) {
	for _, size := range []int{30, 101} {
		r := assembledContextFixture()
		r.Pages = nil
		r.CandidateCount = contextCount(size)
		r.Budget.IncludedCount, r.Budget.OmittedCount = size, 0
		for i := range size {
			id := knowl.PageID(fmt.Sprintf("pages/%03d", i))
			if size == 30 {
				id += knowl.PageID(strings.Repeat("x", 1800))
			}
			r.Pages = append(r.Pages, knowl.ContextPage{PageID: id, SelectionReason: knowl.ContextRecent, Disposition: knowl.ContextIncluded})
		}
		original := append([]knowl.ContextPage(nil), r.Pages...)
		bounded, err := BoundOperationContextReport(r)
		if err != nil {
			t.Fatal(err)
		}
		if bounded.EntriesOmitted == 0 || len(bounded.Pages)+bounded.EntriesOmitted != size || *bounded.CandidateCount != size || bounded.Budget.IncludedCount != size {
			t.Fatalf("lost actual counts: %#v", bounded)
		}
		for i, page := range bounded.Pages {
			if page != original[i] {
				t.Fatal("changed ID/order")
			}
		}
		encoded, err := EncodeOperationContextReport(bounded)
		if err != nil || len(encoded) > 32768 || len(bounded.Pages) > 100 {
			t.Fatalf("report not bounded: bytes=%d err=%v", len(encoded), err)
		}
	}
}

// Catches publication of opaque legacy digests, unsupported warnings or raw operation metadata.
func TestPublicOperationDetailsPublishesOnlyValidatedFacts(t *testing.T) {
	r := assembledContextFixture()
	operation := knowl.Operation{WorkAttempt: 3, Attempt: 2, RetryAttempt: 1, Context: &r,
		Plan:      &knowl.OperationPlanSummary{Digest: strings.Repeat("d", 64)},
		Retrieval: &knowl.RetrievalReport{Requested: knowl.RetrievalLexical, Effective: knowl.RetrievalLexical}, RetrievalAttempt: 2,
		Key:         knowl.OperationKey{Source: knowl.SourceRef{ID: "private-source-text"}},
		Diagnostics: []knowl.MaintenanceDiagnostic{{Code: knowl.DiagnosticOriginalLinkUnresolved, Path: "wiki/pages/a.md", Target: "pages/b"}, {Code: "private-provider-message", Path: "wiki/pages/b.md"}},
	}
	details := PublicOperationDetails(operation)
	if details == nil || details.Context == nil || details.Plan == nil || details.Plan.FileCount != nil || details.Execution.ApplyAttempt != 2 || *details.RetrievalAttempt != 2 || len(details.Warnings) != 1 || details.WarningsOmitted != 1 {
		t.Fatalf("details=%#v", details)
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		t.Fatal(err)
	}
	for key := range parsed {
		if key != "context" && key != "retrieval" && key != "retrieval_attempt" && key != "plan" && key != "warnings" && key != "warnings_omitted" && key != "execution" {
			t.Fatalf("unexpected public field %q", key)
		}
	}
	operation.Plan.Digest = "private-secret"
	operation.Context.WorkAttempt = 4
	if got := PublicOperationDetails(operation); got.Plan != nil || got.Context != nil {
		t.Fatal("unsafe optional facts published")
	}
}

// Exercises actual maximum component serialization, including JSON-escaped identifiers.
func TestPublicOperationDetailsSerializedBound(t *testing.T) {
	report := assembledContextFixture()
	report.Pages = nil
	report.CandidateCount = contextCount(100)
	report.Budget.IncludedCount, report.Budget.OmittedCount = 100, 0
	for i := range 100 {
		report.Pages = append(report.Pages, knowl.ContextPage{PageID: knowl.PageID(fmt.Sprintf("pages/%03d", i) + strings.Repeat("<", 2000)), SelectionReason: knowl.ContextLexical, Disposition: knowl.ContextIncluded})
	}
	report, err := BoundOperationContextReport(report)
	if err != nil {
		t.Fatal(err)
	}
	operation := knowl.Operation{WorkAttempt: 2, Context: &report, Plan: &knowl.OperationPlanSummary{Digest: strings.Repeat("f", 64), FileCount: contextCount(2147483647)}, Retrieval: &knowl.RetrievalReport{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalHybrid, ModelSpace: strings.Repeat("f", 16), LexicalCandidates: 100, VectorCandidates: 100, FusedCandidates: 200, ScannedChunks: 8192, QueryOmittedRunes: 2147483647, IndexOmittedRunes: 2147483647, IndexOmittedChunks: 2147483647}}
	for i := range 7 {
		operation.Diagnostics = append(operation.Diagnostics, knowl.MaintenanceDiagnostic{Code: knowl.DiagnosticOriginalLinkUnresolved, Path: fmt.Sprintf("wiki/%03d", i) + strings.Repeat("x", 2000), Target: "pages/" + strings.Repeat("x", 2000)})
	}
	details := PublicOperationDetails(operation)
	encoded, err := json.Marshal(details)
	if err != nil || len(encoded) >= 96<<10 || len(encoded) < 48<<10 || len(details.Warnings) != 7 || details.Context.EntriesOmitted == 0 {
		t.Fatalf("bytes=%d warnings=%d err=%v", len(encoded), len(details.Warnings), err)
	}
}

// Parsed/custom timestamps can have offsets that cannot be represented by the API's JSON codec.
func TestPublicOperationDetailsRejectsUnrepresentableTime(t *testing.T) {
	ready, err := time.Parse(time.RFC3339, "2026-10-05T00:00:00+24:00")
	if err != nil {
		t.Fatal(err)
	}
	details := PublicOperationDetails(knowl.Operation{ReadyAt: ready})
	if _, err := json.Marshal(details); err != nil {
		t.Fatalf("unsafe optional time broke response: %v", err)
	}
	if details.Execution != nil {
		t.Fatal("invalid execution timestamp published")
	}
}
