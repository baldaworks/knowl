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
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func (store *Store) BeginSync(ctx context.Context, request app.BeginSyncRequest) (knowl.SyncRun, bool, error) {
	if request.Type != knowl.SourceTypeFilesystem || request.Run.Status != knowl.SyncStatusScanning || request.Run.StartedAt.IsZero() || request.Run.CompleteScan || request.Run.Counts != (knowl.SyncCounts{}) || request.Run.FailureClass != "" || request.Run.ContentGeneration != "" || !request.Run.CompletedAt.IsZero() || app.ValidateSyncRun(request.Run) != nil {
		return knowl.SyncRun{}, false, app.ErrSourceInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.SyncRun{}, false, fmt.Errorf("begin source sync: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var advisoryLock any
	if err := sourceQueryRow(ctx, tx, `SELECT pg_advisory_xact_lock(hashtextextended(? || ':' || ?, 0))`, request.Run.Scope, request.Run.ID).Scan(&advisoryLock); err != nil {
		return knowl.SyncRun{}, false, fmt.Errorf("lock source sync begin: %w", err)
	}
	existing, readErr := syncRunTx(ctx, tx, request.Run.Scope, request.Run.ID)
	if readErr == nil {
		if existing.SourceID != request.Run.SourceID || existing.ConfigDigest != request.Run.ConfigDigest || !existing.StartedAt.Equal(request.Run.StartedAt.UTC()) {
			return knowl.SyncRun{}, false, app.ErrSyncConflict
		}
		return existing, true, nil
	}
	if !errors.Is(readErr, app.ErrSyncRunNotFound) {
		return knowl.SyncRun{}, false, readErr
	}
	now := request.Run.UpdatedAt.UTC()
	if now.IsZero() {
		now = request.Run.StartedAt.UTC()
	}
	started := request.Run.StartedAt.UTC()
	_, err = sourceExec(ctx, tx, `
		INSERT INTO knowl_sources (scope, source_id, source_type, config_digest, last_attempt_run_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, source_id) DO UPDATE SET
			source_type = excluded.source_type, config_digest = excluded.config_digest,
			last_attempt_run_id = excluded.last_attempt_run_id, status = excluded.status, updated_at = excluded.updated_at`,
		request.Run.Scope, request.Run.SourceID, request.Type, request.Run.ConfigDigest, request.Run.ID,
		request.Run.Status, formatTime(started), formatTime(now))
	if err != nil {
		return knowl.SyncRun{}, false, fmt.Errorf("upsert sync source: %w", err)
	}
	_, err = sourceExec(ctx, tx, `
		INSERT INTO knowl_sync_runs (run_id, scope, source_id, config_digest, status, cursor, next_page_token,
			complete_scan, added, updated, unchanged, deleted, failed, failure_class, content_generation,
			checkpoint, started_at, updated_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		request.Run.ID, request.Run.Scope, request.Run.SourceID, request.Run.ConfigDigest, request.Run.Status,
		request.Run.Cursor, request.Run.NextPageToken, request.Run.CompleteScan, request.Run.Counts.Added,
		request.Run.Counts.Updated, request.Run.Counts.Unchanged, request.Run.Counts.Deleted, request.Run.Counts.Failed,
		request.Run.FailureClass, request.Run.ContentGeneration, request.Run.Checkpoint, formatTime(started), formatTime(now), optionalTime(request.Run.CompletedAt))
	if err != nil {
		return knowl.SyncRun{}, false, fmt.Errorf("insert sync run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return knowl.SyncRun{}, false, fmt.Errorf("commit source sync begin: %w", err)
	}
	request.Run.StartedAt, request.Run.UpdatedAt = started, now
	return request.Run, false, nil
}

func (store *Store) SyncRun(ctx context.Context, scope knowl.ScopeRef, id knowl.SyncRunID) (knowl.SyncRun, error) {
	return syncRunQuery(ctx, store.db, scope, id)
}

func (store *Store) ScanDocuments(ctx context.Context, scope knowl.ScopeRef, id knowl.SyncRunID, limit int) ([]knowl.DocumentRef, error) {
	if strings.TrimSpace(string(scope)) == "" || app.ValidateSyncRunID(id) != nil || app.ValidateScanDocumentLimit(limit) != nil {
		return nil, app.ErrSourceInvalid
	}
	if _, err := syncRunQuery(ctx, store.db, scope, id); err != nil {
		return nil, err
	}
	rows, err := sourceQuery(ctx, store.db, `SELECT descriptor FROM knowl_sync_seen WHERE run_id = ? ORDER BY ordinal LIMIT ?`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("read scan documents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	documents := make([]knowl.DocumentRef, 0)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("scan seen descriptor: %w", err)
		}
		var ref knowl.DocumentRef
		if json.Unmarshal(encoded, &ref) != nil || app.ValidateDocumentRef(ref) != nil {
			return nil, app.ErrSyncConflict
		}
		documents = append(documents, ref)
	}
	return documents, rows.Err()
}

func (store *Store) RecordScanPage(ctx context.Context, record app.ScanPageRecord) (knowl.SyncRun, error) {
	if strings.TrimSpace(string(record.Scope)) == "" || app.ValidateSourceID(record.SourceID) != nil || record.RunID == "" || record.RecordedAt.IsZero() || !validBounded(record.ExpectedPageToken, 4096, true) || app.ValidateDocumentPage(knowl.DocumentPage{Documents: record.Documents, NextPageToken: record.NextPageToken}, 1000) != nil {
		return knowl.SyncRun{}, app.ErrSourceInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.SyncRun{}, fmt.Errorf("begin scan page: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := syncRunTx(ctx, tx, record.Scope, record.RunID)
	if err != nil {
		return knowl.SyncRun{}, err
	}
	if run.SourceID != record.SourceID || run.Status != knowl.SyncStatusScanning {
		return knowl.SyncRun{}, app.ErrSyncStateTransition
	}
	if run.NextPageToken != record.ExpectedPageToken {
		if run.NextPageToken == record.NextPageToken {
			return run, nil
		}
		return knowl.SyncRun{}, app.ErrSyncConflict
	}
	var ordinal int
	if err := sourceQueryRow(ctx, tx, `SELECT COUNT(*) FROM knowl_sync_seen WHERE run_id = ?`, record.RunID).Scan(&ordinal); err != nil {
		return knowl.SyncRun{}, fmt.Errorf("count seen documents: %w", err)
	}
	for _, document := range record.Documents {
		encoded, encodeErr := json.Marshal(document)
		if encodeErr != nil {
			return knowl.SyncRun{}, fmt.Errorf("encode seen descriptor: %w", encodeErr)
		}
		result, insertErr := sourceExec(ctx, tx, `INSERT INTO knowl_sync_seen (run_id, document_id, revision, path, descriptor, ordinal) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(run_id, document_id) DO NOTHING`, record.RunID, document.ExternalID, document.Revision, document.Path, encoded, ordinal)
		if insertErr != nil {
			return knowl.SyncRun{}, fmt.Errorf("record seen descriptor: %w", insertErr)
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			var stored []byte
			if scanErr := sourceQueryRow(ctx, tx, `SELECT descriptor FROM knowl_sync_seen WHERE run_id = ? AND document_id = ?`, record.RunID, document.ExternalID).Scan(&stored); scanErr != nil || string(stored) != string(encoded) {
				return knowl.SyncRun{}, app.ErrSyncConflict
			}
		} else {
			ordinal++
		}
	}
	updated := record.RecordedAt.UTC()
	if _, err := sourceExec(ctx, tx, `UPDATE knowl_sync_runs SET next_page_token = ?, updated_at = ? WHERE run_id = ?`, record.NextPageToken, formatTime(updated), record.RunID); err != nil {
		return knowl.SyncRun{}, fmt.Errorf("advance scan page: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return knowl.SyncRun{}, fmt.Errorf("commit scan page: %w", err)
	}
	run.NextPageToken, run.UpdatedAt = record.NextPageToken, updated
	return run, nil
}

func (store *Store) PrepareSync(ctx context.Context, prepared app.PreparedSyncState) (knowl.SyncRun, error) {
	if !prepared.CompleteScan || prepared.PreparedAt.IsZero() || !validDigest(prepared.CandidateDigest) || !validBounded(prepared.Checkpoint, 4096, true) || app.ValidateSourceID(prepared.SourceID) != nil || app.ValidateSyncCounts(prepared.Counts) != nil {
		return knowl.SyncRun{}, app.ErrSourceInvalid
	}
	documents, err := app.NormalizePreparedDocuments(prepared.Scope, prepared.SourceID, prepared.RunID, prepared.Documents)
	if err != nil {
		return knowl.SyncRun{}, err
	}
	canonical, err := app.PreparedSyncDigest(prepared)
	if err != nil || canonical != prepared.CandidateDigest {
		return knowl.SyncRun{}, app.ErrSourceInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.SyncRun{}, fmt.Errorf("begin sync prepare: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := syncRunTx(ctx, tx, prepared.Scope, prepared.RunID)
	if err != nil {
		return knowl.SyncRun{}, err
	}
	if run.SourceID != prepared.SourceID {
		return knowl.SyncRun{}, app.ErrSyncConflict
	}
	if run.Status == knowl.SyncStatusPrepared {
		matches, matchErr := preparedDocumentsMatchTx(ctx, tx, prepared.RunID, documents, prepared.CandidateDigest)
		if matchErr != nil {
			return knowl.SyncRun{}, matchErr
		}
		storedDigest, digestErr := storedPreparedDigestTx(ctx, tx, run)
		if digestErr != nil || storedDigest != prepared.CandidateDigest {
			return knowl.SyncRun{}, app.ErrSyncConflict
		}
		if run.Checkpoint == prepared.Checkpoint && run.Counts == prepared.Counts && matches {
			return run, nil
		}
		return knowl.SyncRun{}, app.ErrSyncConflict
	}
	if run.Status != knowl.SyncStatusScanning {
		return knowl.SyncRun{}, app.ErrSyncStateTransition
	}
	for _, document := range documents {
		accepted, encodeErr := json.Marshal(document.State.AcceptedSource)
		if encodeErr != nil {
			return knowl.SyncRun{}, fmt.Errorf("encode candidate source: %w", encodeErr)
		}
		_, err = sourceExec(ctx, tx, `INSERT INTO knowl_sync_candidates (run_id, document_id, action, revision, accepted_source, maintenance_revision, maintenance_operation_id, mirror_path, mirror_digest, last_seen_run_id, deleted_at, candidate_digest) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			prepared.RunID, document.State.DocumentID, document.Action, document.State.Revision, accepted, document.State.MaintenanceRevision, document.State.MaintenanceOperationID, document.State.MirrorPath, document.State.MirrorDigest, document.State.LastSeenRunID, optionalTime(document.State.DeletedAt), prepared.CandidateDigest)
		if err != nil {
			return knowl.SyncRun{}, fmt.Errorf("insert sync candidate: %w", err)
		}
	}
	updated := prepared.PreparedAt.UTC()
	_, err = sourceExec(ctx, tx, `UPDATE knowl_sync_runs SET status = ?, complete_scan = TRUE, added = ?, updated = ?, unchanged = ?, deleted = ?, failed = ?, candidate_digest = ?, checkpoint = ?, updated_at = ? WHERE run_id = ?`,
		knowl.SyncStatusPrepared, prepared.Counts.Added, prepared.Counts.Updated, prepared.Counts.Unchanged, prepared.Counts.Deleted, prepared.Counts.Failed, prepared.CandidateDigest, prepared.Checkpoint, formatTime(updated), prepared.RunID)
	if err != nil {
		return knowl.SyncRun{}, fmt.Errorf("prepare sync run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return knowl.SyncRun{}, fmt.Errorf("commit sync prepare: %w", err)
	}
	run.Status, run.CompleteScan, run.Counts, run.Checkpoint, run.UpdatedAt = knowl.SyncStatusPrepared, true, prepared.Counts, prepared.Checkpoint, updated
	return run, nil
}

func (store *Store) PreparedSync(ctx context.Context, scope knowl.ScopeRef, id knowl.SyncRunID) (app.PreparedSyncRead, error) {
	if strings.TrimSpace(string(scope)) == "" || app.ValidateSyncRunID(id) != nil {
		return app.PreparedSyncRead{}, app.ErrSourceInvalid
	}
	run, err := syncRunQuery(ctx, store.db, scope, id)
	if err != nil {
		return app.PreparedSyncRead{}, err
	}
	switch run.Status {
	case knowl.SyncStatusPrepared, knowl.SyncStatusContentCommitted, knowl.SyncStatusProjected, knowl.SyncStatusSucceeded:
	default:
		return app.PreparedSyncRead{}, app.ErrSyncStateTransition
	}
	documents, err := loadPreparedCandidateRows(ctx, store.db, run)
	if err != nil {
		return app.PreparedSyncRead{}, err
	}
	storedDigest := ""
	if err := sourceQueryRow(ctx, store.db, `SELECT candidate_digest FROM knowl_sync_runs WHERE run_id = ?`, id).Scan(&storedDigest); err != nil {
		return app.PreparedSyncRead{}, fmt.Errorf("read prepared digest: %w", err)
	}
	canonical, err := app.PreparedSyncDigest(app.PreparedSyncState{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, CompleteScan: true, Checkpoint: run.Checkpoint, Counts: run.Counts, Documents: documents})
	if err != nil || canonical != storedDigest {
		return app.PreparedSyncRead{}, app.ErrSyncConflict
	}
	return app.PreparedSyncRead{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, Checkpoint: run.Checkpoint, Counts: run.Counts, Documents: documents, CandidateDigest: storedDigest}, nil
}

func loadPreparedCandidateRows(ctx context.Context, queryer sourceQueryer, run knowl.SyncRun) ([]app.PreparedDocumentState, error) {
	rows, err := sourceQuery(ctx, queryer, `SELECT document_id, action, revision, accepted_source, maintenance_revision, maintenance_operation_id, mirror_path, mirror_digest, last_seen_run_id, deleted_at FROM knowl_sync_candidates WHERE run_id = ? ORDER BY document_id`, run.ID)
	if err != nil {
		return nil, fmt.Errorf("read prepared candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	documents := make([]app.PreparedDocumentState, 0)
	for rows.Next() {
		var documentID, action, revision, maintenanceRevision, maintenanceOperationID, mirrorPath, mirrorDigest, lastSeen string
		var accepted []byte
		var deletedAt sql.NullTime
		if err := rows.Scan(&documentID, &action, &revision, &accepted, &maintenanceRevision, &maintenanceOperationID, &mirrorPath, &mirrorDigest, &lastSeen, &deletedAt); err != nil {
			return nil, fmt.Errorf("scan prepared candidate: %w", err)
		}
		state := knowl.DocumentState{Scope: run.Scope, SourceID: run.SourceID, DocumentID: knowl.DocumentID(documentID), Revision: revision, MaintenanceRevision: maintenanceRevision, MaintenanceOperationID: knowl.OperationID(maintenanceOperationID), MirrorPath: mirrorPath, MirrorDigest: mirrorDigest, LastSeenRunID: knowl.SyncRunID(lastSeen), Deleted: action == string(app.SyncDocumentTombstone)}
		if json.Unmarshal(accepted, &state.AcceptedSource) != nil {
			return nil, app.ErrSyncConflict
		}
		if deletedAt.Valid {
			state.DeletedAt = deletedAt.Time.UTC()
		}
		documents = append(documents, app.PreparedDocumentState{Action: app.SyncDocumentAction(action), State: state})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate prepared candidates: %w", err)
	}
	validated, err := app.NormalizePreparedDocuments(run.Scope, run.SourceID, run.ID, documents)
	if err != nil {
		return nil, app.ErrSyncConflict
	}
	return validated, nil
}

func storedPreparedDigestTx(ctx context.Context, tx *sql.Tx, run knowl.SyncRun) (string, error) {
	documents, err := loadPreparedCandidateRows(ctx, tx, run)
	if err != nil {
		return "", err
	}
	return app.PreparedSyncDigest(app.PreparedSyncState{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, CompleteScan: true, Checkpoint: run.Checkpoint, Counts: run.Counts, Documents: documents})
}

func (store *Store) MarkContentCommitted(ctx context.Context, generation app.SyncGeneration) (knowl.SyncRun, error) {
	return store.markSyncGeneration(ctx, generation, knowl.SyncStatusPrepared, knowl.SyncStatusContentCommitted)
}

func (store *Store) MarkProjected(ctx context.Context, generation app.SyncGeneration) (knowl.SyncRun, error) {
	return store.markSyncGeneration(ctx, generation, knowl.SyncStatusContentCommitted, knowl.SyncStatusProjected)
}

func (store *Store) markSyncGeneration(ctx context.Context, generation app.SyncGeneration, from, to knowl.SyncStatus) (knowl.SyncRun, error) {
	if generation.RunID == "" || generation.UpdatedAt.IsZero() || strings.TrimSpace(generation.Generation) == "" || len(generation.Generation) > 4096 || app.ValidateSourceID(generation.SourceID) != nil {
		return knowl.SyncRun{}, app.ErrSourceInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.SyncRun{}, fmt.Errorf("begin sync generation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := syncRunTx(ctx, tx, generation.Scope, generation.RunID)
	if err != nil {
		return knowl.SyncRun{}, err
	}
	if run.SourceID != generation.SourceID {
		return knowl.SyncRun{}, app.ErrSyncConflict
	}
	if run.Status == to {
		if run.ContentGeneration == generation.Generation {
			return run, nil
		}
		return knowl.SyncRun{}, app.ErrSyncConflict
	}
	if run.Status != from {
		return knowl.SyncRun{}, app.ErrSyncStateTransition
	}
	if run.ContentGeneration != "" && run.ContentGeneration != generation.Generation {
		return knowl.SyncRun{}, app.ErrSyncConflict
	}
	updated := generation.UpdatedAt.UTC()
	if _, err := sourceExec(ctx, tx, `UPDATE knowl_sync_runs SET status = ?, content_generation = ?, updated_at = ? WHERE run_id = ?`, to, generation.Generation, formatTime(updated), generation.RunID); err != nil {
		return knowl.SyncRun{}, fmt.Errorf("mark sync generation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return knowl.SyncRun{}, fmt.Errorf("commit sync generation: %w", err)
	}
	run.Status, run.ContentGeneration, run.UpdatedAt = to, generation.Generation, updated
	return run, nil
}

func (store *Store) FinalizeSync(ctx context.Context, finalization app.SyncFinalization) (knowl.SyncRun, error) {
	if finalization.FinalizedAt.IsZero() || !validDigest(finalization.CandidateDigest) || strings.TrimSpace(finalization.Generation) == "" || !validBounded(finalization.Checkpoint, 4096, true) || app.ValidateSyncCounts(finalization.Counts) != nil || app.ValidateSourceID(finalization.SourceID) != nil {
		return knowl.SyncRun{}, app.ErrSourceInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.SyncRun{}, fmt.Errorf("begin sync finalize: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := syncRunTx(ctx, tx, finalization.Scope, finalization.RunID)
	if err != nil {
		return knowl.SyncRun{}, err
	}
	if run.SourceID != finalization.SourceID {
		return knowl.SyncRun{}, app.ErrSyncConflict
	}
	storedDigest := syncCandidateDigestTx(ctx, tx, finalization.RunID)
	if storedDigest != finalization.CandidateDigest || run.ContentGeneration != finalization.Generation || run.Checkpoint != finalization.Checkpoint || run.Counts != finalization.Counts {
		return knowl.SyncRun{}, app.ErrSyncConflict
	}
	if run.Status == knowl.SyncStatusSucceeded {
		return run, nil
	}
	if run.Status != knowl.SyncStatusProjected {
		return knowl.SyncRun{}, app.ErrSyncStateTransition
	}
	rows, err := sourceQuery(ctx, tx, `SELECT document_id, action, revision, accepted_source, maintenance_revision, maintenance_operation_id, mirror_path, mirror_digest, last_seen_run_id, deleted_at FROM knowl_sync_candidates WHERE run_id = ? ORDER BY document_id`, finalization.RunID)
	if err != nil {
		return knowl.SyncRun{}, fmt.Errorf("read sync candidates: %w", err)
	}
	type candidate struct {
		documentID, action, revision, accepted, maintenanceRevision, maintenanceOperationID string
		mirrorPath, mirrorDigest, lastSeen                                                  string
		deletedAt                                                                           sql.NullTime
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.documentID, &item.action, &item.revision, &item.accepted, &item.maintenanceRevision, &item.maintenanceOperationID, &item.mirrorPath, &item.mirrorDigest, &item.lastSeen, &item.deletedAt); err != nil {
			_ = rows.Close()
			return knowl.SyncRun{}, fmt.Errorf("scan sync candidate: %w", err)
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return knowl.SyncRun{}, err
	}
	now := finalization.FinalizedAt.UTC()
	for _, item := range candidates {
		deleted := item.action == string(app.SyncDocumentTombstone)
		_, err = sourceExec(ctx, tx, `INSERT INTO knowl_source_documents (scope, source_id, document_id, revision, accepted_source, maintenance_revision, maintenance_operation_id, mirror_path, mirror_digest, last_seen_run_id, deleted, deleted_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(scope, source_id, document_id) DO UPDATE SET revision = excluded.revision, accepted_source = excluded.accepted_source, maintenance_revision = excluded.maintenance_revision, maintenance_operation_id = excluded.maintenance_operation_id, mirror_path = excluded.mirror_path, mirror_digest = excluded.mirror_digest, last_seen_run_id = excluded.last_seen_run_id, deleted = excluded.deleted, deleted_at = excluded.deleted_at, updated_at = excluded.updated_at`,
			finalization.Scope, finalization.SourceID, item.documentID, item.revision, item.accepted, item.maintenanceRevision, item.maintenanceOperationID, item.mirrorPath, item.mirrorDigest, item.lastSeen, deleted, nullableTime(item.deletedAt), formatTime(now), formatTime(now))
		if err != nil {
			return knowl.SyncRun{}, fmt.Errorf("apply sync candidate: %w", err)
		}
	}
	_, err = sourceExec(ctx, tx, `UPDATE knowl_sources SET checkpoint = ?, last_success_run_id = ?, status = ?, updated_at = ? WHERE scope = ? AND source_id = ?`, finalization.Checkpoint, finalization.RunID, knowl.SyncStatusSucceeded, formatTime(now), finalization.Scope, finalization.SourceID)
	if err != nil {
		return knowl.SyncRun{}, fmt.Errorf("finalize sync source: %w", err)
	}
	_, err = sourceExec(ctx, tx, `UPDATE knowl_sync_runs SET status = ?, completed_at = ?, updated_at = ? WHERE run_id = ?`, knowl.SyncStatusSucceeded, formatTime(now), formatTime(now), finalization.RunID)
	if err != nil {
		return knowl.SyncRun{}, fmt.Errorf("finalize sync run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return knowl.SyncRun{}, fmt.Errorf("commit sync finalize: %w", err)
	}
	run.Status, run.CompletedAt, run.UpdatedAt = knowl.SyncStatusSucceeded, now, now
	return run, nil
}

func (store *Store) FailSync(ctx context.Context, scope knowl.ScopeRef, id knowl.SyncRunID, failureClass string, failedAt time.Time) (knowl.SyncRun, error) {
	if failedAt.IsZero() || !validFailure(failureClass) {
		return knowl.SyncRun{}, app.ErrSourceInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return knowl.SyncRun{}, fmt.Errorf("begin sync failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := syncRunTx(ctx, tx, scope, id)
	if err != nil {
		return knowl.SyncRun{}, err
	}
	if run.Status == knowl.SyncStatusSucceeded {
		return knowl.SyncRun{}, app.ErrSyncStateTransition
	}
	if run.Status == knowl.SyncStatusFailed {
		if run.FailureClass == failureClass {
			return run, nil
		}
		return knowl.SyncRun{}, app.ErrSyncConflict
	}
	now := failedAt.UTC()
	if _, err := sourceExec(ctx, tx, `UPDATE knowl_sync_runs SET status = ?, failure_class = ?, completed_at = ?, updated_at = ? WHERE run_id = ?`, knowl.SyncStatusFailed, failureClass, formatTime(now), formatTime(now), id); err != nil {
		return knowl.SyncRun{}, fmt.Errorf("fail sync run: %w", err)
	}
	if _, err := sourceExec(ctx, tx, `UPDATE knowl_sources SET status = ?, updated_at = ? WHERE scope = ? AND source_id = ?`, knowl.SyncStatusFailed, formatTime(now), scope, run.SourceID); err != nil {
		return knowl.SyncRun{}, fmt.Errorf("fail sync source: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return knowl.SyncRun{}, fmt.Errorf("commit sync failure: %w", err)
	}
	run.Status, run.FailureClass, run.CompletedAt, run.UpdatedAt = knowl.SyncStatusFailed, failureClass, now, now
	return run, nil
}

func (store *Store) DocumentState(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID, documentID knowl.DocumentID) (knowl.DocumentState, error) {
	if app.ValidateSourceID(sourceID) != nil || app.ValidateDocumentID(documentID) != nil {
		return knowl.DocumentState{}, app.ErrSourceInvalid
	}
	row := sourceQueryRow(ctx, store.db, `SELECT revision, accepted_source, maintenance_revision, maintenance_operation_id, mirror_path, mirror_digest, last_seen_run_id, deleted, deleted_at, created_at, updated_at FROM knowl_source_documents WHERE scope = ? AND source_id = ? AND document_id = ?`, scope, sourceID, documentID)
	return scanDocumentState(row, scope, sourceID, documentID)
}

func (store *Store) DocumentStates(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID, options app.DocumentListOptions) ([]knowl.DocumentState, error) {
	if app.ValidateSourceID(sourceID) != nil {
		return nil, app.ErrSourceInvalid
	}
	options, err := app.ValidateDocumentListOptions(options)
	if err != nil {
		return nil, err
	}
	query := `SELECT document_id, revision, accepted_source, maintenance_revision, maintenance_operation_id, mirror_path, mirror_digest, last_seen_run_id, deleted, deleted_at, created_at, updated_at FROM knowl_source_documents WHERE scope = ? AND source_id = ?`
	args := []any{scope, sourceID}
	if !options.IncludeDeleted {
		query += ` AND deleted = FALSE`
	}
	query += ` ORDER BY document_id LIMIT ?`
	args = append(args, options.Limit)
	rows, err := sourceQuery(ctx, store.db, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list source documents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	states := make([]knowl.DocumentState, 0)
	for rows.Next() {
		var documentID knowl.DocumentID
		var revision, accepted, maintenanceRevision, maintenanceOperationID, mirrorPath, mirrorDigest, lastSeen string
		var deletedAt sql.NullTime
		var createdAt, updatedAt time.Time
		var deleted bool
		if err := rows.Scan(&documentID, &revision, &accepted, &maintenanceRevision, &maintenanceOperationID, &mirrorPath, &mirrorDigest, &lastSeen, &deleted, &deletedAt, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan source document: %w", err)
		}
		state, err := decodeDocumentState(scope, sourceID, documentID, revision, accepted, maintenanceRevision, maintenanceOperationID, mirrorPath, mirrorDigest, lastSeen, deleted, deletedAt, createdAt, updatedAt)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func (store *Store) SourceStatus(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID) (knowl.SourceStatus, error) {
	if app.ValidateSourceID(sourceID) != nil {
		return knowl.SourceStatus{}, app.ErrSourceInvalid
	}
	var status knowl.SourceStatus
	var sourceType, syncStatus string
	var createdAt, updatedAt, lastAttemptAt time.Time
	var lastSuccessfulAt sql.NullTime
	err := sourceQueryRow(ctx, store.db, `
		SELECT source.source_type, source.config_digest, source.checkpoint,
			source.last_attempt_run_id, source.last_success_run_id, source.status,
			attempt.added, attempt.updated, attempt.unchanged, attempt.deleted, attempt.failed,
			source.created_at, COALESCE(attempt.completed_at, attempt.updated_at),
			success.completed_at, source.updated_at
		FROM knowl_sources AS source
		JOIN knowl_sync_runs AS attempt ON attempt.run_id = source.last_attempt_run_id
		LEFT JOIN knowl_sync_runs AS success ON success.run_id = NULLIF(source.last_success_run_id, '')
		WHERE source.scope = ? AND source.source_id = ?`, scope, sourceID).Scan(
		&sourceType, &status.ConfigDigest, &status.Checkpoint, &status.LastAttemptRunID,
		&status.LastSuccessfulRunID, &syncStatus, &status.Counts.Added, &status.Counts.Updated,
		&status.Counts.Unchanged, &status.Counts.Deleted, &status.Counts.Failed, &createdAt,
		&lastAttemptAt, &lastSuccessfulAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.SourceStatus{}, app.ErrSourceNotFound
	}
	if err != nil {
		return knowl.SourceStatus{}, fmt.Errorf("read source status: %w", err)
	}
	status.Scope, status.SourceID, status.Type, status.Status = scope, sourceID, knowl.SourceType(sourceType), knowl.SyncStatus(syncStatus)
	status.CreatedAt = createdAt.UTC()
	status.LastAttemptAt = lastAttemptAt.UTC()
	if lastSuccessfulAt.Valid {
		status.LastSuccessfulAt = lastSuccessfulAt.Time.UTC()
	}
	status.UpdatedAt = updatedAt.UTC()
	status.Maintenance, err = store.sourceMaintenanceStatus(ctx, scope, sourceID)
	if err != nil {
		return knowl.SourceStatus{}, err
	}
	return status, nil
}

const maxMaintenanceSamples = 16

func (store *Store) sourceMaintenanceStatus(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID) (knowl.SourceMaintenanceStatus, error) {
	var result knowl.SourceMaintenanceStatus
	err := sourceQueryRow(ctx, store.db, `
		SELECT
			COALESCE(SUM(CASE WHEN operation.status NOT IN ('committed', 'failed')
				AND (operation.work_lease_token <> '' OR operation.work_ready_at <= CURRENT_TIMESTAMP) THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN operation.status NOT IN ('committed', 'failed')
				AND operation.work_lease_token = '' AND operation.work_ready_at > CURRENT_TIMESTAMP THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN operation.work_attempt > 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN operation.status = 'committed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN operation.status = 'failed' THEN 1 ELSE 0 END), 0)
		FROM knowl_source_documents AS document
		JOIN knowl_operations AS operation
		  ON operation.scope = document.scope
		 AND operation.operation_id = document.maintenance_operation_id
		WHERE document.scope = ? AND document.source_id = ? AND document.deleted = FALSE
		  AND document.maintenance_operation_id <> ''
		  AND document.maintenance_revision = document.revision`, scope, sourceID).Scan(
		&result.Counts.Queued, &result.Counts.Retrying, &result.Counts.Replayed, &result.Counts.Committed, &result.Counts.Failed,
	)
	if err != nil {
		return knowl.SourceMaintenanceStatus{}, fmt.Errorf("read source maintenance counts: %w", err)
	}
	rows, err := sourceQuery(ctx, store.db, `
		SELECT document.document_id, document.revision, operation.operation_id, operation.status,
		       operation.work_attempt, operation.retry_attempt, operation.manual_retry_count,
		       operation.failure_class, operation.failure_reason, operation.work_ready_at, operation.work_lease_token
		FROM knowl_source_documents AS document
		JOIN knowl_operations AS operation
		  ON operation.scope = document.scope
		 AND operation.operation_id = document.maintenance_operation_id
		WHERE document.scope = ? AND document.source_id = ? AND document.deleted = FALSE
		  AND document.maintenance_operation_id <> ''
		  AND document.maintenance_revision = document.revision
		ORDER BY document.document_id, document.revision, operation.operation_id
		LIMIT ?`, scope, sourceID, maxMaintenanceSamples+1)
	if err != nil {
		return knowl.SourceMaintenanceStatus{}, fmt.Errorf("read source maintenance samples: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sample knowl.MaintenanceSample
		var readyAt time.Time
		var leaseToken string
		if err := rows.Scan(&sample.DocumentID, &sample.Revision, &sample.OperationID, &sample.Status,
			&sample.WorkAttempt, &sample.RetryAttempt, &sample.ManualRetryCount,
			&sample.FailureClass, &sample.FailureReason, &readyAt, &leaseToken); err != nil {
			return knowl.SourceMaintenanceStatus{}, fmt.Errorf("scan source maintenance sample: %w", err)
		}
		sample.Replayed = sample.WorkAttempt > 1
		if sample.Status != knowl.StatusCommitted && sample.Status != knowl.StatusFailed && leaseToken == "" && readyAt.After(time.Now().UTC()) {
			sample.NextRetryAt = readyAt.UTC()
		}
		if len(result.Samples) == maxMaintenanceSamples {
			result.Truncated = true
			continue
		}
		result.Samples = append(result.Samples, sample)
	}
	if err := rows.Err(); err != nil {
		return knowl.SourceMaintenanceStatus{}, fmt.Errorf("iterate source maintenance samples: %w", err)
	}
	if result.Samples == nil {
		result.Samples = make([]knowl.MaintenanceSample, 0)
	}
	return result, nil
}

func (store *Store) ResumableSyncRuns(ctx context.Context, scope knowl.ScopeRef, limit int) ([]knowl.SyncRun, error) {
	if limit <= 0 || limit > 1000 {
		return nil, app.ErrSourceInvalid
	}
	rows, err := sourceQuery(ctx, store.db, syncRunSelect+` WHERE scope = ? AND status IN (?, ?, ?, ?) ORDER BY updated_at, run_id LIMIT ?`, scope, knowl.SyncStatusScanning, knowl.SyncStatusPrepared, knowl.SyncStatusContentCommitted, knowl.SyncStatusProjected, limit)
	if err != nil {
		return nil, fmt.Errorf("list resumable sync runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var runs []knowl.SyncRun
	for rows.Next() {
		run, err := scanSyncRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

const syncRunSelect = `SELECT run_id, scope, source_id, config_digest, status, cursor, next_page_token, complete_scan, added, updated, unchanged, deleted, failed, failure_class, content_generation, checkpoint, started_at, updated_at, completed_at FROM knowl_sync_runs`

type scanner interface{ Scan(dest ...any) error }

type sourceExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type sourceQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func syncRunQuery(ctx context.Context, queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, scope knowl.ScopeRef, id knowl.SyncRunID) (knowl.SyncRun, error) {
	return scanSyncRun(sourceQueryRow(ctx, queryer, syncRunSelect+` WHERE scope = ? AND run_id = ?`, scope, id))
}
func syncRunTx(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, id knowl.SyncRunID) (knowl.SyncRun, error) {
	return scanSyncRun(sourceQueryRow(ctx, tx, syncRunSelect+` WHERE scope = ? AND run_id = ? FOR UPDATE`, scope, id))
}
func scanSyncRun(row scanner) (knowl.SyncRun, error) {
	var run knowl.SyncRun
	var status string
	var startedAt, updatedAt time.Time
	var completedAt sql.NullTime
	var complete bool
	err := row.Scan(&run.ID, &run.Scope, &run.SourceID, &run.ConfigDigest, &status, &run.Cursor, &run.NextPageToken, &complete, &run.Counts.Added, &run.Counts.Updated, &run.Counts.Unchanged, &run.Counts.Deleted, &run.Counts.Failed, &run.FailureClass, &run.ContentGeneration, &run.Checkpoint, &startedAt, &updatedAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.SyncRun{}, app.ErrSyncRunNotFound
	}
	if err != nil {
		return knowl.SyncRun{}, fmt.Errorf("read sync run: %w", err)
	}
	run.Status, run.CompleteScan = knowl.SyncStatus(status), complete
	run.StartedAt, run.UpdatedAt = startedAt.UTC(), updatedAt.UTC()
	if completedAt.Valid {
		run.CompletedAt = completedAt.Time.UTC()
	}
	return run, nil
}

func scanDocumentState(row scanner, scope knowl.ScopeRef, sourceID knowl.SourceID, documentID knowl.DocumentID) (knowl.DocumentState, error) {
	var revision, accepted, maintenanceRevision, maintenanceOperationID, mirrorPath, mirrorDigest, lastSeen string
	var deletedAt sql.NullTime
	var createdAt, updatedAt time.Time
	var deleted bool
	err := row.Scan(&revision, &accepted, &maintenanceRevision, &maintenanceOperationID, &mirrorPath, &mirrorDigest, &lastSeen, &deleted, &deletedAt, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.DocumentState{}, app.ErrSourceNotFound
	}
	if err != nil {
		return knowl.DocumentState{}, fmt.Errorf("read source document: %w", err)
	}
	return decodeDocumentState(scope, sourceID, documentID, revision, accepted, maintenanceRevision, maintenanceOperationID, mirrorPath, mirrorDigest, lastSeen, deleted, deletedAt, createdAt, updatedAt)
}
func decodeDocumentState(scope knowl.ScopeRef, sourceID knowl.SourceID, documentID knowl.DocumentID, revision, accepted, maintenanceRevision, maintenanceOperationID, mirrorPath, mirrorDigest, lastSeen string, deleted bool, deletedAt sql.NullTime, createdAt, updatedAt time.Time) (knowl.DocumentState, error) {
	state := knowl.DocumentState{Scope: scope, SourceID: sourceID, DocumentID: documentID, Revision: revision, MaintenanceRevision: maintenanceRevision, MaintenanceOperationID: knowl.OperationID(maintenanceOperationID), MirrorPath: mirrorPath, MirrorDigest: mirrorDigest, LastSeenRunID: knowl.SyncRunID(lastSeen), Deleted: deleted}
	if err := json.Unmarshal([]byte(accepted), &state.AcceptedSource); err != nil {
		return knowl.DocumentState{}, fmt.Errorf("decode accepted source: %w", err)
	}
	if deletedAt.Valid {
		state.DeletedAt = deletedAt.Time.UTC()
	}
	state.CreatedAt, state.UpdatedAt = createdAt.UTC(), updatedAt.UTC()
	return state, nil
}
func syncCandidateDigestTx(ctx context.Context, tx *sql.Tx, id knowl.SyncRunID) string {
	var digest string
	_ = sourceQueryRow(ctx, tx, `SELECT candidate_digest FROM knowl_sync_runs WHERE run_id = ?`, id).Scan(&digest)
	return digest
}

func preparedDocumentsMatchTx(ctx context.Context, tx *sql.Tx, runID knowl.SyncRunID, documents []app.PreparedDocumentState, digest string) (bool, error) {
	rows, err := sourceQuery(ctx, tx, `SELECT document_id, action, revision, accepted_source, maintenance_revision, maintenance_operation_id, mirror_path, mirror_digest, last_seen_run_id, deleted_at, candidate_digest FROM knowl_sync_candidates WHERE run_id = ? ORDER BY document_id`, runID)
	if err != nil {
		return false, fmt.Errorf("read prepared candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	index := 0
	for rows.Next() {
		if index >= len(documents) {
			return false, nil
		}
		var documentID, action, revision, accepted, maintenanceRevision, maintenanceOperationID, mirrorPath, mirrorDigest, lastSeen, storedDigest string
		var deletedAt sql.NullTime
		if err := rows.Scan(&documentID, &action, &revision, &accepted, &maintenanceRevision, &maintenanceOperationID, &mirrorPath, &mirrorDigest, &lastSeen, &deletedAt, &storedDigest); err != nil {
			return false, fmt.Errorf("scan prepared candidate: %w", err)
		}
		document := documents[index]
		encoded, err := json.Marshal(document.State.AcceptedSource)
		if err != nil {
			return false, fmt.Errorf("encode prepared candidate: %w", err)
		}
		if documentID != string(document.State.DocumentID) || action != string(document.Action) || revision != document.State.Revision || accepted != string(encoded) || maintenanceRevision != document.State.MaintenanceRevision || maintenanceOperationID != string(document.State.MaintenanceOperationID) || mirrorPath != document.State.MirrorPath || mirrorDigest != document.State.MirrorDigest || lastSeen != string(document.State.LastSeenRunID) || !sameOptionalTime(deletedAt, document.State.DeletedAt) || storedDigest != digest {
			return false, nil
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate prepared candidates: %w", err)
	}
	return index == len(documents), nil
}
func formatTime(value time.Time) time.Time { return value.UTC() }
func optionalTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatTime(value)
}
func nullableTime(value sql.NullTime) any {
	if !value.Valid {
		return nil
	}
	return value.Time.UTC()
}
func sameOptionalTime(stored sql.NullTime, expected time.Time) bool {
	if !stored.Valid {
		return expected.IsZero()
	}
	return !expected.IsZero() && stored.Time.Equal(expected)
}
func validDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}
func validBounded(value string, maximum int, empty bool) bool {
	return len(value) <= maximum && (empty || value != "") && !strings.ContainsAny(value, "\x00\r\n")
}
func validFailure(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, char := range value {
		letter := char >= 'a' && char <= 'z'
		digit := char >= '0' && char <= '9'
		separator := index > 0 && (char == '_' || char == '.' || char == '-')
		if !letter && !digit && !separator {
			return false
		}
	}
	return true
}

func sourceExec(ctx context.Context, executor sourceExecutor, query string, args ...any) (sql.Result, error) {
	return executor.ExecContext(ctx, bindSourceQuery(query), args...)
}

func sourceQuery(ctx context.Context, queryer sourceQueryer, query string, args ...any) (*sql.Rows, error) {
	return queryer.QueryContext(ctx, bindSourceQuery(query), args...)
}

func sourceQueryRow(ctx context.Context, queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, query string, args ...any) *sql.Row {
	return queryer.QueryRowContext(ctx, bindSourceQuery(query), args...)
}

func bindSourceQuery(query string) string {
	var builder strings.Builder
	position := 0
	for _, char := range query {
		if char != '?' {
			builder.WriteRune(char)
			continue
		}
		position++
		_, _ = fmt.Fprintf(&builder, "$%d", position)
	}
	return builder.String()
}
