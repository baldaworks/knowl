package knowl

import (
	"context"
	"fmt"
	"io"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/postgres"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type projectionChecker interface {
	CheckProjection(ctx context.Context, snapshot domain.WorkspaceSnapshot) error
}

type operationalStore struct {
	operations app.OperationStore
	index      app.SearchIndex
	closer     io.Closer
	checker    projectionChecker
}

func openStore(ctx context.Context, config Config) (operationalStore, error) {
	switch config.StoreDriver {
	case StoreSQLite:
		store, err := sqlite.Open(ctx, config.StorePath)
		if err != nil {
			return operationalStore{}, fmt.Errorf("open sqlite operational store: %w", err)
		}
		return operationalStore{
			operations: store,
			index:      store,
			closer:     store,
			checker:    store,
		}, nil
	case StorePostgres:
		store, err := postgres.Open(ctx, config.PostgresDSN)
		if err != nil {
			return operationalStore{}, fmt.Errorf("open postgres operational store: %w", err)
		}
		return operationalStore{
			operations: store,
			index:      store,
			closer:     store,
			checker:    store,
		}, nil
	default:
		return operationalStore{}, fmt.Errorf("unsupported store driver %q", config.StoreDriver)
	}
}

func ensureProjection(ctx context.Context, index app.SearchIndex, checker projectionChecker, snapshot domain.WorkspaceSnapshot) error {
	if checker == nil {
		return fmt.Errorf("operational store does not expose projection readiness")
	}
	if err := checker.CheckProjection(ctx, snapshot); err == nil {
		return nil
	}
	if err := index.Rebuild(ctx, snapshot); err != nil {
		return fmt.Errorf("rebuild search projection: %w", err)
	}
	if err := checker.CheckProjection(ctx, snapshot); err != nil {
		return fmt.Errorf("verify search projection: %w", err)
	}
	return nil
}
