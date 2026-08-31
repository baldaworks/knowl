package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/operationpayload"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const maxWorkScanLimit = 100

type rowScanner interface {
	Scan(dest ...any) error
}

// Execution returns the validated durable descriptor for one scoped operation.
func (store *Store) Execution(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.ExecutionDescriptor, error) {
	if err := validateScope(scope); err != nil {
		return knowl.ExecutionDescriptor{}, err
	}
	descriptor, key, err := scanExecution(store.db.QueryRowContext(ctx, `
			SELECT operation_id, scope, work_kind, execution_payload, source_adapter, source_id, source_version, source_digest,
			       accepted_media_type, source_manifest_ref, accepted_source_document, schema_digest, schema_version, schema_snapshot
		FROM knowl_operations WHERE scope = $1 AND operation_id = $2`, scope, id))
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.ExecutionDescriptor{}, ErrNotFound
	}
	if err != nil {
		return knowl.ExecutionDescriptor{}, fmt.Errorf("read execution descriptor: %w", err)
	}
	if err := validateStoredExecution(scope, key, descriptor); err != nil {
		return knowl.ExecutionDescriptor{}, err
	}
	return descriptor, nil
}

// ResumeReady lists bounded claimable operation IDs without granting ownership.
func (store *Store) ResumeReady(ctx context.Context, scope knowl.ScopeRef, limit int) ([]knowl.OperationID, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	limit = boundedWorkLimit(limit)
	rows, err := store.db.QueryContext(ctx, `
		SELECT operation_id
		FROM knowl_operations
		WHERE scope = $1
		  AND status NOT IN ($2, $3)
		  AND schema_digest <> '' AND schema_snapshot IS NOT NULL
		  AND ((work_kind = $4 AND accepted_media_type <> '' AND source_manifest_ref <> '')
		       OR (work_kind = $5 AND execution_payload <> ''))
		  AND ((work_lease_token = '' AND work_ready_at <= CURRENT_TIMESTAMP)
		       OR (work_lease_token <> '' AND work_lease_expires_at <= CURRENT_TIMESTAMP))
		ORDER BY CASE WHEN work_lease_token = '' THEN work_ready_at
		              ELSE work_lease_expires_at END ASC,
		         operation_id ASC
		LIMIT $6`, scope, knowl.StatusCommitted, knowl.StatusFailed, knowl.WorkSourceMaintenance, knowl.WorkHierarchy, maxWorkScanLimit)
	if err != nil {
		return nil, fmt.Errorf("inspect ready operations: %w", err)
	}
	var candidates []knowl.OperationID
	for rows.Next() {
		var id knowl.OperationID
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan ready operation: %w", err)
		}
		candidates = append(candidates, id)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close ready operations: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ready operations: %w", err)
	}
	ready := make([]knowl.OperationID, 0, minInt(limit, len(candidates)))
	for _, id := range candidates {
		if _, err := store.Execution(ctx, scope, id); err != nil {
			if errors.Is(err, app.ErrExecutionDescriptorUnavailable) {
				continue
			}
			return nil, err
		}
		ready = append(ready, id)
		if len(ready) == limit {
			break
		}
	}
	return ready, nil
}

