//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3/lock"
)

const migrationTriggerSQLState = "P0001"

func TestPostgresMigrationOperation(t *testing.T) {
	dsn := nativeOracleContainer(t)
	t.Run("base-and-native-order", func(t *testing.T) {
		target := nativeDatabase(t, dsn, "migration_order")
		db := migrationTestDB(t, target)
		migrationAssertSteps(t, target, knowl.MigrationDown, false, nil)
		nativeAssertCount(t, db, `SELECT count(*) FROM pg_class WHERE relname IN ('goose_db_version','goose_pgvector_db_version')`, 0)
		result, err := Migrate(t.Context(), target, knowl.MigrationUp, false)
		if err != nil || len(result.Steps) != 26 || result.Steps[18].Version != 19 {
			t.Fatalf("base Up result=%+v error=%v", result, err)
		}
		nativeAssertCount(t, db, `SELECT count(*) FROM pg_extension WHERE extname='vector'`, 0)
		nativeAssertCount(t, db, `SELECT count(*) FROM pg_class WHERE relname='goose_pgvector_db_version'`, 0)
		migrationAssertSteps(t, target, knowl.MigrationUp, false, nil)
		nativeExec(t, db, `CREATE SCHEMA "trusted vector"`)
		nativeExec(t, db, `CREATE EXTENSION vector SCHEMA "trusted vector"`)
		migrationAssertSteps(t, target, knowl.MigrationUp, true, []knowl.MigrationStep{{Stream: nativeMigrationStream, Version: 1}})
		migrationAssertSteps(t, target, knowl.MigrationUp, true, nil)
		migrationAssertSteps(t, target, knowl.MigrationDown, false, []knowl.MigrationStep{{Stream: nativeMigrationStream, Version: 1}})
		nativeAssertCount(t, db, `SELECT max(version_id) FROM goose_db_version`, 26)
		migrationAssertSteps(t, target, knowl.MigrationDown, false, []knowl.MigrationStep{{Stream: baseMigrationStream, Version: 26}})
		migrationAssertSteps(t, target, knowl.MigrationUp, true, []knowl.MigrationStep{{Stream: baseMigrationStream, Version: 26}, {Stream: nativeMigrationStream, Version: 1}})
		migrationAssertSteps(t, target, knowl.MigrationDown, false, []knowl.MigrationStep{{Stream: nativeMigrationStream, Version: 1}})
		for version := int64(26); version > 0; version-- {
			migrationAssertSteps(t, target, knowl.MigrationDown, false, []knowl.MigrationStep{{Stream: baseMigrationStream, Version: version}})
		}
		migrationAssertSteps(t, target, knowl.MigrationDown, true, nil)
		nativeAssertCount(t, db, `SELECT count(*) FROM goose_db_version`, 1)
		nativeAssertCount(t, db, `SELECT count(*) FROM goose_pgvector_db_version`, 1)
	})
	t.Run("failure-and-retry", func(t *testing.T) {
		target := nativeDatabase(t, dsn, "migration_failure")
		db := migrationTestDB(t, target)
		result, err := Migrate(t.Context(), target, knowl.MigrationUp, true)
		_ = migrationAssertFailure(t, err, knowl.MigrationStageValidation, nativeMigrationStream)
		if len(result.Steps) != 26 {
			t.Fatalf("committed base steps=%+v", result)
		}
		nativeExec(t, db, `CREATE EXTENSION vector`)
		optional := nativeOptionalProvider(t, db)
		if _, err := optional.Status(t.Context()); err != nil {
			t.Fatal(err)
		}
		nativeExec(t, db, `CREATE FUNCTION reject_runner_version() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version_id=1 THEN RAISE EXCEPTION 'secret-shaped-driver-detail' USING ERRCODE='P0001'; END IF; RETURN NEW; END $$`)
		nativeExec(t, db, `CREATE TRIGGER reject_runner_version BEFORE INSERT ON goose_pgvector_db_version FOR EACH ROW EXECUTE FUNCTION reject_runner_version()`)
		result, err = Migrate(t.Context(), target, knowl.MigrationUp, true)
		failure := migrationAssertFailure(t, err, knowl.MigrationStageApply, nativeMigrationStream)
		if failure.SQLState != migrationTriggerSQLState || failure.Version != 1 || len(result.Steps) != 0 {
			t.Fatalf("failed result=%+v metadata=%+v", result, failure)
		}
		nativeAssertCount(t, db, `SELECT count(*) FROM goose_pgvector_db_version WHERE version_id=1`, 0)
		nativeAssertCount(t, db, `SELECT count(*) FROM pg_attribute WHERE attrelid='knowl_embedding_chunks'::regclass AND attname='vector' AND atttypid='bytea'::regtype`, 1)
		nativeExec(t, db, `DROP TRIGGER reject_runner_version ON goose_pgvector_db_version`)
		migrationAssertSteps(t, target, knowl.MigrationUp, true, []knowl.MigrationStep{{Stream: nativeMigrationStream, Version: 1}})
		nativeExec(t, db, `CREATE FUNCTION reject_runner_down() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private-detail' USING ERRCODE='P0001'; END $$`)
		nativeExec(t, db, `CREATE TRIGGER reject_runner_down BEFORE DELETE ON goose_pgvector_db_version FOR EACH ROW EXECUTE FUNCTION reject_runner_down()`)
		result, err = Migrate(t.Context(), target, knowl.MigrationDown, false)
		failure = migrationAssertFailure(t, err, knowl.MigrationStageApply, nativeMigrationStream)
		if failure.Version != 1 || failure.SQLState != migrationTriggerSQLState || len(result.Steps) != 0 {
			t.Fatalf("failed Down result=%+v metadata=%+v", result, failure)
		}
		nativeAssertCount(t, db, `SELECT count(*) FROM goose_pgvector_db_version WHERE version_id=1`, 1)
		nativeAssertCount(t, db, `SELECT count(*) FROM pg_attribute WHERE attrelid='knowl_embedding_chunks'::regclass AND attname='vector' AND atttypid='vector'::regtype`, 1)
		nativeExec(t, db, `DROP TRIGGER reject_runner_down ON goose_pgvector_db_version`)
		migrationAssertSteps(t, target, knowl.MigrationDown, false, []knowl.MigrationStep{{Stream: nativeMigrationStream, Version: 1}})
	})
	t.Run("released-histories-and-runtime-role", func(t *testing.T) {
		target := nativeDatabase(t, dsn, "migration_released")
		store := nativeOpen(t, target)
		nativeExec(t, store.db, `CREATE EXTENSION vector`)
		dense := nativeOpen(t, target, nativeOptions())
		if dense.nativeErr != nil {
			t.Fatal(dense.nativeErr)
		}
		nativeExec(t, store.db, `CREATE ROLE migration_runtime LOGIN PASSWORD 'fixture-runtime'`)
		nativeExec(t, store.db, `GRANT CONNECT ON DATABASE migration_released TO migration_runtime`)
		nativeExec(t, store.db, `GRANT USAGE ON SCHEMA public TO migration_runtime`)
		nativeExec(t, store.db, `GRANT SELECT ON ALL TABLES IN SCHEMA public TO migration_runtime`)
		runtimeURL, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		runtimeURL.User = url.UserPassword("migration_runtime", "fixture-runtime")
		migrationAssertSteps(t, runtimeURL.String(), knowl.MigrationUp, true, nil)
		migrationAssertSteps(t, target, knowl.MigrationDown, false, []knowl.MigrationStep{{Stream: nativeMigrationStream, Version: 1}})
		_, err = Migrate(t.Context(), runtimeURL.String(), knowl.MigrationUp, true)
		failure := migrationAssertFailure(t, err, knowl.MigrationStageApply, nativeMigrationStream)
		if failure.SQLState != "42501" {
			t.Fatalf("pending DDL failure metadata=%+v", failure)
		}
		nativeAssertCount(t, store.db, `SELECT count(*) FROM goose_pgvector_db_version WHERE version_id=1`, 0)
		nativeExec(t, store.db, `REVOKE SELECT ON pg_catalog.pg_extension FROM PUBLIC`)
		_, err = Migrate(t.Context(), runtimeURL.String(), knowl.MigrationUp, true)
		failure = migrationAssertFailure(t, err, knowl.MigrationStageValidation, nativeMigrationStream)
		if failure.SQLState != "42501" {
			t.Fatalf("capability query lost SQLSTATE: %+v", failure)
		}
		nativeAssertSQLState(t, err, "42501")
	})
	t.Run("base-partial-up-retry", func(t *testing.T) {
		target := nativeDatabase(t, dsn, "migration_base_partial")
		db := migrationTestDB(t, target)
		if _, err := Migrate(t.Context(), target, knowl.MigrationUp, false); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if _, err := Migrate(t.Context(), target, knowl.MigrationDown, false); err != nil {
				t.Fatal(err)
			}
		}
		nativeExec(t, db, `CREATE FUNCTION reject_base_version() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version_id=26 THEN RAISE EXCEPTION 'postgres://private:secret@driver-detail' USING ERRCODE='P0001'; END IF; RETURN NEW; END $$`)
		nativeExec(t, db, `CREATE TRIGGER reject_base_version BEFORE INSERT ON goose_db_version FOR EACH ROW EXECUTE FUNCTION reject_base_version()`)
		result, err := Migrate(t.Context(), target, knowl.MigrationUp, false)
		failure := migrationAssertFailure(t, err, knowl.MigrationStageApply, baseMigrationStream)
		want := []knowl.MigrationStep{{Stream: baseMigrationStream, Version: 24}, {Stream: baseMigrationStream, Version: 25}}
		if !reflect.DeepEqual(result.Steps, want) || failure.Version != 26 || failure.SQLState != migrationTriggerSQLState {
			t.Fatalf("partial result=%+v failure=%+v", result, failure)
		}
		if err.Error() != (&knowl.MigrationError{Stage: knowl.MigrationStageApply, Stream: baseMigrationStream, Version: 26, SQLState: migrationTriggerSQLState}).Error() {
			t.Fatalf("unsafe diagnostic=%q", err.Error())
		}
		nativeAssertCount(t, db, `SELECT max(version_id) FROM goose_db_version`, 25)
		nativeAssertCount(t, db, `SELECT count(*) FROM pg_attribute WHERE attrelid='knowl_embedding_state'::regclass AND attname='native_format' AND NOT attisdropped`, 0)
		nativeExec(t, db, `DROP TRIGGER reject_base_version ON goose_db_version`)
		migrationAssertSteps(t, target, knowl.MigrationUp, false, []knowl.MigrationStep{{Stream: baseMigrationStream, Version: 26}})
	})
	t.Run("reject-optional-history-even-when-disabled", func(t *testing.T) {
		target := nativeDatabase(t, dsn, "migration_bad_optional")
		db := migrationTestDB(t, target)
		nativeExec(t, db, `CREATE EXTENSION vector`)
		if _, err := Migrate(t.Context(), target, knowl.MigrationUp, true); err != nil {
			t.Fatal(err)
		}
		nativeExec(t, db, `INSERT INTO goose_pgvector_db_version(version_id,is_applied) VALUES(99,true)`)
		_, err := Migrate(t.Context(), target, knowl.MigrationDown, false)
		_ = migrationAssertFailure(t, err, knowl.MigrationStageHistory, nativeMigrationStream)
		nativeAssertCount(t, db, `SELECT max(version_id) FROM goose_db_version`, 26)
		nativeAssertCount(t, db, `SELECT count(*) FROM goose_pgvector_db_version`, 3)
		// Lexical-only Up does not make an unused native history mandatory.
		migrationAssertSteps(t, target, knowl.MigrationUp, false, nil)
		_, err = Migrate(t.Context(), target, knowl.MigrationUp, true)
		_ = migrationAssertFailure(t, err, knowl.MigrationStageHistory, nativeMigrationStream)
	})
	t.Run("reject-history-without-repair", func(t *testing.T) {
		for _, fixture := range []struct{ name, mutation string }{
			{"empty_ledger", `DELETE FROM goose_db_version`},
			{"unknown", `INSERT INTO goose_db_version(version_id,is_applied) VALUES(99,true)`},
			{"false", `UPDATE goose_db_version SET is_applied=false WHERE version_id=26`},
			{"duplicate", `INSERT INTO goose_db_version(version_id,is_applied) VALUES(26,true)`},
			{"missing_zero", `DELETE FROM goose_db_version WHERE version_id=0`},
			{"out_of_order", `UPDATE goose_db_version SET version_id=CASE version_id WHEN 25 THEN 26 WHEN 26 THEN 25 ELSE version_id END`},
		} {
			t.Run(fixture.name, func(t *testing.T) {
				target := nativeDatabase(t, dsn, "migration_bad_"+fixture.name)
				db := migrationTestDB(t, target)
				if _, err := Migrate(t.Context(), target, knowl.MigrationUp, false); err != nil {
					t.Fatal(err)
				}
				nativeExec(t, db, fixture.mutation)
				var before, after string
				if err := db.QueryRowContext(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(h) ORDER BY id),'[]'::jsonb)::text FROM goose_db_version h`).Scan(&before); err != nil {
					t.Fatal(err)
				}
				for _, direction := range []knowl.MigrationDirection{knowl.MigrationUp, knowl.MigrationDown} {
					_, err := Migrate(t.Context(), target, direction, false)
					_ = migrationAssertFailure(t, err, knowl.MigrationStageHistory, baseMigrationStream)
				}
				if err := db.QueryRowContext(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(h) ORDER BY id),'[]'::jsonb)::text FROM goose_db_version h`).Scan(&after); err != nil || before != after {
					t.Fatalf("history changed %s->%s error=%v", before, after, err)
				}
			})
		}
	})
	t.Run("concurrent-down-and-cancellation", func(t *testing.T) {
		target := nativeDatabase(t, dsn, "migration_concurrent")
		db := migrationTestDB(t, target)
		nativeExec(t, db, `CREATE EXTENSION vector`)
		if _, err := Migrate(t.Context(), target, knowl.MigrationUp, true); err != nil {
			t.Fatal(err)
		}
		var group sync.WaitGroup
		results := make(chan knowl.MigrationResult, 2)
		errorsFound := make(chan error, 2)
		for range 2 {
			group.Go(func() {
				result, err := Migrate(t.Context(), target, knowl.MigrationDown, false)
				results <- result
				errorsFound <- err
			})
		}
		group.Wait()
		for range 2 {
			if err := <-errorsFound; err != nil {
				t.Fatal(err)
			}
		}
		steps := map[knowl.MigrationStep]bool{}
		for range 2 {
			result := <-results
			if len(result.Steps) != 1 {
				t.Fatalf("result=%+v", result)
			}
			steps[result.Steps[0]] = true
		}
		if !steps[knowl.MigrationStep{Stream: nativeMigrationStream, Version: 1}] || !steps[knowl.MigrationStep{Stream: baseMigrationStream, Version: 26}] {
			t.Fatalf("steps=%+v", steps)
		}
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		locker, err := lock.NewPostgresSessionLocker()
		if err != nil {
			t.Fatal(err)
		}
		if err := locker.SessionLock(t.Context(), conn); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		_, err = migrateDatabase(ctx, db, knowl.MigrationUp, true)
		cancel()
		_ = migrationAssertFailure(t, err, knowl.MigrationStageLock, baseMigrationStream)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancellation lost: %v", err)
		}
		if inUse := db.Stats().InUse; inUse != 1 {
			t.Fatalf("canceled runner leaked connection: in-use=%d", inUse)
		}
		if err := locker.SessionUnlock(t.Context(), conn); err != nil {
			t.Fatal(err)
		}
		migrationAssertSteps(t, target, knowl.MigrationUp, true, []knowl.MigrationStep{{Stream: baseMigrationStream, Version: 26}, {Stream: nativeMigrationStream, Version: 1}})
	})
	t.Run("startup-and-standalone-share-lock", func(t *testing.T) {
		target := nativeDatabase(t, dsn, "migration_startup_concurrent")
		db := migrationTestDB(t, target)
		nativeExec(t, db, `CREATE EXTENSION vector`)
		if _, err := Migrate(t.Context(), target, knowl.MigrationUp, true); err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		locker, err := lock.NewPostgresSessionLocker()
		if err != nil {
			t.Fatal(err)
		}
		if err := locker.SessionLock(t.Context(), conn); err != nil {
			t.Fatal(err)
		}
		// Explicit contention proves both entry points honor the existing Goose
		// identity; a start barrier alone could serialize by scheduling chance.
		blocked, cancelBlocked := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancelBlocked()
		blockedErrors := make(chan error, 2)
		go func() {
			store, err := Open(blocked, target, nativeOptions())
			if store != nil {
				err = errors.Join(err, store.Close())
			}
			blockedErrors <- err
		}()
		go func() {
			_, err := Migrate(blocked, target, knowl.MigrationDown, false)
			blockedErrors <- err
		}()
		for range 2 {
			err := <-blockedErrors
			_ = migrationAssertFailure(t, err, knowl.MigrationStageLock, baseMigrationStream)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("blocked operation lost cancellation: %v", err)
			}
		}
		nativeAssertCount(t, db, `SELECT max(version_id) FROM goose_db_version`, 26)
		nativeAssertCount(t, db, `SELECT max(version_id) FROM goose_pgvector_db_version`, 1)
		if err := locker.SessionUnlock(t.Context(), conn); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		start := make(chan struct{})
		errorsFound := make(chan error, 2)
		results := make(chan knowl.MigrationResult, 1)
		go func() {
			<-start
			store, err := Open(ctx, target, nativeOptions())
			if store != nil {
				err = errors.Join(err, store.Close())
			}
			errorsFound <- err
		}()
		go func() {
			<-start
			result, err := Migrate(ctx, target, knowl.MigrationDown, false)
			results <- result
			errorsFound <- err
		}()
		close(start)
		for range 2 {
			if err := <-errorsFound; err != nil {
				t.Fatal(err)
			}
		}
		result := <-results
		if !reflect.DeepEqual(result.Steps, []knowl.MigrationStep{{Stream: nativeMigrationStream, Version: 1}}) {
			t.Fatalf("concurrent Down=%+v", result)
		}
		nativeAssertCount(t, db, `SELECT max(version_id) FROM goose_db_version`, 26)
		// Either serialized order is valid: startup before Down leaves bytea;
		// startup after Down reapplies the native migration. Both must agree
		// with the committed native history, never a mixed schema/history.
		nativeAssertCount(t, db, `SELECT count(*) FROM pg_attribute a JOIN pg_type t ON t.oid=a.atttypid WHERE a.attrelid='knowl_embedding_chunks'::regclass AND a.attname='vector' AND t.typname=CASE WHEN EXISTS(SELECT 1 FROM goose_pgvector_db_version WHERE version_id=1) THEN 'vector' ELSE 'bytea' END`, 1)
	})
}

func migrationTestDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func migrationAssertSteps(t *testing.T, dsn string, direction knowl.MigrationDirection, native bool, want []knowl.MigrationStep) {
	t.Helper()
	result, err := Migrate(t.Context(), dsn, direction, native)
	if want == nil {
		want = []knowl.MigrationStep{}
	}
	if err != nil || result.Backend != migrationBackend || result.Direction != direction || !reflect.DeepEqual(result.Steps, want) {
		t.Fatalf("result=%+v want=%+v error=%v", result, want, err)
	}
}

func migrationAssertFailure(t *testing.T, err error, stage knowl.MigrationStage, stream string) *knowl.MigrationError {
	t.Helper()
	var failure *knowl.MigrationError
	if !errors.As(err, &failure) || failure.Stage != stage || failure.Stream != stream {
		t.Fatalf("failure=%v want stage=%s stream=%s", err, stage, stream)
	}
	return failure
}
