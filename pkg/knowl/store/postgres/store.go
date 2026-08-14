// Package postgres implements Knowl operational state and search projections
// with PostgreSQL-native transactions and full-text search.
package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var (
	ErrConflict           = errors.New("knowl operation conflict")
	ErrNotFound           = app.ErrOperationNotFound
	ErrInvalidState       = errors.New("knowl operation state transition is invalid")
	ErrLeaseConflict      = app.ErrApplyLeaseConflict
	ErrInvalidQuery       = errors.New("knowl search query is invalid")
	ErrProjectionNotReady = errors.New("knowl projection is not ready")
	ErrProjectionDrift    = errors.New("knowl projection drift detected")
)

const (
	maxPageLimit     = 100
	defaultPageLimit = 20
)

// Store implements app.OperationStore and app.SearchIndex.
type Store struct {
	db  *sql.DB
	dsn string
	mu  sync.Mutex
}

var (
	_ app.OperationStore = (*Store)(nil)
	_ app.SearchIndex    = (*Store)(nil)
)

// Open opens a PostgreSQL operational store and runs its embedded migrations.
func Open(ctx context.Context, dsn string) (*Store, error) {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return nil, fmt.Errorf("postgres dsn is required")
	}
	db, err := sql.Open("pgx", trimmed)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)
	store := &Store{db: db, dsn: trimmed}
	if err := store.configure(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the underlying database connection pool.
func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

// DSN returns the configured connection string.
func (store *Store) DSN() string { return store.dsn }

func (store *Store) configure(ctx context.Context) error {
	if err := store.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded postgres migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, store.db, directory)
	if err != nil {
		return fmt.Errorf("create postgres migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply postgres migrations: %w", err)
	}
	return nil
}
