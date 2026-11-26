//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/testcontainers/testcontainers-go"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// This fixture owns a new container and databases, never an operator DSN.
func TestPostgresNativeSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	container, err := pgcontainer.Run(ctx, "pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc", pgcontainer.WithDatabase("native_tests"), pgcontainer.WithUsername("knowl"), pgcontainer.WithPassword("knowl"), pgcontainer.BasicWaitStrategies(), pgcontainer.WithSQLDriver("pgx"))
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("extension-later-and-rollback", func(t *testing.T) {
		store := nativeOpen(t, dsn)
		base := nativeBaseProvider(t, store.db)
		if _, err := base.DownTo(ctx, 25); err != nil {
			t.Fatal(err)
		}
		nativeExec(t, store.db, `INSERT INTO knowl_projection_state(scope,schema_digest,snapshot_digest,page_count,link_count,ready_at) VALUES('native','schema','snapshot',0,0,now())`)
		nativeExec(t, store.db, `INSERT INTO knowl_embedding_state(scope,space,snapshot_digest,dimensions,mode,chunk_count,omitted_chunks,omitted_runes,ready_at) VALUES('native',repeat('a',64),repeat('b',64),1,'hybrid',0,0,0,now())`)
		if _, err := base.Up(ctx); err != nil {
			t.Fatal(err)
		}
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_state`, 0)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_projection_state WHERE scope='native'`, 1)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM pg_attribute WHERE attrelid='knowl_embedding_state'::regclass AND attname='native_format' AND NOT attisdropped`, 1)
		for _, policy := range []app.EmbeddingFailurePolicy{app.EmbeddingStrict, app.EmbeddingFallbackLexical} {
			options := nativeOptions()
			options.FailurePolicy = policy
			nativeAssertOpenFailure(t, dsn, knowl.MigrationStageValidation, options)
		}
		nativeAssertCount(t, store.db, `SELECT count(*) FROM pg_class WHERE relname='goose_pgvector_db_version'`, 0)
		nativeExec(t, store.db, `CREATE SCHEMA "vector extension"`)
		nativeExec(t, store.db, `CREATE EXTENSION vector SCHEMA "vector extension"`)
		lexical := nativeOpen(t, dsn)
		nativeAssertCount(t, lexical.db, `SELECT count(*) FROM pg_class WHERE relname='goose_pgvector_db_version'`, 0)
		dense := nativeOpen(t, dsn, nativeOptions())
		if dense.nativeErr != nil {
			_, migrationErr := nativeOptionalProvider(t, dense.db).Up(ctx)
			t.Fatalf("native initialization=%v migration=%v", dense.nativeErr, migrationErr)
		}
		nativeAssertCount(t, dense.db, `SELECT count(*) FROM goose_pgvector_db_version WHERE version_id=1 AND is_applied`, 1)
		nativeAssertCount(t, dense.db, `SELECT count(*) FROM pg_attribute a JOIN pg_type v ON v.oid=a.atttypid JOIN pg_namespace n ON n.oid=v.typnamespace WHERE a.attrelid='knowl_embedding_chunks'::regclass AND a.attname='vector' AND v.typname='vector' AND n.nspname='vector extension' AND a.atttypmod=-1`, 1)
		if _, err := base.DownTo(ctx, 25); err == nil {
			t.Fatal("base Down accepted still-native schema")
		}
		if version, err := base.GetDBVersion(ctx); err != nil || version != 26 {
			t.Fatalf("wrong-order version=%d err=%v", version, err)
		}
		optional := nativeOptionalProvider(t, dense.db)
		if _, err := optional.Down(ctx); err != nil {
			t.Fatal(err)
		}
		nativeAssertCount(t, dense.db, `SELECT count(*) FROM pg_attribute WHERE attrelid='knowl_embedding_chunks'::regclass AND attname='vector' AND atttypid='bytea'::regtype`, 1)
		if _, err := base.DownTo(ctx, 25); err != nil {
			t.Fatal(err)
		}
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_projection_state WHERE scope='native'`, 1)
		reopened := nativeOpen(t, dsn, nativeOptions())
		assertPostgresNativeLifecycleRebuild(t, reopened)
	})
	t.Run("transactional-history-and-retry", func(t *testing.T) {
		databaseDSN := nativeDatabase(t, dsn, "atomicity")
		store := nativeOpen(t, databaseDSN)
		nativeSeed(t, store.db, false)
		before := nativeAuthoritative(t, store.db)
		nativeExec(t, store.db, `CREATE EXTENSION vector`)
		optional := nativeOptionalProvider(t, store.db)
		if _, err := optional.Status(ctx); err != nil {
			t.Fatal(err)
		}
		// Refuse the history write after the application DDL has executed.
		nativeExec(t, store.db, `CREATE FUNCTION reject_native_version() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version_id=1 THEN RAISE EXCEPTION 'fixture interruption' USING ERRCODE='P0001'; END IF; RETURN NEW; END $$`)
		nativeExec(t, store.db, `CREATE TRIGGER reject_native_version BEFORE INSERT ON goose_pgvector_db_version FOR EACH ROW EXECUTE FUNCTION reject_native_version()`)
		_, err := optional.Up(ctx)
		nativeAssertSQLState(t, err, "P0001")
		nativeAssertCount(t, store.db, `SELECT count(*) FROM goose_pgvector_db_version WHERE version_id=1`, 0)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_state`, 1)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_chunks WHERE octet_length(vector)=4`, 1)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM pg_attribute WHERE attrelid='knowl_embedding_chunks'::regclass AND attname='vector' AND atttypid='bytea'::regtype`, 1)
		nativeExec(t, store.db, `DROP TRIGGER reject_native_version ON goose_pgvector_db_version`)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM goose_pgvector_db_version WHERE version_id=0 AND is_applied`, 1)
		// Normal failure retains version 0 and retries through ordinary Open/Up.
		dense := nativeOpen(t, databaseDSN, nativeOptions())
		if dense.nativeErr != nil {
			t.Fatal(dense.nativeErr)
		}
		nativeAssertCount(t, store.db, `SELECT count(*) FROM goose_pgvector_db_version WHERE version_id=1 AND is_applied`, 1)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_state`, 0)
		if after := nativeAuthoritative(t, store.db); before != after {
			t.Fatal("optional Up changed authoritative rows")
		}
		assertPostgresNativeLifecycleFailure(t, dense, "native", "lexical", knowl.RetrievalProjectionNotReady)
		nativeSeedDense(t, store.db, 1, "[1]", true)
		// Down DDL and deletion of its applied history must also be atomic.
		nativeExec(t, store.db, `CREATE FUNCTION reject_native_down() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture interruption' USING ERRCODE='P0001'; END $$`)
		nativeExec(t, store.db, `CREATE TRIGGER reject_native_down BEFORE DELETE ON goose_pgvector_db_version FOR EACH ROW EXECUTE FUNCTION reject_native_down()`)
		_, err = optional.Down(ctx)
		nativeAssertSQLState(t, err, "P0001")
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_state`, 1)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_chunks`, 1)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM goose_pgvector_db_version WHERE version_id=1 AND is_applied`, 1)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM pg_attribute a JOIN pg_type t ON t.oid=a.atttypid WHERE a.attrelid='knowl_embedding_chunks'::regclass AND a.attname='vector' AND t.typname='vector'`, 1)
		nativeExec(t, store.db, `DROP TRIGGER reject_native_down ON goose_pgvector_db_version`)
		if _, err := optional.Down(ctx); err != nil {
			t.Fatal(err)
		}
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_state`, 0)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_chunks`, 0)
		if after := nativeAuthoritative(t, store.db); before != after {
			t.Fatal("optional Down changed authoritative rows")
		}
		if _, err := optional.Up(ctx); err != nil {
			t.Fatal(err)
		}
		assertPostgresNativeLifecycleRebuild(t, dense)
	})
	for _, corruption := range []struct{ name, damage string }{
		{"missing_zero", `DELETE FROM goose_pgvector_db_version WHERE version_id=0`},
		{"false_applied", `UPDATE goose_pgvector_db_version SET is_applied=false WHERE version_id=1`},
		{"duplicate_zero", `INSERT INTO goose_pgvector_db_version(version_id,is_applied) VALUES(0,true)`},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			databaseDSN := nativeDatabase(t, dsn, corruption.name)
			store := nativeOpen(t, databaseDSN)
			nativeExec(t, store.db, `CREATE EXTENSION vector`)
			dense := nativeOpen(t, databaseDSN, nativeOptions())
			if dense.nativeErr != nil {
				t.Fatal(dense.nativeErr)
			}
			nativeSeedAuthoritative(t, store.db)
			nativeExec(t, store.db, corruption.damage)
			nativeAssertOpenFailure(t, databaseDSN, knowl.MigrationStageHistory, nativeOptions())
		})
	}
	t.Run("empty-history-preserved-until-operator-recovery", func(t *testing.T) {
		databaseDSN := nativeDatabase(t, dsn, "empty_history")
		store := nativeOpen(t, databaseDSN)
		nativeSeed(t, store.db, false)
		nativeExec(t, store.db, `CREATE EXTENSION vector`)
		optional := nativeOptionalProvider(t, store.db)
		if _, err := optional.Status(ctx); err != nil {
			t.Fatal(err)
		}
		// Simulate damaged history; retain a known-good fixture backup for the
		// operator's recovery, separate from application initialization.
		nativeExec(t, store.db, `CREATE TABLE native_history_backup AS TABLE goose_pgvector_db_version`)
		nativeExec(t, store.db, `DELETE FROM goose_pgvector_db_version`)
		before := nativeAuthoritative(t, store.db)
		nativeAssertOpenFailure(t, databaseDSN, knowl.MigrationStageHistory, nativeOptions())
		nativeAssertCount(t, store.db, `SELECT count(*) FROM goose_pgvector_db_version`, 0)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM pg_attribute WHERE attrelid='knowl_embedding_chunks'::regclass AND attname='vector' AND atttypid='bytea'::regtype`, 1)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_state`, 1)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_chunks WHERE octet_length(vector)=4`, 1)
		if after := nativeAuthoritative(t, store.db); before != after {
			t.Fatal("corrupt history Open changed authoritative rows")
		}
		// Only the fixture operator restores the trustworthy pre-damage history.
		nativeExec(t, store.db, `INSERT INTO goose_pgvector_db_version(id,version_id,is_applied,tstamp) SELECT id,version_id,is_applied,tstamp FROM native_history_backup`)
		recovered := nativeOpen(t, databaseDSN, nativeOptions())
		if recovered.nativeErr != nil {
			t.Fatal(recovered.nativeErr)
		}
		nativeAssertCount(t, store.db, `SELECT count(*) FROM goose_pgvector_db_version WHERE version_id=1 AND is_applied`, 1)
		assertPostgresNativeLifecycleFailure(t, recovered, "native", "lexical", knowl.RetrievalProjectionNotReady)
		assertPostgresNativeLifecycleRebuild(t, recovered)
	})
	t.Run("unknown-pending-history-preserves-schema", func(t *testing.T) {
		databaseDSN := nativeDatabase(t, dsn, "unknown_pending")
		store := nativeOpen(t, databaseDSN)
		nativeSeed(t, store.db, false)
		nativeExec(t, store.db, `CREATE EXTENSION vector`)
		optional := nativeOptionalProvider(t, store.db)
		if _, err := optional.Status(ctx); err != nil {
			t.Fatal(err)
		}
		nativeExec(t, store.db, `INSERT INTO goose_pgvector_db_version(version_id,is_applied) VALUES(-1,true)`)
		nativeAssertOpenFailure(t, databaseDSN, knowl.MigrationStageHistory, nativeOptions())
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_state`, 1)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM goose_pgvector_db_version WHERE version_id=1`, 0)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM pg_attribute WHERE attrelid='knowl_embedding_chunks'::regclass AND attname='vector' AND atttypid='bytea'::regtype`, 1)
	})
	t.Run("unknown-legacy-column-preserves-state", func(t *testing.T) {
		databaseDSN := nativeDatabase(t, dsn, "unknown_column")
		store := nativeOpen(t, databaseDSN)
		nativeSeed(t, store.db, false)
		nativeExec(t, store.db, `CREATE EXTENSION vector`)
		nativeExec(t, store.db, `ALTER TABLE knowl_embedding_chunks ALTER COLUMN ordinal TYPE bigint`)
		nativeAssertOpenFailure(t, databaseDSN, knowl.MigrationStageApply, nativeOptions())
		nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_state`, 1)
		nativeAssertCount(t, store.db, `SELECT count(*) FROM goose_pgvector_db_version WHERE version_id=1`, 0)
	})
	t.Run("native-dimensions-and-schema-drift", func(t *testing.T) {
		databaseDSN := nativeDatabase(t, dsn, "dimensions")
		store := nativeOpen(t, databaseDSN)
		nativeExec(t, store.db, `CREATE EXTENSION vector`)
		dense := nativeOpen(t, databaseDSN, nativeOptions())
		if dense.nativeErr != nil {
			t.Fatal(dense.nativeErr)
		}
		nativeSeedAuthoritative(t, store.db)
		for _, dimensions := range []int{1, 2000, 2001, 4096} {
			vector := "[1" + strings.Repeat(",0", dimensions-1) + "]"
			nativeSeedDense(t, store.db, dimensions, vector, true)
			var gotDimensions int
			var norm, cosine float64
			if err := store.db.QueryRowContext(ctx, `SELECT vector_dims(vector),vector_norm(vector),1-(vector <=> $1::vector) FROM knowl_embedding_chunks`, vector).Scan(&gotDimensions, &norm, &cosine); err != nil {
				t.Fatal(err)
			}
			if gotDimensions != dimensions || norm != 1 || cosine != 1 {
				t.Fatalf("dimension/norm/cosine=%d/%v/%v", gotDimensions, norm, cosine)
			}
		}
		for _, dimensions := range []int{0, 4097} {
			_, err := store.db.ExecContext(ctx, `UPDATE knowl_embedding_chunks SET dimensions=$1`, dimensions)
			nativeAssertSQLState(t, err, "23514")
		}
		_, err := store.db.ExecContext(ctx, `UPDATE knowl_embedding_chunks SET vector='[1,0]'::vector`)
		nativeAssertSQLState(t, err, "23514")
		// An unknown constraint with the expected name must not admit readiness.
		nativeExec(t, store.db, `ALTER TABLE knowl_embedding_chunks DROP CONSTRAINT knowl_embedding_chunks_vector_check`)
		nativeExec(t, store.db, `ALTER TABLE knowl_embedding_chunks ADD CONSTRAINT knowl_embedding_chunks_vector_check CHECK (true)`)
		nativeAssertOpenFailure(t, databaseDSN, knowl.MigrationStageValidation, nativeOptions())
	})
	t.Run("unknown-history", func(t *testing.T) {
		databaseDSN := nativeDatabase(t, dsn, "history")
		store := nativeOpen(t, databaseDSN)
		nativeExec(t, store.db, `CREATE EXTENSION vector`)
		dense := nativeOpen(t, databaseDSN, nativeOptions())
		if dense.nativeErr != nil {
			t.Fatal(dense.nativeErr)
		}
		nativeSeedAuthoritative(t, store.db)
		nativeExec(t, store.db, `INSERT INTO goose_pgvector_db_version(version_id,is_applied) VALUES(99,true)`)
		nativeAssertOpenFailure(t, databaseDSN, knowl.MigrationStageHistory, nativeOptions())
	})
	t.Run("runtime-role-and-pending-authority", func(t *testing.T) {
		databaseDSN := nativeDatabase(t, dsn, "privileges")
		store := nativeOpen(t, databaseDSN)
		nativeExec(t, store.db, `CREATE EXTENSION vector`)
		nativeExec(t, store.db, `CREATE ROLE native_runtime LOGIN PASSWORD 'fixture-runtime'`)
		nativeExec(t, store.db, `GRANT USAGE ON SCHEMA public TO native_runtime`)
		nativeExec(t, store.db, `GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO native_runtime`)
		nativeSeedAuthoritative(t, store.db)
		runtimeURL, err := url.Parse(databaseDSN)
		if err != nil {
			t.Fatal(err)
		}
		runtimeURL.User = url.UserPassword("native_runtime", "fixture-runtime")
		nativeAssertOpenFailure(t, runtimeURL.String(), knowl.MigrationStageApply, nativeOptions())
		nativeAssertCount(t, store.db, `SELECT count(*) FROM pg_class WHERE relname='goose_pgvector_db_version'`, 0)
		dense := nativeOpen(t, databaseDSN, nativeOptions())
		if dense.nativeErr != nil {
			t.Fatal(dense.nativeErr)
		}
		nativeExec(t, store.db, `GRANT SELECT ON goose_pgvector_db_version TO native_runtime`)
		runtimeStore := nativeOpen(t, runtimeURL.String(), nativeOptions())
		if runtimeStore.nativeErr != nil {
			t.Fatal(runtimeStore.nativeErr)
		}
		nativeAssertCount(t, runtimeStore.db, `SELECT count(*) FROM pg_class WHERE oid='knowl_embedding_chunks'::regclass AND relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user)`, 0)
		assertPostgresNativeLifecycleRebuild(t, runtimeStore)
	})
	t.Run("concurrent-first-open-and-lock-deadline", func(t *testing.T) {
		databaseDSN := nativeDatabase(t, dsn, "concurrent")
		db, err := sql.Open("pgx", databaseDSN)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		nativeExec(t, db, `CREATE EXTENSION vector`)
		results := make(chan error, 2)
		for range 2 {
			go func() {
				store, err := Open(ctx, databaseDSN, nativeOptions())
				if err == nil {
					err = store.nativeErr
					_ = store.Close()
				}
				results <- err
			}()
		}
		for range 2 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		nativeAssertCount(t, db, `SELECT count(*) FROM goose_pgvector_db_version WHERE version_id=1 AND is_applied`, 1)
		assertPostgresNativeLifecycleRebuild(t, nativeOpen(t, databaseDSN, nativeOptions()))
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lock.DefaultLockID); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lock.DefaultLockID)
		}()
		deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err = Open(deadline, databaseDSN, nativeOptions())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("base lock deadline=%v", err)
		}
		deadlineOptional, cancelOptional := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancelOptional()
		_, err = migrateDatabase(deadlineOptional, db, knowl.MigrationUp, true)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("optional lock deadline=%v", err)
		}
	})
}

func nativeDatabase(t *testing.T, dsn, name string) string {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	nativeExec(t, db, `CREATE DATABASE `+name)
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	return parsed.String()
}

func nativeSeedAuthoritative(t *testing.T, db *sql.DB) {
	t.Helper()
	nativeExec(t, db, `INSERT INTO knowl_projection_state(scope,schema_digest,snapshot_digest,page_count,link_count,ready_at) VALUES('native','schema','snapshot',1,0,now())`)
	nativeExec(t, db, `INSERT INTO knowl_pages(scope,page_id,path,title,body,digest,updated_at,search_vector) VALUES('native','page','page.md','retained','lexical','digest',now(),to_tsvector('simple','retained lexical'))`)
	nativeExec(t, db, `INSERT INTO knowl_operations(operation_id,scope,source_adapter,source_id,source_version,source_digest,schema_digest,status,created_at,updated_at,work_ready_at) VALUES('operation','native','fixture','source','1','digest','schema','reserved',now(),now(),now())`)
	nativeExec(t, db, `INSERT INTO knowl_sources(scope,source_id,source_type,config_digest,created_at,updated_at) VALUES('native','source','fixture','config',now(),now())`)
	nativeExec(t, db, `INSERT INTO knowl_source_documents(scope,source_id,document_id,revision,accepted_source,last_seen_run_id,created_at,updated_at) VALUES('native','source','document','1','{"raw":"retained"}','run',now(),now())`)
}

func nativeSeed(t *testing.T, db *sql.DB, native bool) {
	t.Helper()
	nativeSeedAuthoritative(t, db)
	nativeSeedDense(t, db, 1, "[1]", native)
}

func nativeSeedDense(t *testing.T, db *sql.DB, dimensions int, vector string, native bool) {
	t.Helper()
	nativeExec(t, db, `DELETE FROM knowl_embedding_state`)
	nativeExec(t, db, `INSERT INTO knowl_embedding_state(scope,space,snapshot_digest,dimensions,mode,chunk_count,omitted_chunks,omitted_runes,ready_at,native_format) VALUES('native',repeat('a',64),repeat('b',64),$1,'hybrid',1,0,0,now(),$2)`, dimensions, map[bool]int{true: 1, false: 0}[native])
	value := "decode('0000803f','hex')"
	var args []any
	if native {
		value = "$1::vector"
		args = []any{vector}
	}
	nativeExec(t, db, fmt.Sprintf(`INSERT INTO knowl_embedding_chunks(scope,space,page_id,page_digest,ordinal,content_hash,dimensions,vector) VALUES('native',repeat('a',64),'page','digest',0,repeat('c',64),%d,%s)`, dimensions, value), args...)
}

func nativeAuthoritative(t *testing.T, db *sql.DB) string {
	t.Helper()
	var rows string
	if err := db.QueryRowContext(t.Context(), `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(p)) FROM knowl_pages p),(SELECT jsonb_agg(to_jsonb(o)) FROM knowl_operations o),(SELECT jsonb_agg(to_jsonb(p)) FROM knowl_projection_state p),(SELECT jsonb_agg(to_jsonb(s)) FROM knowl_sources s),(SELECT jsonb_agg(to_jsonb(d)) FROM knowl_source_documents d))::text`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func nativeAssertSQLState(t *testing.T, err error, code string) {
	t.Helper()
	var classified *pgconn.PgError
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("SQLSTATE want=%s got=%v", code, err)
	}
}

func nativeOptions() app.EmbeddingOptions {
	return app.EmbeddingOptions{Provider: structuredEvidenceProvider{}, FailurePolicy: app.EmbeddingStrict, Space: app.EmbeddingSpace{Model: "native-schema", Revision: "1", Dimensions: 2}}
}

func nativeOpen(t *testing.T, dsn string, options ...app.EmbeddingOptions) *Store {
	t.Helper()
	store, err := Open(t.Context(), dsn, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func nativeAssertOpenFailure(t *testing.T, dsn string, stage knowl.MigrationStage, options ...app.EmbeddingOptions) {
	t.Helper()
	store, err := Open(t.Context(), dsn, options...)
	if store != nil {
		_ = store.Close()
		t.Fatal("failed required initialization returned a usable store")
	}
	var failure *knowl.MigrationError
	if !errors.As(err, &failure) || failure.Stage != stage || failure.Stream != "pgvector" {
		t.Fatalf("required initialization failure=%v, want pgvector/%s", err, stage)
	}
}

func nativeBaseProvider(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, directory, goose.WithGoMigrations(operatorOperationsMigration()))
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func nativeOptionalProvider(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	// Use the deployed source directory through the real Goose SQL interface.
	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS("pgvector_migrations"), goose.WithTableName("goose_pgvector_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func nativeExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func nativeAssertCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(t.Context(), query).Scan(&got); err != nil || got != want {
		t.Fatalf("count=%d want=%d err=%v", got, want, err)
	}
}
