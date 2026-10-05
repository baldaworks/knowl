package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var ErrOperationCorrectionReportInvalid = errors.New("invalid operation correction report")

const MaxOperationCorrectionReportBytes = 1024

// EncodeOperationCorrectionReport validates content-free evidence before storage.
func EncodeOperationCorrectionReport(report knowl.OperationCorrectionReport) (string, error) {
	if err := validateOperationCorrection(report); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(report)
	if err != nil || len(encoded) > MaxOperationCorrectionReportBytes {
		return "", ErrOperationCorrectionReportInvalid
	}
	return string(encoded), nil
}

// DecodeOperationCorrectionReport preserves absent legacy facts and measured zeros.
func DecodeOperationCorrectionReport(payload string, workAttempt int) (*knowl.OperationCorrectionReport, error) {
	if len(payload) > MaxOperationCorrectionReportBytes || !utf8.ValidString(payload) || !validContextJSONUnicode(payload) {
		return nil, ErrOperationCorrectionReportInvalid
	}
	if strings.TrimSpace(payload) == "" || strings.TrimSpace(payload) == nullReportJSON {
		return nil, nil
	}
	var wire struct {
		knowl.OperationCorrectionReport
		WorkAttempt    *int `json:"work_attempt"`
		MaxCorrections *int `json:"max_corrections"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return nil, ErrOperationCorrectionReportInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || wire.WorkAttempt == nil || wire.MaxCorrections == nil {
		return nil, ErrOperationCorrectionReportInvalid
	}
	report := wire.OperationCorrectionReport
	report.WorkAttempt, report.MaxCorrections = *wire.WorkAttempt, *wire.MaxCorrections
	if report.WorkAttempt > workAttempt {
		return nil, ErrOperationCorrectionReportInvalid
	}
	if err := validateOperationCorrection(report); err != nil {
		return nil, err
	}
	return &report, nil
}

func ValidOutputValidationCode(code knowl.OutputValidationCode) bool {
	return code == knowl.StructuredOutputInvalid || code == knowl.SourcePlanInvalid || code == knowl.HierarchyPlanInvalid
}

func validateOperationCorrection(report knowl.OperationCorrectionReport) error {
	limits := knowl.OutputCorrectionLimits{MaxCorrections: report.MaxCorrections, MaxOutputBytes: report.MaxOutputBytes, DeadlineNanos: report.DeadlineNanos}
	if report.Version != 1 || !validDiagnosticCount(report.WorkAttempt) || ValidateOutputCorrectionLimits(limits) != nil || (report.ValidationCode != "" && !ValidOutputValidationCode(report.ValidationCode)) {
		return ErrOperationCorrectionReportInvalid
	}
	if report.Outcome == knowl.CorrectionUnavailable {
		if report.Turns != nil || report.Corrections != nil || report.OutputBytes != nil || report.ValidationCode != "" {
			return ErrOperationCorrectionReportInvalid
		}
		return nil
	}
	if report.Turns == nil || report.Corrections == nil || report.OutputBytes == nil {
		return ErrOperationCorrectionReportInvalid
	}
	turns, corrections, used := *report.Turns, *report.Corrections, *report.OutputBytes
	if turns < 0 || turns > report.MaxCorrections+1 || corrections != max(0, turns-1) || used < 0 || used > report.MaxOutputBytes || (corrections > 0 && report.ValidationCode == "") || (turns == 0 && (used != 0 || report.ValidationCode != "")) {
		return ErrOperationCorrectionReportInvalid
	}
	switch report.Outcome {
	case knowl.CorrectionAccepted:
		if turns == 0 || used == 0 || (corrections == 0 && report.ValidationCode != "") {
			return ErrOperationCorrectionReportInvalid
		}
	case knowl.CorrectionExhausted:
		if turns != report.MaxCorrections+1 || report.ValidationCode == "" {
			return ErrOperationCorrectionReportInvalid
		}
	case knowl.CorrectionOutputLimit:
		if turns == 0 {
			return ErrOperationCorrectionReportInvalid
		}
	case knowl.CorrectionDeadline, knowl.CorrectionCanceled, knowl.CorrectionProviderFailed:
	default:
		return ErrOperationCorrectionReportInvalid
	}
	return nil
}
