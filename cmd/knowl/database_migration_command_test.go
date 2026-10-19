package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const databaseMigrationConfigDirFlag = "--config-dir"

func TestDatabaseMigrationCommandsUseProfileWithoutStartingApplication(t *testing.T) {
	t.Chdir(t.TempDir())
	clearKnowlEnv(t)
	var requests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer endpoint.Close()
	t.Setenv("KNOWL_MIGRATION_MISSING_KEY", "")
	configRoot := writeDatabaseMigrationConfig(t, fmt.Sprintf(`knowl:
  workspace:
    path: unselected
  storage:
    type: sqlite
    sqlite:
      path: state.db
profiles:
  migration:
    knowl:
      provider: nonexistent
      workspace:
        path: selected
      sources:
        - id: missing
          type: filesystem
          filesystem:
            root: does-not-exist
      embeddings:
        enabled: true
        endpoint: %q
        model: unavailable
        revision: "1"
        dimensions: 2
        api_key_env: KNOWL_MIGRATION_MISSING_KEY
`, endpoint.URL))
	args := []string{databaseMigrationConfigDirFlag, configRoot, "--profile", "migration", migrateCommandName}
	path := filepath.Join("selected", "state.db")
	result := runDatabaseMigrationCommand(t, args, domain.MigrationUp)
	if result.Backend != defaultStore || result.Direction != domain.MigrationUp || len(result.Steps) != 25 {
		t.Fatalf("fresh up result=%+v", result)
	}
	for index, step := range result.Steps {
		version := int64(index + 1)
		if version >= 20 {
			version++ // The released SQLite stream has no migration 20.
		}
		if step.Stream != databaseMigrationBaseStream || step.Version != version {
			t.Fatalf("up step %d=%+v", index, step)
		}
	}
	assertDatabaseMigrationVersion(t, path, 26)
	result = runDatabaseMigrationCommand(t, args, domain.MigrationUp)
	if result.Steps == nil || len(result.Steps) != 0 {
		t.Fatalf("repeated up steps=%+v", result.Steps)
	}
	for _, version := range []int64{26, 25} {
		result = runDatabaseMigrationCommand(t, args, domain.MigrationDown)
		if result.Direction != domain.MigrationDown || len(result.Steps) != 1 || result.Steps[0] != (domain.MigrationStep{Stream: databaseMigrationBaseStream, Version: version}) {
			t.Fatalf("down result=%+v", result)
		}
		assertDatabaseMigrationVersion(t, path, version-1)
	}
	if requests.Load() != 0 {
		t.Fatalf("migration contacted embedding service %d times", requests.Load())
	}
	for _, untouched := range []string{"unselected", filepath.Join("selected", "wiki"), filepath.Join("selected", "raw")} {
		if _, err := os.Stat(untouched); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("migration initialized unrelated path %s: %v", untouched, err)
		}
	}
}

