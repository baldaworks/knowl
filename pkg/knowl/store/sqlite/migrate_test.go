package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	migrationTestStream     = "base"
	malformedHistoryFixture = "malformed"
)

// Catches implicit Up on Down, multi-step rollback, missing Go19 and inaccurate results.
func TestMigrateSQLiteLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.sqlite")
	result, err := Migrate(t.Context(), path, knowl.MigrationDown)
	if err != nil || result.Backend != migrationBackend || result.Direction != knowl.MigrationDown || result.Steps == nil || len(result.Steps) != 0 {
		t.Fatalf("initial Down: result=%+v error=%v", result, err)
	}
	db := migrationTestDB(t, path)
	var tables int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='goose_db_version'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("Down initialized history: tables=%d error=%v", tables, err)
	}
	result, err = Migrate(t.Context(), path, knowl.MigrationUp)
	if err != nil {
		t.Fatal(err)
	}
	versions := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 21, 22, 23, 24, 25, 26}
	if len(result.Steps) != len(versions) {
		t.Fatalf("Up steps=%+v", result.Steps)
	}
	for i, version := range versions {
		if result.Steps[i] != (knowl.MigrationStep{Stream: migrationTestStream, Version: version}) {
			t.Fatalf("step %d: %+v", i, result.Steps[i])
		}
	}
	before := migrationHistoryRecords(t, db)
	result, err = Migrate(t.Context(), path, knowl.MigrationUp)
	if err != nil || len(result.Steps) != 0 || !reflect.DeepEqual(before, migrationHistoryRecords(t, db)) {
		t.Fatalf("repeat Up: %+v %v", result, err)
	}
	for i := len(versions) - 1; i >= 0; i-- {
		result, err = Migrate(t.Context(), path, knowl.MigrationDown)
		if err != nil || !reflect.DeepEqual(result.Steps, []knowl.MigrationStep{{Stream: migrationTestStream, Version: versions[i]}}) {
			t.Fatalf("Down %d: %+v %v", versions[i], result, err)
		}
		if got := migrationHistory(t, db); len(got) != i+1 {
			t.Fatalf("Down %d left history %+v", versions[i], got)
		}
	}
	result, err = Migrate(t.Context(), path, knowl.MigrationDown)
	if err != nil || len(result.Steps) != 0 {
		t.Fatalf("zero Down: %+v %v", result, err)
	}
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if got := migrationHistory(t, db); len(got) != 26 || got[len(got)-1] != 26 {
		t.Fatalf("Open did not upgrade: %+v", got)
	}
}

// Catches silent repair of corrupt history on either operation.
func TestMigrateSQLiteRejectsHistory(t *testing.T) {
	for _, direction := range []knowl.MigrationDirection{knowl.MigrationUp, knowl.MigrationDown} {
		for _, fixture := range []string{"empty-history", "unknown", "duplicate", "missing-zero", "unapplied", "out-of-order", malformedHistoryFixture} {
			t.Run(string(direction)+"/"+fixture, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "bad.sqlite")
				db := migrationTestDB(t, path)
				if fixture == malformedHistoryFixture {
					if _, err := db.ExecContext(t.Context(), `CREATE TABLE goose_db_version (unexpected TEXT)`); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := Migrate(t.Context(), path, knowl.MigrationUp); err != nil {
						t.Fatal(err)
					}
					statements := map[string]string{
						"empty-history": `DELETE FROM goose_db_version`,
						"unknown":       `INSERT INTO goose_db_version(version_id,is_applied) VALUES(999,1)`,
						"duplicate":     `INSERT INTO goose_db_version(version_id,is_applied) VALUES(26,1)`,
						"missing-zero":  `DELETE FROM goose_db_version WHERE version_id=0`,
						"unapplied":     `UPDATE goose_db_version SET is_applied=0 WHERE version_id=26`,
						"out-of-order":  `UPDATE goose_db_version SET version_id=CASE version_id WHEN 25 THEN 26 WHEN 26 THEN 25 ELSE version_id END`,
					}
					if _, err := db.ExecContext(t.Context(), statements[fixture]); err != nil {
						t.Fatal(err)
					}
				}
				var before int
				var beforeRecords []migrationHistoryRecord
				if fixture != malformedHistoryFixture {
					beforeRecords = migrationHistoryRecords(t, db)
				}
				if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM goose_db_version`).Scan(&before); err != nil {
					t.Fatal(err)
				}
				_, err := Migrate(t.Context(), path, direction)
				var failure *knowl.MigrationError
				if !errors.As(err, &failure) || failure.Stage != knowl.MigrationStageHistory {
					t.Fatalf("error=%v", err)
				}
				var after int
				if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM goose_db_version`).Scan(&after); err != nil || after != before {
					t.Fatalf("history mutated: %d -> %d: %v", before, after, err)
				}
				if fixture != malformedHistoryFixture && !reflect.DeepEqual(beforeRecords, migrationHistoryRecords(t, db)) {
					t.Fatal("rejected operation changed history records")
				}
			})
		}
	}
}

