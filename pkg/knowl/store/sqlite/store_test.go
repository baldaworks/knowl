package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/internal/runnertest"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
	"github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
)

func TestDurableRunnerContract(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir()+"/runner.sqlite")
	if err != nil {
		t.Fatalf("open runner store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runnertest.Run(t, store, store, "runner_sqlite")
}

const (
	testLocalScope   = "local"
	testFixture      = "fixture"
	testSchemaDigest = "schema"
	testSharedPageID = "shared"
	testSourceID     = "engineering"
	testRevision     = "revision-1"
)

func TestResumableWorkContract(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/work-contract.sqlite"
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open contract store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	storetest.RunWorkContract(t, storetest.WorkHarness{
		Store: store,
		OpenPeer: func(t *testing.T) app.OperationStore {
			t.Helper()
			peer, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("open contract peer: %v", err)
			}
			t.Cleanup(func() { _ = peer.Close() })
			return peer
		},
		Expire: func(t *testing.T, _ knowl.ScopeRef, id knowl.OperationID) {
			t.Helper()
			if _, err := store.db.ExecContext(ctx, `UPDATE knowl_operations SET work_lease_expires_at = ? WHERE operation_id = ?`, time.Unix(1, 0).UTC().Format(time.RFC3339Nano), id); err != nil {
				t.Fatalf("expire contract work lease: %v", err)
			}
		},
		WorkAttempts: func(t *testing.T, scope knowl.ScopeRef, id knowl.OperationID) int {
			t.Helper()
			var attempts int
			if err := store.db.QueryRowContext(ctx, `SELECT work_attempt FROM knowl_operations WHERE scope = ? AND operation_id = ?`, scope, id).Scan(&attempts); err != nil {
				t.Fatalf("read contract work attempts: %v", err)
			}
			return attempts
		},
		IsConflict: func(err error) bool { return errors.Is(err, ErrConflict) },
		Scope:      "sqlite_contract",
	})
}

