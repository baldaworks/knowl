package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const sourceLifecycleColumns = `scope, adapter, source_id, state, config_digest,
authority_generation, authority_epoch, activated_generation, activated_epoch,
retirement_generation, retirement_epoch, scan_cursor,
scan_round_matches, scan_complete, ready, replacement_pending, blocker_code, activated_at, retiring_at,
deleted_at, updated_at`

func (store *Store) ReconcileSourceLifecycles(ctx context.Context, request app.SourceLifecycleReconcileRequest) error {
	normalized, err := app.NormalizeSourceLifecycleReconcile(request)
	if err != nil {
		return err
	}
	if err := store.mu.LockContext(ctx); err != nil {
		return err
	}
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin source lifecycle reconcile: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Historical config initializes a typed exact owner at epoch zero. The first
	// desired reconciliation assigns its unrepeatable activation epoch.
	//nolint:dupword
	bootstrapQuery := `INSERT INTO knowl_source_lifecycle (
scope, adapter, source_id, state, config_digest, authority_generation,
activated_generation, activated_at, updated_at)
SELECT scope, CASE source_type WHEN 'filesystem' THEN 'wiki-filesystem' WHEN 'git' THEN 'git' END,
source_id, 'active', config_digest, config_digest, config_digest, created_at, updated_at
FROM knowl_sources WHERE scope = ? AND source_type IN ('filesystem', 'git')
ON CONFLICT(scope, adapter, source_id) DO NOTHING`
	if _, err := sourceExec(ctx, tx, `INSERT INTO knowl_source_lifecycle_authority (scope, generation, epoch, updated_at)
SELECT scope, '', 0, MAX(updated_at) FROM knowl_sources WHERE scope = ? GROUP BY scope
ON CONFLICT(scope) DO NOTHING`, normalized.Scope); err != nil {
		return fmt.Errorf("bootstrap source lifecycle authority: %w", err)
	}
	if _, err := sourceExec(ctx, tx, `INSERT INTO knowl_source_lifecycle_authority (scope, generation, epoch, updated_at)
VALUES (?, '', 0, ?) ON CONFLICT(scope) DO NOTHING`, normalized.Scope, formatTime(normalized.UpdatedAt)); err != nil {
		return fmt.Errorf("initialize source lifecycle authority: %w", err)
	}
	authority, err := scanPostgresSourceLifecycleAuthority(sourceQueryRow(ctx, tx, `SELECT scope, generation, epoch, updated_at FROM knowl_source_lifecycle_authority WHERE scope = ? FOR UPDATE`, normalized.Scope))
	if err != nil {
		return err
	}
	if authority.Epoch == 0 {
		if _, err := sourceExec(ctx, tx, bootstrapQuery, normalized.Scope); err != nil {
			return fmt.Errorf("bootstrap source lifecycles: %w", err)
		}
	}
	rows, err := sourceQuery(ctx, tx, `SELECT `+sourceLifecycleColumns+` FROM knowl_source_lifecycle WHERE scope = ? ORDER BY adapter COLLATE "C", source_id COLLATE "C" FOR UPDATE`, normalized.Scope)
	if err != nil {
		return fmt.Errorf("read source lifecycles: %w", err)
	}
	existing := make([]knowl.SourceLifecycle, 0)
	for rows.Next() {
		state, scanErr := scanPostgresSourceLifecycle(rows)
		if scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		existing = append(existing, state)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate source lifecycles: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close source lifecycles: %w", err)
	}
	desired := make(map[knowl.SourceOwner]app.DesiredSourceOwner, len(normalized.Desired))
	for _, item := range normalized.Desired {
		desired[item.Owner] = item
	}
	if authority.Generation == normalized.AuthorityGeneration {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit repeated source lifecycle reconcile: %w", err)
		}
		return nil
	}
	if authority.Epoch != normalized.ExpectedAuthorityEpoch || authority.Epoch == math.MaxInt64 {
		return app.ErrSourceLifecycleConflict
	}
	for _, current := range existing {
		if normalized.UpdatedAt.Before(current.UpdatedAt) {
			return app.ErrSourceLifecycleConflict
		}
	}
	if !authority.UpdatedAt.IsZero() && normalized.UpdatedAt.Before(authority.UpdatedAt) {
		return app.ErrSourceLifecycleConflict
	}
	nextEpoch := authority.Epoch + 1
	updatedAt := formatTime(normalized.UpdatedAt)
	existingByOwner := make(map[knowl.SourceOwner]knowl.SourceLifecycle, len(existing))
	for _, current := range existing {
		existingByOwner[current.Owner] = current
	}
	additionCount := 0
	for _, item := range normalized.Desired {
		current, exists := existingByOwner[item.Owner]
		if !exists || current.State != knowl.SourceLifecycleActive {
			additionCount++
		}
	}
	for _, current := range existing {
		item, wanted := desired[current.Owner]
		if wanted {
			delete(desired, current.Owner)
			if current.State == knowl.SourceLifecycleActive && current.ConfigDigest == item.ConfigDigest {
				next := current
				next.AuthorityGeneration, next.AuthorityEpoch, next.UpdatedAt = normalized.AuthorityGeneration, nextEpoch, normalized.UpdatedAt
				if next.ActivatedEpoch == 0 {
					next.ActivatedGeneration, next.ActivatedEpoch = normalized.AuthorityGeneration, nextEpoch
				}
				if app.ValidateSourceLifecycle(next) != nil {
					return app.ErrSourceLifecycleInvalid
				}
				result, execErr := sourceExec(ctx, tx, `UPDATE knowl_source_lifecycle SET authority_generation = ?, authority_epoch = ?, activated_generation = ?, activated_epoch = ?, updated_at = ? WHERE scope = ? AND adapter = ? AND source_id = ? AND state = ? AND authority_epoch = ?`, normalized.AuthorityGeneration, nextEpoch, next.ActivatedGeneration, next.ActivatedEpoch, updatedAt, normalized.Scope, current.Owner.Adapter, current.Owner.SourceID, current.State, current.AuthorityEpoch)
				if execErr != nil {
					return fmt.Errorf("refresh source lifecycle authority: %w", execErr)
				}
				if changed, _ := result.RowsAffected(); changed != 1 {
					return app.ErrSourceLifecycleConflict
				}
				continue
			}
			if current.State == knowl.SourceLifecycleActive {
				if current.Ready {
					if _, execErr := sourceExec(ctx, tx, `UPDATE knowl_source_lifecycle SET
replacement_pending = replacement_pending + 1, updated_at = ?
WHERE scope = ? AND state = 'retiring' AND retirement_epoch = ?`, updatedAt, normalized.Scope, current.ActivatedEpoch); execErr != nil {
						return fmt.Errorf("invalidate replacement readiness: %w", execErr)
					}
				}
				next := current
				next.ConfigDigest = item.ConfigDigest
				next.AuthorityGeneration, next.AuthorityEpoch = normalized.AuthorityGeneration, nextEpoch
				if next.ActivatedEpoch == 0 {
					next.ActivatedGeneration, next.ActivatedEpoch = normalized.AuthorityGeneration, nextEpoch
				}
				next.ScanCursor, next.ScanRoundMatches, next.ScanComplete, next.Ready, next.BlockerCode = "", 0, false, false, ""
				next.UpdatedAt = normalized.UpdatedAt
				if app.ValidateSourceLifecycle(next) != nil {
					return app.ErrSourceLifecycleInvalid
				}
				result, execErr := sourceExec(ctx, tx, `UPDATE knowl_source_lifecycle SET
	config_digest = ?, authority_generation = ?, authority_epoch = ?, activated_generation = ?, activated_epoch = ?,
	scan_cursor = '', scan_round_matches = 0, scan_complete = FALSE, ready = FALSE, blocker_code = '', updated_at = ?
	WHERE scope = ? AND adapter = ? AND source_id = ? AND state = 'active' AND authority_epoch = ?`, item.ConfigDigest,
					normalized.AuthorityGeneration, nextEpoch, next.ActivatedGeneration, next.ActivatedEpoch, updatedAt,
					normalized.Scope, current.Owner.Adapter, current.Owner.SourceID, current.AuthorityEpoch)
				if execErr != nil {
					return fmt.Errorf("refresh active source config: %w", execErr)
				}
				if changed, _ := result.RowsAffected(); changed != 1 {
					return app.ErrSourceLifecycleConflict
				}
				continue
			}
			next := current
			next.State, next.ConfigDigest = knowl.SourceLifecycleActive, item.ConfigDigest
			next.AuthorityGeneration, next.AuthorityEpoch = normalized.AuthorityGeneration, nextEpoch
			next.ActivatedGeneration, next.ActivatedEpoch = normalized.AuthorityGeneration, nextEpoch
			next.ScanCursor, next.ScanRoundMatches, next.ScanComplete, next.Ready, next.ReplacementPending, next.BlockerCode = "", 0, false, false, 0, ""
			next.ActivatedAt, next.RetiringAt, next.DeletedAt, next.UpdatedAt = normalized.UpdatedAt, time.Time{}, time.Time{}, normalized.UpdatedAt
			if app.ValidateSourceLifecycle(next) != nil {
				return app.ErrSourceLifecycleInvalid
			}
			result, execErr := sourceExec(ctx, tx, `UPDATE knowl_source_lifecycle SET
state = 'active', config_digest = ?, authority_generation = ?, authority_epoch = ?, activated_generation = ?, activated_epoch = ?,
scan_cursor = '', scan_round_matches = 0, scan_complete = FALSE, ready = FALSE, replacement_pending = 0, blocker_code = '',
activated_at = ?, retiring_at = NULL, deleted_at = NULL, updated_at = ?
WHERE scope = ? AND adapter = ? AND source_id = ? AND state = ? AND authority_epoch = ?`, item.ConfigDigest, normalized.AuthorityGeneration,
				nextEpoch, normalized.AuthorityGeneration, nextEpoch, updatedAt, updatedAt, normalized.Scope, current.Owner.Adapter, current.Owner.SourceID, current.State, current.AuthorityEpoch)
			if execErr != nil {
				return fmt.Errorf("activate source lifecycle: %w", execErr)
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return app.ErrSourceLifecycleConflict
			}
			continue
		}
		if current.State == knowl.SourceLifecycleActive {
			next := current
			next.State, next.AuthorityGeneration, next.AuthorityEpoch = knowl.SourceLifecycleRetiring, normalized.AuthorityGeneration, nextEpoch
			if next.ActivatedEpoch == 0 {
				next.ActivatedGeneration, next.ActivatedEpoch = normalized.AuthorityGeneration, nextEpoch
			}
			next.RetirementGeneration, next.RetirementEpoch = normalized.AuthorityGeneration, nextEpoch
			next.ScanCursor, next.ScanRoundMatches, next.ScanComplete, next.Ready, next.BlockerCode = "", 0, false, false, ""
			next.ReplacementPending = additionCount
			next.RetiringAt, next.DeletedAt, next.UpdatedAt = normalized.UpdatedAt, time.Time{}, normalized.UpdatedAt
			if app.ValidateSourceLifecycle(next) != nil {
				return app.ErrSourceLifecycleInvalid
			}
			result, execErr := sourceExec(ctx, tx, `UPDATE knowl_source_lifecycle SET
state = 'retiring', authority_generation = ?, authority_epoch = ?, activated_generation = ?, activated_epoch = ?, retirement_generation = ?, retirement_epoch = ?,
scan_cursor = '', scan_round_matches = 0, scan_complete = FALSE, ready = FALSE, replacement_pending = ?, blocker_code = '',
retiring_at = ?, deleted_at = NULL, updated_at = ?
WHERE scope = ? AND adapter = ? AND source_id = ? AND state = 'active' AND authority_epoch = ?`, normalized.AuthorityGeneration,
				nextEpoch, next.ActivatedGeneration, next.ActivatedEpoch, normalized.AuthorityGeneration, nextEpoch, additionCount, updatedAt, updatedAt, normalized.Scope, current.Owner.Adapter, current.Owner.SourceID, current.AuthorityEpoch)
			if execErr != nil {
				return fmt.Errorf("retire source lifecycle: %w", execErr)
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return app.ErrSourceLifecycleConflict
			}
			continue
		}
		next := current
		next.AuthorityGeneration, next.AuthorityEpoch, next.UpdatedAt = normalized.AuthorityGeneration, nextEpoch, normalized.UpdatedAt
		if app.ValidateSourceLifecycle(next) != nil {
			return app.ErrSourceLifecycleInvalid
		}
		result, execErr := sourceExec(ctx, tx, `UPDATE knowl_source_lifecycle SET authority_generation = ?, authority_epoch = ?, updated_at = ? WHERE scope = ? AND adapter = ? AND source_id = ? AND state = ? AND authority_epoch = ?`, normalized.AuthorityGeneration, nextEpoch, updatedAt, normalized.Scope, current.Owner.Adapter, current.Owner.SourceID, current.State, current.AuthorityEpoch)
		if execErr != nil {
			return fmt.Errorf("refresh retired lifecycle authority: %w", execErr)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return app.ErrSourceLifecycleConflict
		}
	}
	for _, item := range normalized.Desired {
		if _, exists := desired[item.Owner]; !exists {
			continue
		}
		if _, err := sourceExec(ctx, tx, `INSERT INTO knowl_source_lifecycle (
scope, adapter, source_id, state, config_digest, authority_generation, authority_epoch,
activated_generation, activated_epoch, activated_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?)`,
			normalized.Scope, item.Owner.Adapter, item.Owner.SourceID, item.ConfigDigest,
			normalized.AuthorityGeneration, nextEpoch, normalized.AuthorityGeneration, nextEpoch, updatedAt, updatedAt); err != nil {
			return fmt.Errorf("insert source lifecycle: %w", err)
		}
	}
	result, err := sourceExec(ctx, tx, `UPDATE knowl_source_lifecycle_authority SET generation = ?, epoch = ?, updated_at = ? WHERE scope = ? AND generation = ? AND epoch = ?`, normalized.AuthorityGeneration, nextEpoch, updatedAt, normalized.Scope, authority.Generation, authority.Epoch)
	if err != nil {
		return fmt.Errorf("advance source lifecycle authority: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return app.ErrSourceLifecycleConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit source lifecycle reconcile: %w", err)
	}
	return nil
}

