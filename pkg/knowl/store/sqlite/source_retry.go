package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

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
	if request.MaintenanceGeneration != "" {
		return store.retrySourceMaintenanceGeneration(ctx, request, classes)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin source maintenance retry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	classClause, classArgs := sqliteRetryClasses(classes)
	query := `
		SELECT document.revision, document.maintenance_revision,
		       operation.operation_id, operation.scope, operation.work_kind, operation.status,
		       CASE WHEN operation.work_lease_token <> ''
		                  AND julianday(operation.work_lease_expires_at) > julianday('now') THEN 1 ELSE 0 END,
		       CASE WHEN operation.lease_token <> ''
		                  AND julianday(operation.lease_expires_at) > julianday('now') THEN 1 ELSE 0 END
		FROM knowl_source_documents AS document
		JOIN knowl_operations AS operation
		  ON operation.operation_id = document.maintenance_operation_id
		WHERE document.scope = ? AND document.source_id = ? AND document.deleted = 0
		  AND document.maintenance_operation_id <> ''
		  AND operation.failure_class IN (` + classClause + `)
		ORDER BY operation.operation_id, document.document_id`
	args := []any{request.Scope, request.SourceID}
	args = append(args, classArgs...)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return result, fmt.Errorf("select source maintenance retry: %w", err)
	}
	invalid, err := scanSQLiteRetryCandidates(rows, request.Scope, &result)
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

	now := nowString()
	update := `
		UPDATE knowl_operations
		SET status = ?, plan_digest = '', commit_generation = '', maintenance_diagnostics = '', failure_class = '', failure_reason = '',
		    lease_token = '', lease_expires_at = '', work_lease_token = '', work_lease_expires_at = '',
		    work_ready_at = ?, retry_attempt = 0, manual_retry_count = manual_retry_count + 1, updated_at = ?
		WHERE scope = ? AND work_kind = ? AND status = ?
		  AND (work_lease_token = '' OR COALESCE(julianday(work_lease_expires_at), 0) <= julianday('now'))
		  AND (lease_token = '' OR COALESCE(julianday(lease_expires_at), 0) <= julianday('now'))
		  AND operation_id IN (
			SELECT document.maintenance_operation_id
			FROM knowl_source_documents AS document
			JOIN knowl_operations AS candidate ON candidate.operation_id = document.maintenance_operation_id
			WHERE document.scope = ? AND document.source_id = ? AND document.deleted = 0
			  AND document.maintenance_revision = document.revision
			  AND candidate.scope = document.scope AND candidate.work_kind = ? AND candidate.status = ?
			  AND (candidate.work_lease_token = '' OR COALESCE(julianday(candidate.work_lease_expires_at), 0) <= julianday('now'))
			  AND (candidate.lease_token = '' OR COALESCE(julianday(candidate.lease_expires_at), 0) <= julianday('now'))
			  AND candidate.failure_class IN (` + classClause + `)
		  )`
	updateArgs := []any{knowl.StatusReceived, now, now, request.Scope, knowl.WorkSourceMaintenance, knowl.StatusFailed,
		request.Scope, request.SourceID, knowl.WorkSourceMaintenance, knowl.StatusFailed}
	updateArgs = append(updateArgs, classArgs...)
	updated, err := tx.ExecContext(ctx, update, updateArgs...)
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

type generationRetryCandidate struct {
	documentID          knowl.DocumentID
	documentRevision    string
	documentGeneration  string
	operationID         knowl.OperationID
	key                 knowl.OperationKey
	maintenanceRevision string
	operationScope      knowl.ScopeRef
	kind                knowl.WorkKind
	status              knowl.OperationStatus
	workLeaseActive     int
	applyLeaseActive    int
}