func TestSourceStateContract(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/source-contract.sqlite"
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open source contract store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	storetest.RunSourceContract(t, storetest.SourceHarness{
		Store: store,
		OpenPeer: func(t *testing.T) app.SourceStateStore {
			t.Helper()
			peer, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("open source-state peer: %v", err)
			}
			t.Cleanup(func() { _ = peer.Close() })
			return peer
		},
		IsConflict: func(err error) bool { return errors.Is(err, app.ErrSyncConflict) },
		Scope:      "sqlite_source_contract",
	})
	var operationRows int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM knowl_operations WHERE scope = ?`, "sqlite_source_contract").Scan(&operationRows); err != nil || operationRows != 0 {
		t.Fatalf("source sync operation rows = %d, %v", operationRows, err)
	}
}

func TestSourceStateSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/source-reopen.sqlite"
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(50, 0).UTC()
	activeRun := sqliteSourceRun("reopen-active", base)
	active := sqliteDocumentState(activeRun, false, time.Time{})
	finalizeSQLiteSourceRun(t, ctx, store, activeRun, active, app.SyncDocumentActive, "active-checkpoint", "active-generation", base.Add(time.Second))

	deleteRun := sqliteSourceRun("reopen-delete", base.Add(10*time.Second))
	tombstone := active
	tombstone.LastSeenRunID = deleteRun.ID
	tombstone.Deleted = true
	tombstone.DeletedAt = base.Add(11 * time.Second)
	finalizeSQLiteSourceRun(t, ctx, store, deleteRun, tombstone, app.SyncDocumentTombstone, "delete-checkpoint", "delete-generation", base.Add(11*time.Second))

	failureRun := sqliteSourceRun("reopen-failure", base.Add(20*time.Second))
	if _, _, err := store.BeginSync(ctx, app.BeginSyncRequest{Run: failureRun, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailSync(ctx, failureRun.Scope, failureRun.ID, "adapter_unavailable", base.Add(21*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	failed, err := store.SyncRun(ctx, failureRun.Scope, failureRun.ID)
	if err != nil || failed.Status != knowl.SyncStatusFailed || failed.FailureClass != "adapter_unavailable" {
		t.Fatalf("reopened failed run = %#v, %v", failed, err)
	}
	status, err := store.SourceStatus(ctx, failureRun.Scope, failureRun.SourceID)
	if err != nil || status.Status != knowl.SyncStatusFailed || status.Checkpoint != "delete-checkpoint" || status.LastAttemptRunID != failureRun.ID || status.LastSuccessfulRunID != deleteRun.ID || status.ConfigDigest != failureRun.ConfigDigest || status.Counts != (knowl.SyncCounts{}) || !status.CreatedAt.Equal(base) || !status.LastAttemptAt.Equal(base.Add(21*time.Second)) || !status.LastSuccessfulAt.Equal(base.Add(13*time.Second)) || !status.UpdatedAt.Equal(base.Add(21*time.Second)) {
		t.Fatalf("reopened SourceStatus() = %#v, %v", status, err)
	}
	reopened, err := store.DocumentState(ctx, failureRun.Scope, failureRun.SourceID, "docs/page.md")
	if err != nil || !reopened.Deleted || !reopened.DeletedAt.Equal(tombstone.DeletedAt) || reopened.Revision != active.Revision || reopened.AcceptedSource.ManifestRef != active.AcceptedSource.ManifestRef || reopened.MirrorPath != active.MirrorPath {
		t.Fatalf("reopened tombstone = %#v, %v", reopened, err)
	}
}

func sqliteSourceRun(id knowl.SyncRunID, at time.Time) knowl.SyncRun {
	return knowl.SyncRun{ID: id, Scope: "reopen", SourceID: testSourceID, ConfigDigest: strings.Repeat("a", 64), Status: knowl.SyncStatusScanning, StartedAt: at, UpdatedAt: at}
}

func sqliteDocumentState(run knowl.SyncRun, deleted bool, deletedAt time.Time) knowl.DocumentState {
	return knowl.DocumentState{
		Scope: run.Scope, SourceID: run.SourceID, DocumentID: "docs/page.md", Revision: testRevision,
		AcceptedSource: knowl.AcceptedSource{Scope: run.Scope, Source: knowl.SourceRef{Adapter: "wiki-filesystem", ID: "engineering/docs/page.md"}, Version: knowl.SourceVersion{Version: testRevision, Digest: strings.Repeat("d", 64)}, MediaType: "text/markdown", ManifestRef: "raw/manifest.json"},
		MirrorPath:     "wiki/sources/engineering/docs/page.md", MirrorDigest: strings.Repeat("e", 64), LastSeenRunID: run.ID, Deleted: deleted, DeletedAt: deletedAt,
	}
}

func finalizeSQLiteSourceRun(t *testing.T, ctx context.Context, store *Store, run knowl.SyncRun, state knowl.DocumentState, action app.SyncDocumentAction, checkpoint, generation string, at time.Time) {
	t.Helper()
	if _, _, err := store.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	counts := knowl.SyncCounts{Added: 1}
	if action == app.SyncDocumentTombstone {
		counts = knowl.SyncCounts{Deleted: 1}
	}
	prepared := app.PreparedSyncState{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, CompleteScan: true, Checkpoint: checkpoint, Counts: counts, Documents: []app.PreparedDocumentState{{Action: action, State: state}}, PreparedAt: at}
	digest, err := app.PreparedSyncDigest(prepared)
	if err != nil {
		t.Fatalf("canonical prepared digest: %v", err)
	}
	prepared.CandidateDigest = digest
	if _, err := store.PrepareSync(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	transition := app.SyncGeneration{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, Generation: generation, UpdatedAt: at.Add(time.Second)}
	if _, err := store.MarkContentCommitted(ctx, transition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkProjected(ctx, transition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeSync(ctx, app.SyncFinalization{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, CandidateDigest: digest, Generation: generation, Checkpoint: checkpoint, Counts: counts, FinalizedAt: at.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceMigrationPreservesVersionTwoOperation(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/source-migration.sqlite"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(60, 0).UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO knowl_operations (operation_id, scope, source_adapter, source_id, source_version, source_digest, schema_digest, status, created_at, updated_at, work_ready_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "legacy-operation", "legacy", "fixture", "document", "1", "digest", "schema", knowl.StatusReceived, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowl_pages (scope, page_id, path, title, body, digest, source_refs, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "legacy", "legacy-page", "wiki/legacy.md", "Legacy page", "preserved projection body", "page-digest", `["raw/legacy.json"]`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowl_projection_state (scope, schema_digest, snapshot_digest, page_count, link_count, ready_at) VALUES (?, ?, ?, ?, ?, ?)`, "legacy", "schema", "snapshot", 1, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	operation, err := store.Operation(ctx, "legacy", "legacy-operation")
	if err != nil || operation.ID != "legacy-operation" {
		t.Fatalf("preserved operation = %#v, %v", operation, err)
	}
	var title, body, digest, sourceRefs string
	var sourceID, sourceDocument sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT title, body, digest, source_refs, source_id, source_document FROM knowl_pages WHERE scope = ? AND page_id = ?`, "legacy", "legacy-page").Scan(&title, &body, &digest, &sourceRefs, &sourceID, &sourceDocument); err != nil {
		t.Fatalf("read preserved projection page: %v", err)
	}
	if title != "Legacy page" || body != "preserved projection body" || digest != "page-digest" || sourceRefs != `["raw/legacy.json"]` || sourceID.Valid || sourceDocument.Valid {
		t.Fatalf("preserved projection page = %q %q %q %q %#v %#v", title, body, digest, sourceRefs, sourceID, sourceDocument)
	}
	var snapshotDigest string
	var pageCount, linkCount int
	if err := store.db.QueryRowContext(ctx, `SELECT snapshot_digest, page_count, link_count FROM knowl_projection_state WHERE scope = ?`, "legacy").Scan(&snapshotDigest, &pageCount, &linkCount); err != nil {
		t.Fatalf("read preserved projection state: %v", err)
	}
	if snapshotDigest != "snapshot" || pageCount != 1 || linkCount != 0 {
		t.Fatalf("preserved projection state = %q %d %d", snapshotDigest, pageCount, linkCount)
	}
	var syncRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowl_sync_runs`).Scan(&syncRows); err != nil || syncRows != 0 {
		t.Fatalf("sync rows = %d, %v", syncRows, err)
	}
}

