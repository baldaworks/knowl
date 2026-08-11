package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// Reserve creates or returns the operation for one source revision.
func (store *Store) Reserve(ctx context.Context, key knowl.OperationKey, meta knowl.OperationMeta) (knowl.Operation, error) {
	if err := validateOperationKey(key); err != nil {
		return knowl.Operation{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.Operation{}, fmt.Errorf("begin operation reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := meta.CreatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	operationID := operationID(key)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO knowl_operations (
			operation_id, scope, source_adapter, source_id, source_version, source_digest,
			schema_digest, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
		ON CONFLICT (scope, source_adapter, source_id, source_version) DO NOTHING`,
		operationID, key.Scope, key.Source.Adapter, key.Source.ID, key.Version.Version,
		key.Version.Digest, meta.SchemaDigest, knowl.StatusReceived, now)
	if err != nil {
		return knowl.Operation{}, fmt.Errorf("reserve operation: %w", err)
	}

	var existingID, existingDigest string
	err = tx.QueryRowContext(ctx, `
		SELECT operation_id, source_digest
		FROM knowl_operations
		WHERE scope = $1 AND source_adapter = $2 AND source_id = $3 AND source_version = $4
		FOR UPDATE`,
		key.Scope, key.Source.Adapter, key.Source.ID, key.Version.Version).
		Scan(&existingID, &existingDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.Operation{}, fmt.Errorf("reserved operation disappeared: %w", ErrNotFound)
	}
	if err != nil {
		return knowl.Operation{}, fmt.Errorf("inspect existing operation: %w", err)
	}
	if existingDigest != key.Version.Digest {
		return knowl.Operation{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return knowl.Operation{}, fmt.Errorf("commit operation reservation: %w", err)
	}
	return store.Operation(ctx, key.Scope, knowl.OperationID(existingID))
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
		schemaDigest, status, failureClass                   string
		attempt                                              int
		updatedAt                                            time.Time
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT operation_id, source_adapter, source_id, source_version, source_digest,
		       schema_digest, status, attempt, failure_class, updated_at
		FROM knowl_operations
		WHERE scope = $1 AND operation_id = $2`,
		scope, id).Scan(
		&operationIDValue, &sourceAdapter, &sourceID, &sourceVersion, &sourceDigest,
		&schemaDigest, &status, &attempt, &failureClass, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.Operation{}, ErrNotFound
	}
	if err != nil {
		return knowl.Operation{}, fmt.Errorf("read operation: %w", err)
	}
	operation := knowl.Operation{
		ID: knowl.OperationID(operationIDValue),
		Key: knowl.OperationKey{
			Scope:   scope,
			Source:  knowl.SourceRef{Adapter: sourceAdapter, ID: sourceID},
			Version: knowl.SourceVersion{Version: sourceVersion, Digest: sourceDigest},
		},
		Status:    knowl.OperationStatus(status),
		Attempt:   attempt,
		UpdatedAt: updatedAt,
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
