//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
)

const (
	postgresLifecycleMigrationScope    = "migration"
	postgresLifecycleMigrationSource   = "legacy"
	postgresLifecycleMigrationRevision = "one"
	postgresLifecycleMediaType         = "text/markdown"
	postgresLifecyclePeerSource        = "source"
	postgresLifecycleCursor            = "wiki/a.md"
	postgresLifecycleLockedScope       = "locked"
)

func TestPostgresSourceLifecycleContract(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("KNOWL_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set KNOWL_POSTGRES_DSN to run PostgreSQL integration tests")
	}
	runSourceLifecyclePostgres(t, dsn)
}

func runSourceLifecycleMigrationPostgres(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	schema := fmt.Sprintf("source_lifecycle_migration_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`) })
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, directory, goose.WithGoMigrations(operatorOperationsMigration()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 21); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_810_000_000, 0).UTC()
	digest := strings.Repeat("a", 64)
	const replacementScope = "migration_replacement"
	legacyStore := &Store{db: db}
	for _, run := range []knowl.SyncRun{
		{ID: "migration-run", Scope: postgresLifecycleMigrationScope, SourceID: postgresLifecycleMigrationSource, ConfigDigest: digest, Status: knowl.SyncStatusScanning, StartedAt: now, UpdatedAt: now},
		{ID: "replacement-run", Scope: replacementScope, SourceID: postgresLifecycleMigrationSource, ConfigDigest: digest, Status: knowl.SyncStatusScanning, StartedAt: now, UpdatedAt: now},
	} {
		if _, replay, err := legacyStore.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil || replay {
			t.Fatalf("create legacy source %q: replay=%v err=%v", run.Scope, replay, err)
		}
	}
	accepted := knowl.AcceptedSource{
		Scope: postgresLifecycleMigrationScope, Source: knowl.SourceRef{Adapter: string(knowl.SourceAdapterFilesystem), ID: "legacy/doc.md"},
		Version: knowl.SourceVersion{Version: postgresLifecycleMigrationRevision, Digest: strings.Repeat("b", 64)}, MediaType: postgresLifecycleMediaType,
		SourceDocument: knowl.SourceDocument{SourceID: postgresLifecycleMigrationSource, DocumentID: "doc.md", Revision: postgresLifecycleMigrationRevision, URI: "file:///srv/wiki/doc.md"},
		ManifestRef:    "raw/manifest.json",
	}
	encodedAccepted, err := json.Marshal(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowl_source_documents (scope, source_id, document_id, revision, accepted_source, last_seen_run_id, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, postgresLifecycleMigrationScope, postgresLifecycleMigrationSource, "doc.md", postgresLifecycleMigrationRevision, encodedAccepted, "run", now, now); err != nil {
		t.Fatal(err)
	}
	operationKey, operationMeta := postgresExecutionFixture(postgresLifecycleMigrationScope, postgresLifecycleMigrationSource, now)
	reservation, err := legacyStore.Reserve(ctx, operationKey, operationMeta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 23); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db}
	state, err := store.SourceLifecycle(ctx, postgresLifecycleMigrationScope, knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: postgresLifecycleMigrationSource})
	if err != nil {
		t.Fatal(err)
	}
	if state.State != knowl.SourceLifecycleActive || state.ConfigDigest != digest || state.AuthorityEpoch != 0 || state.ActivatedEpoch != 0 || !state.ActivatedAt.Equal(now) || !state.UpdatedAt.Equal(now) {
		t.Fatalf("migrated lifecycle = %#v", state)
	}
	authority, err := store.SourceLifecycleAuthority(ctx, postgresLifecycleMigrationScope)
	if err != nil || authority.Generation != "" || authority.Epoch != 0 || !authority.UpdatedAt.Equal(now) {
		t.Fatalf("migrated authority = %#v, %v", authority, err)
	}
	status, err := store.SourceStatus(ctx, postgresLifecycleMigrationScope, postgresLifecycleMigrationSource)
	if err != nil {
		t.Fatal(err)
	}
	if status.Type != knowl.SourceTypeFilesystem || status.ConfigDigest != digest || status.LastAttemptRunID != "migration-run" {
		t.Fatalf("preserved source status = %#v", status)
	}
	documents, err := store.ListSourceDocuments(ctx, postgresLifecycleMigrationScope, postgresLifecycleMigrationSource, app.OperatorReadOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents.Items) != 1 || documents.Items[0].ID != "doc.md" || documents.Items[0].AcceptedRevision != postgresLifecycleMigrationRevision {
		t.Fatalf("preserved source documents = %#v", documents)
	}
	operation, err := store.Operation(ctx, postgresLifecycleMigrationScope, reservation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != knowl.StatusReceived || operation.Key != operationKey {
		t.Fatalf("preserved operation = %#v", operation)
	}
	descriptor, err := store.Execution(ctx, postgresLifecycleMigrationScope, reservation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Source != operationMeta.AcceptedSource || descriptor.Schema.Digest != operationMeta.Schema.Digest {
		t.Fatalf("preserved execution descriptor = %#v", descriptor)
	}
	authoritativeAt := now.Add(time.Second)
	empty := app.SourceLifecycleReconcileRequest{
		Scope: postgresLifecycleMigrationScope, AuthorityGeneration: app.SourceLifecycleGeneration(nil), UpdatedAt: authoritativeAt,
	}
	if err := store.ReconcileSourceLifecycles(ctx, empty); err != nil {
		t.Fatalf("first authoritative empty inventory: %v", err)
	}
	retiring, err := store.SourceLifecycle(ctx, postgresLifecycleMigrationScope, knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: postgresLifecycleMigrationSource})
	if err != nil {
		t.Fatal(err)
	}
	if retiring.State != knowl.SourceLifecycleRetiring || retiring.AuthorityEpoch != 1 || retiring.ActivatedEpoch != 1 || retiring.RetirementEpoch != 1 ||
		retiring.ActivatedGeneration != empty.AuthorityGeneration || retiring.RetirementGeneration != empty.AuthorityGeneration {
		t.Fatalf("retired migrated lifecycle = %#v", retiring)
	}
	gitOwner := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: postgresLifecycleMigrationSource}
	gitDigest := strings.Repeat("c", 64)
	desired := []app.DesiredSourceOwner{{Owner: gitOwner, ConfigDigest: gitDigest}}
	replacement := app.SourceLifecycleReconcileRequest{
		Scope: replacementScope, Desired: desired,
		AuthorityGeneration: app.SourceLifecycleGeneration(desired), UpdatedAt: authoritativeAt,
	}
	if err := store.ReconcileSourceLifecycles(ctx, replacement); err != nil {
		t.Fatalf("first authoritative replacement inventory: %v", err)
	}
	oldOwner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: postgresLifecycleMigrationSource}
	oldState, err := store.SourceLifecycle(ctx, replacementScope, oldOwner)
	if err != nil {
		t.Fatal(err)
	}
	wantOld := knowl.SourceLifecycle{
		Scope: replacementScope, Owner: oldOwner, State: knowl.SourceLifecycleRetiring, ConfigDigest: digest,
		AuthorityGeneration: replacement.AuthorityGeneration, AuthorityEpoch: 1,
		ActivatedGeneration: replacement.AuthorityGeneration, ActivatedEpoch: 1,
		RetirementGeneration: replacement.AuthorityGeneration, RetirementEpoch: 1,
		ReplacementPending: 1,
		ActivatedAt:        now, RetiringAt: authoritativeAt, UpdatedAt: authoritativeAt,
	}
	if oldState != wantOld {
		t.Fatalf("replacement old owner = %#v, want %#v", oldState, wantOld)
	}
	newState, err := store.SourceLifecycle(ctx, replacementScope, gitOwner)
	if err != nil {
		t.Fatal(err)
	}
	wantNew := knowl.SourceLifecycle{
		Scope: replacementScope, Owner: gitOwner, State: knowl.SourceLifecycleActive, ConfigDigest: gitDigest,
		AuthorityGeneration: replacement.AuthorityGeneration, AuthorityEpoch: 1,
		ActivatedGeneration: replacement.AuthorityGeneration, ActivatedEpoch: 1,
		ActivatedAt: authoritativeAt, UpdatedAt: authoritativeAt,
	}
	if newState != wantNew {
		t.Fatalf("replacement new owner = %#v, want %#v", newState, wantNew)
	}
}