func TestDatabaseMigrationDownOnFreshDatabaseDoesNotApplyUp(t *testing.T) {
	t.Chdir(t.TempDir())
	clearKnowlEnv(t)
	configRoot := writeDatabaseMigrationConfig(t, "knowl:\n  workspace:\n    path: fresh\n  storage:\n    type: sqlite\n    sqlite:\n      path: state.db\n")
	result := runDatabaseMigrationCommand(t, []string{databaseMigrationConfigDirFlag, configRoot, migrateCommandName}, domain.MigrationDown)
	if result.Backend != defaultStore || result.Direction != domain.MigrationDown || result.Steps == nil || len(result.Steps) != 0 {
		t.Fatalf("fresh down result=%+v", result)
	}
	db, err := sql.Open("sqlite", filepath.Join("fresh", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var tables int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("fresh down created %d tables, err=%v", tables, err)
	}
}

// Catches migration failures being reported as successful CLI work and retries
// skipping a transaction which Goose rolled back.
func TestDatabaseMigrationSQLFailureHasNoSuccessAndRetryUsesCommittedHistory(t *testing.T) {
	t.Chdir(t.TempDir())
	clearKnowlEnv(t)
	configRoot := writeDatabaseMigrationConfig(t, "knowl:\n  workspace:\n    path: invalid-workspace\n  storage:\n    type: sqlite\n    sqlite:\n      path: state.db\n")
	args := []string{databaseMigrationConfigDirFlag, configRoot, migrateCommandName}
	_ = runDatabaseMigrationCommand(t, args, domain.MigrationUp)
	_ = runDatabaseMigrationCommand(t, args, domain.MigrationDown)
	path := filepath.Join("invalid-workspace", "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// Invalid canonical content must not participate in DB administration.
	canonical := filepath.Join("invalid-workspace", "wiki")
	invalid := []byte("not a canonical workspace directory")
	if err := os.WriteFile(canonical, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER reject_cli_version BEFORE INSERT ON goose_db_version WHEN NEW.version_id=26 BEGIN SELECT RAISE(ABORT,'postgres://private:secret@driver-detail'); END`); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := executeCLICommand(newRootCommand(), append(args, "up"), nil)
	var failure *domain.MigrationError
	if stdout != "" || !errors.As(err, &failure) || failure.Stage != domain.MigrationStageApply || failure.Version != 26 {
		t.Fatalf("failed migration output=%q error=%v", stdout, err)
	}
	if err.Error() != (&domain.MigrationError{Stage: domain.MigrationStageApply, Stream: databaseMigrationBaseStream, Version: 26}).Error() {
		t.Fatalf("unsafe SQL diagnostic=%q", err.Error())
	}
	assertDatabaseMigrationVersion(t, path, 25)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KNOWL_TEST_MIGRATION_PROCESS", "1")
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestDatabaseMigrationProcessHelper$", "--", databaseMigrationConfigDirFlag, configRoot, migrateCommandName, "up")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || len(output) != 0 || stderr.Len() == 0 {
		t.Fatalf("SQL failure process err=%v stdout=%q stderr=%q", err, output, stderr.String())
	}
	assertDatabaseMigrationVersion(t, path, 25)
	if _, err := db.ExecContext(t.Context(), `DROP TRIGGER reject_cli_version`); err != nil {
		t.Fatal(err)
	}
	result := runDatabaseMigrationCommand(t, args, domain.MigrationUp)
	if len(result.Steps) != 1 || result.Steps[0] != (domain.MigrationStep{Stream: databaseMigrationBaseStream, Version: 26}) {
		t.Fatalf("retry result=%+v", result)
	}
	assertDatabaseMigrationVersion(t, path, 26)
	actual, err := os.ReadFile(canonical)
	if err != nil || !bytes.Equal(actual, invalid) {
		t.Fatalf("migration touched invalid canonical content: %v", err)
	}
}

func TestDatabaseMigrationConfigurationFailureIsSafeAndHasNoSuccessResult(t *testing.T) {
	t.Chdir(t.TempDir())
	clearKnowlEnv(t)
	configRoot := writeDatabaseMigrationConfig(t, "knowl:\n  storage:\n    type: postgres://private:secret@invalid\n")
	stdout, _, err := executeCLICommand(newRootCommand(), []string{databaseMigrationConfigDirFlag, configRoot, migrateCommandName, "up"}, nil)
	var failure *domain.MigrationError
	if !errors.As(err, &failure) || failure.Stage != domain.MigrationStageConfiguration {
		t.Fatalf("configuration failure=%v", err)
	}
	if stdout != "" || err.Error() != (&domain.MigrationError{Stage: domain.MigrationStageConfiguration, Stream: databaseMigrationBaseStream}).Error() {
		t.Fatalf("unsafe failure output=%q error=%v", stdout, err)
	}
}

func TestDatabaseMigrationConnectionFailureIsSafeAndHasNoSuccessResult(t *testing.T) {
	t.Chdir(t.TempDir())
	clearKnowlEnv(t)
	configRoot := writeDatabaseMigrationConfig(t, "knowl:\n  storage:\n    type: postgres\n    postgres:\n      dsn: 'postgres://private:secret@%invalid'\n")
	stdout, _, err := executeCLICommand(newRootCommand(), []string{databaseMigrationConfigDirFlag, configRoot, migrateCommandName, "up"}, nil)
	var failure *domain.MigrationError
	if !errors.As(err, &failure) || failure.Stage != domain.MigrationStageConnection {
		t.Fatalf("connection failure=%v", err)
	}
	if stdout != "" || err.Error() != (&domain.MigrationError{Stage: domain.MigrationStageConnection, Stream: databaseMigrationBaseStream}).Error() {
		t.Fatalf("unsafe failure output=%q error=%v", stdout, err)
	}
}

func TestDatabaseMigrationProcessExitAndOutput(t *testing.T) {
	t.Chdir(t.TempDir())
	clearKnowlEnv(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KNOWL_TEST_MIGRATION_PROCESS", "1")
	for _, scenario := range []struct {
		name, document string
		failed         bool
	}{
		{name: "success", document: "knowl:\n  workspace:\n    path: fresh-process\n"},
		{name: "connection failure", document: "knowl:\n  storage:\n    type: postgres\n    postgres:\n      dsn: 'postgres://private:secret@%invalid'\n", failed: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			configRoot := writeDatabaseMigrationConfig(t, scenario.document)
			command := exec.CommandContext(t.Context(), executable, "-test.run=^TestDatabaseMigrationProcessHelper$", "--", databaseMigrationConfigDirFlag, configRoot, migrateCommandName, "down")
			var stderr bytes.Buffer
			command.Stderr = &stderr
			stdout, err := command.Output()
			if scenario.failed {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 || len(stdout) != 0 {
					t.Fatalf("failed process err=%v stdout=%q", err, stdout)
				}
				if stderr.Len() == 0 {
					t.Fatal("failed process emitted no diagnostic")
				}
				return
			}
			var result domain.MigrationResult
			if err != nil || json.Unmarshal(stdout, &result) != nil || result.Backend != defaultStore || result.Direction != domain.MigrationDown || result.Steps == nil || len(result.Steps) != 0 {
				t.Fatalf("successful process err=%v stdout=%q stderr=%q", err, stdout, stderr.String())
			}
		})
	}
}

// Run the actual executable entry point in a disposable subprocess, including
// main's exit code and structured failure logging, without building twice.
func TestDatabaseMigrationProcessHelper(t *testing.T) {
	if os.Getenv("KNOWL_TEST_MIGRATION_PROCESS") != "1" {
		return
	}
	for index, argument := range os.Args {
		if argument == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[index+1:]...)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing subprocess arguments")
}

func writeDatabaseMigrationConfig(t *testing.T, document string) string {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "knowl")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config.yaml"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func runDatabaseMigrationCommand(t *testing.T, args []string, direction domain.MigrationDirection) domain.MigrationResult {
	t.Helper()
	stdout, stderr, err := executeCLICommand(newRootCommand(), append(append([]string{}, args...), string(direction)), nil)
	if err != nil {
		t.Fatalf("migration command failed: %v, stderr=%s", err, stderr)
	}
	var result domain.MigrationResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode migration result: %v, output=%q", err, stdout)
	}
	return result
}

func assertDatabaseMigrationVersion(t *testing.T, path string, want int64) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var version int64
	if err := db.QueryRowContext(t.Context(), `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil || version != want {
		t.Fatalf("schema version=%d want=%d, err=%v", version, want, err)
	}
}
