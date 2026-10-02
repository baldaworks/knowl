package knowl

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestContextBaselineBlockedExecution(t *testing.T) {
	var previous []string
	for pass := range 2 {
		sequence := observeBlockedBaseline(t)
		if pass == 1 && !reflect.DeepEqual(previous, sequence) {
			t.Fatal("execution sequence changed between controlled runs")
		}
		previous = sequence
		outcome := "gap"
		if slices.Index(sequence, "second_started") < slices.Index(sequence, "first_released") {
			outcome = "met"
		}
		encoded, err := json.Marshal(struct {
			CaseID   string   `json:"case_id"`
			Sequence []string `json:"sequence"`
			Outcome  string   `json:"outcome"`
		}{"blocked-execution", sequence, outcome})
		if err != nil {
			t.Fatal(err)
		}
		t.Log(string(encoded))
	}
}

func observeBlockedBaseline(t *testing.T) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	store := &schedulerStore{claims: []domain.WorkClaim{schedulerClaim("first"), schedulerClaim("second")}}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
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
			record("second_started")
		}
		claim.Operation.Status = domain.StatusCommitted
		return app.IngestResult{Operation: claim.Operation}, nil
	}), schedulerOptions{claimBatch: 2})
	go func() {
		scheduler.cycle(ctx)
		close(done)
	}()
	// On any assertion failure cancel the blocked runner and join the actual cycle.
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("baseline scheduler did not terminate after cancellation")
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first operation never started")
	}
	scheduler.Wake("second")
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("baseline scheduler did not complete released work")
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
