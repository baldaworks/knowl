package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
)

const (
	migrationBackend = "sqlite"
	migrationStream  = "base"
)

// Migrate applies schema changes without constructing the store or search engine.
// Down never upgrades the database, including when no history exists.
func Migrate(ctx context.Context, path string, direction knowl.MigrationDirection) (knowl.MigrationResult, error) {
	result := knowl.MigrationResult{Backend: migrationBackend, Direction: direction, Steps: []knowl.MigrationStep{}}
	if direction != knowl.MigrationUp && direction != knowl.MigrationDown {
		return result, migrationFailure(knowl.MigrationStageConfiguration, errors.New("invalid migration direction"))
	}
	db, err := openConnection(ctx, path)
	if err != nil {
		return result, err
	}
	defer func() { _ = db.Close() }()
	return migrateDatabase(ctx, db, direction)
}

func openConnection(ctx context.Context, path string) (*sql.DB, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, migrationFailure(knowl.MigrationStageConfiguration, errors.New("sqlite path is required"))
	}
	if err := os.MkdirAll(filepath.Dir(trimmed), 0o700); err != nil {
		return nil, migrationFailure(knowl.MigrationStageConnection, err)
	}
	databaseURL := url.URL{Scheme: "file", Path: trimmed}
	query := databaseURL.Query()
	query.Set("_txlock", "immediate")
	databaseURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return nil, migrationFailure(knowl.MigrationStageConnection, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			if statement == "PRAGMA journal_mode=WAL" {
				continue
			}
			_ = db.Close()
			return nil, migrationFailure(knowl.MigrationStageConnection, err)
		}
	}
	return db, nil
}

func migrationProvider(db *sql.DB) (*goose.Provider, error) {
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(goose.DialectSQLite3, db, directory, goose.WithGoMigrations(operatorOperationsMigration()))
}

func migrateDatabase(ctx context.Context, db *sql.DB, direction knowl.MigrationDirection) (knowl.MigrationResult, error) {
	result := knowl.MigrationResult{Backend: migrationBackend, Direction: direction, Steps: []knowl.MigrationStep{}}
	provider, err := migrationProvider(db)
	if err != nil {
		return result, migrationFailure(knowl.MigrationStageConfiguration, err)
	}
	version, err := recognizeHistory(ctx, db, provider.ListSources())
	if err != nil {
		return result, migrationFailure(knowl.MigrationStageHistory, err)
	}
	if direction == knowl.MigrationDown {
		if version == 0 {
			return result, nil
		}
		step, err := provider.Down(ctx)
		if err != nil {
			return result, migrationFailure(knowl.MigrationStageApply, err)
		}
		result.Steps = append(result.Steps, knowl.MigrationStep{Stream: migrationStream, Version: step.Source.Version})
		return result, nil
	}
	steps, err := provider.Up(ctx)
	var partial *goose.PartialError
	if errors.As(err, &partial) {
		steps = partial.Applied
	}
	for _, step := range steps {
		if step.Error == nil {
			result.Steps = append(result.Steps, knowl.MigrationStep{Stream: migrationStream, Version: step.Source.Version})
		}
	}
	return result, migrationFailure(knowl.MigrationStageApply, err)
}

// recognizeHistory never creates or repairs history. A prefix of the embedded
// ordered sources is valid, including after any number of one-step rollbacks.
func recognizeHistory(ctx context.Context, db *sql.DB, sources []*goose.Source) (int64, error) {
	var tableType string
	err := db.QueryRowContext(ctx, `SELECT type FROM sqlite_schema WHERE name='goose_db_version'`).Scan(&tableType)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if tableType != "table" {
		return 0, errors.New("invalid migration history object")
	}
	rows, err := db.QueryContext(ctx, `SELECT id,version_id,is_applied,tstamp FROM goose_db_version ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	index := 0
	var version, lastID int64
	for rows.Next() {
		var id, current, applied int64
		var stamp any
		if err := rows.Scan(&id, &current, &applied, &stamp); err != nil {
			return 0, err
		}
		if id <= lastID || applied != 1 || index > len(sources) {
			return 0, errors.New("invalid migration history record")
		}
		if index == 0 {
			if current != 0 {
				return 0, errors.New("missing migration zero version")
			}
		} else if current != sources[index-1].Version {
			return 0, fmt.Errorf("unrecognized migration history version %d", current)
		}
		version = current
		lastID = id
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

func migrationFailure(stage knowl.MigrationStage, err error) error {
	if err == nil {
		return nil
	}
	failure := &knowl.MigrationError{Stage: stage, Stream: migrationStream, Cause: err}
	var partial *goose.PartialError
	if errors.As(err, &partial) && partial.Failed != nil && partial.Failed.Source != nil {
		failure.Version = partial.Failed.Source.Version
	}
	return failure
}
