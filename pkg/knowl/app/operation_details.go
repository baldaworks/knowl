package app

import (
	"encoding/hex"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// PublicOperationDetails publishes only validated, bounded application facts.
func PublicOperationDetails(operation knowl.Operation) *knowl.OperationDetails {
	details := &knowl.OperationDetails{}
	if operation.Context != nil {
		if encoded, err := EncodeOperationContextReport(*operation.Context); err == nil {
			details.Context, _ = DecodeOperationContextReport(encoded, operation.WorkAttempt)
		}
	}
	if operation.Retrieval != nil && validDiagnosticCount(operation.RetrievalAttempt) && operation.RetrievalAttempt <= operation.WorkAttempt {
		if encoded, err := EncodeRetrievalReport(*operation.Retrieval); err == nil {
			details.Retrieval, _ = DecodeRetrievalReport(encoded)
			attempt := operation.RetrievalAttempt
			details.RetrievalAttempt = &attempt
		}
	}
	if plan := operation.Plan; plan != nil && ValidOperationPlanDigest(plan.Digest) && (plan.FileCount == nil || validDiagnosticCount(*plan.FileCount)) {
		details.Plan = &knowl.OperationPlanSummary{Digest: plan.Digest}
		if plan.FileCount != nil {
			count := *plan.FileCount
			details.Plan.FileCount = &count
		}
	}
	for _, warning := range operation.Diagnostics {
		if len(details.Warnings) >= maxMaintenanceDiagnostics || !publicWarningCode(warning.Code) {
			details.WarningsOmitted++
			continue
		}
		if _, err := NormalizeMaintenanceDiagnostics([]knowl.MaintenanceDiagnostic{warning}); err != nil {
			details.WarningsOmitted++
			continue
		}
		details.Warnings = append(details.Warnings, warning)
	}
	if normalized, err := NormalizeMaintenanceDiagnostics(details.Warnings); err == nil {
		details.Warnings = normalized
	} else {
		details.WarningsOmitted += len(details.Warnings)
		details.Warnings = nil
	}
	_, timeErr := operation.ReadyAt.MarshalJSON()
	if validDiagnosticCount(operation.WorkAttempt) && validDiagnosticCount(operation.RetryAttempt) && validDiagnosticCount(operation.ManualRetryCount) && validDiagnosticCount(operation.Attempt) && timeErr == nil {
		details.Execution = &knowl.OperationExecutionDetails{WorkAttempt: operation.WorkAttempt, RetryAttempt: operation.RetryAttempt, ManualRetryCount: operation.ManualRetryCount, ApplyAttempt: operation.Attempt, ReadyAt: operation.ReadyAt}
	}
	return details
}

// ValidOperationPlanDigest keeps opaque legacy/custom digests out of public summaries.
func ValidOperationPlanDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func publicWarningCode(code string) bool {
	switch code {
	case knowl.DiagnosticCitationUnknownSource, knowl.DiagnosticOriginalLinkUnresolved, knowl.DiagnosticCatalogDependencyReject:
		return true
	default:
		return false
	}
}
