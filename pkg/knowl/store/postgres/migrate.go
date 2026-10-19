package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

const (
	migrationBackend      = "postgres"
	baseMigrationStream   = "base"
	nativeMigrationStream = "pgvector"
)

// Migrate changes only the configured database schema, without store/provider
// initialization. Down never calls Up, even when both histories are absent.
func Migrate(ctx context.Context, dsn string, direction knowl.MigrationDirection, native bool) (knowl.MigrationResult, error) {
	result := knowl.MigrationResult{Backend: migrationBackend, Direction: direction, Steps: []knowl.MigrationStep{}}
	if direction != knowl.MigrationUp && direction != knowl.MigrationDown {
		return result, migrationFailure(knowl.MigrationStageConfiguration, baseMigrationStream, errors.New("invalid migration direction"))
	}
	db, err := openConnection(ctx, dsn)
	if err != nil {
		return result, err
	}
	defer func() { _ = db.Close() }()
	return migrateDatabase(ctx, db, direction, native)
}

func openConnection(ctx context.Context, dsn string) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, migrationFailure(knowl.MigrationStageConfiguration, baseMigrationStream, errors.New("postgres dsn is required"))
	}
	db, err := sql.Open("pgx", strings.TrimSpace(dsn))
	if err != nil {
		return nil, migrationFailure(knowl.MigrationStageConnection, baseMigrationStream, err)
	}
	// One connection owns the session lock while Goose uses the pool for its
	// own transactional migrations. A one-connection pool would deadlock.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, migrationFailure(knowl.MigrationStageConnection, baseMigrationStream, err)
	}
	return db, nil
}

// migrationProvider deliberately has no provider-level locker. Its caller must
// hold the operation lock across history inspection, selection and execution.
func migrationProvider(db *sql.DB, native bool) (*goose.Provider, error) {
	files, directory := fs.FS(migrationFiles), "migrations"
	options := []goose.ProviderOption{goose.WithDisableGlobalRegistry(true)}
	if native {
		files, directory = pgvectorMigrationFiles, "pgvector_migrations"
		options = append(options, goose.WithTableName(pgvectorHistory))
	} else {
		options = append(options, goose.WithGoMigrations(operatorOperationsMigration()))
	}
	source, err := fs.Sub(files, directory)
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(goose.DialectPostgres, db, source, options...)
}