func runSourceReadinessMigrationPostgres(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	schema := fmt.Sprintf("source_readiness_migration_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`) })
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, directory, goose.WithGoMigrations(operatorOperationsMigration()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 22); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: db, dsn: parsed.String()}
	base := time.Unix(1_820_000_000, 0).UTC()
	const scope = knowl.ScopeRef("legacy_readiness_migration")
	tests := []struct {
		name   string
		status knowl.SyncStatus
	}{
		{name: "prepared", status: knowl.SyncStatusPrepared},
		{name: "content_committed", status: knowl.SyncStatusContentCommitted},
		{name: "projected", status: knowl.SyncStatusProjected},
	}
	prepared := make(map[string]app.PreparedSyncState, len(tests))
	for index, test := range tests {
		sourceID := knowl.SourceID("legacy-" + test.name)
		run := knowl.SyncRun{
			ID: knowl.SyncRunID("legacy-" + test.name), Scope: scope, SourceID: sourceID,
			ConfigDigest: strings.Repeat(string(rune('a'+index)), 64), Status: knowl.SyncStatusScanning,
			StartedAt: base, UpdatedAt: base,
		}
		if _, _, err := legacy.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil {
			t.Fatalf("seed %s run: %v", test.name, err)
		}
		state := app.PreparedSyncState{
			RunID: run.ID, Scope: scope, SourceID: sourceID, CompleteScan: true,
			Checkpoint: "legacy-checkpoint", PreparedAt: base,
		}
		digest, err := app.PreparedSyncDigestVersioned(state, app.PreparedSyncDigestLegacyV0)
		if err != nil {
			t.Fatalf("legacy %s digest: %v", test.name, err)
		}
		state.CandidateDigest = digest
		prepared[test.name] = state
		generation := ""
		if test.status != knowl.SyncStatusPrepared {
			generation = "legacy-generation"
		}
		if _, err := sourceExec(ctx, db, `UPDATE knowl_sync_runs SET status = ?, complete_scan = TRUE,
candidate_digest = ?, checkpoint = ?, content_generation = ?, updated_at = ? WHERE run_id = ?`,
			test.status, digest, state.Checkpoint, generation, base, run.ID); err != nil {
			t.Fatalf("persist %s run: %v", test.name, err)
		}
	}
	mismatchRun := knowl.SyncRun{
		ID: "legacy-mismatch", Scope: scope, SourceID: "legacy-mismatch", ConfigDigest: strings.Repeat("f", 64),
		Status: knowl.SyncStatusScanning, StartedAt: base, UpdatedAt: base,
	}
	if _, _, err := legacy.BeginSync(ctx, app.BeginSyncRequest{Run: mismatchRun, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceExec(ctx, db, `UPDATE knowl_sync_runs SET status = ?, complete_scan = TRUE,
candidate_digest = ?, checkpoint = ?, updated_at = ? WHERE run_id = ?`, knowl.SyncStatusPrepared,
		strings.Repeat("e", 64), "legacy-checkpoint", base, mismatchRun.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 23); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, dsn: parsed.String()}
	for index, test := range tests {
		state := prepared[test.name]
		read, err := store.PreparedSync(ctx, scope, state.RunID)
		if err != nil || read.MaintenanceReady || read.CandidateDigest != state.CandidateDigest {
			t.Fatalf("read migrated %s = %#v, %v", test.name, read, err)
		}
		generation := "legacy-generation"
		if test.status == knowl.SyncStatusPrepared {
			if _, err := store.MarkContentCommitted(ctx, app.SyncGeneration{
				RunID: state.RunID, Scope: scope, SourceID: state.SourceID,
				Generation: generation, UpdatedAt: base.Add(time.Duration(index+1) * time.Second),
			}); err != nil {
				t.Fatalf("resume %s content commit: %v", test.name, err)
			}
		}
		if test.status != knowl.SyncStatusProjected {
			if _, err := store.MarkProjected(ctx, app.SyncGeneration{
				RunID: state.RunID, Scope: scope, SourceID: state.SourceID,
				Generation: generation, UpdatedAt: base.Add(time.Duration(index+4) * time.Second),
			}); err != nil {
				t.Fatalf("resume %s projection: %v", test.name, err)
			}
		}
		finalized, err := store.FinalizeSync(ctx, app.SyncFinalization{
			RunID: state.RunID, Scope: scope, SourceID: state.SourceID,
			CandidateDigest: state.CandidateDigest, Generation: generation, Checkpoint: state.Checkpoint,
			FinalizedAt: base.Add(time.Duration(index+8) * time.Second),
		})
		if err != nil || finalized.Status != knowl.SyncStatusSucceeded {
			t.Fatalf("resume %s finalization = %#v, %v", test.name, finalized, err)
		}
	}
	if _, err := store.PreparedSync(ctx, scope, mismatchRun.ID); !errors.Is(err, app.ErrSyncConflict) {
		t.Fatalf("mismatched legacy digest error = %v, want ErrSyncConflict", err)
	}
}

func runSourceLifecyclePostgres(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Run("writer-lock-cancellation", func(t *testing.T) {
		testPostgresSourceLifecycleMutationLockCancellation(t, store)
	})
	t.Run("reconcile-readiness-concurrency", func(t *testing.T) {
		testPostgresReconcileReadinessConcurrency(t, dsn)
	})
	t.Run("reconcile-scan-concurrency", func(t *testing.T) {
		testPostgresReconcileScanConcurrency(t, dsn)
	})
	t.Run("contract", func(t *testing.T) {
		storetest.RunSourceLifecycleContract(t, storetest.SourceLifecycleHarness{
			Store:  store,
			Legacy: store,
			OpenPeer: func(t *testing.T) app.SourceLifecycleStore {
				t.Helper()
				peer, err := Open(ctx, dsn)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = peer.Close() })
				return peer
			},
			IsConflict: func(err error) bool { return errors.Is(err, ErrConflict) },
			Scope:      "postgres_source_lifecycle_contract",
		})
	})
}

