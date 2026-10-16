//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
)

func BenchmarkVectorKernel(b *testing.B) {
	searchtest.RunVectorKernelBenchmarks(b, "postgres", func(b *testing.B, ctx context.Context) (*sql.DB, func(), error) {
		store, err := Open(ctx, vectorRuntimePostgresDSN(b, ctx))
		if err != nil {
			return nil, nil, err
		}
		return store.db, func() { _ = store.Close() }, nil
	})
}
