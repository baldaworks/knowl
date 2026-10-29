// Package sqlite implements Knowl operational state and search projections
// using the same modernc.org/sqlite engine as Balda.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/writelock"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
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
	ErrInvalidProjection  = lexical.ErrInvalidProjection
)

const (
	maxPageLimit     = 100
	defaultPageLimit = 20
)

// Store implements app.OperationStore and app.SearchIndex.
type Store struct {
	embedding *hybrid.Engine
	db        *sql.DB
	path      string
	mu        writelock.Mutex
}

var (
	_ app.OperationStore           = (*Store)(nil)
	_ app.OperationDiagnosticStore = (*Store)(nil)
	_ app.SearchIndex              = (*Store)(nil)
	_ app.SourceStateStore         = (*Store)(nil)
	_ app.SourceLifecycleStore     = (*Store)(nil)
	_ app.StatisticsReader         = (*Store)(nil)
)

// Open opens or creates a Knowl SQLite operational store and runs migrations.
func Open(ctx context.Context, path string, options ...app.EmbeddingOptions) (*Store, error) {
	embedding, err := hybrid.New(options)
	if err != nil {
		return nil, err
	}
	db, err := openConnection(ctx, path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, path: strings.TrimSpace(path), embedding: embedding}
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
	_, err := migrateDatabase(ctx, store.db, knowl.MigrationUp)
	return err
}
