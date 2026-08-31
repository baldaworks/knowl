package sqlite

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

// Reserve creates or returns the operation for one source revision.
func (store *Store) Reserve(ctx context.Context, key knowl.OperationKey, meta knowl.OperationMeta) (app.OperationReservation, error) {
	if strings.TrimSpace(string(key.Scope)) == "" || strings.TrimSpace(key.Source.Adapter) == "" || strings.TrimSpace(key.Source.ID) == "" || strings.TrimSpace(key.Version.Version) == "" || strings.TrimSpace(key.Version.Digest) == "" {
		return app.OperationReservation{}, fmt.Errorf("operation key is incomplete: %w", ErrConflict)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operationID := knowl.OperationID(operationID(key))
	var descriptor knowl.ExecutionDescriptor
	hasDescriptor := meta.AcceptedSource != (knowl.AcceptedSource{}) ||
		meta.Schema.Scope != "" || meta.Schema.Digest != "" || meta.Schema.Version != "" || len(meta.Schema.Content) != 0
	if hasDescriptor {
		var err error
		descriptor, err = app.ExecutionDescriptorFromMeta(operationID, key, meta)
		if err != nil {
			return app.OperationReservation{}, err
		}
	}
	encodedSourceDocument, err := encodeAcceptedSourceDocument(descriptor.Source.SourceDocument)
	if err != nil {
		return app.OperationReservation{}, err
	}
	var existingID, existingDigest, existingSourceDocument string
	err = store.db.QueryRowContext(ctx, `
		SELECT operation_id, source_digest, accepted_source_document
		FROM knowl_operations
		WHERE scope = ? AND source_adapter = ? AND source_id = ? AND source_version = ?`,
		key.Scope, key.Source.Adapter, key.Source.ID, key.Version.Version).Scan(&existingID, &existingDigest, &existingSourceDocument)
	if err == nil {
		if existingDigest != key.Version.Digest {
			return app.OperationReservation{}, ErrConflict
		}
		if encodedSourceDocument != "" && existingSourceDocument == "" {
			if _, updateErr := store.db.ExecContext(ctx, `UPDATE knowl_operations SET accepted_source_document = ? WHERE operation_id = ? AND accepted_source_document = ''`, encodedSourceDocument, existingID); updateErr != nil {
				return app.OperationReservation{}, fmt.Errorf("enrich operation source document: %w", updateErr)
			}
		}
		operation, readErr := store.Operation(ctx, key.Scope, knowl.OperationID(existingID))
		if readErr != nil {
			return app.OperationReservation{}, readErr
		}
		if hasDescriptor {
			descriptor, readErr = store.Execution(ctx, key.Scope, operation.ID)
			if readErr != nil {
				return app.OperationReservation{}, readErr
			}
		}
		return app.OperationReservation{Operation: operation, Descriptor: descriptor}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return app.OperationReservation{}, fmt.Errorf("inspect existing operation: %w", err)
	}
	now := meta.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO knowl_operations (
			operation_id, scope, source_adapter, source_id, source_version, source_digest,
			schema_digest, status, created_at, updated_at, accepted_media_type,
			source_manifest_ref, accepted_source_document, schema_version, schema_snapshot, work_ready_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		operationID, key.Scope, key.Source.Adapter, key.Source.ID, key.Version.Version, key.Version.Digest,
		meta.SchemaDigest, knowl.StatusReceived, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		descriptor.Source.MediaType, descriptor.Source.ManifestRef, encodedSourceDocument, descriptor.Schema.Version,
		nullBytes(descriptor.Schema.Content), now.Format(time.RFC3339Nano))
	if err != nil {
		return app.OperationReservation{}, fmt.Errorf("reserve operation: %w", err)
	}
	operation, readErr := store.Operation(ctx, key.Scope, operationID)
	return app.OperationReservation{Operation: operation, Descriptor: descriptor, New: true}, readErr
}

// ReserveOperation creates or replays one generic hierarchy operation.
func (store *Store) ReserveOperation(ctx context.Context, identity knowl.OperationIdentity, descriptor knowl.ExecutionDescriptor) (app.OperationReservation, error) {
	id, err := app.OperationIDForIdentity(identity)
	if err != nil || descriptor.OperationID != id || app.ValidateOperationDescriptor(identity, descriptor) != nil {
		return app.OperationReservation{}, app.ErrExecutionDescriptorUnavailable
	}
	payload, err := operationpayload.EncodeHierarchy(descriptor)
	if err != nil {
		return app.OperationReservation{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	var existingScope, existingKind, existingPayload string
	err = store.db.QueryRowContext(ctx, `SELECT scope, work_kind, execution_payload FROM knowl_operations WHERE operation_id = ?`, id).
		Scan(&existingScope, &existingKind, &existingPayload)
	if err == nil {
		if existingScope != string(identity.Scope) || existingKind != string(identity.Kind) || existingPayload != payload {
			return app.OperationReservation{}, ErrConflict
		}
		operation, readErr := store.Operation(ctx, identity.Scope, id)
		if readErr != nil {
			return app.OperationReservation{}, readErr
		}
		stored, readErr := store.Execution(ctx, identity.Scope, id)
		return app.OperationReservation{Operation: operation, Descriptor: stored}, readErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return app.OperationReservation{}, fmt.Errorf("inspect generic operation: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO knowl_operations (
			operation_id, scope, source_adapter, source_id, source_version, source_digest,
			schema_digest, status, created_at, updated_at, accepted_media_type,
			source_manifest_ref, accepted_source_document, schema_version, schema_snapshot, work_ready_at,
			work_kind, execution_payload
		) VALUES (?, ?, '', '', ?, '', ?, ?, ?, ?, '', '', '', ?, ?, ?, ?, ?)`,
		id, identity.Scope, id, descriptor.Schema.Digest, knowl.StatusReceived, now, now,
		descriptor.Schema.Version, descriptor.Schema.Content, now, identity.Kind, payload)
	if err != nil {
		return app.OperationReservation{}, fmt.Errorf("reserve generic operation: %w", err)
	}
	operation, readErr := store.Operation(ctx, identity.Scope, id)
	return app.OperationReservation{Operation: operation, Descriptor: descriptor, New: true}, readErr
}

func encodeAcceptedSourceDocument(document knowl.SourceDocument) (string, error) {
	if document == (knowl.SourceDocument{}) {
		return "", nil
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("encode accepted source document: %w", err)
	}
	return string(encoded), nil
}

func nullBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

// SavePlan persists a redacted plan digest and advances the operation.
func (store *Store) SavePlan(ctx context.Context, id knowl.OperationID, summary knowl.PlanSummary) error {
	if strings.TrimSpace(summary.Digest) == "" {
		return fmt.Errorf("plan digest is required: %w", ErrConflict)
	}
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		if current.status == knowl.StatusPlanned {
			if current.planDigest == summary.Digest {
				return nil
			}
			return fmt.Errorf("plan digest differs: %w", ErrConflict)
		}
		if current.status != knowl.StatusReceived {
			return invalidTransition(current.status, knowl.StatusPlanned)
		}
		return updateOperationTx(ctx, tx, id, `status = ?, plan_digest = ?, updated_at = ?`, knowl.StatusPlanned, summary.Digest, nowString())
	})
}

// MarkAwaitingReview marks an operation that needs explicit apply.
func (store *Store) MarkAwaitingReview(ctx context.Context, id knowl.OperationID) error {
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		if current.status == knowl.StatusAwaitingReview {
			return nil
		}
		if current.status != knowl.StatusPlanned {
			return invalidTransition(current.status, knowl.StatusAwaitingReview)
		}
		return updateOperationTx(ctx, tx, id, `status = ?, updated_at = ?`, knowl.StatusAwaitingReview, nowString())
	})
}

// MarkApplying claims an operation with a lease.
func (store *Store) MarkApplying(ctx context.Context, id knowl.OperationID, lease knowl.Lease) error {
	if strings.TrimSpace(lease.Token) == "" || lease.ExpiresAt.IsZero() {
		return fmt.Errorf("lease token and expiry are required: %w", ErrConflict)
	}
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		now := time.Now().UTC()
		if current.status == knowl.StatusApplying {
			expiresAt, err := parseOptionalTime(current.leaseExpiresAt)
			if err != nil {
				return err
			}
			if expiresAt.After(now) {
				return ErrLeaseConflict
			}
		} else if current.status != knowl.StatusPlanned && current.status != knowl.StatusAwaitingReview {
			return invalidTransition(current.status, knowl.StatusApplying)
		}
		return updateOperationTx(ctx, tx, id, `status = ?, attempt = attempt + 1, lease_token = ?, lease_expires_at = ?, updated_at = ?`, knowl.StatusApplying, lease.Token, lease.ExpiresAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	})
}

// CommitOutcome marks a canonical content commit as complete.
func (store *Store) CommitOutcome(ctx context.Context, id knowl.OperationID, commit knowl.ContentCommit) error {
	if strings.TrimSpace(commit.Generation) == "" {
		return fmt.Errorf("commit generation is required: %w", ErrConflict)
	}
	if commit.OperationID != "" && knowl.OperationID(commit.OperationID) != id {
		return fmt.Errorf("commit belongs to %q, want %q: %w", commit.OperationID, id, ErrConflict)
	}
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		if current.status == knowl.StatusCommitted {
			if current.commitGeneration == commit.Generation {
				return nil
			}
			return fmt.Errorf("commit generation differs: %w", ErrConflict)
		}
		if current.status != knowl.StatusApplying {
			return invalidTransition(current.status, knowl.StatusCommitted)
		}
		return updateOperationTx(ctx, tx, id, `status = ?, commit_generation = ?, failure_class = '', failure_reason = '',
			lease_token = '', lease_expires_at = '', work_lease_token = '', work_lease_expires_at = '', updated_at = ?`,
			knowl.StatusCommitted, commit.Generation, nowString())
	})
}

// Fail records stable redacted failure metadata.
func (store *Store) Fail(ctx context.Context, id knowl.OperationID, failure knowl.Failure) error {
	if !app.ValidateSafeFailure(failure, false) {
		return fmt.Errorf("safe failure metadata is required: %w", ErrConflict)
	}
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		if current.status == knowl.StatusFailed {
			if current.failureClass == failure.Class && current.failureReason == failure.Reason {
				return nil
			}
			return fmt.Errorf("failure class differs: %w", ErrConflict)
		}
		if current.status == knowl.StatusCommitted {
			return invalidTransition(current.status, knowl.StatusFailed)
		}
		return updateOperationTx(ctx, tx, id, `status = ?, failure_class = ?, failure_reason = ?,
			lease_token = '', lease_expires_at = '', work_lease_token = '', work_lease_expires_at = '', updated_at = ?`,
			knowl.StatusFailed, failure.Class, failure.Reason, nowString())
	})
}

// Operation reads one operation within its scope.
func (store *Store) Operation(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.Operation, error) {
	var operation knowl.Operation
	var sourceAdapter, sourceID, sourceVersion, sourceDigest, schemaDigest string
	var kind, status, failureClass, failureReason, readyAt, updatedAt string
	err := store.db.QueryRowContext(ctx, `
		SELECT operation_id, work_kind, source_adapter, source_id, source_version, source_digest,
		       schema_digest, status, attempt, work_attempt, retry_attempt, manual_retry_count,
		       failure_class, failure_reason, work_ready_at, updated_at
		FROM knowl_operations WHERE scope = ? AND operation_id = ?`, scope, id).
		Scan(&operation.ID, &kind, &sourceAdapter, &sourceID, &sourceVersion, &sourceDigest,
			&schemaDigest, &status, &operation.Attempt, &operation.WorkAttempt, &operation.RetryAttempt,
			&operation.ManualRetryCount, &failureClass, &failureReason, &readyAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.Operation{}, ErrNotFound
	}
	if err != nil {
		return knowl.Operation{}, fmt.Errorf("read operation: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return knowl.Operation{}, fmt.Errorf("parse operation time: %w", err)
	}
	operation.Kind = knowl.WorkKind(kind)
	if operation.Kind == "" || operation.Kind == knowl.WorkSourceMaintenance {
		operation.Kind = knowl.WorkSourceMaintenance
		operation.Key = knowl.OperationKey{Scope: scope, Source: knowl.SourceRef{Adapter: sourceAdapter, ID: sourceID}, Version: knowl.SourceVersion{Version: sourceVersion, Digest: sourceDigest}}
	}
	operation.Status = knowl.OperationStatus(status)
	operation.UpdatedAt = parsed
	operation.ReadyAt, err = parseOptionalTime(readyAt)
	if err != nil {
		return knowl.Operation{}, fmt.Errorf("parse operation readiness: %w", err)
	}
	if failureClass != "" {
		operation.Failure = &knowl.Failure{Class: failureClass, Reason: failureReason, OperationID: string(operation.ID)}
	}
	_ = schemaDigest
	return operation, nil
}

type operationRow struct {
	status           knowl.OperationStatus
	planDigest       string
	commitGeneration string
	failureClass     string
	failureReason    string
	leaseExpiresAt   string
}

func (store *Store) transition(ctx context.Context, id knowl.OperationID, update func(*sql.Tx, operationRow) error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin operation transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var current operationRow
	var status string
	if err := tx.QueryRowContext(ctx, `
		SELECT status, plan_digest, commit_generation, failure_class, failure_reason, lease_expires_at
		FROM knowl_operations WHERE operation_id = ?`, id).
		Scan(&status, &current.planDigest, &current.commitGeneration, &current.failureClass, &current.failureReason, &current.leaseExpiresAt); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("read operation transition: %w", err)
	}
	current.status = knowl.OperationStatus(status)
	if err := update(tx, current); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit operation transition: %w", err)
	}
	return nil
}

func updateOperationTx(ctx context.Context, tx *sql.Tx, id knowl.OperationID, assignments string, args ...any) error {
	result, err := tx.ExecContext(ctx, `UPDATE knowl_operations SET `+assignments+` WHERE operation_id = ?`, append(args, id)...)
	if err != nil {
		return fmt.Errorf("update operation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect operation update: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func invalidTransition(from, to knowl.OperationStatus) error {
	return fmt.Errorf("cannot transition operation from %q to %q: %w", from, to, ErrInvalidState)
}

func parseOptionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse lease expiry: %w", err)
	}
	return parsed, nil
}

func nowString() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func operationID(key knowl.OperationKey) string {
	return fmt.Sprintf("%s:%s:%s@%s#%s", key.Scope, key.Source.Adapter, key.Source.ID, key.Version.Version, key.Version.Digest[:minInt(len(key.Version.Digest), 16)])
}
