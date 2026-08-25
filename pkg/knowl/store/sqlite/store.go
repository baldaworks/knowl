// Package sqlite implements Knowl operational state and search projections
// using the same modernc.org/sqlite engine as Balda.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
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
	db   *sql.DB
	path string
	mu   sync.Mutex
}

var (
	_ app.OperationStore   = (*Store)(nil)
	_ app.SearchIndex      = (*Store)(nil)
	_ app.SourceStateStore = (*Store)(nil)
)

// Open opens or creates a Knowl SQLite operational store and runs migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if err := os.MkdirAll(filepath.Dir(trimmed), 0o700); err != nil {
		return nil, fmt.Errorf("create sqlite parent: %w", err)
	}
	databaseURL := url.URL{Scheme: "file", Path: trimmed}
	query := databaseURL.Query()
	query.Set("_txlock", "immediate")
	databaseURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, path: trimmed}
	if err := store.configure(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the underlying database.
func (store *Store) Close() error { return store.db.Close() }

// Path returns the configured database path.
func (store *Store) Path() string { return store.path }

func (store *Store) configure(ctx context.Context) error {
	for _, statement := range []string{
		"PRAGMA foreign_keys=ON",
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			if statement == "PRAGMA journal_mode=WAL" {
				continue
			}
			return fmt.Errorf("apply sqlite pragma: %w", err)
		}
	}
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded sqlite migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, store.db, directory)
	if err != nil {
		return fmt.Errorf("create sqlite migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply sqlite migrations: %w", err)
	}
	return nil
}