func TestResumableMigrationPreservesVersionOneOperations(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/migration.sqlite"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open version-one fixture: %v", err)
	}
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("open migration fixtures: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, directory)
	if err != nil {
		t.Fatalf("create version-one provider: %v", err)
	}
	if _, err := provider.UpTo(ctx, 1); err != nil {
		t.Fatalf("migrate fixture to version one: %v", err)
	}
	const (
		scope       = "sqlite_migration"
		committedID = "sqlite_migration:fixture:committed@1#aaaaaaaaaaaaaaaa"
		applyingID  = "sqlite_migration:fixture:applying@1#bbbbbbbbbbbbbbbb"
		failedID    = "sqlite_migration:fixture:failed@1#cccccccccccccccc"
	)
	createdAt := time.Unix(10, 0).UTC().Format(time.RFC3339Nano)
	leaseExpiry := time.Unix(100, 0).UTC().Format(time.RFC3339Nano)
	insert := `INSERT INTO knowl_operations (
		operation_id, scope, source_adapter, source_id, source_version, source_digest,
		schema_digest, status, attempt, plan_digest, failure_class, commit_generation,
		lease_token, lease_expires_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, '1', ?, 'schema-v1', ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := db.ExecContext(ctx, insert, committedID, scope, testFixture, "committed", strings.Repeat("a", 64), knowl.StatusCommitted, 2, "plan-committed", "", "generation-1", "", "", createdAt, createdAt); err != nil {
		t.Fatalf("insert committed version-one row: %v", err)
	}
	if _, err := db.ExecContext(ctx, insert, applyingID, scope, testFixture, "applying", strings.Repeat("b", 64), knowl.StatusApplying, 3, "plan-applying", "", "", "apply-owner", leaseExpiry, createdAt, createdAt); err != nil {
		t.Fatalf("insert applying version-one row: %v", err)
	}
	if _, err := db.ExecContext(ctx, insert, failedID, scope, testFixture, "failed", strings.Repeat("c", 64), knowl.StatusFailed, 1, "plan-failed", "provider", "", "", "", createdAt, createdAt); err != nil {
		t.Fatalf("insert failed version-one row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version-one fixture: %v", err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("apply resumable migration: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	committed, err := store.Operation(ctx, scope, committedID)
	if err != nil || committed.Status != knowl.StatusCommitted || committed.Attempt != 2 {
		t.Fatalf("migrated committed operation = %#v, err = %v", committed, err)
	}
	applying, err := store.Operation(ctx, scope, applyingID)
	if err != nil || applying.Status != knowl.StatusApplying || applying.Attempt != 3 {
		t.Fatalf("migrated applying operation = %#v, err = %v", applying, err)
	}
	failed, err := store.Operation(ctx, scope, failedID)
	if err != nil || failed.Status != knowl.StatusFailed || failed.Failure == nil || failed.Failure.Class != "provider" {
		t.Fatalf("migrated failed operation = %#v, err = %v", failed, err)
	}
	var committedGeneration string
	if err := store.db.QueryRowContext(ctx, `SELECT commit_generation FROM knowl_operations WHERE operation_id = ?`, committedID).Scan(&committedGeneration); err != nil {
		t.Fatalf("read preserved commit generation: %v", err)
	}
	if committedGeneration != "generation-1" {
		t.Fatalf("preserved commit generation = %q", committedGeneration)
	}
	var planDigest, generation, leaseToken, migratedLeaseExpiry, workReadyAt string
	if err := store.db.QueryRowContext(ctx, `SELECT plan_digest, commit_generation, lease_token, lease_expires_at, work_ready_at FROM knowl_operations WHERE operation_id = ?`, applyingID).Scan(
		&planDigest, &generation, &leaseToken, &migratedLeaseExpiry, &workReadyAt,
	); err != nil {
		t.Fatalf("read preserved applying fields: %v", err)
	}
	if planDigest != "plan-applying" || generation != "" || leaseToken != "apply-owner" || migratedLeaseExpiry != leaseExpiry || workReadyAt != createdAt {
		t.Fatalf("preserved applying fields = %q %q %q %q %q", planDigest, generation, leaseToken, migratedLeaseExpiry, workReadyAt)
	}
	failures, err := store.DescriptorFailures(ctx, scope, 10)
	if err != nil || len(failures) != 1 || failures[0] != applyingID {
		t.Fatalf("migrated descriptor failures = %v, err = %v", failures, err)
	}
	ready, err := store.ResumeReady(ctx, scope, 10)
	if err != nil || len(ready) != 0 {
		t.Fatalf("migrated ready operations = %v, err = %v", ready, err)
	}
}

func TestStoreMigratesAndReservesIdempotently(t *testing.T) {
	const pageID = "one"
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/knowl.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	key := knowl.OperationKey{Scope: testLocalScope, Source: knowl.SourceRef{Adapter: testFixture, ID: pageID}, Version: knowl.SourceVersion{Version: "1", Digest: "digest-a"}}
	meta := knowl.OperationMeta{Key: key, SchemaDigest: testSchemaDigest, CreatedAt: time.Unix(1, 0).UTC()}
	first, err := store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatalf("reserve operation: %v", err)
	}
	second, err := store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatalf("replay operation: %v", err)
	}
	if first.ID != second.ID || first.Status != knowl.StatusReceived {
		t.Fatalf("replay operation changed: %#v %#v", first, second)
	}
	conflict := key
	conflict.Version.Digest = "digest-b"
	if _, err := store.Reserve(ctx, conflict, meta); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error = %v, want conflict", err)
	}
}

func TestStorePersistsOperationTransitionsAndReopens(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/knowl.sqlite"
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	key := knowl.OperationKey{Scope: testLocalScope, Source: knowl.SourceRef{Adapter: testFixture, ID: "transition"}, Version: knowl.SourceVersion{Version: "1", Digest: "digest"}}
	operation, err := store.Reserve(ctx, key, knowl.OperationMeta{Key: key, SchemaDigest: testSchemaDigest})
	if err != nil {
		t.Fatalf("reserve operation: %v", err)
	}
	if err := store.SavePlan(ctx, operation.ID, knowl.PlanSummary{OperationID: string(operation.ID), Digest: "plan"}); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := store.MarkAwaitingReview(ctx, operation.ID); err != nil {
		t.Fatalf("mark review: %v", err)
	}
	lease := knowl.Lease{Token: "lease-1", ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.MarkApplying(ctx, operation.ID, lease); err != nil {
		t.Fatalf("mark applying: %v", err)
	}
	if err := store.MarkApplying(ctx, operation.ID, knowl.Lease{Token: "lease-2", ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("active lease error = %v, want lease conflict", err)
	}
	if err := store.CommitOutcome(ctx, operation.ID, knowl.ContentCommit{OperationID: string(operation.ID), Generation: "generation"}); err != nil {
		t.Fatalf("commit operation: %v", err)
	}
	if err := store.CommitOutcome(ctx, operation.ID, knowl.ContentCommit{OperationID: string(operation.ID), Generation: "generation"}); err != nil {
		t.Fatalf("replay commit: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = store.Close() }()
	reopened, err := store.Operation(ctx, key.Scope, operation.ID)
	if err != nil {
		t.Fatalf("read reopened operation: %v", err)
	}
	if reopened.Status != knowl.StatusCommitted || reopened.Attempt != 1 {
		t.Fatalf("reopened operation = %#v", reopened)
	}
}

func TestStorePersistsAndReplaysExecutionDescriptor(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/knowl.sqlite"
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	key, meta := executionFixture("local", "descriptor", time.Unix(1, 0).UTC())
	first, err := store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatalf("reserve operation: %v", err)
	}
	replayed, err := store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatalf("replay operation: %v", err)
	}
	if replayed.New || replayed.ID != first.ID || replayed.Descriptor.Schema.Digest != meta.Schema.Digest {
		t.Fatalf("replayed reservation = %#v", replayed)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = store.Close() }()
	descriptor, err := store.Execution(ctx, key.Scope, first.ID)
	if err != nil {
		t.Fatalf("read descriptor: %v", err)
	}
	if descriptor.Source != meta.AcceptedSource || string(descriptor.Schema.Content) != string(meta.Schema.Content) {
		t.Fatalf("descriptor = %#v", descriptor)
	}
}

func TestStoreClaimsReleasesAndReclaimsReadyWork(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/knowl.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	key, meta := executionFixture("local", "claim", time.Unix(1, 0).UTC())
	reservation, err := store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatalf("reserve operation: %v", err)
	}
	ready, err := store.ResumeReady(ctx, key.Scope, 10)
	if err != nil || len(ready) != 1 || ready[0] != reservation.ID {
		t.Fatalf("ready = %v, err = %v", ready, err)
	}
	firstLease := knowl.WorkLease{Token: "worker-1", ExpiresAt: time.Now().Add(time.Minute)}
	claim, err := store.ClaimReady(ctx, key.Scope, firstLease)
	if err != nil {
		t.Fatalf("claim ready: %v", err)
	}
	if claim.Operation.ID != reservation.ID || claim.Lease != firstLease || claim.Descriptor.OperationID != reservation.ID {
		t.Fatalf("claim = %#v", claim)
	}
	if _, err := store.ClaimReady(ctx, key.Scope, knowl.WorkLease{Token: "worker-2", ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, app.ErrNoReadyOperation) {
		t.Fatalf("second claim error = %v", err)
	}
	if err := store.RenewClaim(ctx, key.Scope, reservation.ID, "wrong", knowl.WorkLease{Token: "worker-3", ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, app.ErrWorkLeaseConflict) {
		t.Fatalf("wrong-token renewal error = %v", err)
	}
	renewed := knowl.WorkLease{Token: "worker-1-renewed", ExpiresAt: time.Now().Add(2 * time.Minute)}
	if err := store.RenewClaim(ctx, key.Scope, reservation.ID, firstLease.Token, renewed); err != nil {
		t.Fatalf("renew claim: %v", err)
	}
	if err := store.ReleaseClaim(ctx, key.Scope, reservation.ID, renewed.Token); err != nil {
		t.Fatalf("release claim: %v", err)
	}
	if ready, err = store.ResumeReady(ctx, key.Scope, 1); err != nil || len(ready) != 1 {
		t.Fatalf("ready after release = %v, err = %v", ready, err)
	}
	expired := knowl.WorkLease{Token: "expired-owner", ExpiresAt: time.Now().Add(time.Minute)}
	if _, err := store.ClaimReady(ctx, key.Scope, expired); err != nil {
		t.Fatalf("claim before expiry fixture: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE knowl_operations SET work_lease_expires_at = ? WHERE operation_id = ?`, time.Unix(1, 0).UTC().Format(time.RFC3339Nano), reservation.ID); err != nil {
		t.Fatalf("expire work lease: %v", err)
	}
	reclaimed, err := store.ClaimReady(ctx, key.Scope, knowl.WorkLease{Token: "new-owner", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || reclaimed.Operation.ID != reservation.ID {
		t.Fatalf("reclaimed = %#v, err = %v", reclaimed, err)
	}
}

func TestStoreClaimsAreScopedExclusiveAndTerminalSafe(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/knowl.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	localKey, localMeta := executionFixture("local", "shared", time.Unix(1, 0).UTC())
	otherKey, otherMeta := executionFixture("other", "shared", time.Unix(1, 0).UTC())
	local, err := store.Reserve(ctx, localKey, localMeta)
	if err != nil {
		t.Fatalf("reserve local: %v", err)
	}
	if _, err := store.Reserve(ctx, otherKey, otherMeta); err != nil {
		t.Fatalf("reserve other: %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, claimErr := store.ClaimReady(ctx, localKey.Scope, knowl.WorkLease{
				Token: fmt.Sprintf("worker-%d", index), ExpiresAt: time.Now().Add(time.Minute),
			})
			results <- claimErr
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	var successes, empty int
	for claimErr := range results {
		switch {
		case claimErr == nil:
			successes++
		case errors.Is(claimErr, app.ErrNoReadyOperation):
			empty++
		default:
			t.Fatalf("claim error = %v", claimErr)
		}
	}
	if successes != 1 || empty != 1 {
		t.Fatalf("claim results: successes=%d empty=%d", successes, empty)
	}
	if err := store.Fail(ctx, local.ID, knowl.Failure{Class: testFixture, OperationID: string(local.ID)}); err != nil {
		t.Fatalf("fail terminal operation: %v", err)
	}
	if ready, err := store.ResumeReady(ctx, localKey.Scope, 10); err != nil || len(ready) != 0 {
		t.Fatalf("terminal local ready = %v, err = %v", ready, err)
	}
	otherReady, err := store.ResumeReady(ctx, otherKey.Scope, 10)
	if err != nil || len(otherReady) != 1 {
		t.Fatalf("other-scope ready = %v, err = %v", otherReady, err)
	}
}

func TestStoreClassifiesLegacyNonTerminalDescriptorFailure(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/knowl.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	key := knowl.OperationKey{Scope: "local", Source: knowl.SourceRef{Adapter: testFixture, ID: "legacy"}, Version: knowl.SourceVersion{Version: "1", Digest: strings.Repeat("a", 64)}}
	legacy, err := store.Reserve(ctx, key, knowl.OperationMeta{Key: key, SchemaDigest: "historical-schema"})
	if err != nil {
		t.Fatalf("reserve legacy operation: %v", err)
	}
	if _, err := store.Execution(ctx, key.Scope, legacy.ID); !errors.Is(err, app.ErrExecutionDescriptorUnavailable) {
		t.Fatalf("legacy descriptor error = %v", err)
	}
	failures, err := store.DescriptorFailures(ctx, key.Scope, 10)
	if err != nil || len(failures) != 1 || failures[0] != legacy.ID {
		t.Fatalf("descriptor failures = %v, err = %v", failures, err)
	}
	if ready, err := store.ResumeReady(ctx, key.Scope, 10); err != nil || len(ready) != 0 {
		t.Fatalf("legacy ready = %v, err = %v", ready, err)
	}
}

func TestStoreClaimSkipsInvalidDescriptorWithoutStarvingReadyWork(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/knowl.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	invalidKey, invalidMeta := executionFixture("local", "invalid-first", time.Unix(1, 0).UTC())
	invalid, err := store.Reserve(ctx, invalidKey, invalidMeta)
	if err != nil {
		t.Fatalf("reserve invalid fixture: %v", err)
	}
	validKey, validMeta := executionFixture("local", "valid-second", time.Unix(2, 0).UTC())
	valid, err := store.Reserve(ctx, validKey, validMeta)
	if err != nil {
		t.Fatalf("reserve valid fixture: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE knowl_operations SET schema_snapshot = ? WHERE operation_id = ?`, []byte("corrupt"), invalid.ID); err != nil {
		t.Fatalf("corrupt descriptor: %v", err)
	}
	claim, err := store.ClaimReady(ctx, "local", knowl.WorkLease{Token: "worker", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("claim ready: %v", err)
	}
	if claim.Operation.ID != valid.ID {
		t.Fatalf("claimed %q, want %q", claim.Operation.ID, valid.ID)
	}
	failures, err := store.DescriptorFailures(ctx, "local", 10)
	if err != nil || len(failures) != 1 || failures[0] != invalid.ID {
		t.Fatalf("descriptor failures = %v, err = %v", failures, err)
	}
}

func executionFixture(scope knowl.ScopeRef, id string, createdAt time.Time) (knowl.OperationKey, knowl.OperationMeta) {
	schema := []byte("# Schema\n\nversion: 1\n")
	digest := fmt.Sprintf("%x", sha256.Sum256(schema))
	key := knowl.OperationKey{
		Scope:   scope,
		Source:  knowl.SourceRef{Adapter: testFixture, ID: id},
		Version: knowl.SourceVersion{Version: "1", Digest: strings.Repeat("a", 64)},
	}
	return key, knowl.OperationMeta{
		Key: key,
		AcceptedSource: knowl.AcceptedSource{
			Scope: scope, Source: key.Source, Version: key.Version,
			MediaType: "text/markdown", ManifestRef: "raw/source/version/manifest.yaml",
		},
		Schema:       knowl.SchemaDocument{Scope: scope, Digest: digest, Version: "1", Content: schema},
		SchemaDigest: digest,
		CreatedAt:    createdAt,
	}
}

func TestStoreRebuildsAndSearchesCanonicalSnapshot(t *testing.T) {
	const pageID = "one"
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/knowl.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	sourceDocument := &knowl.SourceDocument{
		SourceID: testSourceID, DocumentID: "docs/one.md", Revision: testRevision, URI: "file:///source/docs/one.md",
	}
	snapshot := knowl.WorkspaceSnapshot{
		Scope:      "local",
		CapturedAt: time.Unix(1, 0).UTC(),
		Pages: []knowl.PageSnapshot{
			{ID: pageID, Path: "wiki/sources/engineering/docs/one.md", Title: "One", Content: "alpha knowledge", Digest: "digest", SourceRefs: []string{"fixture:one@1"}, SourceDocument: sourceDocument},
			{ID: "curated", Path: "wiki/curated.md", Title: "Curated", Content: "curated knowledge", Digest: "curated-digest"},
		},
		Links: []knowl.LinkReference{{From: pageID, To: "two", Relation: "related"}},
	}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("rebuild projections: %v", err)
	}
	results, err := store.Search(ctx, "local", "alpha", knowl.ReadLimits{Pages: 5, Characters: 20}, nil)
	if err != nil {
		t.Fatalf("search projection: %v", err)
	}
	if len(results) != 1 || !results[0].Untrusted || results[0].ID != pageID || results[0].SourceDocument == nil || *results[0].SourceDocument != *sourceDocument {
		t.Fatalf("search results = %#v", results)
	}
	var curatedSourceID, curatedSourceDocument sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT source_id, source_document FROM knowl_pages WHERE scope = ? AND page_id = ?`, snapshot.Scope, "curated").Scan(&curatedSourceID, &curatedSourceDocument); err != nil {
		t.Fatalf("read curated projection metadata: %v", err)
	}
	if curatedSourceID.Valid || curatedSourceDocument.Valid {
		t.Fatalf("curated projection metadata = %#v, %#v; want NULL", curatedSourceID, curatedSourceDocument)
	}
	links, err := store.Links(ctx, "local", pageID, knowl.ReadLimits{Pages: 5})
	if err != nil {
		t.Fatalf("links projection: %v", err)
	}
	if len(links) != 1 || !links[0].Untrusted {
		t.Fatalf("links = %#v", links)
	}
}

func TestStoreRebuildIsScopedAndDetectsDrift(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/knowl.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	sourceDocument := &knowl.SourceDocument{
		SourceID: testSourceID, DocumentID: "shared.md", Revision: testRevision, URI: "file:///source/shared.md",
	}
	snapshot := knowl.WorkspaceSnapshot{
		Scope:        testLocalScope,
		SchemaDigest: testSchemaDigest,
		Pages:        []knowl.PageSnapshot{{ID: testSharedPageID, Path: "wiki/shared.md", Title: "Shared", Content: "local alpha", Digest: "digest-local", SourceDocument: sourceDocument}},
		Links:        []knowl.LinkReference{{From: testSharedPageID, To: "other", Relation: "related"}},
	}
	other := knowl.WorkspaceSnapshot{
		Scope:        "other",
		SchemaDigest: "schema",
		Pages:        []knowl.PageSnapshot{{ID: testSharedPageID, Path: "wiki/shared.md", Title: "Shared", Content: "other beta", Digest: "digest-other"}},
	}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("rebuild local projection: %v", err)
	}
	if err := store.Rebuild(ctx, other); err != nil {
		t.Fatalf("rebuild other projection: %v", err)
	}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("repeat local rebuild: %v", err)
	}
	results, err := store.Search(ctx, "local", "alpha", knowl.ReadLimits{Pages: 5}, nil)
	if err != nil {
		t.Fatalf("search local projection: %v", err)
	}
	if len(results) != 1 || results[0].ID != testSharedPageID {
		t.Fatalf("local search results = %#v", results)
	}
	otherResults, err := store.Search(ctx, "other", "beta", knowl.ReadLimits{Pages: 5}, nil)
	if err != nil {
		t.Fatalf("search other projection: %v", err)
	}
	if len(otherResults) != 1 || otherResults[0].ID != testSharedPageID {
		t.Fatalf("other search results = %#v", otherResults)
	}
	state, err := store.ProjectionStatus(ctx, "local")
	if err != nil {
		t.Fatalf("projection status: %v", err)
	}
	if state.PageCount != 1 || state.LinkCount != 1 {
		t.Fatalf("projection state = %#v", state)
	}
	if err := store.CheckProjection(ctx, snapshot); err != nil {
		t.Fatalf("check projection: %v", err)
	}
	drifted := snapshot
	drifted.Pages = append([]knowl.PageSnapshot(nil), snapshot.Pages...)
	drifted.Pages[0].Content = "changed canonical text"
	if err := store.CheckProjection(ctx, drifted); !errors.Is(err, ErrProjectionDrift) {
		t.Fatalf("drift error = %v, want projection drift", err)
	}
	metadataDrifted := snapshot
	metadataDrifted.Pages = append([]knowl.PageSnapshot(nil), snapshot.Pages...)
	changedDocument := *sourceDocument
	changedDocument.Revision = "revision-2"
	metadataDrifted.Pages[0].SourceDocument = &changedDocument
	if err := store.CheckProjection(ctx, metadataDrifted); !errors.Is(err, ErrProjectionDrift) {
		t.Fatalf("source metadata drift error = %v, want projection drift", err)
	}
}

func TestStoreProjectsEmptyCanonicalScope(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/knowl.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	snapshot := knowl.WorkspaceSnapshot{Scope: "empty", SchemaDigest: "schema"}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("rebuild empty scope: %v", err)
	}
	if _, err := store.ProjectionStatus(ctx, snapshot.Scope); err != nil {
		t.Fatalf("empty scope readiness: %v", err)
	}
}
