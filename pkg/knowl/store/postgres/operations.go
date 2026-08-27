package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/operationpayload"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// Reserve creates or returns the operation for one source revision.
func (store *Store) Reserve(ctx context.Context, key knowl.OperationKey, meta knowl.OperationMeta) (app.OperationReservation, error) {
	if err := validateOperationKey(key); err != nil {
		return app.OperationReservation{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operationIDValue := knowl.OperationID(operationID(key))
	var descriptor knowl.ExecutionDescriptor
	hasDescriptor := meta.AcceptedSource != (knowl.AcceptedSource{}) ||
		meta.Schema.Scope != "" || meta.Schema.Digest != "" || meta.Schema.Version != "" || len(meta.Schema.Content) != 0
	if hasDescriptor {
		var descriptorErr error
		descriptor, descriptorErr = app.ExecutionDescriptorFromMeta(operationIDValue, key, meta)
		if descriptorErr != nil {
			return app.OperationReservation{}, descriptorErr
		}
	}
	encodedSourceDocument, err := encodeAcceptedSourceDocument(descriptor.Source.SourceDocument)
	if err != nil {
		return app.OperationReservation{}, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return app.OperationReservation{}, fmt.Errorf("begin operation reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := meta.CreatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO knowl_operations (
			operation_id, scope, source_adapter, source_id, source_version, source_digest,
			schema_digest, status, created_at, updated_at, accepted_media_type,
			source_manifest_ref, accepted_source_document, schema_version, schema_snapshot, work_ready_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, $10, $11, $12, $13, $14, $9)
		ON CONFLICT (scope, source_adapter, source_id, source_version) DO NOTHING`,
		operationIDValue, key.Scope, key.Source.Adapter, key.Source.ID, key.Version.Version,
		key.Version.Digest, meta.SchemaDigest, knowl.StatusReceived, now,
		descriptor.Source.MediaType, descriptor.Source.ManifestRef, encodedSourceDocument, descriptor.Schema.Version,
		nullBytes(descriptor.Schema.Content))
	if err != nil {
		return app.OperationReservation{}, fmt.Errorf("reserve operation: %w", err)
	}
	created, err := result.RowsAffected()
	if err != nil {
		return app.OperationReservation{}, fmt.Errorf("inspect operation reservation: %w", err)
	}

	var existingID, existingDigest, existingSourceDocument string
	err = tx.QueryRowContext(ctx, `
		SELECT operation_id, source_digest, accepted_source_document
		FROM knowl_operations
		WHERE scope = $1 AND source_adapter = $2 AND source_id = $3 AND source_version = $4
		FOR UPDATE`,
		key.Scope, key.Source.Adapter, key.Source.ID, key.Version.Version).
		Scan(&existingID, &existingDigest, &existingSourceDocument)
	if errors.Is(err, sql.ErrNoRows) {
		return app.OperationReservation{}, fmt.Errorf("reserved operation disappeared: %w", ErrNotFound)
	}
	if err != nil {
		return app.OperationReservation{}, fmt.Errorf("inspect existing operation: %w", err)
	}
	if existingDigest != key.Version.Digest {
		return app.OperationReservation{}, ErrConflict
	}
	if encodedSourceDocument != "" && existingSourceDocument == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE knowl_operations SET accepted_source_document = $1 WHERE operation_id = $2 AND accepted_source_document = ''`, encodedSourceDocument, existingID); err != nil {
			return app.OperationReservation{}, fmt.Errorf("enrich operation source document: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return app.OperationReservation{}, fmt.Errorf("commit operation reservation: %w", err)
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
	return app.OperationReservation{Operation: operation, Descriptor: descriptor, New: created == 1}, nil
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
	now := time.Now().UTC()
	result, err := store.db.ExecContext(ctx, `
		INSERT INTO knowl_operations (
			operation_id, scope, source_adapter, source_id, source_version, source_digest,
			schema_digest, status, created_at, updated_at, accepted_media_type,
			source_manifest_ref, accepted_source_document, schema_version, schema_snapshot, work_ready_at,
			work_kind, execution_payload
		) VALUES ($1, $2, '', '', $1, '', $3, $4, $5, $5, '', '', '', $6, $7, $5, $8, $9)
		ON CONFLICT (operation_id) DO NOTHING`,
		id, identity.Scope, descriptor.Schema.Digest, knowl.StatusReceived, now,
		descriptor.Schema.Version, descriptor.Schema.Content, identity.Kind, payload)
	if err != nil {
		return app.OperationReservation{}, fmt.Errorf("reserve generic operation: %w", err)
	}
	created, err := result.RowsAffected()
	if err != nil {
		return app.OperationReservation{}, fmt.Errorf("inspect generic reservation: %w", err)
	}
	var existingScope, existingKind, existingPayload string
	if err := store.db.QueryRowContext(ctx, `SELECT scope, work_kind, execution_payload FROM knowl_operations WHERE operation_id = $1`, id).
		Scan(&existingScope, &existingKind, &existingPayload); err != nil {
		return app.OperationReservation{}, fmt.Errorf("inspect generic operation: %w", err)
	}
	if existingScope != string(identity.Scope) || existingKind != string(identity.Kind) || existingPayload != payload {
		return app.OperationReservation{}, ErrConflict
	}
	operation, readErr := store.Operation(ctx, identity.Scope, id)
	if readErr != nil {
		return app.OperationReservation{}, readErr
	}
	stored, readErr := store.Execution(ctx, identity.Scope, id)
	return app.OperationReservation{Operation: operation, Descriptor: stored, New: created == 1}, readErr
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
		return updateOperationTx(ctx, tx, id,
			"status = $1, plan_digest = $2, updated_at = $3",
			knowl.StatusPlanned, summary.Digest, nowTime())
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
		return updateOperationTx(ctx, tx, id,
			"status = $1, updated_at = $2", knowl.StatusAwaitingReview, nowTime())
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
			if current.leaseExpiresAt.Valid && current.leaseExpiresAt.Time.After(now) {
				return ErrLeaseConflict
			}
		} else if current.status != knowl.StatusPlanned && current.status != knowl.StatusAwaitingReview {
			return invalidTransition(current.status, knowl.StatusApplying)
		}
		return updateOperationTx(ctx, tx, id,
			"status = $1, attempt = attempt + 1, lease_token = $2, lease_expires_at = $3, updated_at = $4",
			knowl.StatusApplying, lease.Token, lease.ExpiresAt.UTC(), now)
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
		return updateOperationTx(ctx, tx, id,
			"status = $1, commit_generation = $2, lease_token = '', lease_expires_at = NULL, updated_at = $3",
			knowl.StatusCommitted, commit.Generation, nowTime())
	})
}

// Fail records a stable redacted failure class.
func (store *Store) Fail(ctx context.Context, id knowl.OperationID, failure knowl.Failure) error {
	if strings.TrimSpace(failure.Class) == "" {
		return fmt.Errorf("failure class is required: %w", ErrConflict)
	}
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		if current.status == knowl.StatusFailed {
			if current.failureClass == failure.Class {
				return nil
			}
			return fmt.Errorf("failure class differs: %w", ErrConflict)
		}
		if current.status == knowl.StatusCommitted {
			return invalidTransition(current.status, knowl.StatusFailed)
		}
		return updateOperationTx(ctx, tx, id,
			"status = $1, failure_class = $2, lease_token = '', lease_expires_at = NULL, updated_at = $3",
			knowl.StatusFailed, failure.Class, nowTime())
	})
}