func (store *Store) SourceLifecycleAuthority(ctx context.Context, scope knowl.ScopeRef) (knowl.SourceLifecycleAuthority, error) {
	if strings.TrimSpace(string(scope)) == "" {
		return knowl.SourceLifecycleAuthority{}, app.ErrSourceLifecycleInvalid
	}
	authority, err := scanPostgresSourceLifecycleAuthority(sourceQueryRow(ctx, store.db, `SELECT scope, generation, epoch, updated_at FROM knowl_source_lifecycle_authority WHERE scope = ?`, scope))
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.SourceLifecycleAuthority{Scope: scope}, nil
	}
	return authority, err
}

func (store *Store) SourceLifecycle(ctx context.Context, scope knowl.ScopeRef, owner knowl.SourceOwner) (knowl.SourceLifecycle, error) {
	if strings.TrimSpace(string(scope)) == "" || app.ValidateSourceOwner(owner) != nil {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleInvalid
	}
	state, err := scanPostgresSourceLifecycle(sourceQueryRow(ctx, store.db, `SELECT `+sourceLifecycleColumns+` FROM knowl_source_lifecycle WHERE scope = ? AND adapter = ? AND source_id = ?`, scope, owner.Adapter, owner.SourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleUnowned
	}
	return state, err
}

func (store *Store) SourceLifecycles(ctx context.Context, scope knowl.ScopeRef, options app.SourceLifecycleListOptions) ([]knowl.SourceLifecycle, error) {
	if strings.TrimSpace(string(scope)) == "" {
		return nil, app.ErrSourceLifecycleInvalid
	}
	normalized, err := app.NormalizeSourceLifecycleListOptions(options)
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + sourceLifecycleColumns + ` FROM knowl_source_lifecycle WHERE scope = ?`
	args := []any{scope}
	if normalized.State != "" {
		query += ` AND state = ?`
		args = append(args, normalized.State)
	}
	if normalized.After != (knowl.SourceOwner{}) {
		query += ` AND (adapter > ? OR (adapter = ? AND source_id > ?))`
		args = append(args, normalized.After.Adapter, normalized.After.Adapter, normalized.After.SourceID)
	}
	query += ` ORDER BY adapter, source_id LIMIT ?`
	args = append(args, normalized.Limit)
	rows, err := sourceQuery(ctx, store.db, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list source lifecycles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]knowl.SourceLifecycle, 0, normalized.Limit)
	for rows.Next() {
		state, scanErr := scanPostgresSourceLifecycle(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source lifecycles: %w", err)
	}
	return result, nil
}

func (store *Store) MarkSourceLifecycleReady(ctx context.Context, update app.SourceLifecycleReadyUpdate) (knowl.SourceLifecycle, error) {
	if err := app.ValidateSourceLifecycleReadyUpdate(update); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	if err := store.mu.LockContext(ctx); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.SourceLifecycle{}, fmt.Errorf("begin source readiness: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanPostgresSourceLifecycle(sourceQueryRow(ctx, tx, `SELECT `+sourceLifecycleColumns+` FROM knowl_source_lifecycle WHERE scope = ? AND adapter = ? AND source_id = ? FOR UPDATE`, update.Scope, update.Owner.Adapter, update.Owner.SourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleUnowned
	}
	if err != nil {
		return knowl.SourceLifecycle{}, err
	}
	if current.State != knowl.SourceLifecycleActive || current.AuthorityGeneration != update.AuthorityGeneration ||
		current.AuthorityEpoch != update.AuthorityEpoch || current.ActivatedEpoch != update.ActivatedEpoch ||
		current.ConfigDigest != update.ConfigDigest || update.ReadyAt.Before(current.UpdatedAt) {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	if current.Ready {
		return current, nil
	}
	next := current
	next.Ready, next.BlockerCode, next.UpdatedAt = true, "", update.ReadyAt.UTC()
	if app.ValidateSourceLifecycle(next) != nil {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleInvalid
	}
	if _, err := sourceExec(ctx, tx, `UPDATE knowl_source_lifecycle SET
replacement_pending = replacement_pending - 1, updated_at = GREATEST(updated_at, ?)
WHERE scope = ? AND state = 'retiring' AND retirement_epoch = ? AND replacement_pending > 0`,
		formatTime(update.ReadyAt), update.Scope, update.ActivatedEpoch); err != nil {
		return knowl.SourceLifecycle{}, fmt.Errorf("satisfy replacement readiness: %w", err)
	}
	result, err := sourceExec(ctx, tx, `UPDATE knowl_source_lifecycle SET ready = TRUE, blocker_code = '', updated_at = ?
WHERE scope = ? AND adapter = ? AND source_id = ? AND state = 'active' AND authority_generation = ?
AND authority_epoch = ? AND activated_epoch = ? AND config_digest = ?`, formatTime(update.ReadyAt), update.Scope,
		update.Owner.Adapter, update.Owner.SourceID, update.AuthorityGeneration, update.AuthorityEpoch, update.ActivatedEpoch, update.ConfigDigest)
	if err != nil {
		return knowl.SourceLifecycle{}, fmt.Errorf("mark source lifecycle ready: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	if err := tx.Commit(); err != nil {
		return knowl.SourceLifecycle{}, fmt.Errorf("commit source readiness: %w", err)
	}
	return next, nil
}

func (store *Store) UpdateSourceRetirement(ctx context.Context, update app.SourceRetirementUpdate) (knowl.SourceLifecycle, error) {
	if err := app.ValidateSourceRetirementUpdate(update); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	if err := store.mu.LockContext(ctx); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.SourceLifecycle{}, fmt.Errorf("begin retirement update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanPostgresSourceLifecycle(sourceQueryRow(ctx, tx, `SELECT `+sourceLifecycleColumns+` FROM knowl_source_lifecycle WHERE scope = ? AND adapter = ? AND source_id = ? FOR UPDATE`, update.Scope, update.Owner.Adapter, update.Owner.SourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleUnowned
	}
	if err != nil {
		return knowl.SourceLifecycle{}, err
	}
	if current.State != knowl.SourceLifecycleRetiring || current.AuthorityGeneration != update.AuthorityGeneration ||
		current.AuthorityEpoch != update.AuthorityEpoch || current.RetirementGeneration != update.RetirementGeneration ||
		current.RetirementEpoch != update.RetirementEpoch || current.ScanCursor != update.ExpectedCursor ||
		update.UpdatedAt.Before(current.UpdatedAt) ||
		!validRetirementProgress(current, update) {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	next := current
	next.ScanCursor, next.ScanRoundMatches, next.ScanComplete = update.Cursor, update.RoundMatches, update.ScanComplete
	next.BlockerCode, next.UpdatedAt = update.BlockerCode, update.UpdatedAt.UTC()
	if app.ValidateSourceLifecycle(next) != nil {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleInvalid
	}
	result, err := sourceExec(ctx, tx, `UPDATE knowl_source_lifecycle SET scan_cursor = ?, scan_round_matches = ?,
scan_complete = ?, blocker_code = ?, updated_at = ? WHERE scope = ? AND adapter = ? AND source_id = ?
AND state = 'retiring' AND authority_generation = ? AND authority_epoch = ?
AND retirement_generation = ? AND retirement_epoch = ? AND scan_cursor = ?`,
		update.Cursor, update.RoundMatches, update.ScanComplete, update.BlockerCode, formatTime(update.UpdatedAt),
		update.Scope, update.Owner.Adapter, update.Owner.SourceID, update.AuthorityGeneration, update.AuthorityEpoch,
		update.RetirementGeneration, update.RetirementEpoch, update.ExpectedCursor)
	if err != nil {
		return knowl.SourceLifecycle{}, fmt.Errorf("update source retirement: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	state, err := scanPostgresSourceLifecycle(sourceQueryRow(ctx, tx, `SELECT `+sourceLifecycleColumns+` FROM knowl_source_lifecycle WHERE scope = ? AND adapter = ? AND source_id = ?`, update.Scope, update.Owner.Adapter, update.Owner.SourceID))
	if err != nil {
		return knowl.SourceLifecycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return knowl.SourceLifecycle{}, fmt.Errorf("commit retirement update: %w", err)
	}
	return state, nil
}

func (store *Store) FinalizeSourceRetirement(ctx context.Context, finalization app.SourceRetirementFinalization) (knowl.SourceLifecycle, error) {
	if err := app.ValidateSourceRetirementFinalization(finalization); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	if err := store.mu.LockContext(ctx); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.SourceLifecycle{}, fmt.Errorf("begin source retirement finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanPostgresSourceLifecycle(sourceQueryRow(ctx, tx, `SELECT `+sourceLifecycleColumns+` FROM knowl_source_lifecycle WHERE scope = ? AND adapter = ? AND source_id = ? FOR UPDATE`, finalization.Scope, finalization.Owner.Adapter, finalization.Owner.SourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleUnowned
	}
	if err != nil {
		return knowl.SourceLifecycle{}, err
	}
	if current.State == knowl.SourceLifecycleDeleted && current.AuthorityGeneration == finalization.AuthorityGeneration &&
		current.AuthorityEpoch == finalization.AuthorityEpoch && current.RetirementGeneration == finalization.RetirementGeneration &&
		current.RetirementEpoch == finalization.RetirementEpoch {
		return current, nil
	}
	if current.State != knowl.SourceLifecycleRetiring || current.AuthorityGeneration != finalization.AuthorityGeneration ||
		current.AuthorityEpoch != finalization.AuthorityEpoch || current.RetirementGeneration != finalization.RetirementGeneration ||
		current.RetirementEpoch != finalization.RetirementEpoch || current.ReplacementPending != 0 || !current.ScanComplete || current.ScanRoundMatches != 0 ||
		current.BlockerCode != "" || finalization.FinalizedAt.Before(current.UpdatedAt) || finalization.FinalizedAt.Before(current.RetiringAt) {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	next := current
	next.State, next.DeletedAt, next.UpdatedAt = knowl.SourceLifecycleDeleted, finalization.FinalizedAt.UTC(), finalization.FinalizedAt.UTC()
	if app.ValidateSourceLifecycle(next) != nil {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleInvalid
	}
	result, err := sourceExec(ctx, tx, `UPDATE knowl_source_lifecycle SET state = 'deleted', deleted_at = ?, updated_at = ?
WHERE scope = ? AND adapter = ? AND source_id = ? AND state = 'retiring' AND authority_generation = ?
AND authority_epoch = ? AND retirement_generation = ? AND retirement_epoch = ?
AND replacement_pending = 0 AND scan_complete = TRUE AND scan_round_matches = 0 AND blocker_code = ''`, formatTime(finalization.FinalizedAt),
		formatTime(finalization.FinalizedAt), finalization.Scope, finalization.Owner.Adapter, finalization.Owner.SourceID,
		finalization.AuthorityGeneration, finalization.AuthorityEpoch, finalization.RetirementGeneration, finalization.RetirementEpoch)
	if err != nil {
		return knowl.SourceLifecycle{}, fmt.Errorf("finalize source retirement: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	if err := tx.Commit(); err != nil {
		return knowl.SourceLifecycle{}, fmt.Errorf("commit source retirement finalization: %w", err)
	}
	return next, nil
}

type lifecycleScanner interface{ Scan(dest ...any) error }

func scanPostgresSourceLifecycle(scanner lifecycleScanner) (knowl.SourceLifecycle, error) {
	var state knowl.SourceLifecycle
	var adapter, sourceID, lifecycleState string
	var authorityEpoch, activatedEpoch, retirementEpoch int64
	var retiringAt, deletedAt sql.NullTime
	err := scanner.Scan(&state.Scope, &adapter, &sourceID, &lifecycleState, &state.ConfigDigest,
		&state.AuthorityGeneration, &authorityEpoch, &state.ActivatedGeneration, &activatedEpoch,
		&state.RetirementGeneration, &retirementEpoch, &state.ScanCursor,
		&state.ScanRoundMatches, &state.ScanComplete, &state.Ready, &state.ReplacementPending, &state.BlockerCode, &state.ActivatedAt,
		&retiringAt, &deletedAt, &state.UpdatedAt)
	if err != nil {
		return knowl.SourceLifecycle{}, err
	}
	if authorityEpoch < 0 || activatedEpoch < 0 || retirementEpoch < 0 {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleInvalid
	}
	state.AuthorityEpoch, state.ActivatedEpoch, state.RetirementEpoch = uint64(authorityEpoch), uint64(activatedEpoch), uint64(retirementEpoch)
	state.Owner = knowl.SourceOwner{Adapter: knowl.SourceAdapterID(adapter), SourceID: knowl.SourceID(sourceID)}
	state.State = knowl.SourceLifecycleState(lifecycleState)
	state.ActivatedAt, state.UpdatedAt = state.ActivatedAt.UTC(), state.UpdatedAt.UTC()
	if retiringAt.Valid {
		state.RetiringAt = retiringAt.Time.UTC()
	}
	if deletedAt.Valid {
		state.DeletedAt = deletedAt.Time.UTC()
	}
	if app.ValidateSourceLifecycle(state) != nil {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleInvalid
	}
	return state, nil
}

func scanPostgresSourceLifecycleAuthority(scanner lifecycleScanner) (knowl.SourceLifecycleAuthority, error) {
	var authority knowl.SourceLifecycleAuthority
	var epoch int64
	if err := scanner.Scan(&authority.Scope, &authority.Generation, &epoch, &authority.UpdatedAt); err != nil {
		return knowl.SourceLifecycleAuthority{}, err
	}
	if epoch < 0 || authority.Generation != "" && !validDigest(authority.Generation) {
		return knowl.SourceLifecycleAuthority{}, app.ErrSourceLifecycleInvalid
	}
	authority.Epoch, authority.UpdatedAt = uint64(epoch), authority.UpdatedAt.UTC()
	return authority, nil
}

func validRetirementProgress(current knowl.SourceLifecycle, update app.SourceRetirementUpdate) bool {
	if current.ScanCursor != "" && update.Cursor == "" && update.RoundMatches == 0 && !update.ScanComplete && update.BlockerCode == "" {
		return true
	}
	if current.ScanComplete {
		return update.Cursor == "" && update.RoundMatches == 0 && !update.ScanComplete && update.BlockerCode == ""
	}
	if update.RoundMatches < current.ScanRoundMatches {
		return false
	}
	if update.ScanComplete {
		return update.Cursor >= update.ExpectedCursor
	}
	return update.Cursor > update.ExpectedCursor
}
