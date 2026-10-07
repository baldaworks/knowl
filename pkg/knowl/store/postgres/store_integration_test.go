//go:build integration

package postgres

import (
	"context"
	"errors"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestStoreContractWithTestcontainers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := postgres.Run(
		ctx,
		"postgres:16-alpine",
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
	t.Run("operator-documents", func(t *testing.T) { runOperatorDocumentsPostgres(t, dsn) })
	t.Run("operator-cursor", func(t *testing.T) { runOperatorCursorBoundsPostgres(t, dsn) })
	t.Run("operator-retry", func(t *testing.T) { runOperatorRetryOperationsPostgres(t, dsn) })
	t.Run("operator-migration", func(t *testing.T) { runOperatorMigrationPostgres(t, dsn) })
	t.Run("operator-operations", func(t *testing.T) { runOperatorOperationsPostgres(t, dsn) })
	t.Run("operation-details", func(t *testing.T) { runOperationDetailsPostgres(t, dsn) })
	t.Run("correction-reports", func(t *testing.T) { runCorrectionReportPostgres(t, dsn) })
	t.Run("correction-report-migration", func(t *testing.T) { runCorrectionReportMigrationPostgres(t, dsn) })
	t.Run("operation-details-migration", func(t *testing.T) { runOperationDetailsMigrationPostgres(t, dsn) })
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
