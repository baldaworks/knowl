package sqlite

import (
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
)

func TestOperatorOperations(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/operator.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	storetest.RunOperatorOperations(t, store, "operator-operations")
}

func TestOperatorMigration(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/upgrade.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, store.db, directory, goose.WithGoMigrations(operatorOperationsMigration()))
	if err != nil {
		t.Fatal(err)
	}
	storetest.RunOperatorMigration(t, storetest.OperatorMigrationHarness{
		Store: store, Scope: "operator-upgrade",
		Downgrade: func(t *testing.T) {
			t.Helper()
			if _, err := provider.DownTo(t.Context(), 18); err != nil {
				t.Fatal(err)
			}
		},
		Upgrade: func(t *testing.T) {
			t.Helper()
			if err := store.configure(t.Context()); err != nil {
				t.Fatal(err)
			}
		},
		Document: func(t *testing.T, id knowl.OperationID, raw string) {
			t.Helper()
			if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_operations SET accepted_source_document=? WHERE operation_id=?`, raw, id); err != nil {
				t.Fatal(err)
			}
		},
		History: func(t *testing.T) []storetest.OperatorHistory {
			t.Helper()
			rows, err := store.db.QueryContext(t.Context(), `SELECT operation_id,attempt,work_attempt,retry_attempt,manual_retry_count,retrieval_report_attempt,CAST(created_at AS TEXT),CAST(updated_at AS TEXT),status,accepted_source_document,execution_payload,plan_digest,context_report,correction_report,retrieval_report FROM knowl_operations ORDER BY operation_id`)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			history := []storetest.OperatorHistory{}
			for rows.Next() {
				var row storetest.OperatorHistory
				if err := rows.Scan(&row.ID, &row.Attempt, &row.WorkAttempt, &row.RetryAttempt, &row.ManualRetryCount, &row.RetrievalAttempt, &row.CreatedAt, &row.UpdatedAt, &row.Status, &row.Document, &row.Execution, &row.Plan, &row.Context, &row.Correction, &row.Retrieval); err != nil {
					t.Fatal(err)
				}
				history = append(history, row)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return history
		},
	})
}

func TestOperatorMigrationInvalidCreationPreservesHistory(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/corrupt.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key, meta := storetest.Fixture("corrupt-upgrade", "invalid-created", time.Unix(1, 0).UTC())
	r, err := store.Reserve(t.Context(), key, meta)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, store.db, directory, goose.WithGoMigrations(operatorOperationsMigration()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(t.Context(), 18); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_operations SET created_at=? WHERE operation_id=?`, "invalid", r.ID); err != nil {
		t.Fatal(err)
	}
	var parseError *time.ParseError
	if err := store.configure(t.Context()); !errors.As(err, &parseError) {
		t.Fatalf("corrupt creation: %v", err)
	}
	var created string
	if err := store.db.QueryRowContext(t.Context(), `SELECT created_at FROM knowl_operations WHERE operation_id=?`, r.ID).Scan(&created); err != nil || created != "invalid" {
		t.Fatalf("history was altered: %q %v", created, err)
	}
	current, err := provider.GetDBVersion(t.Context())
	if err != nil || current != 18 {
		t.Fatalf("failed migration advanced: %d %v", current, err)
	}
}

func TestOperatorRetryOperations(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/retry-list.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	storetest.RunOperatorRetryOperations(t, store, "operator-retry")
}
