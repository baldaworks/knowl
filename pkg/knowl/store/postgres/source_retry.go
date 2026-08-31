package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// RetrySourceMaintenance atomically previews or requeues selected terminal
// current-revision source maintenance operations.
func (store *Store) RetrySourceMaintenance(ctx context.Context, request app.SourceMaintenanceRetryRequest) (app.SourceMaintenanceRetryResult, error) {
	result := app.SourceMaintenanceRetryResult{SourceID: request.SourceID, DryRun: request.DryRun, OperationIDs: make([]knowl.OperationID, 0)}
	if err := validateScope(request.Scope); err != nil || app.ValidateSourceID(request.SourceID) != nil {
		return result, app.ErrSourceInvalid
	}
	classes, err := app.NormalizeRetryFailureClasses(request.FailureClasses)
	if err != nil {
		return result, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin source maintenance retry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	classClause, classArgs := postgresRetryClasses(classes)
	query := `
		SELECT document.revision, document.maintenance_revision,
		       operation.operation_id, operation.scope, operation.work_kind, operation.status,
		       CASE WHEN operation.work_lease_token <> ''
		                  AND operation.work_lease_expires_at > CURRENT_TIMESTAMP THEN 1 ELSE 0 END,
		       CASE WHEN operation.lease_token <> ''
		                  AND operation.lease_expires_at > CURRENT_TIMESTAMP THEN 1 ELSE 0 END
		FROM knowl_source_documents AS document
		JOIN knowl_operations AS operation
		  ON operation.operation_id = document.maintenance_operation_id
		WHERE document.scope = ? AND document.source_id = ? AND document.deleted = FALSE
		  AND document.maintenance_operation_id <> ''
		  AND operation.failure_class IN (` + classClause + `)
		ORDER BY operation.operation_id, document.document_id`
	args := []any{request.Scope, request.SourceID}
	args = append(args, classArgs...)
	rows, err := sourceQuery(ctx, tx, query, args...)
	if err != nil {
		return result, fmt.Errorf("select source maintenance retry: %w", err)
	}
	invalid, err := scanPostgresRetryCandidates(rows, request.Scope, &result)
	if err != nil {
		return result, err
	}
	result.Rejected = invalid
	if invalid != 0 {
		return result, app.ErrSourceRetryConflict
	}
	if request.DryRun || result.Matched == 0 {
		if err := tx.Commit(); err != nil {
			return result, fmt.Errorf("commit source maintenance retry preview: %w", err)
		}
		return result, nil
	}

	now := time.Now().UTC()
	update := `
		UPDATE knowl_operations
		SET status = ?, plan_digest = '', commit_generation = '', failure_class = '', failure_reason = '',
		    lease_token = '', lease_expires_at = NULL, work_lease_token = '', work_lease_expires_at = NULL,
		    work_ready_at = ?, retry_attempt = 0, manual_retry_count = manual_retry_count + 1, updated_at = ?
		WHERE scope = ? AND work_kind = ? AND status = ?
		  AND (work_lease_token = '' OR work_lease_expires_at IS NULL OR work_lease_expires_at <= CURRENT_TIMESTAMP)
		  AND (lease_token = '' OR lease_expires_at IS NULL OR lease_expires_at <= CURRENT_TIMESTAMP)
		  AND operation_id IN (
			SELECT document.maintenance_operation_id
			FROM knowl_source_documents AS document
			JOIN knowl_operations AS candidate ON candidate.operation_id = document.maintenance_operation_id
			WHERE document.scope = ? AND document.source_id = ? AND document.deleted = FALSE
			  AND document.maintenance_revision = document.revision
			  AND candidate.scope = document.scope AND candidate.work_kind = ? AND candidate.status = ?
			  AND (candidate.work_lease_token = '' OR candidate.work_lease_expires_at IS NULL OR candidate.work_lease_expires_at <= CURRENT_TIMESTAMP)
			  AND (candidate.lease_token = '' OR candidate.lease_expires_at IS NULL OR candidate.lease_expires_at <= CURRENT_TIMESTAMP)
			  AND candidate.failure_class IN (` + classClause + `)
		  )`
	updateArgs := []any{knowl.StatusReceived, now, now, request.Scope, knowl.WorkSourceMaintenance, knowl.StatusFailed,
		request.Scope, request.SourceID, knowl.WorkSourceMaintenance, knowl.StatusFailed}
	updateArgs = append(updateArgs, classArgs...)
	updated, err := sourceExec(ctx, tx, update, updateArgs...)
	if err != nil {
		return result, fmt.Errorf("requeue source maintenance: %w", err)
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return result, fmt.Errorf("inspect source maintenance retry: %w", err)
	}
	if changed != result.Matched {
		return result, app.ErrSourceRetryConflict
	}
	result.Requeued = changed
	if err := tx.Commit(); err != nil {
		return app.SourceMaintenanceRetryResult{SourceID: request.SourceID, DryRun: request.DryRun, OperationIDs: make([]knowl.OperationID, 0)}, fmt.Errorf("commit source maintenance retry: %w", err)
	}
	return result, nil
}

func postgresRetryClasses(classes []string) (string, []any) {
	placeholders := make([]string, len(classes))
	args := make([]any, len(classes))
	for index, class := range classes {
		placeholders[index] = "?"
		args[index] = class
	}
	return strings.Join(placeholders, ", "), args
}

func scanPostgresRetryCandidates(rows *sql.Rows, scope knowl.ScopeRef, result *app.SourceMaintenanceRetryResult) (int64, error) {
	defer func() { _ = rows.Close() }()
	var lastID knowl.OperationID
	var currentValid bool
	var rejected int64
	flush := func() {
		if lastID == "" {
			return
		}
		result.Matched++
		if !currentValid {
			rejected++
		}
		if len(result.OperationIDs) < app.MaxSourceMaintenanceRetryResultIDs() {
			result.OperationIDs = append(result.OperationIDs, lastID)
		} else {
			result.Truncated = true
		}
	}
	for rows.Next() {
		var revision, maintenanceRevision, operationScope, kind, status string
		var workLeaseActive, applyLeaseActive int
		var id knowl.OperationID
		if err := rows.Scan(&revision, &maintenanceRevision, &id, &operationScope, &kind, &status, &workLeaseActive, &applyLeaseActive); err != nil {
			return 0, fmt.Errorf("scan source maintenance retry: %w", err)
		}
		valid := maintenanceRevision == revision && knowl.ScopeRef(operationScope) == scope &&
			knowl.WorkKind(kind) == knowl.WorkSourceMaintenance && knowl.OperationStatus(status) == knowl.StatusFailed &&
			workLeaseActive == 0 && applyLeaseActive == 0
		if id != lastID {
			flush()
			lastID = id
			currentValid = valid
		} else {
			currentValid = currentValid && valid
		}
	}
	if err := rows.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return 0, fmt.Errorf("iterate source maintenance retry: %w", err)
	}
	flush()
	return rejected, rows.Err()
}
