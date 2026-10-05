package knowl

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestContextBaselineBlockedExecution(t *testing.T) {
	for _, capacity := range []int{1, 2} {
		t.Run(fmt.Sprintf("workers%d", capacity), func(t *testing.T) {
			var previous []string
			for pass := range 2 {
				sequence := observeBlockedBaseline(t, capacity)
				if pass == 1 && !reflect.DeepEqual(previous, sequence) {
					t.Fatal("execution sequence changed between controlled runs")
				}
				previous = sequence
				want := []string{"first_started", "first_released", "second_started"}
				outcome := "gap"
				if capacity == 2 {
					want = []string{"first_started", "second_started", "first_released"}
					outcome = "met"
				}
				if !reflect.DeepEqual(sequence, want) {
					t.Fatalf("capacity%d sequence=%v want%v", capacity, sequence, want)
				}
				encoded, err := json.Marshal(struct {
					CaseID   string   `json:"case_id"`
					Capacity int      `json:"capacity"`
					Sequence []string `json:"sequence"`
					Outcome  string   `json:"outcome"`
				}{"blocked-execution", capacity, sequence, outcome})
				if err != nil {
					t.Fatal(err)
				}
				t.Log(string(encoded))
			}
		})
	}
}

func observeBlockedBaseline(t *testing.T, capacity int) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	store := &schedulerStore{claims: []domain.WorkClaim{schedulerClaim("first"), schedulerClaim("second")}}
	started, secondStarted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var sequence []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		sequence = append(sequence, event)
	}
	scheduler := newTestScheduler(t, store, runnerFunc(func(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		switch claim.Operation.ID {
		case "first":
			record("first_started")
			close(started)
			select {
			case <-release:
				record("first_released")
			case <-ctx.Done():
				return app.IngestResult{Operation: claim.Operation}, ctx.Err()
			}
		case "second":
			select {
			case <-started:
			case <-ctx.Done():
				return app.IngestResult{}, ctx.Err()
			}
			record("second_started")
			close(secondStarted)
		}
		claim.Operation.Status = domain.StatusCommitted
		return app.IngestResult{Operation: claim.Operation}, nil
	}), schedulerOptions{claimBatch: 2, slots: newExecutionSlots(baselineOwners(capacity))})
	if err := scheduler.start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		if err := scheduler.stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first operation never started")
	}
	scheduler.Wake("second")
	if capacity == 2 {
		select {
		case <-secondStarted:
		case <-ctx.Done():
			t.Fatal("second owner did not start before releasing first")
		}
	}
	close(release)
	if capacity == 1 {
		select {
		case <-secondStarted:
		case <-ctx.Done():
			t.Fatal("default serial work did not progress")
		}
	}
	if err := scheduler.stop(ctx); err != nil {
		t.Fatal(err)
	}
	if store.claimCount() != 0 || len(store.recordedFailures()) != 0 || len(store.recordedClaimFailures()) != 0 || len(store.recordedRetries()) != 0 {
		t.Fatal("scheduler lost queued work or unexpectedly failed/retried")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sequence) != 3 || slices.Index(sequence, "first_started") != 0 || slices.Index(sequence, "first_released") < 0 || slices.Index(sequence, "second_started") < 0 {
		t.Fatal("scheduler omitted or duplicated a controlled execution event")
	}
	return slices.Clone(sequence)
}

func baselineOwners(capacity int) []*executionSlot {
	owners := make([]*executionSlot, capacity)
	for i := range owners {
		owners[i] = &executionSlot{}
	}
	return owners
}
