//go:build integration

package knowl

import (
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/testcontainers/testcontainers-go"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestHostPostgresRequiredMigrationFailurePreventsReadiness(t *testing.T) {
	container, err := pgcontainer.Run(t.Context(), "pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc", pgcontainer.WithDatabase("startup_failure"), pgcontainer.WithUsername("knowl"), pgcontainer.WithPassword("knowl"), pgcontainer.BasicWaitStrategies(), pgcontainer.WithSQLDriver("pgx"))
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
	var calls atomic.Int32
	embeddings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer embeddings.Close()
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.StoreDriver = StorePostgres
	config.PostgresDSN = dsn
	config.Embeddings = EmbeddingsConfig{Enabled: true, Endpoint: embeddings.URL, Model: "startup", Revision: "1", Dimensions: 2, FailurePolicy: app.EmbeddingFallbackLexical}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config.ListenAddr = listener.Addr().String()
	_ = listener.Close()
	host, err := NewHost(t.Context(), config, provider.Fixture{})
	if host != nil {
		_ = host.Close()
		t.Fatal("required native migration failure returned a host")
	}
	var failure *domain.MigrationError
	if !errors.As(err, &failure) || failure.Stage != domain.MigrationStageValidation || failure.Stream != "pgvector" {
		t.Fatalf("startup failure=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("schema failure reached embedding provider %d times", calls.Load())
	}
	listener, err = net.Listen("tcp", config.ListenAddr)
	if err != nil {
		t.Fatalf("failed initialization retained listener: %v", err)
	}
	_ = listener.Close()
	var connections int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND backend_type='client backend'`).Scan(&connections); err != nil || connections != 0 {
		t.Fatalf("failed startup retained %d database sessions, err=%v", connections, err)
	}
	// Lexical-only startup uses the same committed base migration state without
	// installing the extension or touching optional history.
	config.Embeddings.Enabled = false
	host, err = NewHost(t.Context(), config, provider.Fixture{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()
	if host.Ready() || host.listener != nil || host.started {
		t.Fatal("composition unexpectedly started listeners or workers")
	}
	var version, optional int
	if err := db.QueryRowContext(t.Context(), `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil || version != 26 {
		t.Fatalf("base schema version=%d err=%v", version, err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_class WHERE relname='goose_pgvector_db_version'`).Scan(&optional); err != nil || optional != 0 {
		t.Fatalf("lexical startup optional histories=%d err=%v", optional, err)
	}
}