func testPostgresReconcileReadinessConcurrency(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	primary, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = primary.Close() })
	peer, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	primary.db.SetMaxOpenConns(1)
	peer.db.SetMaxOpenConns(1)
	scope := knowl.ScopeRef(fmt.Sprintf("reconcile_readiness_%d", time.Now().UnixNano()))
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: postgresLifecyclePeerSource}
	digest := strings.Repeat("b", 64)
	base := time.Unix(1_850_000_000, 0).UTC()
	desired := []app.DesiredSourceOwner{{Owner: owner, ConfigDigest: digest}}
	initial := app.SourceLifecycleReconcileRequest{
		Scope: scope, Desired: desired, AuthorityGeneration: app.SourceLifecycleGeneration(desired), UpdatedAt: base,
	}
	if err := primary.ReconcileSourceLifecycles(ctx, initial); err != nil {
		t.Fatal(err)
	}
	authority, err := primary.SourceLifecycleAuthority(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}

	blockerDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blockerDB.Close() })
	blocker, err := blockerDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	var locked int
	if err := blocker.QueryRowContext(ctx, `SELECT 1 FROM knowl_source_lifecycle WHERE scope = $1 AND adapter = $2 AND source_id = $3 FOR UPDATE`, scope, owner.Adapter, owner.SourceID).Scan(&locked); err != nil {
		t.Fatal(err)
	}

	var readinessPID, reconcilePID int
	if err := peer.db.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&readinessPID); err != nil {
		t.Fatal(err)
	}
	if err := primary.db.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&reconcilePID); err != nil {
		t.Fatal(err)
	}
	readinessResult := make(chan error, 1)
	go func() {
		_, err := peer.MarkSourceLifecycleReady(ctx, app.SourceLifecycleReadyUpdate{
			Scope: scope, Owner: owner, AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch,
			ActivatedEpoch: authority.Epoch, ConfigDigest: digest, ReadyAt: base.Add(2 * time.Second),
		})
		readinessResult <- err
	}()
	waitPostgresLifecycleLock(t, blockerDB, readinessPID)

	reconcileResult := make(chan error, 1)
	go func() {
		empty := app.SourceLifecycleReconcileRequest{
			Scope: scope, ExpectedAuthorityEpoch: authority.Epoch,
			AuthorityGeneration: app.SourceLifecycleGeneration(nil), UpdatedAt: base.Add(time.Second),
		}
		reconcileResult <- primary.ReconcileSourceLifecycles(ctx, empty)
	}()
	waitPostgresLifecycleLock(t, blockerDB, reconcilePID)
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-readinessResult; err != nil {
		t.Fatalf("readiness error = %v", err)
	}
	if err := <-reconcileResult; !errors.Is(err, app.ErrSourceLifecycleConflict) {
		t.Fatalf("reconcile error = %v, want lifecycle conflict", err)
	}
	current, err := primary.SourceLifecycle(ctx, scope, owner)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != knowl.SourceLifecycleActive || !current.Ready || !current.UpdatedAt.Equal(base.Add(2*time.Second)) {
		t.Fatalf("concurrent lifecycle = %#v", current)
	}
}

