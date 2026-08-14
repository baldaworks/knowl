package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/internal/knowledgetest"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/contexttest"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
)

func TestSearchContract(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir()+"/search-contract.sqlite")
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	searchtest.Run(t, store, func(err error) bool { return errors.Is(err, ErrInvalidQuery) })
}

func TestContextContract(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir()+"/context-contract.sqlite")
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	contexttest.Run(t, store)
}

func TestGoldenProjectionReplay(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir()+"/golden-projection.sqlite")
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	metrics, err := knowledgetest.EvaluateProjectionReplay(context.Background(), store, "golden-sqlite")
	if err != nil {
		t.Fatalf("EvaluateProjectionReplay(): %v", err)
	}
	if metrics.Total != knowledgetest.QueryCount || metrics.Hits < knowledgetest.MinimumHits {
		t.Fatalf("golden metrics = %#v", metrics)
	}
}