// Operation reads one operation within its scope.
func (store *Store) Operation(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.Operation, error) {
	var (
		operationIDValue                                     string
		sourceAdapter, sourceID, sourceVersion, sourceDigest string
		kind, schemaDigest, status, failureClass             string
		attempt                                              int
		updatedAt                                            time.Time
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT operation_id, work_kind, source_adapter, source_id, source_version, source_digest,
		       schema_digest, status, attempt, failure_class, updated_at
		FROM knowl_operations
		WHERE scope = $1 AND operation_id = $2`,
		scope, id).Scan(
		&operationIDValue, &kind, &sourceAdapter, &sourceID, &sourceVersion, &sourceDigest,
		&schemaDigest, &status, &attempt, &failureClass, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.Operation{}, ErrNotFound
	}
	if err != nil {
		return knowl.Operation{}, fmt.Errorf("read operation: %w", err)
	}
	operation := knowl.Operation{
		ID:   knowl.OperationID(operationIDValue),
		Kind: knowl.WorkKind(kind),
		Key: knowl.OperationKey{
			Scope:   scope,
			Source:  knowl.SourceRef{Adapter: sourceAdapter, ID: sourceID},
			Version: knowl.SourceVersion{Version: sourceVersion, Digest: sourceDigest},
		},
		Status:    knowl.OperationStatus(status),
		Attempt:   attempt,
		UpdatedAt: updatedAt,
	}
	if operation.Kind == knowl.WorkHierarchy {
		operation.Key = knowl.OperationKey{}
	} else {
		operation.Kind = knowl.WorkSourceMaintenance
	}
	if failureClass != "" {
		operation.Failure = &knowl.Failure{Class: failureClass, OperationID: operationIDValue}
	}
	_ = schemaDigest
	return operation, nil
}

type operationRow struct {
	status           knowl.OperationStatus
	planDigest       string
	commitGeneration string
	failureClass     string
	leaseExpiresAt   sql.NullTime
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
	err = tx.QueryRowContext(ctx, `
		SELECT status, plan_digest, commit_generation, failure_class, lease_expires_at
		FROM knowl_operations
		WHERE operation_id = $1
		FOR UPDATE`, id).
		Scan(&status, &current.planDigest, &current.commitGeneration, &current.failureClass, &current.leaseExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
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
	idPlaceholder := strconv.Itoa(len(args) + 1)
	query := "UPDATE knowl_operations SET " + assignments + " WHERE operation_id = $" + idPlaceholder
	args = append(args, id)
	result, err := tx.ExecContext(ctx, query, args...)
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

func nowTime() time.Time { return time.Now().UTC() }

func validateOperationKey(key knowl.OperationKey) error {
	if strings.TrimSpace(string(key.Scope)) == "" ||
		strings.TrimSpace(key.Source.Adapter) == "" ||
		strings.TrimSpace(key.Source.ID) == "" ||
		strings.TrimSpace(key.Version.Version) == "" ||
		strings.TrimSpace(key.Version.Digest) == "" {
		return fmt.Errorf("operation key is incomplete: %w", ErrConflict)
	}
	return nil
}

func operationID(key knowl.OperationKey) string {
	digest := key.Version.Digest
	return fmt.Sprintf("%s:%s:%s@%s#%s",
		key.Scope, key.Source.Adapter, key.Source.ID, key.Version.Version,
		digest[:minInt(len(digest), 16)])
}
