//go:build integration

package main

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/testcontainers/testcontainers-go"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestDatabaseMigrationPostgresCommandSelectsNativeModeAndRollbackStream(t *testing.T) {
	container, err := pgcontainer.Run(t.Context(), "pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc", pgcontainer.WithDatabase("migration_commands"), pgcontainer.WithUsername("knowl"), pgcontainer.WithPassword("knowl"), pgcontainer.BasicWaitStrategies(), pgcontainer.WithSQLDriver("pgx"))
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	dsn, err := container.ConnectionString(t.Context(), "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), "CREATE EXTENSION vector"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	clearKnowlEnv(t)
	t.Setenv("KNOWL_MIGRATION_MISSING_KEY", "")
	configRoot := writeDatabaseMigrationConfig(t, fmt.Sprintf(`knowl:
  provider: nonexistent
  workspace:
    path: uninitialized
  storage:
    type: postgres
    postgres:
      dsn: %q
  embeddings:
    enabled: true
    endpoint: http://127.0.0.1:1
    model: unavailable
    revision: "1"
    dimensions: 2
    api_key_env: KNOWL_MIGRATION_MISSING_KEY
profiles:
  lexical:
    knowl:
      embeddings:
        enabled: false
`, dsn))
	args := []string{databaseMigrationConfigDirFlag, configRoot, migrateCommandName}
	result := runDatabaseMigrationCommand(t, args, domain.MigrationUp)
	if result.Backend != "postgres" || result.Direction != domain.MigrationUp || len(result.Steps) != 27 || result.Steps[26] != (domain.MigrationStep{Stream: "pgvector", Version: 1}) {
		t.Fatalf("native PostgreSQL up=%+v", result)
	}
	assertPostgresCommandVersion(t, db, 26, 1)
	result = runDatabaseMigrationCommand(t, args, domain.MigrationUp)
	if result.Steps == nil || len(result.Steps) != 0 {
		t.Fatalf("repeat native up=%+v", result)
	}
	// Configuration mode cannot hide an existing optional stream during Down.
	args = []string{databaseMigrationConfigDirFlag, configRoot, "--profile", "lexical", migrateCommandName}
	result = runDatabaseMigrationCommand(t, args, domain.MigrationDown)
	if len(result.Steps) != 1 || result.Steps[0] != (domain.MigrationStep{Stream: "pgvector", Version: 1}) {
		t.Fatalf("lexical-mode native down=%+v", result)
	}
	assertPostgresCommandVersion(t, db, 26, 0)
	result = runDatabaseMigrationCommand(t, args, domain.MigrationDown)
	if len(result.Steps) != 1 || result.Steps[0] != (domain.MigrationStep{Stream: databaseMigrationBaseStream, Version: 26}) {
		t.Fatalf("base down=%+v", result)
	}
	assertPostgresCommandVersion(t, db, 25, 0)
	if _, err := os.Stat("uninitialized"); !os.IsNotExist(err) {
		t.Fatalf("database command initialized workspace: %v", err)
	}
}

func assertPostgresCommandVersion(t *testing.T, db *sql.DB, base, native int) {
	t.Helper()
	var actualBase, actualNative int
	if err := db.QueryRowContext(t.Context(), `SELECT (SELECT max(version_id) FROM goose_db_version WHERE is_applied), (SELECT max(version_id) FROM goose_pgvector_db_version WHERE is_applied)`).Scan(&actualBase, &actualNative); err != nil || actualBase != base || actualNative != native {
		t.Fatalf("PostgreSQL base=%d native=%d want=%d/%d err=%v", actualBase, actualNative, base, native, err)
	}
}
