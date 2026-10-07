package postgres

import (
	"context"
	"database/sql"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/operationlist"
	"github.com/pressly/goose/v3"
)

func operatorOperationsMigration() *goose.Migration {
	return goose.NewGoMigration(19, &goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
		for _, statement := range []string{`ALTER TABLE knowl_operations ADD COLUMN configured_source_id TEXT`} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		if err := operationlist.Backfill(ctx, tx, false); err != nil {
			return err
		}
		for _, statement := range []string{`CREATE INDEX knowl_operations_operator_created ON knowl_operations(scope, created_at DESC, operation_id COLLATE "C" DESC)`,
			`CREATE INDEX knowl_operations_operator_status_created ON knowl_operations(scope, status, created_at DESC, operation_id COLLATE "C" DESC)`,
			`CREATE INDEX knowl_operations_operator_source_created ON knowl_operations(scope, configured_source_id, created_at DESC, operation_id COLLATE "C" DESC)`,
			`CREATE INDEX knowl_operations_operator_source_status_created ON knowl_operations(scope, configured_source_id, status, created_at DESC, operation_id COLLATE "C" DESC)`} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	}}, &goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
		for _, statement := range []string{`DROP INDEX knowl_operations_operator_created`,
			`DROP INDEX knowl_operations_operator_status_created`,
			`DROP INDEX knowl_operations_operator_source_created`,
			`DROP INDEX knowl_operations_operator_source_status_created`,
			`ALTER TABLE knowl_operations DROP COLUMN configured_source_id`} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	}})
}
