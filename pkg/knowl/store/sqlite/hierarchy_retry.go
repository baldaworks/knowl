package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/operationpayload"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// RetryHierarchy previews or requeues exactly one failed, unplanned attempt.
func (store *Store) RetryHierarchy(ctx context.Context, request app.HierarchyRetryRequest) (app.HierarchyRetryResult, error) {
	d := request.Descriptor
	result := app.HierarchyRetryResult{OperationID: d.OperationID, DryRun: request.DryRun, WorkAttempt: request.WorkAttempt}
	if d.Kind != knowl.WorkHierarchy || app.ValidateGenericExecutionDescriptor(d.Schema.Scope, d) != nil || request.WorkAttempt < 1 {
		return result, app.ErrHierarchyRetryConflict
	}
	payload, err := operationpayload.EncodeHierarchy(d)
	if err != nil {
		return result, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	args := []any{d.Schema.Scope, d.OperationID, knowl.WorkHierarchy, knowl.StatusFailed, request.WorkAttempt, d.Schema.Digest, payload}
	const eligible = `scope=? AND operation_id=? AND work_kind=? AND status=? AND work_attempt=? AND schema_digest=? AND execution_payload=? AND plan_digest='' AND commit_generation='' AND (work_lease_token = '' OR COALESCE(julianday(work_lease_expires_at),0) <= julianday('now')) AND (lease_token = '' OR COALESCE(julianday(lease_expires_at),0) <= julianday('now'))`
	if request.DryRun {
		err = tx.QueryRowContext(ctx, `SELECT manual_retry_count FROM knowl_operations WHERE `+eligible, args...).Scan(&result.ManualRetryCount)
		if errors.Is(err, sql.ErrNoRows) {
			return result, app.ErrHierarchyRetryConflict
		}
		if err != nil {
			return result, err
		}
		return result, tx.Commit()
	}

	now := nowString()
	auditArgs := append([]any{now}, args...)
	audit, err := tx.ExecContext(ctx, `INSERT INTO knowl_hierarchy_retries (operation_id,work_attempt,manual_retry_count,failure_class,failure_reason,correction_report,failure_diagnostics,failed_at,retried_at)
 SELECT operation_id,work_attempt,manual_retry_count+1,failure_class,failure_reason,COALESCE(correction_report,''),failure_diagnostics,updated_at,? FROM knowl_operations WHERE `+eligible+` ON CONFLICT (operation_id,work_attempt) DO NOTHING`, auditArgs...)
	if err != nil {
		return result, fmt.Errorf("archive hierarchy attempt: %w", err)
	}
	n, err := audit.RowsAffected()
	if err != nil {
		return result, err
	}
	if n != 1 {
		return result, app.ErrHierarchyRetryConflict
	}
	updateArgs := append([]any{knowl.StatusReceived, now, now}, args...)
	changed, err := tx.ExecContext(ctx, `UPDATE knowl_operations SET status=?,failure_class='',failure_reason='',failure_diagnostics='',maintenance_diagnostics='',work_ready_at=?,updated_at=?,retry_attempt=0,manual_retry_count=manual_retry_count+1,work_lease_token='',work_lease_expires_at='',lease_token='',lease_expires_at='' WHERE `+eligible, updateArgs...)
	if err != nil {
		return result, err
	}
	n, err = changed.RowsAffected()
	if err != nil {
		return result, err
	}
	if n != 1 {
		return result, app.ErrHierarchyRetryConflict
	}
	if err := tx.QueryRowContext(ctx, `SELECT manual_retry_count FROM knowl_operations WHERE operation_id=?`, d.OperationID).Scan(&result.ManualRetryCount); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	result.Requeued = true
	return result, nil
}

// HierarchyRetryHistory returns at most 100 previous failed attempts, newest first.
func (store *Store) HierarchyRetryHistory(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, limit int) ([]app.HierarchyRetryAttempt, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, err := store.db.QueryContext(ctx, `SELECT history.work_attempt,history.manual_retry_count,history.failure_class,history.failure_reason,CASE WHEN length(CAST(history.correction_report AS BLOB)) > 1024 THEN '{}' ELSE history.correction_report END,CASE WHEN length(CAST(history.failure_diagnostics AS BLOB)) > 32768 THEN '{}' ELSE history.failure_diagnostics END,history.failed_at,history.retried_at
 FROM knowl_hierarchy_retries history JOIN knowl_operations operation ON operation.operation_id=history.operation_id WHERE operation.scope=? AND history.operation_id=? ORDER BY history.work_attempt DESC LIMIT ?`, scope, id, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]app.HierarchyRetryAttempt, 0)
	for rows.Next() {
		var item app.HierarchyRetryAttempt
		var correction, diagnostics string
		var failedAt, retriedAt string
		if err := rows.Scan(&item.WorkAttempt, &item.ManualRetryCount, &item.Failure.Class, &item.Failure.Reason, &correction, &diagnostics, &failedAt, &retriedAt); err != nil {
			return nil, err
		}
		item.Failure.OperationID = string(id)
		item.Correction, err = app.DecodeOperationCorrectionReport(correction, item.WorkAttempt)
		if err != nil {
			return nil, err
		}
		item.Diagnostics, err = app.DecodeMaintenanceDiagnostics(diagnostics)
		if err != nil {
			return nil, err
		}
		item.FailedAt, err = parseTime(failedAt)
		if err != nil {
			return nil, err
		}
		item.RetriedAt, err = parseTime(retriedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