// ClaimReady atomically grants a work lease using database-level row locking.
func (store *Store) ClaimReady(ctx context.Context, scope knowl.ScopeRef, lease knowl.WorkLease) (knowl.WorkClaim, error) {
	if err := validateScope(scope); err != nil {
		return knowl.WorkClaim{}, err
	}
	if err := validateWorkLease(lease); err != nil {
		return knowl.WorkClaim{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("begin work claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if !lease.ExpiresAt.After(time.Now().UTC()) {
		return knowl.WorkClaim{}, app.ErrWorkLeaseConflict
	}
	rows, err := tx.QueryContext(ctx, `
			SELECT operation_id, scope, work_kind, execution_payload, source_adapter, source_id, source_version, source_digest,
			       accepted_media_type, source_manifest_ref, accepted_source_document, schema_digest, schema_version, schema_snapshot
		FROM knowl_operations
		WHERE scope = $1
		  AND status NOT IN ($2, $3)
		  AND schema_digest <> '' AND schema_snapshot IS NOT NULL
		  AND ((work_kind = $4 AND accepted_media_type <> '' AND source_manifest_ref <> '')
		       OR (work_kind = $5 AND execution_payload <> ''))
		  AND ((work_lease_token = '' AND work_ready_at <= CURRENT_TIMESTAMP)
		       OR (work_lease_token <> '' AND work_lease_expires_at <= CURRENT_TIMESTAMP))
		ORDER BY CASE WHEN work_lease_token = '' THEN work_ready_at
		              ELSE work_lease_expires_at END ASC,
		         operation_id ASC
		LIMIT $6
		FOR UPDATE SKIP LOCKED`, scope, knowl.StatusCommitted, knowl.StatusFailed, knowl.WorkSourceMaintenance, knowl.WorkHierarchy, maxWorkScanLimit)
	if err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("select ready operation: %w", err)
	}
	var id knowl.OperationID
	var descriptor knowl.ExecutionDescriptor
	for rows.Next() {
		candidate, key, scanErr := scanExecution(rows)
		if scanErr != nil {
			_ = rows.Close()
			return knowl.WorkClaim{}, fmt.Errorf("scan ready descriptor: %w", scanErr)
		}
		if validationErr := validateStoredExecution(scope, key, candidate); validationErr != nil {
			if errors.Is(validationErr, app.ErrExecutionDescriptorUnavailable) {
				continue
			}
			_ = rows.Close()
			return knowl.WorkClaim{}, validationErr
		}
		id = candidate.OperationID
		descriptor = candidate
		break
	}
	if closeErr := rows.Close(); closeErr != nil {
		return knowl.WorkClaim{}, fmt.Errorf("close ready operation candidates: %w", closeErr)
	}
	if err := rows.Err(); err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("iterate ready operation candidates: %w", err)
	}
	if id == "" {
		return knowl.WorkClaim{}, app.ErrNoReadyOperation
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE knowl_operations
		SET work_attempt = work_attempt + 1, retry_attempt = retry_attempt + 1,
		    work_lease_token = $1, work_lease_expires_at = $2
		WHERE scope = $3 AND operation_id = $4
		  AND status NOT IN ($5, $6)
		  AND ((work_lease_token = '' AND work_ready_at <= CURRENT_TIMESTAMP)
		       OR (work_lease_token <> '' AND work_lease_expires_at <= CURRENT_TIMESTAMP))`,
		lease.Token, lease.ExpiresAt.UTC(), scope, id, knowl.StatusCommitted, knowl.StatusFailed)
	if err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("grant work lease: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("inspect work lease: %w", err)
	}
	if changed != 1 {
		return knowl.WorkClaim{}, app.ErrWorkLeaseConflict
	}
	operation, err := operationFromScanner(tx.QueryRowContext(ctx, `
		SELECT operation_id, work_kind, source_adapter, source_id, source_version, source_digest,
		       status, attempt, work_attempt, retry_attempt, manual_retry_count,
		       failure_class, failure_reason, work_ready_at, updated_at
		FROM knowl_operations WHERE scope = $1 AND operation_id = $2`, scope, id), scope)
	if err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("read claimed operation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("commit work claim: %w", err)
	}
	return knowl.WorkClaim{Operation: operation, Descriptor: descriptor, Lease: lease}, nil
}

// ClaimOperation atomically grants a work lease for one specific claimable operation.
func (store *Store) ClaimOperation(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, lease knowl.WorkLease) (knowl.WorkClaim, error) {
	if err := validateScope(scope); err != nil {
		return knowl.WorkClaim{}, err
	}
	if strings.TrimSpace(string(id)) == "" {
		return knowl.WorkClaim{}, app.ErrNoReadyOperation
	}
	if err := validateWorkLease(lease); err != nil {
		return knowl.WorkClaim{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("begin targeted work claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if !lease.ExpiresAt.After(time.Now().UTC()) {
		return knowl.WorkClaim{}, app.ErrWorkLeaseConflict
	}
	descriptor, key, err := scanExecution(tx.QueryRowContext(ctx, `
		SELECT operation_id, scope, work_kind, execution_payload, source_adapter, source_id, source_version, source_digest,
		       accepted_media_type, source_manifest_ref, accepted_source_document, schema_digest, schema_version, schema_snapshot
		FROM knowl_operations
		WHERE scope = $1 AND operation_id = $2
		  AND status NOT IN ($3, $4)
		  AND ((work_lease_token = '' AND work_ready_at <= CURRENT_TIMESTAMP)
		       OR (work_lease_token <> '' AND work_lease_expires_at <= CURRENT_TIMESTAMP))
		FOR UPDATE`, scope, id, knowl.StatusCommitted, knowl.StatusFailed))
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.WorkClaim{}, app.ErrNoReadyOperation
	}
	if err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("read targeted work descriptor: %w", err)
	}
	if err := validateStoredExecution(scope, key, descriptor); err != nil {
		return knowl.WorkClaim{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE knowl_operations
		SET work_attempt = work_attempt + 1, retry_attempt = retry_attempt + 1,
		    work_lease_token = $1, work_lease_expires_at = $2
		WHERE scope = $3 AND operation_id = $4
		  AND status NOT IN ($5, $6)
		  AND ((work_lease_token = '' AND work_ready_at <= CURRENT_TIMESTAMP)
		       OR (work_lease_token <> '' AND work_lease_expires_at <= CURRENT_TIMESTAMP))`,
		lease.Token, lease.ExpiresAt.UTC(), scope, id, knowl.StatusCommitted, knowl.StatusFailed)
	if err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("grant targeted work lease: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("inspect targeted work lease: %w", err)
	}
	if changed != 1 {
		return knowl.WorkClaim{}, app.ErrWorkLeaseConflict
	}
	operation, err := operationFromScanner(tx.QueryRowContext(ctx, `
		SELECT operation_id, work_kind, source_adapter, source_id, source_version, source_digest,
		       status, attempt, work_attempt, retry_attempt, manual_retry_count,
		       failure_class, failure_reason, work_ready_at, updated_at
		FROM knowl_operations WHERE scope = $1 AND operation_id = $2`, scope, id), scope)
	if err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("read targeted claimed operation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return knowl.WorkClaim{}, fmt.Errorf("commit targeted work claim: %w", err)
	}
	return knowl.WorkClaim{Operation: operation, Descriptor: descriptor, Lease: lease}, nil
}

