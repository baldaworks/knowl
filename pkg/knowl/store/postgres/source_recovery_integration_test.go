//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPostgresSourceRecoveryWithTestcontainers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	container, err := pgcontainer.Run(ctx,
		"pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc",
		pgcontainer.WithDatabase("knowl"), pgcontainer.WithUsername("knowl"), pgcontainer.WithPassword("knowl"),
		pgcontainer.BasicWaitStrategies(), pgcontainer.WithSQLDriver("pgx"))
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("contract", func(t *testing.T) {
		scoped := recoveryPostgresSchema(t, dsn)
		store, err := Open(t.Context(), scoped)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		storetest.RunSourceLifecycleRecoveryContract(t, postgresRecoveryHarness(store, scoped))
	})
	t.Run("hierarchy_retry", func(t *testing.T) {
		scoped := recoveryPostgresSchema(t, dsn)
		store, err := Open(t.Context(), scoped)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		peer, err := Open(t.Context(), scoped)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = peer.Close() })
		storetest.RunHierarchyRetryContract(t, store, peer)
	})

	t.Run("migration", func(t *testing.T) { testPostgresSourceRecoveryMigration(t, recoveryPostgresSchema(t, dsn)) })
}

func recoveryPostgresSchema(t *testing.T, dsn string) string {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := fmt.Sprintf("source_recovery_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := db.ExecContext(t.Context(), `CREATE SCHEMA `+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`) })
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func postgresRecoveryHarness(store *Store, dsn string) storetest.SourceLifecycleRecoveryHarness {
	return storetest.SourceLifecycleRecoveryHarness{SourceLifecycleHarness: storetest.SourceLifecycleHarness{Store: store, Legacy: store, Scope: "postgres_recovery", OpenPeer: func(t *testing.T) app.SourceLifecycleStore {
		t.Helper()
		peer, err := Open(t.Context(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = peer.Close() })
		return peer
	}}, CorruptOperation: func(t *testing.T, scope knowl.ScopeRef, id knowl.OperationID) {
		t.Helper()
		if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_operations SET accepted_source_document='{}' WHERE scope=$1 AND operation_id=$2`, scope, id); err != nil {
			t.Fatal(err)
		}
	}, ReadProof: func(t *testing.T, scope knowl.ScopeRef, owner knowl.SourceOwner) string {
		t.Helper()
		var payload string
		if err := store.db.QueryRowContext(t.Context(), `SELECT recovery_proof_payload FROM knowl_source_lifecycle WHERE scope=$1 AND adapter=$2 AND source_id=$3`, scope, owner.Adapter, owner.SourceID).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}, WriteProof: func(t *testing.T, scope knowl.ScopeRef, owner knowl.SourceOwner, payload string) {
		t.Helper()
		if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_source_lifecycle SET recovery_proof_payload=$1 WHERE scope=$2 AND adapter=$3 AND source_id=$4`, payload, scope, owner.Adapter, owner.SourceID); err != nil {
			t.Fatal(err)
		}
	}}
}

func testPostgresSourceRecoveryMigration(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
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
	if _, err := provider.UpTo(t.Context(), 27); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_850_000_000, 0).UTC()
	states := []knowl.SourceLifecycleState{knowl.SourceLifecycleActive, knowl.SourceLifecycleRetiring, knowl.SourceLifecycleDeleted}
	for _, state := range states {
		var retiring, deleted any
		generation := ""
		var retirementEpoch int
		complete := false
		if state != knowl.SourceLifecycleActive {
			retiring, generation, retirementEpoch = base.Add(time.Second), strings.Repeat("b", 64), 2
		}
		if state == knowl.SourceLifecycleDeleted {
			deleted, complete = base.Add(2*time.Second), true
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO knowl_source_lifecycle
(scope,adapter,source_id,state,config_digest,authority_generation,authority_epoch,activated_generation,activated_epoch,retirement_generation,retirement_epoch,scan_complete,activated_at,retiring_at,deleted_at,updated_at)
VALUES ('migration', 'wiki-filesystem', $1,$2,$3,$4,3,$5,1,$6,$7,$8,$9,$10,$11,$12)`, string(state), state, strings.Repeat("a", 64), strings.Repeat("c", 64), strings.Repeat("a", 64), generation, retirementEpoch, complete, base, retiring, deleted, base.Add(3*time.Second)); err != nil {
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
	rows, err := db.QueryContext(t.Context(), `SELECT indexname FROM pg_indexes WHERE schemaname=current_schema() AND tablename='knowl_source_lifecycle'`)
	if err != nil {
		t.Fatal(err)
	}
	indexes := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
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
	if _, err := provider.DownTo(t.Context(), 27); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 28); err != nil {
		t.Fatal(err)
	}
	storetest.RunSourceLifecycleRecoveryContract(t, postgresRecoveryHarness(store, dsn))
	if _, err := provider.DownTo(t.Context(), 27); err == nil {
		t.Fatal("downgrade erased used recovery proof")
	} else {
		var failure *pgconn.PgError
		if !errors.As(err, &failure) || failure.Code != "23514" {
			t.Fatalf("unexpected downgrade failure: %v", err)
		}
	}
	version, err := provider.GetDBVersion(t.Context())
	if err != nil || version != 28 {
		t.Fatalf("failed downgrade was not atomic: version=%d error=%v", version, err)
	}
}
