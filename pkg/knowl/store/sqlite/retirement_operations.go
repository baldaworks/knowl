package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var _ app.RetirementOperationStore = (*Store)(nil)
var _ app.RetirementOperationAssociator = (*Store)(nil)

// AssociateRetirementOperation idempotently binds one shared page unit to an exact owner.
func (store *Store) AssociateRetirementOperation(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, owner knowl.SourceOwner, retirementGeneration string, pageID knowl.PageID) error {
	if err := app.ValidateRetirementOperationAssociation(scope, id, owner, retirementGeneration, pageID); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO knowl_retirement_operation_owners (
			operation_id, scope, source_adapter, source_id, retirement_generation, page_id
		) SELECT operation_id, scope, ?, ?, ?, ?
		FROM knowl_operations
		WHERE operation_id = ? AND scope = ? AND work_kind = ? AND source_digest = ? AND source_version = ?
		ON CONFLICT(operation_id, source_adapter, source_id) DO UPDATE SET
			scope = excluded.scope,
			retirement_generation = excluded.retirement_generation,
			page_id = excluded.page_id
		WHERE knowl_retirement_operation_owners.scope = excluded.scope
		  AND knowl_retirement_operation_owners.retirement_generation = excluded.retirement_generation
		  AND knowl_retirement_operation_owners.page_id = excluded.page_id`,
		owner.Adapter, owner.SourceID, retirementGeneration, pageID, id, scope,
		knowl.WorkSourceRetirementPage, retirementGeneration, pageID)
	if err != nil {
		return fmt.Errorf("associate retirement operation: %w", err)
	}
	var associated int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM knowl_retirement_operation_owners
		WHERE operation_id = ? AND scope = ? AND source_adapter = ? AND source_id = ?
		  AND retirement_generation = ? AND page_id = ?`,
		id, scope, owner.Adapter, owner.SourceID, retirementGeneration, pageID).Scan(&associated); err != nil {
		return fmt.Errorf("inspect retirement operation association: %w", err)
	}
	if associated != 1 {
		return app.ErrSourceLifecycleInvalid
	}
	return nil
}

// RetirementOperations returns one exact-owner/generation operation page.
func (store *Store) RetirementOperations(ctx context.Context, request app.RetirementOperationReadRequest) (app.RetirementOperationPage, error) {
	result := app.RetirementOperationPage{Items: make([]app.RetirementOperationRecord, 0)}
	if err := app.ValidateRetirementOperationReadRequest(request); err != nil {
		return result, err
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT operation_id, page_id
		FROM knowl_retirement_operation_owners
		WHERE scope = ? AND source_adapter = ? AND source_id = ?
		  AND retirement_generation = ? AND operation_id > ?
		ORDER BY operation_id ASC LIMIT ?`,
		request.Scope, request.Owner.Adapter, request.Owner.SourceID,
		request.RetirementGeneration, request.After, request.Limit+1)
	if err != nil {
		return result, fmt.Errorf("list retirement operations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type identity struct {
		id     knowl.OperationID
		pageID knowl.PageID
	}
	identities := make([]identity, 0, request.Limit+1)
	for rows.Next() {
		var item identity
		if err := rows.Scan(&item.id, &item.pageID); err != nil {
			return result, fmt.Errorf("scan retirement operation: %w", err)
		}
		identities = append(identities, item)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("iterate retirement operations: %w", err)
	}
	if len(identities) > request.Limit {
		identities = identities[:request.Limit]
		result.Next = identities[len(identities)-1].id
	}
	for _, identity := range identities {
		operation, err := store.Operation(ctx, request.Scope, identity.id)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, app.RetirementOperationRecord{
			Operation: operation, Owner: request.Owner, RetirementGeneration: request.RetirementGeneration, PageID: identity.pageID,
		})
	}
	return result, nil
}

// RetryRetirementOperation compare-and-sets one failed page unit to received.
func (store *Store) RetryRetirementOperation(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, retirementGeneration string, retriedAt time.Time) (bool, error) {
	if err := app.ValidateRetirementOperationRetry(scope, id, retirementGeneration, retriedAt); err != nil {
		return false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	when := retriedAt.UTC().Format(time.RFC3339Nano)
	result, err := store.db.ExecContext(ctx, `
		UPDATE knowl_operations SET status = ?, plan_digest = '', plan_file_count = NULL,
			commit_generation = '', maintenance_diagnostics = '', failure_class = '', failure_reason = '',
			lease_token = '', lease_expires_at = '', work_lease_token = '', work_lease_expires_at = '',
			work_ready_at = ?, retry_attempt = 0, manual_retry_count = manual_retry_count + 1,
			updated_at = CASE WHEN updated_at > ? THEN updated_at ELSE ? END
		WHERE scope = ? AND operation_id = ? AND work_kind = ? AND source_digest = ? AND status = ?`,
		knowl.StatusReceived, when, when, when, scope, id, knowl.WorkSourceRetirementPage, retirementGeneration, knowl.StatusFailed)
	if err != nil {
		return false, fmt.Errorf("retry retirement operation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect retirement retry: %w", err)
	}
	return changed == 1, nil
}
