package postgres

import (
	"context"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const operationCorrectionColumn = `CASE WHEN octet_length(correction_report) > 1024 THEN '{}' ELSE correction_report END`

var _ app.OperationCorrectionReportStore = (*Store)(nil)

// SaveOperationCorrectionReport persists one immutable snapshot for a work attempt.
func (store *Store) SaveOperationCorrectionReport(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, attempt int, report knowl.OperationCorrectionReport) error {
	encoded, err := app.EncodeOperationCorrectionReport(report)
	if err != nil {
		return err
	}
	if report.WorkAttempt != attempt {
		return ErrLeaseConflict
	}
	return store.saveAttemptReport(ctx, scope, id, attempt, encoded, operationCorrectionColumn, "correction_report", func(payload string, currentAttempt int) (int, error) {
		previous, err := app.DecodeOperationCorrectionReport(payload, currentAttempt)
		if err != nil {
			return -1, err
		}
		if previous == nil {
			return -1, nil
		}
		return previous.WorkAttempt, nil
	})
}
