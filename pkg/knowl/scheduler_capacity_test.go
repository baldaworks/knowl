package knowl

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"strings"
)

const schedulerCapacityFixture = "capacity_fixture"

func TestSchedulerOwnedCapacityAndDurablePending(t *testing.T) {
	for _, capacity := range []int{1, 2} {
		for _, background := range []bool{false, true} {
			t.Run(fmt.Sprintf("capacity%d/background%t", capacity, background), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = store.Close() }()
				var ids []domain.OperationID
				for i := range 3 {
					key, meta := capacityFixture(DefaultScope, fmt.Sprintf("capacity-%d", i), time.Unix(int64(i+1), 0))
					reservation, reserveErr := store.Reserve(ctx, key, meta)
					if reserveErr != nil {
						t.Fatal(reserveErr)
					}
					ids = append(ids, reservation.ID)
				}
				owners := make([]*executionSlot, capacity)
				for i := range owners {
					owners[i] = &executionSlot{}
				}
				pool := newExecutionSlots(owners)
				entered := make(chan domain.OperationID, 3)
				release := make(chan struct{}, 3)
				var active, peak atomic.Int32
				runner := runnerFunc(func(runCtx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
					current := active.Add(1)
					defer active.Add(-1)
					for previous := peak.Load(); current > previous; previous = peak.Load() {
						if peak.CompareAndSwap(previous, current) {
							break
						}
					}
					entered <- claim.Operation.ID
					select {
					case <-release:
					case <-runCtx.Done():
						return app.IngestResult{Operation: claim.Operation}, runCtx.Err()
					}
					failure := domain.Failure{Class: schedulerCapacityFixture, OperationID: string(claim.Operation.ID)}
					if failErr := store.Fail(runCtx, claim.Operation.ID, failure); failErr != nil {
						return app.IngestResult{}, failErr
					}
					claim.Operation.Status = domain.StatusFailed
					return app.IngestResult{Operation: claim.Operation}, nil
				})
				scheduler, err := newOperationScheduler(store, runner, DefaultScope, schedulerOptions{slots: pool, scanInterval: time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				drained := make(chan error, 1)
				if background {
					if err = scheduler.start(ctx); err != nil {
						t.Fatal(err)
					}
				} else {
					go func() {
						result, drainErr := scheduler.Drain(ctx)
						if drainErr == nil && result.Total != 3 {
							drainErr = fmt.Errorf("drained %d, want3", result.Total)
						}
						drained <- drainErr
					}()
				}
				defer func() {
					cancel()
					_ = scheduler.stop(context.Background())
					if !background {
						<-drained
					}
				}()
				for range capacity {
					select {
					case <-entered:
					case <-ctx.Done():
						t.Fatal("configured owners did not enter inference", ctx.Err())
					}
				}
				ready, err := store.ResumeReady(ctx, DefaultScope, 10)
				if err != nil || len(ready) != 3-capacity {
					t.Fatalf("durable pending=%v err=%v", ready, err)
				}
				for _, id := range ready {
					op, readErr := store.Operation(ctx, DefaultScope, id)
					if readErr != nil || op.WorkAttempt != 0 {
						t.Fatalf("pending claimed before capacity: %#v %v", op, readErr)
					}
				}
				release <- struct{}{}
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("released slot did not progress")
				}
				for range 3 {
					release <- struct{}{}
				}
				// The last pending operation at capacity one also has to enter.
				if capacity == 1 {
					select {
					case <-entered:
					case <-ctx.Done():
						t.Fatal("last pending operation did not progress")
					}
				}
				if background {
					if err = scheduler.stop(ctx); err != nil {
						t.Fatal(err)
					}
				} else {
					select {
					case err = <-drained:
						drained <- err
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if peak.Load() != int32(capacity) {
					t.Fatalf("observed capacity=%d want%d", peak.Load(), capacity)
				}
				for _, id := range ids {
					op, readErr := store.Operation(ctx, DefaultScope, id)
					if readErr != nil || op.WorkAttempt != 1 {
						t.Fatalf("claim attribution=%#v %v", op, readErr)
					}
				}
			})
		}
	}
}

func TestSchedulerDrainWaitIsCancelable(t *testing.T) {
	entered := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	scheduler := newTestScheduler(t, &schedulerStore{claims: []domain.WorkClaim{schedulerClaim("drain-held")}}, runnerFunc(func(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		close(entered)
		<-ctx.Done()
		return app.IngestResult{Operation: claim.Operation}, ctx.Err()
	}), schedulerOptions{})
	first := make(chan error, 1)
	go func() { _, err := scheduler.Drain(ctx); first <- err }()
	<-entered
	waiting, stopWaiting := context.WithCancel(t.Context())
	stopWaiting()
	if _, err := scheduler.Drain(waiting); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting drain=%v", err)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first drain=%v", err)
	}
}

func capacityFixture(scope domain.ScopeRef, id string, createdAt time.Time) (domain.OperationKey, domain.OperationMeta) {
	schema := []byte("# Schema\n\nversion: 1\n")
	key := domain.OperationKey{Scope: scope, Source: domain.SourceRef{Adapter: schedulerCapacityFixture, ID: id}, Version: domain.SourceVersion{Version: "1", Digest: strings.Repeat("a", 64)}}
	return key, domain.OperationMeta{Key: key, AcceptedSource: domain.AcceptedSource{Scope: scope, Source: key.Source, Version: key.Version, MediaType: "text/markdown", ManifestRef: "raw/source/version/manifest.yaml"}, Schema: domain.SchemaDocument{Scope: scope, Digest: fmt.Sprintf("%x", sha256.Sum256(schema)), Version: "1", Content: schema}, SchemaDigest: fmt.Sprintf("%x", sha256.Sum256(schema)), CreatedAt: createdAt}
}

func TestSchedulerDrainSharesBackgroundCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered := make(chan domain.OperationID, 3)
	release := make(chan struct{}, 3)
	var active, peak atomic.Int32
	store := &schedulerStore{claims: []domain.WorkClaim{schedulerClaim("mixed-a"), schedulerClaim("mixed-b"), schedulerClaim("mixed-c")}}
	scheduler := newTestScheduler(t, store, runnerFunc(func(runCtx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, current) {
				break
			}
		}
		entered <- claim.Operation.ID
		select {
		case <-release:
			claim.Operation.Status = domain.StatusCommitted
			return app.IngestResult{Operation: claim.Operation}, nil
		case <-runCtx.Done():
			return app.IngestResult{Operation: claim.Operation}, runCtx.Err()
		}
	}), schedulerOptions{slots: newExecutionSlots([]*executionSlot{{}, {}})})
	if err := scheduler.start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = scheduler.stop(context.Background()) }()
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	drained := make(chan error, 1)
	go func() { _, err := scheduler.Drain(ctx); drained <- err }()
	release <- struct{}{}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("mixed callers did not progress")
	}
	release <- struct{}{}
	release <- struct{}{}
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := scheduler.stop(ctx); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 || active.Load() != 0 || store.claimCount() != 0 {
		t.Fatalf("peak=%d active=%d pending=%d", peak.Load(), active.Load(), store.claimCount())
	}
}

type selectiveRenewalStore struct {
	*schedulerStore
	renewed chan domain.OperationID
}

func (store *selectiveRenewalStore) RenewClaim(_ context.Context, _ domain.ScopeRef, id domain.OperationID, _ string, _ domain.WorkLease) error {
	select {
	case store.renewed <- id:
	default:
	}
	if id == "lease-a" {
		return app.ErrWorkLeaseConflict
	}
	return nil
}

func TestSchedulerLeaseLossOnlyCancelsOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	store := &selectiveRenewalStore{schedulerStore: &schedulerStore{claims: []domain.WorkClaim{schedulerClaim("lease-a"), schedulerClaim("lease-b")}}, renewed: make(chan domain.OperationID, 20)}
	entered := make(chan domain.OperationID, 2)
	canceled := make(chan domain.OperationID, 2)
	release := make(chan struct{})
	scheduler := newTestScheduler(t, store, runnerFunc(func(runCtx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		entered <- claim.Operation.ID
		select {
		case <-runCtx.Done():
			canceled <- claim.Operation.ID
			return app.IngestResult{Operation: claim.Operation}, runCtx.Err()
		case <-release:
			claim.Operation.Status = domain.StatusCommitted
			return app.IngestResult{Operation: claim.Operation}, nil
		}
	}), schedulerOptions{slots: newExecutionSlots([]*executionSlot{{}, {}}), workLeaseDuration: 30 * time.Millisecond})
	// newTestScheduler intentionally supplies its conservative default; use a short
	// lease here to exercise both independent renewers through their real tickers.
	scheduler.options.workLeaseDuration = 30 * time.Millisecond
	if err := scheduler.start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = scheduler.stop(context.Background()) }()
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	select {
	case id := <-canceled:
		if id != "lease-a" {
			t.Fatalf("wrong owner canceled=%s", id)
		}
	case <-ctx.Done():
		t.Fatal("lost owner was not canceled")
	}
	renewedB := false
	for !renewedB {
		select {
		case id := <-store.renewed:
			renewedB = id == "lease-b"
		case <-ctx.Done():
			t.Fatal("sibling lease not renewed")
		}
	}
	select {
	case id := <-canceled:
		t.Fatalf("sibling canceled=%s", id)
	default:
	}
	close(release)
	if err := scheduler.stop(ctx); err != nil {
		t.Fatal(err)
	}
	if len(store.recordedRetries()) != 0 || len(store.recordedClaimFailures()) != 0 {
		t.Fatal("lease loss changed transport retry budget")
	}
}

func TestSchedulerStopsPendingAcquisitionWithoutCancelingOwners(t *testing.T) {
	pool := newExecutionSlots([]*executionSlot{{}, {}})
	first, err := pool.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	second, err := pool.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer second.release()
	store := &schedulerStore{claims: []domain.WorkClaim{schedulerClaim("waiting-for-owner")}}
	scheduler := newTestScheduler(t, store, runnerFunc(func(context.Context, domain.WorkClaim) (app.IngestResult, error) {
		t.Error("runner entered with every owner occupied")
		return app.IngestResult{}, nil
	}), schedulerOptions{slots: pool})
	if err = scheduler.start(t.Context()); err != nil {
		t.Fatal(err)
	}
	stopCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = scheduler.stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if first.ctx.Err() != nil || second.ctx.Err() != nil || store.claimCount() != 1 {
		t.Fatalf("stop canceled owners or preclaimed pending: %v %v pending%d", first.ctx.Err(), second.ctx.Err(), store.claimCount())
	}
}
