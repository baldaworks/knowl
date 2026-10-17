//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestStoreContractWithTestcontainers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := postgres.Run(
		ctx,
		"pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc",
		postgres.WithDatabase("knowl"),
		postgres.WithUsername("knowl"),
		postgres.WithPassword("knowl"),
		postgres.BasicWaitStrategies(),
		postgres.WithSQLDriver("pgx"),
	)
	if err != nil {
		t.Fatalf("start PostgreSQL Testcontainer: %v", err)
	}
	testcontainers.CleanupContainer(t, container)

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get PostgreSQL Testcontainer connection string: %v", err)
	}
	// The positive dense contract requires operator-provisioned pgvector. This
	// fixture owns the disposable database; application Open never installs it.
	operator := nativeOpen(t, dsn)
	nativeExec(t, operator.db, `CREATE EXTENSION vector`)
	t.Run("operator-documents", func(t *testing.T) { runOperatorDocumentsPostgres(t, dsn) })
	t.Run("operator-cursor", func(t *testing.T) { runOperatorCursorBoundsPostgres(t, dsn) })
	t.Run("operator-retry", func(t *testing.T) { runOperatorRetryOperationsPostgres(t, dsn) })
	t.Run("operator-migration", func(t *testing.T) { runOperatorMigrationPostgres(t, dsn) })
	t.Run("operator-operations", func(t *testing.T) { runOperatorOperationsPostgres(t, dsn) })
	t.Run("operation-details", func(t *testing.T) { runOperationDetailsPostgres(t, dsn) })
	t.Run("correction-reports", func(t *testing.T) { runCorrectionReportPostgres(t, dsn) })
	t.Run("correction-report-migration", func(t *testing.T) { runCorrectionReportMigrationPostgres(t, dsn) })
	t.Run("operation-details-migration", func(t *testing.T) { runOperationDetailsMigrationPostgres(t, dsn) })
	t.Run("source-lifecycle", func(t *testing.T) { runSourceLifecyclePostgres(t, dsn) })
	t.Run("source-lifecycle-migration", func(t *testing.T) { runSourceLifecycleMigrationPostgres(t, dsn) })
	t.Run("source-readiness-migration", func(t *testing.T) { runSourceReadinessMigrationPostgres(t, dsn) })
	t.Run("retirement-work", func(t *testing.T) {
		store, openErr := Open(t.Context(), dsn)
		if openErr != nil {
			t.Fatal(openErr)
		}
		t.Cleanup(func() { _ = store.Close() })
		storetest.RunRetirementWorkContract(t, store, "postgres_retirement", func(err error) bool { return errors.Is(err, ErrConflict) }, func(t *testing.T, id knowl.OperationID) {
			t.Helper()
			if _, err := store.db.ExecContext(t.Context(), `DELETE FROM knowl_retirement_operation_owners WHERE operation_id = $1`, id); err != nil {
				t.Fatalf("drop retirement owner association: %v", err)
			}
		})
	})
	t.Run("hybrid", func(t *testing.T) {
		searchtest.RunHybrid(t, func(t *testing.T, options ...app.EmbeddingOptions) searchtest.HybridIndex {
			t.Helper()
			store, err := Open(t.Context(), dsn, options...)
			if err != nil {
				t.Fatal(err)
			}
			return store
		}, func(err error) bool { return errors.Is(err, ErrProjectionDrift) })
	})
	t.Run("embeddings", func(t *testing.T) { runEmbeddingPostgres(t, dsn) })
	t.Run("generic", func(t *testing.T) { runGenericPostgres(t, dsn) })
	runStoreContract(t, dsn)
}
