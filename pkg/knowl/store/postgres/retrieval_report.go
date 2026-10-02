package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var _ app.RetrievalReportStore = (*Store)(nil)

// SaveRetrievalReport records a bounded report for the current worker attempt.
// Replays are idempotent; a terminal operation or newer attempt is immutable.
func (store *Store) SaveRetrievalReport(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, attempt int, report knowl.RetrievalReport) error {
	encoded, err := app.EncodeRetrievalReport(report)
	if err != nil {
		return err
	}
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		var stored sql.NullString
		var workAttempt, reportAttempt int
		err := tx.QueryRowContext(ctx, `SELECT work_attempt, retrieval_report_attempt, retrieval_report FROM knowl_operations WHERE scope = $1 AND operation_id = $2`, scope, id).Scan(&workAttempt, &reportAttempt, &stored)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read retrieval report: %w", err)
		}
		if attempt < 0 || attempt != workAttempt {
			return ErrLeaseConflict
		}
		if stored.Valid && reportAttempt == attempt {
			if stored.String == encoded {
				return nil
			}
			return ErrConflict
		}
		if current.status == knowl.StatusCommitted || current.status == knowl.StatusFailed {
			return ErrInvalidState
		}
		return updateOperationTx(ctx, tx, id, `retrieval_report = $1, retrieval_report_attempt = $2`, encoded, attempt)
	})
}