// RenewClaim replaces a live work lease when the caller still owns its token.
func (store *Store) RenewClaim(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, currentToken string, next knowl.WorkLease) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if strings.TrimSpace(currentToken) == "" {
		return app.ErrWorkLeaseConflict
	}
	if err := validateWorkLease(next); err != nil {
		return err
	}
	return store.updateWorkLease(ctx, `
		UPDATE knowl_operations
		SET work_lease_token = $1, work_lease_expires_at = $2
		WHERE scope = $3 AND operation_id = $4 AND work_lease_token = $5
		  AND status NOT IN ($6, $7)`,
		next.Token, next.ExpiresAt.UTC(), scope, id, currentToken,
		knowl.StatusCommitted, knowl.StatusFailed)
}

// ReleaseClaim makes owned non-terminal work immediately ready again.
func (store *Store) ReleaseClaim(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, token string) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return app.ErrWorkLeaseConflict
	}
	return store.updateWorkLease(ctx, `
		UPDATE knowl_operations
		SET work_lease_token = '', work_lease_expires_at = NULL, work_ready_at = CURRENT_TIMESTAMP
		WHERE scope = $1 AND operation_id = $2 AND work_lease_token = $3
		  AND status NOT IN ($4, $5)`, scope, id, token,
		knowl.StatusCommitted, knowl.StatusFailed)
}

