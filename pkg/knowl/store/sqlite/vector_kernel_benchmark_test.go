package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
)

func BenchmarkVectorKernel(b *testing.B) {
	searchtest.RunVectorKernelBenchmarks(b, "sqlite", func(b *testing.B, ctx context.Context) (*sql.DB, func(), error) {
		store, err := Open(ctx, b.TempDir()+"/isolated-kernel.sqlite")
		if err != nil {
			return nil, nil, err
		}
		return store.db, func() { _ = store.Close() }, nil
	})
}