// Catches loss of cancellation and leaking raw configuration through public errors.
func TestMigrateSQLiteSafeFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Migrate(ctx, filepath.Join(t.TempDir(), "cancel.sqlite"), knowl.MigrationUp)
	var failure *knowl.MigrationError
	if !errors.Is(err, context.Canceled) || !errors.As(err, &failure) {
		t.Fatalf("error=%v", err)
	}
	_, err = Migrate(t.Context(), "", knowl.MigrationUp)
	if !errors.As(err, &failure) || failure.Stage != knowl.MigrationStageConfiguration {
		t.Fatalf("error=%v", err)
	}
	_, err = Migrate(t.Context(), filepath.Join(t.TempDir(), "invalid.sqlite"), knowl.MigrationDirection("secret-input"))
	if !errors.As(err, &failure) || failure.Stage != knowl.MigrationStageConfiguration {
		t.Fatalf("error=%v", err)
	}
}

// Catches falsely reporting a failed body as executed and retry skipping rolled-back work.
func TestMigrateSQLiteTransactionalFailureRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry.sqlite")
	if _, err := Migrate(t.Context(), path, knowl.MigrationUp); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(t.Context(), path, knowl.MigrationDown); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t, path)
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER interrupt_migration BEFORE INSERT ON goose_db_version WHEN NEW.version_id=26 BEGIN SELECT RAISE(ABORT,'secret-driver-text'); END`); err != nil {
		t.Fatal(err)
	}
	before := migrationHistory(t, db)
	result, err := Migrate(t.Context(), path, knowl.MigrationUp)
	var failure *knowl.MigrationError
	if !errors.As(err, &failure) || failure.Stage != knowl.MigrationStageApply || failure.Version != 26 || len(result.Steps) != 0 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if !reflect.DeepEqual(before, migrationHistory(t, db)) {
		t.Fatal("failed Up committed history")
	}
	var columns int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM pragma_table_info('knowl_embedding_state') WHERE name='native_format'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("failed Up committed schema: %d %v", columns, err)
	}
	if _, err := db.ExecContext(t.Context(), `DROP TRIGGER interrupt_migration`); err != nil {
		t.Fatal(err)
	}
	result, err = Migrate(t.Context(), path, knowl.MigrationUp)
	if err != nil || !reflect.DeepEqual(result.Steps, []knowl.MigrationStep{{Stream: migrationTestStream, Version: 26}}) {
		t.Fatalf("retry: %+v %v", result, err)
	}
}

// Catches dropping successful steps when Goose reports a later transactional failure.
func TestMigrateSQLitePartialProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.sqlite")
	if _, err := Migrate(t.Context(), path, knowl.MigrationUp); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := Migrate(t.Context(), path, knowl.MigrationDown); err != nil {
			t.Fatal(err)
		}
	}
	db := migrationTestDB(t, path)
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER interrupt_migration BEFORE INSERT ON goose_db_version WHEN NEW.version_id=26 BEGIN SELECT RAISE(ABORT,'secret-driver-text'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := Migrate(t.Context(), path, knowl.MigrationUp)
	var failure *knowl.MigrationError
	if !errors.As(err, &failure) || failure.Version != 26 {
		t.Fatalf("error=%v", err)
	}
	want := []knowl.MigrationStep{{Stream: migrationTestStream, Version: 24}, {Stream: migrationTestStream, Version: 25}}
	if !reflect.DeepEqual(result.Steps, want) {
		t.Fatalf("successful steps=%+v; want %+v", result.Steps, want)
	}
	versions := migrationHistory(t, db)
	if versions[len(versions)-1] != 25 {
		t.Fatalf("committed history=%+v", versions)
	}
}

func migrationTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func migrationHistory(t *testing.T, db *sql.DB) []int64 {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var versions []int64
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return versions
}

type migrationHistoryRecord struct {
	ID      int64
	Version int64
	Applied int64
	Stamp   any
}

func migrationHistoryRecords(t *testing.T, db *sql.DB) []migrationHistoryRecord {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT id,version_id,is_applied,tstamp FROM goose_db_version ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var records []migrationHistoryRecord
	for rows.Next() {
		var record migrationHistoryRecord
		if err := rows.Scan(&record.ID, &record.Version, &record.Applied, &record.Stamp); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}
