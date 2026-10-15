package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func (store *Store) operationFailureDiagnostics(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) ([]knowl.MaintenanceDiagnostic, error) {
	var exists bool
	if err := store.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'knowl_operations' AND column_name = 'failure_diagnostics'
		)`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("inspect operation diagnostics schema: %w", err)
	}
	if !exists {
		return nil, nil
	}
	var encoded string
	if err := store.db.QueryRowContext(ctx, `SELECT failure_diagnostics FROM knowl_operations WHERE scope = $1 AND operation_id = $2`, scope, id).Scan(&encoded); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("read operation diagnostics: %w", err)
	}
	diagnostics, err := app.DecodeMaintenanceDiagnostics(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode failure diagnostics: %w", err)
	}
	return diagnostics, nil
}

// SaveOperationDiagnostics records bounded failure evidence only for the
// current work attempt of a non-terminal operation.
func (store *Store) SaveOperationDiagnostics(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, workAttempt int, diagnostics []knowl.MaintenanceDiagnostic) error {
	if err := validateScope(scope); err != nil || workAttempt <= 0 {
		return app.ErrWorkLeaseConflict
	}
	encoded, err := app.EncodeMaintenanceDiagnostics(diagnostics)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	result, err := store.db.ExecContext(ctx, `
		UPDATE knowl_operations SET failure_diagnostics = $1, updated_at = $2
		WHERE scope = $3 AND operation_id = $4 AND work_attempt = $5 AND status NOT IN ($6, $7)`,
		encoded, nowTime(), scope, id, workAttempt, knowl.StatusCommitted, knowl.StatusFailed)
	if err != nil {
		return fmt.Errorf("save operation diagnostics: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect operation diagnostics: %w", err)
	}
	if changed != 1 {
		return app.ErrWorkLeaseConflict
	}
	return nil
}
