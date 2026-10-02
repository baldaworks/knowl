package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const maxRetrievalReportBytes = 2048

// PublicRetrievalStatus excludes internal selection counts and model identity.
// Missing legacy evidence remains missing; invalid evidence is never published.
func PublicRetrievalStatus(report *knowl.RetrievalReport) *knowl.RetrievalStatus {
	if report == nil || ValidateRetrievalReport(*report) != nil {
		return nil
	}
	return &knowl.RetrievalStatus{Effective: report.Effective, Reason: report.Reason}
}

var ErrRetrievalReportInvalid = errors.New("invalid retrieval report")

// ValidateRetrievalReport enforces the allowlisted modes/reasons and finite
// producer counters before durable or transport publication.
func ValidateRetrievalReport(report knowl.RetrievalReport) error {
	if report.Requested != knowl.RetrievalLexical && report.Requested != knowl.RetrievalHybrid {
		return ErrRetrievalReportInvalid
	}
	switch report.Effective {
	case knowl.RetrievalLexical:
		if report.Requested != knowl.RetrievalLexical || report.Reason != "" || report.ModelSpace != "" || report.VectorCandidates != 0 || report.ScannedChunks != 0 {
			return ErrRetrievalReportInvalid
		}
	case knowl.RetrievalHybrid:
		if report.Requested != knowl.RetrievalHybrid || report.Reason != "" {
			return ErrRetrievalReportInvalid
		}
	case knowl.RetrievalDegraded, knowl.RetrievalFailed:
		if report.Effective == knowl.RetrievalDegraded && (report.Reason == knowl.RetrievalInvalidInput || report.Reason == knowl.RetrievalInvalidConfiguration) {
			return ErrRetrievalReportInvalid
		}
		if report.Requested != knowl.RetrievalHybrid || !ValidRetrievalFailure(report.Reason) {
			return ErrRetrievalReportInvalid
		}
	default:
		return ErrRetrievalReportInvalid
	}
	if report.ModelSpace != "" {
		if len(report.ModelSpace) != 16 || report.ModelSpace != strings.ToLower(report.ModelSpace) {
			return ErrRetrievalReportInvalid
		}
		if _, err := hex.DecodeString(report.ModelSpace); err != nil {
			return ErrRetrievalReportInvalid
		}
	}
	for _, value := range []int{report.LexicalCandidates, report.VectorCandidates, report.FusedCandidates} {
		if value < 0 || value > 200 {
			return ErrRetrievalReportInvalid
		}
	}
	if report.LexicalCandidates > 100 || report.VectorCandidates > 100 || report.ScannedChunks < 0 || report.ScannedChunks > 8192 {
		return ErrRetrievalReportInvalid
	}
	for _, value := range []int{report.QueryOmittedRunes, report.IndexOmittedChunks, report.IndexOmittedRunes} {
		if value < 0 || int64(value) > 2147483647 {
			return ErrRetrievalReportInvalid
		}
	}
	return nil
}

// ValidRetrievalFailure rejects arbitrary upstream strings from safe reports.
func ValidRetrievalFailure(code knowl.RetrievalFailure) bool {
	switch code {
	case knowl.RetrievalInvalidConfiguration, knowl.RetrievalInvalidInput, knowl.RetrievalUnavailable, knowl.RetrievalDeadline,
		knowl.RetrievalInputLimit, knowl.RetrievalResponseLimit, knowl.RetrievalInvalidResponse, knowl.RetrievalModelMismatch,
		knowl.RetrievalDimensionMismatch, knowl.RetrievalProjectionNotReady, knowl.RetrievalProjectionDrift, knowl.RetrievalProjectionCapacity:
		return true
	default:
		return false
	}
}

// EncodeRetrievalReport produces bounded redacted JSON for an actual attempt.
func EncodeRetrievalReport(report knowl.RetrievalReport) (string, error) {
	if err := ValidateRetrievalReport(report); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(report)
	if err != nil || len(encoded) > maxRetrievalReportBytes {
		return "", ErrRetrievalReportInvalid
	}
	return string(encoded), nil
}

// DecodeRetrievalReport accepts legacy null/empty fields and otherwise fails
// closed on malformed, oversized, unknown-field or invalid report data.
func DecodeRetrievalReport(encoded string) (*knowl.RetrievalReport, error) {
	if len(encoded) > maxRetrievalReportBytes {
		return nil, ErrRetrievalReportInvalid
	}
	if strings.TrimSpace(encoded) == "" || strings.TrimSpace(encoded) == "null" {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(encoded))
	decoder.DisallowUnknownFields()
	var report knowl.RetrievalReport
	if err := decoder.Decode(&report); err != nil {
		return nil, ErrRetrievalReportInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, ErrRetrievalReportInvalid
	}
	if err := ValidateRetrievalReport(report); err != nil {
		return nil, err
	}
	return &report, nil
}

// DecodeOperationRetrieval preserves historical attempt evidence for recovery
// while identifying which attempt produced it. Corrupt/future attempts fail closed.
func DecodeOperationRetrieval(encoded string, attempt, workAttempt int) (*knowl.RetrievalReport, int, error) {
	if attempt < 0 || attempt > workAttempt {
		return nil, 0, ErrRetrievalReportInvalid
	}
	report, err := DecodeRetrievalReport(encoded)
	if err != nil {
		return nil, 0, err
	}
	if report == nil {
		return nil, 0, nil
	}
	return report, attempt, nil
}

// FailedRetrievalReport marks a failed hybrid read without publishing unsafe causes.
func FailedRetrievalReport(ctx context.Context, report knowl.RetrievalReport, cause error) knowl.RetrievalReport {
	if report.Requested != knowl.RetrievalHybrid {
		return report
	}
	report.Effective = knowl.RetrievalFailed
	if !ValidRetrievalFailure(report.Reason) {
		report.Reason = knowl.RetrievalUnavailable
	}
	var classified *EmbeddingError
	if errors.As(cause, &classified) && ValidRetrievalFailure(classified.Code) {
		report.Reason = classified.Code
	}
	if ctx.Err() != nil {
		report.Reason = knowl.RetrievalDeadline
	}
	return report
}
