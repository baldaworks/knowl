package postgres

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
	result, err := store.db.ExecContext(ctx, `
		INSERT INTO knowl_retirement_operation_owners (
			operation_id, scope, source_adapter, source_id, retirement_generation, page_id
		) SELECT operation_id, scope, $1, $2, $3, $4
		FROM knowl_operations
		WHERE operation_id = $5 AND scope = $6 AND work_kind = $7 AND source_digest = $3 AND source_version = $4
		ON CONFLICT(operation_id, source_adapter, source_id) DO UPDATE SET
			scope = excluded.scope,
			retirement_generation = excluded.retirement_generation,
			page_id = excluded.page_id
		WHERE knowl_retirement_operation_owners.scope = excluded.scope
		  AND knowl_retirement_operation_owners.retirement_generation = excluded.retirement_generation
		  AND knowl_retirement_operation_owners.page_id = excluded.page_id`,
		owner.Adapter, owner.SourceID, retirementGeneration, pageID, id, scope, knowl.WorkSourceRetirementPage)
	if err != nil {
		return fmt.Errorf("associate retirement operation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect retirement operation association: %w", err)
	}
	if changed != 1 {
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
		WHERE scope = $1 AND source_adapter = $2 AND source_id = $3
		  AND retirement_generation = $4 AND operation_id > $5
		ORDER BY operation_id ASC LIMIT $6`,
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
	when := retriedAt.UTC()
	result, err := store.db.ExecContext(ctx, `
		UPDATE knowl_operations SET status = $1, plan_digest = '', plan_file_count = NULL,
			commit_generation = '', maintenance_diagnostics = '', failure_class = '', failure_reason = '',
			lease_token = '', lease_expires_at = NULL, work_lease_token = '', work_lease_expires_at = NULL,
			work_ready_at = $2, retry_attempt = 0, manual_retry_count = manual_retry_count + 1,
			updated_at = GREATEST(updated_at, $2)
		WHERE scope = $3 AND operation_id = $4 AND work_kind = $5 AND source_digest = $6 AND status = $7`,
		knowl.StatusReceived, when, scope, id, knowl.WorkSourceRetirementPage, retirementGeneration, knowl.StatusFailed)
	if err != nil {
		return false, fmt.Errorf("retry retirement operation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect retirement retry: %w", err)
	}
	return changed == 1, nil
}