func testPostgresReconcileScanConcurrency(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	primary, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = primary.Close() })
	peer, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	primary.db.SetMaxOpenConns(1)
	peer.db.SetMaxOpenConns(1)
	scope := knowl.ScopeRef(fmt.Sprintf("reconcile_scan_%d", time.Now().UnixNano()))
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: postgresLifecyclePeerSource}
	digest := strings.Repeat("c", 64)
	base := time.Unix(1_850_100_000, 0).UTC()
	desired := []app.DesiredSourceOwner{{Owner: owner, ConfigDigest: digest}}
	initial := app.SourceLifecycleReconcileRequest{
		Scope: scope, Desired: desired, AuthorityGeneration: app.SourceLifecycleGeneration(desired), UpdatedAt: base,
	}
	if err := primary.ReconcileSourceLifecycles(ctx, initial); err != nil {
		t.Fatal(err)
	}
	first, err := primary.SourceLifecycleAuthority(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	emptyGeneration := app.SourceLifecycleGeneration(nil)
	if err := primary.ReconcileSourceLifecycles(ctx, app.SourceLifecycleReconcileRequest{
		Scope: scope, ExpectedAuthorityEpoch: first.Epoch, AuthorityGeneration: emptyGeneration, UpdatedAt: base.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	retiring, err := primary.SourceLifecycle(ctx, scope, owner)
	if err != nil {
		t.Fatal(err)
	}

	blockerDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blockerDB.Close() })
	blocker, err := blockerDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	var locked int
	if err := blocker.QueryRowContext(ctx, `SELECT 1 FROM knowl_source_lifecycle WHERE scope = $1 AND adapter = $2 AND source_id = $3 FOR UPDATE`, scope, owner.Adapter, owner.SourceID).Scan(&locked); err != nil {
		t.Fatal(err)
	}

	var scanPID, reconcilePID int
	if err := peer.db.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&scanPID); err != nil {
		t.Fatal(err)
	}
	if err := primary.db.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&reconcilePID); err != nil {
		t.Fatal(err)
	}
	scanResult := make(chan error, 1)
	go func() {
		_, err := peer.UpdateSourceRetirement(ctx, app.SourceRetirementUpdate{
			Scope: scope, Owner: owner, AuthorityGeneration: emptyGeneration, AuthorityEpoch: retiring.AuthorityEpoch,
			RetirementGeneration: emptyGeneration, RetirementEpoch: retiring.RetirementEpoch,
			Cursor: postgresLifecycleCursor, RoundMatches: 1, UpdatedAt: base.Add(3 * time.Second),
		})
		scanResult <- err
	}()
	waitPostgresLifecycleMutationLock(t, blockerDB, scanPID, scanResult)

	reconcileResult := make(chan error, 1)
	go func() {
		reconcileResult <- primary.ReconcileSourceLifecycles(ctx, app.SourceLifecycleReconcileRequest{
			Scope: scope, Desired: desired, ExpectedAuthorityEpoch: retiring.AuthorityEpoch,
			AuthorityGeneration: app.SourceLifecycleGeneration(desired), UpdatedAt: base.Add(2 * time.Second),
		})
	}()
	waitPostgresLifecycleLock(t, blockerDB, reconcilePID)
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-scanResult; err != nil {
		t.Fatalf("scan error = %v", err)
	}
	if err := <-reconcileResult; !errors.Is(err, app.ErrSourceLifecycleConflict) {
		t.Fatalf("reconcile error = %v, want lifecycle conflict", err)
	}
	current, err := primary.SourceLifecycle(ctx, scope, owner)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != knowl.SourceLifecycleRetiring || current.ScanCursor != postgresLifecycleCursor || current.ScanRoundMatches != 1 ||
		!current.UpdatedAt.Equal(base.Add(3*time.Second)) {
		t.Fatalf("concurrent lifecycle = %#v", current)
	}
}