// ScheduleRetry records a safe transient failure and delays work owned by the
// current lease holder. The operation remains non-terminal.
func (store *Store) ScheduleRetry(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, token string, failure knowl.Failure, readyAt time.Time) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" || !app.ValidateSafeFailure(failure, true) || !readyAt.After(time.Now().UTC()) {
		return app.ErrWorkLeaseConflict
	}
	return store.updateWorkLease(ctx, `
		UPDATE knowl_operations
		SET failure_class = $1, failure_reason = $2, work_lease_token = '',
		    work_lease_expires_at = NULL, work_ready_at = $3, updated_at = $4
		WHERE scope = $5 AND operation_id = $6 AND work_lease_token = $7
		  AND work_lease_expires_at > CURRENT_TIMESTAMP
		  AND status NOT IN ($8, $9)`,
		failure.Class, failure.Reason, readyAt.UTC(), time.Now().UTC(), scope, id, token,
		knowl.StatusCommitted, knowl.StatusFailed)
}

// FailClaim terminalizes work only while the caller still owns a live lease.
func (store *Store) FailClaim(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, token string, failure knowl.Failure) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" || !app.ValidateSafeFailure(failure, true) {
		return app.ErrWorkLeaseConflict
	}
	return store.updateWorkLease(ctx, `
		UPDATE knowl_operations
		SET status = $1, failure_class = $2, failure_reason = $3,
		    lease_token = '', lease_expires_at = NULL, work_lease_token = '', work_lease_expires_at = NULL, updated_at = $4
		WHERE scope = $5 AND operation_id = $6 AND work_lease_token = $7
		  AND work_lease_expires_at > CURRENT_TIMESTAMP
		  AND status NOT IN ($8, $9)`,
		knowl.StatusFailed, failure.Class, failure.Reason, time.Now().UTC(), scope, id, token,
		knowl.StatusCommitted, knowl.StatusFailed)
}

func (store *Store) updateWorkLease(ctx context.Context, query string, args ...any) error {
	result, err := store.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update work lease: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect work lease update: %w", err)
	}
	if changed != 1 {
		return app.ErrWorkLeaseConflict
	}
	return nil
}

// DescriptorFailures lists non-terminal operations whose durable inputs cannot be validated.
func (store *Store) DescriptorFailures(ctx context.Context, scope knowl.ScopeRef, limit int) ([]knowl.OperationID, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	limit = boundedWorkLimit(limit)
	rows, err := store.db.QueryContext(ctx, `
		SELECT operation_id FROM knowl_operations
		WHERE scope = $1 AND status NOT IN ($2, $3)
		ORDER BY created_at ASC, operation_id ASC
		LIMIT $4`, scope, knowl.StatusCommitted, knowl.StatusFailed, maxWorkScanLimit)
	if err != nil {
		return nil, fmt.Errorf("inspect descriptor failures: %w", err)
	}
	var candidates []knowl.OperationID
	for rows.Next() {
		var id knowl.OperationID
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan descriptor failure: %w", err)
		}
		candidates = append(candidates, id)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close descriptor failures: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate descriptor failures: %w", err)
	}
	failures := make([]knowl.OperationID, 0, minInt(limit, len(candidates)))
	for _, id := range candidates {
		_, err := store.Execution(ctx, scope, id)
		if errors.Is(err, app.ErrExecutionDescriptorUnavailable) {
			failures = append(failures, id)
			if len(failures) == limit {
				break
			}
		} else if err != nil {
			return nil, err
		}
	}
	return failures, nil
}