func (store *Store) retrySourceMaintenanceGeneration(ctx context.Context, request app.SourceMaintenanceRetryRequest, classes []string) (app.SourceMaintenanceRetryResult, error) {
	result := app.SourceMaintenanceRetryResult{SourceID: request.SourceID, DryRun: request.DryRun, OperationIDs: make([]knowl.OperationID, 0)}
	if app.ValidateMaintenanceGeneration(request.MaintenanceGeneration) != nil ||
		app.ValidateExecutionSchema(request.Scope, request.Schema) != nil {
		return result, app.ErrSourceInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin generation source maintenance retry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	classClause, classArgs := sqliteRetryClasses(classes)
	query := `
		SELECT document.document_id, document.revision, document.maintenance_revision, document.maintenance_generation,
		       operation.operation_id, operation.scope, operation.work_kind, operation.status,
		       operation.source_adapter, operation.source_id, operation.source_version,
		       operation.source_digest, operation.maintenance_generation,
		       CASE WHEN operation.work_lease_token <> '' AND julianday(operation.work_lease_expires_at) > julianday('now') THEN 1 ELSE 0 END,
		       CASE WHEN operation.lease_token <> '' AND julianday(operation.lease_expires_at) > julianday('now') THEN 1 ELSE 0 END
		FROM knowl_source_documents AS document
		JOIN knowl_operations AS operation ON operation.operation_id = document.maintenance_operation_id
		WHERE document.scope = ? AND document.source_id = ? AND document.deleted = 0
		  AND operation.failure_class IN (` + classClause + `)
		ORDER BY operation.operation_id, document.document_id`
	args := []any{request.Scope, request.SourceID}
	args = append(args, classArgs...)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return result, fmt.Errorf("select generation source maintenance retry: %w", err)
	}
	var candidates []generationRetryCandidate
	for rows.Next() {
		var candidate generationRetryCandidate
		if err := rows.Scan(&candidate.documentID, &candidate.documentRevision, &candidate.maintenanceRevision, &candidate.documentGeneration,
			&candidate.operationID, &candidate.operationScope, &candidate.kind, &candidate.status,
			&candidate.key.Source.Adapter, &candidate.key.Source.ID, &candidate.key.Version.Version,
			&candidate.key.Version.Digest, &candidate.key.MaintenanceGeneration,
			&candidate.workLeaseActive, &candidate.applyLeaseActive); err != nil {
			_ = rows.Close()
			return result, fmt.Errorf("scan generation source maintenance retry: %w", err)
		}
		candidate.key.Scope = candidate.operationScope
		candidates = append(candidates, candidate)
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	seen := make(map[knowl.OperationID]struct{}, len(candidates))
	for _, candidate := range candidates {
		expectedID, identityErr := app.SourceOperationID(candidate.key)
		if candidate.maintenanceRevision != candidate.documentRevision || candidate.key.Version.Version != candidate.documentRevision ||
			candidate.documentGeneration != candidate.key.MaintenanceGeneration || candidate.operationScope != request.Scope ||
			candidate.kind != knowl.WorkSourceMaintenance || candidate.status != knowl.StatusFailed ||
			candidate.workLeaseActive != 0 || candidate.applyLeaseActive != 0 || identityErr != nil || expectedID != candidate.operationID {
			result.Rejected++
		}
		if _, exists := seen[candidate.operationID]; exists {
			continue
		}
		seen[candidate.operationID] = struct{}{}
		result.Matched++
		key := candidate.key
		key.MaintenanceGeneration = request.MaintenanceGeneration
		id, idErr := app.SourceOperationID(key)
		if idErr != nil {
			result.Rejected++
			id = candidate.operationID
		}
		if candidate.key.MaintenanceGeneration == request.MaintenanceGeneration {
			id = candidate.operationID
		}
		if len(result.OperationIDs) < app.MaxSourceMaintenanceRetryResultIDs() {
			result.OperationIDs = append(result.OperationIDs, id)
		} else {
			result.Truncated = true
		}
	}
	if result.Rejected != 0 {
		return result, app.ErrSourceRetryConflict
	}
	if request.DryRun || result.Matched == 0 {
		if err := tx.Commit(); err != nil {
			return result, err
		}
		return result, nil
	}
	now := nowString()
	processed := make(map[knowl.OperationID]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, exists := processed[candidate.operationID]; exists {
			continue
		}
		processed[candidate.operationID] = struct{}{}
		if candidate.key.MaintenanceGeneration == request.MaintenanceGeneration {
			updated, updateErr := tx.ExecContext(ctx, `UPDATE knowl_operations SET status = ?, plan_digest = '', commit_generation = '', maintenance_diagnostics = '', failure_class = '', failure_reason = '', lease_token = '', lease_expires_at = '', work_lease_token = '', work_lease_expires_at = '', work_ready_at = ?, retry_attempt = 0, manual_retry_count = manual_retry_count + 1, updated_at = ? WHERE operation_id = ? AND status = ?`, knowl.StatusReceived, now, now, candidate.operationID, knowl.StatusFailed)
			if updateErr != nil {
				return result, updateErr
			}
			changed, _ := updated.RowsAffected()
			result.Requeued += changed
			continue
		}
		key := candidate.key
		key.MaintenanceGeneration = request.MaintenanceGeneration
		newID, _ := app.SourceOperationID(key)
		_, insertErr := tx.ExecContext(ctx, `INSERT INTO knowl_operations (operation_id, scope, source_adapter, source_id, source_version, source_digest, schema_digest, status, created_at, updated_at, accepted_media_type, source_manifest_ref, accepted_source_document, schema_version, schema_snapshot, work_ready_at, work_kind, maintenance_generation, manual_retry_count) SELECT ?, scope, source_adapter, source_id, source_version, source_digest, ?, ?, ?, ?, accepted_media_type, source_manifest_ref, accepted_source_document, ?, ?, ?, work_kind, ?, manual_retry_count + 1 FROM knowl_operations WHERE operation_id = ? ON CONFLICT(scope, source_adapter, source_id, source_version, maintenance_generation) DO NOTHING`, newID, request.Schema.Digest, knowl.StatusReceived, now, now, request.Schema.Version, request.Schema.Content, now, request.MaintenanceGeneration, candidate.operationID)
		if insertErr != nil {
			return result, insertErr
		}
		var targetExists int
		if queryErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowl_operations WHERE operation_id = ? AND scope = ? AND source_adapter = ? AND source_id = ? AND source_version = ? AND source_digest = ? AND maintenance_generation = ? AND work_kind = ?`, newID, candidate.key.Scope, candidate.key.Source.Adapter, candidate.key.Source.ID, candidate.key.Version.Version, candidate.key.Version.Digest, request.MaintenanceGeneration, knowl.WorkSourceMaintenance).Scan(&targetExists); queryErr != nil {
			return result, queryErr
		}
		if targetExists != 1 {
			return result, app.ErrSourceRetryConflict
		}
		updated, updateErr := tx.ExecContext(ctx, `UPDATE knowl_source_documents SET maintenance_operation_id = ?, maintenance_generation = ?, updated_at = ? WHERE scope = ? AND source_id = ? AND maintenance_operation_id = ?`, newID, request.MaintenanceGeneration, now, request.Scope, request.SourceID, candidate.operationID)
		if updateErr != nil {
			return result, updateErr
		}
		changed, _ := updated.RowsAffected()
		if changed == 0 {
			return result, app.ErrSourceRetryConflict
		}
		result.Requeued++
	}
	if result.Requeued != result.Matched {
		return result, app.ErrSourceRetryConflict
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit generation source maintenance retry: %w", err)
	}
	return result, nil
}

func sqliteRetryClasses(classes []string) (string, []any) {
	placeholders := make([]string, len(classes))
	args := make([]any, len(classes))
	for index, class := range classes {
		placeholders[index] = "?"
		args[index] = class
	}
	return strings.Join(placeholders, ", "), args
}

func scanSQLiteRetryCandidates(rows *sql.Rows, scope knowl.ScopeRef, result *app.SourceMaintenanceRetryResult) (int64, error) {
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
