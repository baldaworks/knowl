package knowl

import (
	"context"
	"errors"

	"github.com/baldaworks/knowl/pkg/knowl/store/postgres"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

// MigrationConfig selects schema administration without runtime or provider setup.
type MigrationConfig struct {
	StoreDriver   string
	StorePath     string
	PostgresDSN   string
	NativeVectors bool
}

// MigrateDatabase changes only the selected database schema and closes its connection.
func MigrateDatabase(ctx context.Context, config MigrationConfig, direction domain.MigrationDirection) (domain.MigrationResult, error) {
	switch config.StoreDriver {
	case StoreSQLite:
		return sqlite.Migrate(ctx, config.StorePath, direction)
	case StorePostgres:
		return postgres.Migrate(ctx, config.PostgresDSN, direction, config.NativeVectors)
	default:
		return domain.MigrationResult{}, &domain.MigrationError{
			Stage: domain.MigrationStageConfiguration, Stream: "base", Cause: errors.New("unsupported migration backend"),
		}
	}
}
