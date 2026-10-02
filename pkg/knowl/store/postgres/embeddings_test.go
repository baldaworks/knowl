package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
)

func TestEmbeddingCancellationBeforeSQL(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// No database connection is needed to reject a canceled projection operation.
	store := new(Store)
	if err := store.publishEmbeddings(ctx, "scope", hybrid.ProjectionState{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled publication=%v", err)
	}
	if _, _, err := embeddingProjectionTx(ctx, nil, "scope", "space", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read=%v", err)
	}
}