func scanExecution(scanner rowScanner) (knowl.ExecutionDescriptor, knowl.OperationKey, error) {
	var descriptor knowl.ExecutionDescriptor
	var key knowl.OperationKey
	var kindText, payload, sourceDocument, schemaDigest, schemaVersion string
	var schemaSnapshot []byte
	if err := scanner.Scan(
		&descriptor.OperationID, &key.Scope, &kindText, &payload, &key.Source.Adapter, &key.Source.ID,
		&key.Version.Version, &key.Version.Digest, &descriptor.Source.MediaType,
		&descriptor.Source.ManifestRef, &sourceDocument, &schemaDigest, &schemaVersion, &schemaSnapshot,
	); err != nil {
		return knowl.ExecutionDescriptor{}, knowl.OperationKey{}, err
	}
	kind, err := operationpayload.Kind(kindText)
	if err != nil {
		return knowl.ExecutionDescriptor{}, knowl.OperationKey{}, app.ErrExecutionDescriptorUnavailable
	}
	descriptor.Kind = kind
	descriptor.Source.Scope = key.Scope
	descriptor.Source.Source = key.Source
	descriptor.Source.Version = key.Version
	if sourceDocument != "" {
		if err := json.Unmarshal([]byte(sourceDocument), &descriptor.Source.SourceDocument); err != nil {
			return knowl.ExecutionDescriptor{}, knowl.OperationKey{}, app.ErrExecutionDescriptorUnavailable
		}
	}
	descriptor.Schema = knowl.SchemaDocument{
		Scope: key.Scope, Digest: schemaDigest, Version: schemaVersion,
		Content: append([]byte(nil), schemaSnapshot...),
	}
	if kind == knowl.WorkHierarchy {
		descriptor.Source = knowl.AcceptedSource{}
		if err := operationpayload.DecodeHierarchy(payload, &descriptor); err != nil {
			return knowl.ExecutionDescriptor{}, knowl.OperationKey{}, err
		}
	}
	return descriptor, key, nil
}

func operationFromScanner(scanner rowScanner, scope knowl.ScopeRef) (knowl.Operation, error) {
	var operation knowl.Operation
	var kind, status, failureClass, failureReason string
	if err := scanner.Scan(
		&operation.ID, &kind, &operation.Key.Source.Adapter, &operation.Key.Source.ID,
		&operation.Key.Version.Version, &operation.Key.Version.Digest,
		&status, &operation.Attempt, &operation.WorkAttempt, &operation.RetryAttempt,
		&operation.ManualRetryCount, &failureClass, &failureReason, &operation.ReadyAt, &operation.UpdatedAt,
	); err != nil {
		return knowl.Operation{}, err
	}
	operation.Key.Scope = scope
	operation.Kind = knowl.WorkKind(kind)
	if operation.Kind == knowl.WorkHierarchy {
		operation.Key = knowl.OperationKey{}
	} else {
		operation.Kind = knowl.WorkSourceMaintenance
	}
	operation.Status = knowl.OperationStatus(status)
	if failureClass != "" {
		operation.Failure = &knowl.Failure{Class: failureClass, Reason: failureReason, OperationID: string(operation.ID)}
	}
	return operation, nil
}

func validateStoredExecution(scope knowl.ScopeRef, key knowl.OperationKey, descriptor knowl.ExecutionDescriptor) error {
	if descriptor.Kind == knowl.WorkHierarchy {
		return app.ValidateGenericExecutionDescriptor(scope, descriptor)
	}
	return app.ValidateExecutionDescriptor(key, descriptor)
}

func validateWorkLease(lease knowl.WorkLease) error {
	if strings.TrimSpace(lease.Token) == "" || !lease.ExpiresAt.After(time.Now().UTC()) {
		return app.ErrWorkLeaseConflict
	}
	return nil
}

func boundedWorkLimit(limit int) int {
	if limit <= 0 {
		return 1
	}
	if limit > maxWorkScanLimit {
		return maxWorkScanLimit
	}
	return limit
}
