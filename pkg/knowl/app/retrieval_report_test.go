package app

import (
	"errors"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestRetrievalReportRejectsInvalidModesAndSecretReasons(t *testing.T) {
	for _, report := range []knowl.RetrievalReport{
		{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalDegraded, Reason: "https://secret@example.test"},
		{Requested: knowl.RetrievalLexical, Effective: knowl.RetrievalHybrid},
		{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalDegraded},
		{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalHybrid, Reason: knowl.RetrievalUnavailable},
		{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalHybrid, ScannedChunks: 8193},
		{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalHybrid, ModelSpace: "secret"},
		{Requested: knowl.RetrievalLexical, Effective: knowl.RetrievalLexical, VectorCandidates: 1},
		{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalHybrid, LexicalCandidates: -1},
	} {
		if _, err := EncodeRetrievalReport(report); !errors.Is(err, ErrRetrievalReportInvalid) {
			t.Fatalf("invalid report accepted: %#v %v", report, err)
		}
	}
}

func TestRetrievalReportTypedRoundTripAndLegacy(t *testing.T) {
	report := knowl.RetrievalReport{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalDegraded, Reason: knowl.RetrievalUnavailable, ModelSpace: "0123456789abcdef", LexicalCandidates: 2, FusedCandidates: 2}
	encoded, err := EncodeRetrievalReport(report)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRetrievalReport(encoded)
	if err != nil || got == nil || *got != report {
		t.Fatalf("report round trip=%#v %v", got, err)
	}
	for _, legacy := range []string{"", nullReportJSON} {
		if got, err := DecodeRetrievalReport(legacy); err != nil || got != nil {
			t.Fatalf("legacy report=%#v %v", got, err)
		}
	}
}

func TestRetrievalReportRejectsMalformedOversizedAndUnknownFields(t *testing.T) {
	for _, encoded := range []string{`{`, strings.Repeat(" ", 2049), `{"requested":"lexical","effective":"lexical","secret":"bearer"}`, `{"requested":"lexical","effective":"lexical"} {}`} {
		if _, err := DecodeRetrievalReport(encoded); !errors.Is(err, ErrRetrievalReportInvalid) {
			t.Fatalf("unsafe persisted report accepted: %v", err)
		}
	}
}

// Invalid caller input/configuration must never be published as successful fallback.
func TestRetrievalReportCannotDegradeInvalidCallerInput(t *testing.T) {
	for _, reason := range []knowl.RetrievalFailure{knowl.RetrievalInvalidInput, knowl.RetrievalInvalidConfiguration} {
		report := knowl.RetrievalReport{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalDegraded, Reason: reason}
		if _, err := EncodeRetrievalReport(report); !errors.Is(err, ErrRetrievalReportInvalid) {
			t.Fatalf("invalid caller input became degraded success: %v", err)
		}
		report.Effective = knowl.RetrievalFailed
		if _, err := EncodeRetrievalReport(report); err != nil {
			t.Fatalf("classified failure rejected: %v", err)
		}
	}
}
