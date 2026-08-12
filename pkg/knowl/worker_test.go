package knowl

import (
	"context"
	"testing"
	"time"
)

func TestWorkerSubmitDoesNotWaitOrInheritCallerCancellation(t *testing.T) {
	worker := newWorker(1)
	if err := worker.start(context.Background()); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(func() {
		if err := worker.stop(context.Background()); err != nil {
			t.Errorf("stop worker: %v", err)
		}
	})

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	defer cancelCaller()
	if err := worker.submit(callerCtx, func(jobCtx context.Context) error {
		close(started)
		<-release
		if err := jobCtx.Err(); err != nil {
			return err
		}
		close(finished)
		return nil
	}); err != nil {
		t.Fatalf("submit worker task: %v", err)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("submitted work did not start")
	}
	cancelCaller()
	select {
	case <-finished:
		t.Fatal("submitted work completed before being released")
	default:
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("submitted work was canceled by its caller")
	}
}

func TestWorkerStopCancelsSubmittedWork(t *testing.T) {
	worker := newWorker(1)
	if err := worker.start(context.Background()); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	started := make(chan struct{})
	canceled := make(chan struct{})
	if err := worker.submit(context.Background(), func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}); err != nil {
		t.Fatalf("submit worker task: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("submitted work did not start")
	}
	if err := worker.stop(context.Background()); err != nil {
		t.Fatalf("stop worker: %v", err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("stop returned before submitted work observed cancellation")
	}
}