func migrateDatabase(ctx context.Context, db *sql.DB, direction knowl.MigrationDirection, native bool) (result knowl.MigrationResult, err error) {
	result = knowl.MigrationResult{Backend: migrationBackend, Direction: direction, Steps: []knowl.MigrationStep{}}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return result, migrationFailure(knowl.MigrationStageLock, baseMigrationStream, err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return result, migrationFailure(knowl.MigrationStageConnection, baseMigrationStream, err)
	}
	defer func() { _ = conn.Close() }()
	if err := locker.SessionLock(ctx, conn); err != nil {
		// Cancellation can leave acquisition uncertain. Never return a possibly
		// locked session to the pool, even when no lock was confirmed locally.
		discardMigrationConnection(conn)
		return result, migrationFailure(knowl.MigrationStageLock, baseMigrationStream, err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if unlockErr := locker.SessionUnlock(cleanup, conn); unlockErr != nil {
			discardMigrationConnection(conn)
			err = errors.Join(err, migrationFailure(knowl.MigrationStageLock, baseMigrationStream, unlockErr))
		}
	}()
	base, err := migrationProvider(db, false)
	if err != nil {
		return result, migrationFailure(knowl.MigrationStageConfiguration, baseMigrationStream, err)
	}
	baseVersion, err := recognizeHistory(ctx, db, goose.DefaultTablename, base.ListSources())
	if err != nil {
		return result, migrationFailure(knowl.MigrationStageHistory, baseMigrationStream, err)
	}
	if direction == knowl.MigrationDown {
		return migrateDown(ctx, db, base, baseVersion, result)
	}
	if err := applyMigrations(ctx, base, baseMigrationStream, &result); err != nil {
		return result, err
	}
	if native {
		err = migrateNativeUp(ctx, db, &result)
	}
	return result, err
}

func discardMigrationConnection(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

func migrateDown(ctx context.Context, db *sql.DB, base *goose.Provider, baseVersion int64, result knowl.MigrationResult) (knowl.MigrationResult, error) {
	optional, err := migrationProvider(db, true)
	if err != nil {
		return result, migrationFailure(knowl.MigrationStageConfiguration, nativeMigrationStream, err)
	}
	nativeVersion, err := recognizeHistory(ctx, db, pgvectorHistory, optional.ListSources())
	if err != nil {
		return result, migrationFailure(knowl.MigrationStageHistory, nativeMigrationStream, err)
	}
	provider, stream, version := base, baseMigrationStream, baseVersion
	if nativeVersion != 0 {
		provider, stream, version = optional, nativeMigrationStream, nativeVersion
	}
	if version == 0 {
		return result, nil
	}
	step, err := provider.Down(ctx)
	if err != nil {
		failure := migrationFailure(knowl.MigrationStageApply, stream, err)
		var metadata *knowl.MigrationError
		if errors.As(failure, &metadata) {
			metadata.Version = version
		}
		return result, failure
	}
	result.Steps = append(result.Steps, knowl.MigrationStep{Stream: stream, Version: step.Source.Version})
	return result, nil
}

func migrateNativeUp(ctx context.Context, db *sql.DB, result *knowl.MigrationResult) error {
	capability, err := readNativeVectorCapabilityCause(ctx, db)
	if err != nil {
		return migrationFailure(knowl.MigrationStageValidation, nativeMigrationStream, err)
	}
	provider, err := migrationProvider(db, true)
	if err != nil {
		return migrationFailure(knowl.MigrationStageConfiguration, nativeMigrationStream, err)
	}
	if _, err := recognizeHistory(ctx, db, pgvectorHistory, provider.ListSources()); err != nil {
		return migrationFailure(knowl.MigrationStageHistory, nativeMigrationStream, err)
	}
	if err := applyMigrations(ctx, provider, nativeMigrationStream, result); err != nil {
		return err
	}
	return migrationFailure(knowl.MigrationStageValidation, nativeMigrationStream, validateNativeSchema(ctx, db, provider, capability))
}

func applyMigrations(ctx context.Context, provider *goose.Provider, stream string, result *knowl.MigrationResult) error {
	steps, err := provider.Up(ctx)
	var partial *goose.PartialError
	if errors.As(err, &partial) {
		steps = partial.Applied
	}
	for _, step := range steps {
		if step.Error == nil {
			result.Steps = append(result.Steps, knowl.MigrationStep{Stream: stream, Version: step.Source.Version})
		}
	}
	return migrationFailure(knowl.MigrationStageApply, stream, err)
}

// recognizeHistory reads only existing records. Goose deletes a rolled-back
// version, so a recognized ledger is exactly a prefix of the embedded sources.
// In particular, missing history is fresh; an existing empty table is not.
func recognizeHistory(ctx context.Context, db *sql.DB, table string, sources []*goose.Source) (int64, error) {
	var kind string
	err := db.QueryRowContext(ctx, `SELECT relkind::text FROM pg_class WHERE oid=to_regclass($1)`, table).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if kind != "r" {
		return 0, errors.New("invalid migration history object")
	}
	// Only fixed, internal history names reach this helper.
	query := `SELECT id,version_id,is_applied,tstamp FROM goose_db_version ORDER BY id`
	if table == pgvectorHistory {
		query = `SELECT id,version_id,is_applied,tstamp FROM goose_pgvector_db_version ORDER BY id`
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	index := 0
	var version, lastID int64
	for rows.Next() {
		var id, current int64
		var applied bool
		var stamp time.Time
		if err := rows.Scan(&id, &current, &applied, &stamp); err != nil {
			return 0, err
		}
		if id <= lastID || !applied || index > len(sources) {
			return 0, errors.New("invalid migration history record")
		}
		if index == 0 {
			if current != 0 {
				return 0, errors.New("missing migration zero version")
			}
		} else if current != sources[index-1].Version {
			return 0, fmt.Errorf("unrecognized migration history version %d", current)
		}
		version, lastID = current, id
		index++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if index == 0 {
		return 0, errors.New("empty migration history")
	}
	return version, nil
}

func migrationFailure(stage knowl.MigrationStage, stream string, err error) error {
	if err == nil {
		return nil
	}
	failure := &knowl.MigrationError{Stage: stage, Stream: stream, Cause: err}
	var partial *goose.PartialError
	if errors.As(err, &partial) && partial.Failed != nil && partial.Failed.Source != nil {
		failure.Version = partial.Failed.Source.Version
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		failure.SQLState = state.SQLState()
	}
	return failure
}
