// Package postgres implements Knowl operational state and search projections
// with PostgreSQL-native transactions and full-text search.
package postgres

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
	_ "github.com/jackc/pgx/v5/stdlib"
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
	native    nativeVectorCapability
	nativeErr error
	db        *sql.DB
	dsn       string
	mu        writelock.Mutex
}

var (
	_ app.OperationStore           = (*Store)(nil)
	_ app.OperationDiagnosticStore = (*Store)(nil)
	_ app.SearchIndex              = (*Store)(nil)
	_ app.SourceStateStore         = (*Store)(nil)
	_ app.SourceLifecycleStore     = (*Store)(nil)
)

// Open opens a PostgreSQL operational store and runs its embedded migrations.
func Open(ctx context.Context, dsn string, options ...app.EmbeddingOptions) (*Store, error) {
	embedding, err := hybrid.New(options)
	if err != nil {
		return nil, err
	}
	db, err := openConnection(ctx, dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, dsn: strings.TrimSpace(dsn), embedding: embedding}
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
	if _, err := migrateDatabase(ctx, store.db, knowl.MigrationUp, store.embedding != nil); err != nil {
		return err
	}
	if store.embedding != nil {
		capability, err := readNativeVectorCapabilityCause(ctx, store.db)
		if err != nil {
			return migrationFailure(knowl.MigrationStageValidation, nativeMigrationStream, err)
		}
		store.native = capability
	}
	return nil
}
