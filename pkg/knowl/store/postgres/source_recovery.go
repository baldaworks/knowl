package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var _ app.SourceLifecycleRecoveryStore = (*Store)(nil)

// OrphanSourceOperations reads one finite raw operation-ID window. Invalid
// descriptors retain their cursor, allowing the coordinator to report a blocker
// and continue observation instead of repeatedly stopping at the first row.
func (store *Store) OrphanSourceOperations(ctx context.Context, request app.OrphanSourceOperationReadRequest) (app.OrphanSourceOperationPage, error) {
	if err := app.ValidateOrphanSourceOperationRead(request); err != nil {
		return app.OrphanSourceOperationPage{}, err
	}
	rows, err := sourceQuery(ctx, store.db, `SELECT operation_id, source_adapter, configured_source_id
 FROM knowl_operations AS operation
 WHERE scope = ? AND work_kind = ? AND source_adapter IN ('wiki-filesystem', 'git')
 AND configured_source_id <> '' AND operation_id COLLATE "C" > ?
 AND NOT EXISTS (SELECT 1 FROM knowl_source_lifecycle AS lifecycle WHERE lifecycle.scope = operation.scope
  AND lifecycle.adapter = operation.source_adapter AND lifecycle.source_id = operation.configured_source_id)
 ORDER BY operation_id COLLATE "C" LIMIT ?`, request.Scope, knowl.WorkSourceMaintenance, request.After, request.Limit)
	if err != nil {
		return app.OrphanSourceOperationPage{}, fmt.Errorf("read orphan source operations: %w", err)
	}
	page := app.OrphanSourceOperationPage{Items: make([]app.OrphanSourceOperation, 0, request.Limit)}
	for rows.Next() {
		var item app.OrphanSourceOperation
		if err := rows.Scan(&item.OperationID, &item.Owner.Adapter, &item.Owner.SourceID); err != nil {
			_ = rows.Close()
			return app.OrphanSourceOperationPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return app.OrphanSourceOperationPage{}, err
	}
	if err := rows.Close(); err != nil {
		return app.OrphanSourceOperationPage{}, err
	}
	if len(page.Items) == request.Limit {
		page.Next = page.Items[len(page.Items)-1].OperationID
	}
	for i := range page.Items {
		item := &page.Items[i]
		descriptor, err := store.Execution(ctx, request.Scope, item.OperationID)
		if err != nil {
			if errors.Is(err, app.ErrExecutionDescriptorUnavailable) || errors.Is(err, ErrNotFound) {
				item.Invalid = true
				continue
			}
			return app.OrphanSourceOperationPage{}, err
		}
		if descriptor.Kind != knowl.WorkSourceMaintenance || app.ValidateAcceptedSourceDescriptor(descriptor.Source) != nil ||
			knowl.SourceAdapterID(descriptor.Source.Source.Adapter) != item.Owner.Adapter || descriptor.Source.SourceDocument.SourceID != item.Owner.SourceID {
			item.Invalid = true
			continue
		}
		item.Source = descriptor.Source
	}
	return page, nil
}

// RecoverSourceLifecycle never activates an owner. Its request contains the
// coordinator's canonical/raw proof; this transaction independently verifies
// the persisted operation and the current authority while excluding activation.
func (store *Store) RecoverSourceLifecycle(ctx context.Context, request app.SourceLifecycleRecoveryRequest) (knowl.SourceLifecycle, error) {
	payload, proof, err := app.EncodeSourceLifecycleRecoveryProof(request)
	if err != nil {
		return knowl.SourceLifecycle{}, err
	}
	if err := store.mu.LockContext(ctx); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.SourceLifecycle{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// Lock the current authority row before reading the owner preimage.
	// Inventory reconciliation takes the same row lock.
	result, err := sourceExec(ctx, tx, `UPDATE knowl_source_lifecycle_authority SET epoch=epoch WHERE scope=? AND generation=? AND epoch=?`, request.Scope, request.AuthorityGeneration, request.AuthorityEpoch)
	if err != nil {
		return knowl.SourceLifecycle{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	authority, err := scanPostgresSourceLifecycleAuthority(sourceQueryRow(ctx, tx, `SELECT scope,generation,epoch,updated_at FROM knowl_source_lifecycle_authority WHERE scope=?`, request.Scope))
	if err != nil {
		return knowl.SourceLifecycle{}, err
	}
	if authority.Generation != request.AuthorityGeneration || authority.Epoch != request.AuthorityEpoch || request.RecoveredAt.Before(authority.UpdatedAt) {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	current, err := scanPostgresSourceLifecycle(sourceQueryRow(ctx, tx, `SELECT `+sourceLifecycleColumns+` FROM knowl_source_lifecycle WHERE scope=? AND adapter=? AND source_id=?`, request.Scope, request.Owner.Adapter, request.Owner.SourceID))
	if err == nil {
		if current.State == knowl.SourceLifecycleRetiring && current.RecoveryProofDigest == proof {
			return current, nil
		}
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return knowl.SourceLifecycle{}, err
	}
	var configuredID knowl.SourceID
	if err := sourceQueryRow(ctx, tx, `SELECT configured_source_id FROM knowl_operations WHERE scope=? AND operation_id=?`, request.Scope, request.OperationID).Scan(&configuredID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
		}
		return knowl.SourceLifecycle{}, err
	}
	descriptor, key, err := scanExecution(sourceQueryRow(ctx, tx, `SELECT operation_id, scope, work_kind, execution_payload, source_adapter, source_id, source_version, source_digest, maintenance_generation,
 accepted_media_type, source_manifest_ref, accepted_source_document, schema_digest, schema_version, schema_snapshot
 FROM knowl_operations WHERE scope=? AND operation_id=?`, request.Scope, request.OperationID))
	if err != nil {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	if descriptor.Kind != knowl.WorkSourceMaintenance || validateStoredExecution(request.Scope, key, descriptor) != nil || descriptor.Source != request.Source || configuredID != request.Owner.SourceID {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	// Historical rollout membership is unknowable. Conservatively wait for all
	// current accepted-sync readiness before recovery rather than inventing a
	// replacement relation or waiting for maintenance operations to commit.
	rows, err := sourceQuery(ctx, tx, `SELECT `+sourceLifecycleColumns+` FROM knowl_source_lifecycle WHERE scope=? AND state='active' LIMIT 129`, request.Scope)
	if err != nil {
		return knowl.SourceLifecycle{}, err
	}
	desired := make([]app.DesiredSourceOwner, 0)
	unready := false
	for rows.Next() {
		active, scanErr := scanPostgresSourceLifecycle(rows)
		if scanErr != nil {
			_ = rows.Close()
			return knowl.SourceLifecycle{}, scanErr
		}
		if len(desired) >= 128 || active.AuthorityGeneration != authority.Generation || active.AuthorityEpoch != authority.Epoch {
			_ = rows.Close()
			return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
		}
		desired = append(desired, app.DesiredSourceOwner{Owner: active.Owner, ConfigDigest: active.ConfigDigest})
		unready = unready || !active.Ready
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return knowl.SourceLifecycle{}, err
	}
	if err := rows.Close(); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	// The persisted active inventory must reproduce the authority fence. In
	// particular, recovery cannot fill a missing current desired owner as retiring.
	if app.SourceLifecycleGeneration(desired) != authority.Generation {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	if unready {
		return knowl.SourceLifecycle{}, app.ErrSourceReplacementNotReady
	}
	at := request.RecoveredAt.UTC()
	state := knowl.SourceLifecycle{Scope: request.Scope, Owner: request.Owner, State: knowl.SourceLifecycleRetiring, RecoveryProofDigest: proof,
		AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch, ActivatedGeneration: authority.Generation, ActivatedEpoch: authority.Epoch,
		RetirementGeneration: proof, RetirementEpoch: authority.Epoch, ActivatedAt: at, RetiringAt: at, UpdatedAt: at}
	if err := app.ValidateSourceLifecycle(state); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	_, err = sourceExec(ctx, tx, `INSERT INTO knowl_source_lifecycle (scope,adapter,source_id,state,config_digest,recovery_proof_digest,recovery_proof_payload,authority_generation,authority_epoch,
 activated_generation,activated_epoch,retirement_generation,retirement_epoch,activated_at,retiring_at,updated_at)
 VALUES (?,?,?,'retiring','',?,?,?,?,?,?,?,?,?,?,?)`, state.Scope, state.Owner.Adapter, state.Owner.SourceID, proof, payload, state.AuthorityGeneration, state.AuthorityEpoch,
		state.ActivatedGeneration, state.ActivatedEpoch, state.RetirementGeneration, state.RetirementEpoch, formatTime(at), formatTime(at), formatTime(at))
	if err != nil {
		return knowl.SourceLifecycle{}, fmt.Errorf("insert recovered retiring source: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	return state, nil
}
