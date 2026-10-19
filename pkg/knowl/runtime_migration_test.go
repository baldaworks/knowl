package knowl

import (
	"database/sql"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/provider"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestHostSQLiteMigrationsPrecedeReadiness(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.StorePath = filepath.Join(config.Workspace, "startup.sqlite")
	if _, err := sqlite.Migrate(t.Context(), config.StorePath, domain.MigrationUp); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.Migrate(t.Context(), config.StorePath, domain.MigrationDown); err != nil {
		t.Fatal(err)
	}
	host, err := NewHost(t.Context(), config, provider.Fixture{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()
	if host.Ready() || host.listener != nil || host.started {
		t.Fatal("composition started service before lifecycle start")
	}
	db, err := sql.Open("sqlite", config.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var version int
	if err := db.QueryRowContext(t.Context(), `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil || version != 26 {
		t.Fatalf("startup schema version=%d err=%v", version, err)
	}
}

func TestHostSQLiteMigrationFailureNeverBecomesReady(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.StorePath = filepath.Join(config.Workspace, "startup.sqlite")
	if _, err := sqlite.Migrate(t.Context(), config.StorePath, domain.MigrationUp); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", config.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), `DELETE FROM goose_db_version WHERE version_id=0`); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config.ListenAddr = listener.Addr().String()
	_ = listener.Close()
	host, err := NewHost(t.Context(), config, provider.Fixture{})
	if host != nil {
		_ = host.Close()
		t.Fatal("failed migration returned a host")
	}
	var failure *domain.MigrationError
	if !errors.As(err, &failure) || failure.Stage != domain.MigrationStageHistory {
		t.Fatalf("migration failure=%v", err)
	}
	listener, err = net.Listen("tcp", config.ListenAddr)
	if err != nil {
		t.Fatalf("failed initialization retained listener: %v", err)
	}
	_ = listener.Close()
	// Failure neither repairs history nor leaves a database write lock behind.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO goose_db_version(version_id,is_applied) VALUES(0,true)`); err != nil {
		t.Fatal(err)
	}
}
