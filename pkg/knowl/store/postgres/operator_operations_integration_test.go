//go:build integration

package postgres

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
)

func runOperatorOperationsPostgres(t *testing.T, dsn string) {
	store, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// Exercise production listing under a language collation that orders tied IDs
	// differently from SQLite's bytewise comparison.
	if _, err := store.db.ExecContext(t.Context(), `CREATE COLLATION knowl_operator_fixture (provider=icu,locale='en')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `ALTER TABLE knowl_operations ALTER COLUMN operation_id TYPE TEXT COLLATE knowl_operator_fixture`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := store.db.ExecContext(ctx, `ALTER TABLE knowl_operations ALTER COLUMN operation_id TYPE TEXT COLLATE "default"`); err != nil {
			t.Error(err)
		}
		if _, err := store.db.ExecContext(ctx, `DROP COLLATION knowl_operator_fixture`); err != nil {
			t.Error(err)
		}
	})
	storetest.RunOperatorOperations(t, store, "operator-operations")
}

func runOperatorMigrationPostgres(t *testing.T, dsn string) {
	store, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, store.db, directory, goose.WithGoMigrations(operatorOperationsMigration()))
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
			if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_operations SET accepted_source_document=$1 WHERE operation_id=$2`, raw, id); err != nil {
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

func runOperatorRetryOperationsPostgres(t *testing.T, dsn string) {
	store, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	storetest.RunOperatorRetryOperations(t, store, "operator-retry")
}

func runOperatorCursorBoundsPostgres(t *testing.T, dsn string) {
	store, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	storetest.RunOperatorCursorBounds(t, store, "operator-cursor")
}
