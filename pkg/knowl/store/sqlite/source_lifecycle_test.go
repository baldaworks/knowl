package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
)

const (
	lifecycleMigrationScope    = "lifecycle_migration"
	lifecycleReplacementScope  = "lifecycle_replacement"
	lifecycleMigrationSource   = "lifecycle_legacy"
	lifecycleMigrationRevision = "one"
	lifecycleCorruptScope      = "lifecycle_corrupt"
	lifecycleLockedScope       = "locked"
)

func TestSQLiteSourceLifecycleContract(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/source-lifecycle.sqlite"
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	storetest.RunSourceLifecycleContract(t, storetest.SourceLifecycleHarness{
		Store:  store,
		Legacy: store,
		OpenPeer: func(t *testing.T) app.SourceLifecycleStore {
			t.Helper()
			peer, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = peer.Close() })
			return peer
		},
		IsConflict: func(err error) bool { return errors.Is(err, ErrConflict) },
		Scope:      "sqlite_source_lifecycle_contract",
	})
}

func TestSQLiteSourceLifecycleMigrationBootstrapsAndPreservesLegacyRows(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/source-lifecycle-migration.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, directory, goose.WithGoMigrations(operatorOperationsMigration()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 21); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_810_000_000, 0).UTC()
	timestamp := formatTime(now)
	digest := strings.Repeat("a", 64)
	legacyStore := &Store{db: db}
	for _, run := range []knowl.SyncRun{
		{ID: "migration-run", Scope: lifecycleMigrationScope, SourceID: lifecycleMigrationSource, ConfigDigest: digest, Status: knowl.SyncStatusScanning, StartedAt: now, UpdatedAt: now},
		{ID: "replacement-run", Scope: lifecycleReplacementScope, SourceID: lifecycleMigrationSource, ConfigDigest: digest, Status: knowl.SyncStatusScanning, StartedAt: now, UpdatedAt: now},
	} {
		if _, replay, err := legacyStore.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil || replay {
			t.Fatalf("create legacy source %q: replay=%v err=%v", run.Scope, replay, err)
		}
	}
	accepted := knowl.AcceptedSource{
		Scope:   lifecycleMigrationScope,
		Source:  knowl.SourceRef{Adapter: string(knowl.SourceAdapterFilesystem), ID: lifecycleMigrationSource + "/doc.md"},
		Version: knowl.SourceVersion{Version: lifecycleMigrationRevision, Digest: strings.Repeat("b", 64)}, MediaType: "text/markdown",
		SourceDocument: knowl.SourceDocument{SourceID: lifecycleMigrationSource, DocumentID: "doc.md", Revision: lifecycleMigrationRevision, URI: "file:///srv/wiki/doc.md"},
		ManifestRef:    "raw/manifest.json",
	}
	encodedAccepted, err := json.Marshal(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowl_source_documents (scope, source_id, document_id, revision, accepted_source, last_seen_run_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, lifecycleMigrationScope, lifecycleMigrationSource, "doc.md", lifecycleMigrationRevision, encodedAccepted, "run", timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
	operationKey, operationMeta := executionFixture(lifecycleMigrationScope, lifecycleMigrationSource, now)
	reservation, err := legacyStore.Reserve(ctx, operationKey, operationMeta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 23); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db}
	state, err := store.SourceLifecycle(ctx, lifecycleMigrationScope, knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: lifecycleMigrationSource})
	if err != nil {
		t.Fatal(err)
	}
	if state.State != knowl.SourceLifecycleActive || state.ConfigDigest != digest || state.AuthorityEpoch != 0 || state.ActivatedEpoch != 0 || !state.ActivatedAt.Equal(now) || !state.UpdatedAt.Equal(now) {
		t.Fatalf("migrated lifecycle = %#v", state)
	}
	authority, err := store.SourceLifecycleAuthority(ctx, lifecycleMigrationScope)
	if err != nil || authority.Generation != "" || authority.Epoch != 0 || !authority.UpdatedAt.Equal(now) {
		t.Fatalf("migrated authority = %#v, %v", authority, err)
	}
	status, err := store.SourceStatus(ctx, lifecycleMigrationScope, lifecycleMigrationSource)
	if err != nil {
		t.Fatal(err)
	}
	if status.Type != knowl.SourceTypeFilesystem || status.ConfigDigest != digest || status.LastAttemptRunID != "migration-run" {
		t.Fatalf("preserved source status = %#v", status)
	}
	documents, err := store.ListSourceDocuments(ctx, lifecycleMigrationScope, lifecycleMigrationSource, app.OperatorReadOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents.Items) != 1 || documents.Items[0].ID != "doc.md" || documents.Items[0].AcceptedRevision != lifecycleMigrationRevision {
		t.Fatalf("preserved source documents = %#v", documents)
	}
	operation, err := store.Operation(ctx, lifecycleMigrationScope, reservation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != knowl.StatusReceived || operation.Key != operationKey {
		t.Fatalf("preserved operation = %#v", operation)
	}
	descriptor, err := store.Execution(ctx, lifecycleMigrationScope, reservation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Source != operationMeta.AcceptedSource || descriptor.Schema.Digest != operationMeta.Schema.Digest {
		t.Fatalf("preserved execution descriptor = %#v", descriptor)
	}
	authoritativeAt := now.Add(time.Second)
	empty := app.SourceLifecycleReconcileRequest{
		Scope: lifecycleMigrationScope, AuthorityGeneration: app.SourceLifecycleGeneration(nil), UpdatedAt: authoritativeAt,
	}
	if err := store.ReconcileSourceLifecycles(ctx, empty); err != nil {
		t.Fatalf("first authoritative empty inventory: %v", err)
	}
	retiring, err := store.SourceLifecycle(ctx, lifecycleMigrationScope, knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: lifecycleMigrationSource})
	if err != nil {
		t.Fatal(err)
	}
	if retiring.State != knowl.SourceLifecycleRetiring || retiring.AuthorityEpoch != 1 || retiring.ActivatedEpoch != 1 || retiring.RetirementEpoch != 1 ||
		retiring.ActivatedGeneration != empty.AuthorityGeneration || retiring.RetirementGeneration != empty.AuthorityGeneration {
		t.Fatalf("retired migrated lifecycle = %#v", retiring)
	}
	gitOwner := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: lifecycleMigrationSource}
	gitDigest := strings.Repeat("c", 64)
	desired := []app.DesiredSourceOwner{{Owner: gitOwner, ConfigDigest: gitDigest}}
	replacement := app.SourceLifecycleReconcileRequest{
		Scope: lifecycleReplacementScope, Desired: desired,
		AuthorityGeneration: app.SourceLifecycleGeneration(desired), UpdatedAt: authoritativeAt,
	}
	if err := store.ReconcileSourceLifecycles(ctx, replacement); err != nil {
		t.Fatalf("first authoritative replacement inventory: %v", err)
	}
	oldOwner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: lifecycleMigrationSource}
	oldState, err := store.SourceLifecycle(ctx, lifecycleReplacementScope, oldOwner)
	if err != nil {
		t.Fatal(err)
	}
	wantOld := knowl.SourceLifecycle{
		Scope: lifecycleReplacementScope, Owner: oldOwner, State: knowl.SourceLifecycleRetiring, ConfigDigest: digest,
		AuthorityGeneration: replacement.AuthorityGeneration, AuthorityEpoch: 1,
		ActivatedGeneration: replacement.AuthorityGeneration, ActivatedEpoch: 1,
		RetirementGeneration: replacement.AuthorityGeneration, RetirementEpoch: 1,
		ReplacementPending: 1,
		ActivatedAt:        now, RetiringAt: authoritativeAt, UpdatedAt: authoritativeAt,
	}
	if oldState != wantOld {
		t.Fatalf("replacement old owner = %#v, want %#v", oldState, wantOld)
	}
	newState, err := store.SourceLifecycle(ctx, lifecycleReplacementScope, gitOwner)
	if err != nil {
		t.Fatal(err)
	}
	wantNew := knowl.SourceLifecycle{
		Scope: lifecycleReplacementScope, Owner: gitOwner, State: knowl.SourceLifecycleActive, ConfigDigest: gitDigest,
		AuthorityGeneration: replacement.AuthorityGeneration, AuthorityEpoch: 1,
		ActivatedGeneration: replacement.AuthorityGeneration, ActivatedEpoch: 1,
		ActivatedAt: authoritativeAt, UpdatedAt: authoritativeAt,
	}
	if newState != wantNew {
		t.Fatalf("replacement new owner = %#v, want %#v", newState, wantNew)
	}
}

