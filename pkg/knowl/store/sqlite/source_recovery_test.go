package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
	driverSQLite "modernc.org/sqlite"
	lib "modernc.org/sqlite/lib"
)

func TestSQLiteSourceLifecycleRecovery(t *testing.T) {
	path := t.TempDir() + "/recovery.sqlite"
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	storetest.RunSourceLifecycleRecoveryContract(t, storetest.SourceLifecycleRecoveryHarness{SourceLifecycleHarness: storetest.SourceLifecycleHarness{Store: store, Legacy: store, Scope: "sqlite_recovery", OpenPeer: func(t *testing.T) app.SourceLifecycleStore {
		t.Helper()
		peer, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = peer.Close() })
		return peer
	}}, CorruptOperation: func(t *testing.T, scope knowl.ScopeRef, id knowl.OperationID) {
		t.Helper()
		if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_operations SET accepted_source_document='{}' WHERE scope=? AND operation_id=?`, scope, id); err != nil {
			t.Fatal(err)
		}
	}, ReadProof: func(t *testing.T, scope knowl.ScopeRef, owner knowl.SourceOwner) string {
		t.Helper()
		var payload string
		if err := store.db.QueryRowContext(t.Context(), `SELECT recovery_proof_payload FROM knowl_source_lifecycle WHERE scope=? AND adapter=? AND source_id=?`, scope, owner.Adapter, owner.SourceID).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}, WriteProof: func(t *testing.T, scope knowl.ScopeRef, owner knowl.SourceOwner, payload string) {
		t.Helper()
		if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_source_lifecycle SET recovery_proof_payload=? WHERE scope=? AND adapter=? AND source_id=?`, payload, scope, owner.Adapter, owner.SourceID); err != nil {
			t.Fatal(err)
		}
	}})
}

func TestSQLiteSourceRecoveryMigrationPreservesRowsAndAudit(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/recovery-migration.sqlite")
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
	if _, err := provider.UpTo(t.Context(), 27); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_850_000_000, 0).UTC()
	states := []knowl.SourceLifecycleState{knowl.SourceLifecycleActive, knowl.SourceLifecycleRetiring, knowl.SourceLifecycleDeleted}
	for _, state := range states {
		retiring, deleted, generation := "", "", ""
		var retirementEpoch, complete int
		if state != knowl.SourceLifecycleActive {
			retiring, generation, retirementEpoch = formatTime(base.Add(time.Second)), strings.Repeat("b", 64), 2
		}
		if state == knowl.SourceLifecycleDeleted {
			deleted, complete = formatTime(base.Add(2*time.Second)), 1
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO knowl_source_lifecycle
(scope,adapter,source_id,state,config_digest,authority_generation,authority_epoch,activated_generation,activated_epoch,retirement_generation,retirement_epoch,scan_complete,activated_at,retiring_at,deleted_at,updated_at)
VALUES ('migration', 'wiki-filesystem', ?, ?, ?, ?, 3, ?, 1, ?, ?, ?, ?, ?, ?, ?)`, string(state), state, strings.Repeat("a", 64), strings.Repeat("c", 64), strings.Repeat("a", 64), generation, retirementEpoch, complete, formatTime(base), retiring, deleted, formatTime(base.Add(3*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provider.UpTo(t.Context(), 28); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db}
	for _, expected := range states {
		state, err := store.SourceLifecycle(t.Context(), "migration", knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: knowl.SourceID(expected)})
		if err != nil || state.State != expected || state.ConfigDigest != strings.Repeat("a", 64) || state.RecoveryProofDigest != "" || state.AuthorityEpoch != 3 || state.ActivatedEpoch != 1 {
			t.Fatalf("migration changed old lifecycle: %+v %v", state, err)
		}
	}
	rows, err := db.QueryContext(t.Context(), `PRAGMA index_list(knowl_source_lifecycle)`)
	if err != nil {
		t.Fatal(err)
	}
	indexes := map[string]bool{}
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		indexes[name] = true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"knowl_source_lifecycle_scope_state", "knowl_source_lifecycle_scan_eligibility"} {
		if !indexes[name] {
			t.Fatalf("migration lost index %s", name)
		}
	}
	// Downgrade is safe before any recovery proof is used.
	if _, err := provider.DownTo(t.Context(), 27); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 28); err != nil {
		t.Fatal(err)
	}
	storetest.RunSourceLifecycleRecoveryContract(t, recoveryHarness(t, store))
	if _, err := provider.DownTo(t.Context(), 27); err == nil {
		t.Fatal("downgrade erased used recovery proof")
	} else {
		var failure *driverSQLite.Error
		if !errors.As(err, &failure) || failure.Code()&255 != lib.SQLITE_CONSTRAINT {
			t.Fatalf("unexpected downgrade failure: %v", err)
		}
	}
	version, err := provider.GetDBVersion(t.Context())
	if err != nil || version != 28 {
		t.Fatalf("failed downgrade was not atomic: version=%d error=%v", version, err)
	}
}

func recoveryHarness(t *testing.T, store *Store) storetest.SourceLifecycleRecoveryHarness {
	t.Helper()
	return storetest.SourceLifecycleRecoveryHarness{SourceLifecycleHarness: storetest.SourceLifecycleHarness{Store: store, Legacy: store, Scope: "migration_recovery", OpenPeer: func(t *testing.T) app.SourceLifecycleStore { t.Helper(); return store }},
		CorruptOperation: func(t *testing.T, scope knowl.ScopeRef, id knowl.OperationID) {
			t.Helper()
			if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_operations SET accepted_source_document='{}' WHERE scope=? AND operation_id=?`, scope, id); err != nil {
				t.Fatal(err)
			}
		}, ReadProof: func(t *testing.T, scope knowl.ScopeRef, owner knowl.SourceOwner) string {
			t.Helper()
			var payload string
			if err := store.db.QueryRowContext(t.Context(), `SELECT recovery_proof_payload FROM knowl_source_lifecycle WHERE scope=? AND adapter=? AND source_id=?`, scope, owner.Adapter, owner.SourceID).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			return payload
		}, WriteProof: func(t *testing.T, scope knowl.ScopeRef, owner knowl.SourceOwner, payload string) {
			t.Helper()
			if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_source_lifecycle SET recovery_proof_payload=? WHERE scope=? AND adapter=? AND source_id=?`, payload, scope, owner.Adapter, owner.SourceID); err != nil {
				t.Fatal(err)
			}
		}}
}
