package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestPreparedSourceSyncWaitsForPublicationAndRecovers(t *testing.T) {
	harness := newStageHarness(t, nil)
	changedListing(t, harness)
	original := harness.service
	gate := &syncApplyCoordinator{occupied: make(chan struct{}, 1), requested: make(chan struct{}, 1)}
	gate.occupied <- struct{}{}
	service, err := NewService(Dependencies{Adapters: original.adapters, State: original.state, Content: original.content, SourceContent: original.sourceContent, Search: original.search, Maintenance: original.maintenance, ApplyCoordinator: gate}, original.options)
	if err != nil {
		t.Fatal(err)
	}
	harness.service = service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type completion struct {
		result Result
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		result, err := service.SyncSource(ctx, harness.scope, harness.source(knowl.SourceFlavorMarkdown))
		done <- completion{result, err}
	}()
	select {
	case <-gate.requested:
	case <-ctx.Done():
		t.Fatal("source sync bypassed publication coordinator")
	}
	select {
	case value := <-done:
		t.Fatalf("sync escaped occupied publication gate: %+v", value)
	default:
	}
	cancel()
	stopped := <-done
	if !errors.Is(stopped.err, context.Canceled) || stopped.result.Run.Status != knowl.SyncStatusPrepared {
		t.Fatalf("canceled publication=%+v %v", stopped.result, stopped.err)
	}
	inventory, err := service.sourceContent.SourceDigests(t.Context(), harness.scope, harness.sourceID, 16)
	if err != nil || len(inventory) != 1 || inventory[0].Digest != sha256Hex("before") {
		t.Fatalf("waiting sync changed canonical source: %+v %v", inventory, err)
	}
	<-gate.occupied
	recovered, err := service.Recover(t.Context(), harness.scope, []knowl.Source{harness.source(knowl.SourceFlavorMarkdown)})
	if err != nil || len(recovered) != 1 || recovered[0].Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("publication recovery=%+v %v", recovered, err)
	}
	assertCanonicalConverged(t, harness, "after")
}

type syncApplyCoordinator struct{ occupied, requested chan struct{} }

func (gate *syncApplyCoordinator) Acquire(ctx context.Context) (func(), error) {
	select {
	case gate.requested <- struct{}{}:
	default:
	}
	select {
	case gate.occupied <- struct{}{}:
		return func() { <-gate.occupied }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
