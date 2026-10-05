package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	operationContextColumn    = `CASE WHEN octet_length(context_report) > 32768 THEN '{}' ELSE context_report END`
	operationPlanDigestColumn = `CASE WHEN octet_length(plan_digest) = 64 THEN plan_digest ELSE '' END`
)

var _ app.OperationContextReportStore = (*Store)(nil)

// SaveOperationContextReport persists one immutable snapshot for a work attempt.
func (store *Store) SaveOperationContextReport(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, attempt int, report knowl.OperationContextReport) error {
	encoded, err := app.EncodeOperationContextReport(report)
	if err != nil {
		return err
	}
	if report.WorkAttempt != attempt {
		return ErrLeaseConflict
	}
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		var stored sql.NullString
		var workAttempt int
		err := tx.QueryRowContext(ctx, `SELECT work_attempt, `+operationContextColumn+` FROM knowl_operations WHERE scope = $1 AND operation_id = $2`, scope, id).Scan(&workAttempt, &stored)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read operation context: %w", err)
		}
		if attempt != workAttempt {
			return ErrLeaseConflict
		}
		previous, err := app.DecodeOperationContextReport(stored.String, workAttempt)
		if err != nil {
			return err
		}
		if previous != nil && previous.WorkAttempt == attempt {
			if stored.String == encoded {
				return nil
			}
			return ErrConflict
		}
		if current.status == knowl.StatusCommitted || current.status == knowl.StatusFailed {
			return ErrInvalidState
		}
		return updateOperationTx(ctx, tx, id, `context_report = $1`, encoded)
	})
}

func decodeOperationDetails(operation *knowl.Operation, contextReport, planDigest string, planFileCount sql.NullInt64) error {
	var err error
	operation.Context, err = app.DecodeOperationContextReport(contextReport, operation.WorkAttempt)
	if err != nil {
		return err
	}
	if app.ValidOperationPlanDigest(planDigest) {
		operation.Plan = &knowl.OperationPlanSummary{Digest: planDigest}
		if planFileCount.Valid {
			if planFileCount.Int64 < 0 || planFileCount.Int64 > 2147483647 {
				return ErrConflict
			}
			count := int(planFileCount.Int64)
			operation.Plan.FileCount = &count
		}
	}
	return nil
}