func TestSQLiteSourceReadinessMigrationResumesLegacyPreparedStages(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/source-readiness-migration.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, directory, goose.WithGoMigrations(operatorOperationsMigration()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 22); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: db}
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
		if _, err := db.ExecContext(ctx, `UPDATE knowl_sync_runs SET status = ?, complete_scan = 1,
candidate_digest = ?, checkpoint = ?, content_generation = ?, updated_at = ? WHERE run_id = ?`,
			test.status, digest, state.Checkpoint, generation, formatTime(base), run.ID); err != nil {
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
	if _, err := db.ExecContext(ctx, `UPDATE knowl_sync_runs SET status = ?, complete_scan = 1,
candidate_digest = ?, checkpoint = ?, updated_at = ? WHERE run_id = ?`, knowl.SyncStatusPrepared,
		strings.Repeat("e", 64), "legacy-checkpoint", formatTime(base), mismatchRun.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 23); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db}
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

func TestSQLiteSourceLifecycleRejectsMalformedDurableRow(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/source-lifecycle-corrupt.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	desired := []app.DesiredSourceOwner{{
		Owner:        knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: lifecycleCorruptScope},
		ConfigDigest: strings.Repeat("c", 64),
	}}
	request := app.SourceLifecycleReconcileRequest{
		Scope: lifecycleCorruptScope, Desired: desired, AuthorityGeneration: app.SourceLifecycleGeneration(desired), UpdatedAt: time.Unix(1_820_000_000, 0).UTC(),
	}
	if err := store.ReconcileSourceLifecycles(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE knowl_source_lifecycle SET state = 'poisoned' WHERE scope = ?`, request.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SourceLifecycle(ctx, request.Scope, desired[0].Owner); !errors.Is(err, app.ErrSourceLifecycleInvalid) {
		t.Fatalf("malformed durable row error = %v", err)
	}
}

func TestSQLiteSourceLifecycleMutationsCancelWhileWriterLockIsContended(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/source-lifecycle-lock.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	digest := strings.Repeat("a", 64)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: lifecycleLockedScope}
	at := time.Unix(1_840_000_000, 0).UTC()
	desired := []app.DesiredSourceOwner{{Owner: owner, ConfigDigest: digest}}
	mutations := map[string]func(context.Context) error{
		"reconcile": func(ctx context.Context) error {
			return store.ReconcileSourceLifecycles(ctx, app.SourceLifecycleReconcileRequest{
				Scope: lifecycleLockedScope, Desired: desired, AuthorityGeneration: app.SourceLifecycleGeneration(desired), UpdatedAt: at,
			})
		},
		"readiness": func(ctx context.Context) error {
			_, err := store.MarkSourceLifecycleReady(ctx, app.SourceLifecycleReadyUpdate{
				Scope: lifecycleLockedScope, Owner: owner, AuthorityGeneration: digest, AuthorityEpoch: 1, ActivatedEpoch: 1,
				ConfigDigest: digest, ReadyAt: at,
			})
			return err
		},
		"scan": func(ctx context.Context) error {
			_, err := store.UpdateSourceRetirement(ctx, app.SourceRetirementUpdate{
				Scope: lifecycleLockedScope, Owner: owner, AuthorityGeneration: digest, AuthorityEpoch: 1,
				RetirementGeneration: digest, RetirementEpoch: 1, UpdatedAt: at,
			})
			return err
		},
		"finalization": func(ctx context.Context) error {
			_, err := store.FinalizeSourceRetirement(ctx, app.SourceRetirementFinalization{
				Scope: lifecycleLockedScope, Owner: owner, AuthorityGeneration: digest, AuthorityEpoch: 1,
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