func waitPostgresLifecycleLock(t *testing.T, observer *sql.DB, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastWait, lastState, lastQuery sql.NullString
	for time.Now().Before(deadline) {
		if err := observer.QueryRowContext(t.Context(), `SELECT wait_event_type, state, query FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&lastWait, &lastState, &lastQuery); err != nil {
			t.Fatal(err)
		}
		if lastWait.String == "Lock" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("PostgreSQL backend %d did not wait on a lock: wait=%q state=%q query=%q", pid, lastWait.String, lastState.String, lastQuery.String)
}

func waitPostgresLifecycleMutationLock(t *testing.T, observer *sql.DB, pid int, result <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-result:
			t.Fatalf("mutation returned before row lock was released: %v", err)
		default:
		}
		var waitType sql.NullString
		if err := observer.QueryRowContext(t.Context(), `SELECT wait_event_type FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&waitType); err != nil {
			t.Fatal(err)
		}
		if waitType.String == "Lock" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("PostgreSQL backend %d did not wait on a lock", pid)
}

func testPostgresSourceLifecycleMutationLockCancellation(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	digest := strings.Repeat("a", 64)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: postgresLifecycleLockedScope}
	at := time.Unix(1_840_000_000, 0).UTC()
	desired := []app.DesiredSourceOwner{{Owner: owner, ConfigDigest: digest}}
	mutations := map[string]func(context.Context) error{
		"reconcile": func(ctx context.Context) error {
			return store.ReconcileSourceLifecycles(ctx, app.SourceLifecycleReconcileRequest{
				Scope: postgresLifecycleLockedScope, Desired: desired, AuthorityGeneration: app.SourceLifecycleGeneration(desired), UpdatedAt: at,
			})
		},
		"readiness": func(ctx context.Context) error {
			_, err := store.MarkSourceLifecycleReady(ctx, app.SourceLifecycleReadyUpdate{
				Scope: postgresLifecycleLockedScope, Owner: owner, AuthorityGeneration: digest, AuthorityEpoch: 1, ActivatedEpoch: 1,
				ConfigDigest: digest, ReadyAt: at,
			})
			return err
		},
		"scan": func(ctx context.Context) error {
			_, err := store.UpdateSourceRetirement(ctx, app.SourceRetirementUpdate{
				Scope: postgresLifecycleLockedScope, Owner: owner, AuthorityGeneration: digest, AuthorityEpoch: 1,
				RetirementGeneration: digest, RetirementEpoch: 1, UpdatedAt: at,
			})
			return err
		},
		"finalization": func(ctx context.Context) error {
			_, err := store.FinalizeSourceRetirement(ctx, app.SourceRetirementFinalization{
				Scope: postgresLifecycleLockedScope, Owner: owner, AuthorityGeneration: digest, AuthorityEpoch: 1,
				RetirementGeneration: digest, RetirementEpoch: 1, FinalizedAt: at,
			})
			return err
		},
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			store.mu.Lock()
			mutationCtx, cancel := context.WithCancel(ctx)
			result := make(chan error, 1)
			go func() { result <- mutation(mutationCtx) }()
			cancel()
			select {
			case err := <-result:
				store.mu.Unlock()
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("mutation error = %v, want context canceled", err)
				}
			case <-time.After(100 * time.Millisecond):
				store.mu.Unlock()
				<-result
				t.Fatal("mutation did not cancel while writer lock was contended")
			}
		})
	}
}
