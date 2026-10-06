package knowl

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

// Hold the real scheduler's initial store read past cancellation so shutdown
// must retain its resources until the read actually exits.
type startupInspectionStore struct {
	app.OperationStore
	entered  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (store *startupInspectionStore) DescriptorFailures(ctx context.Context, scope domain.ScopeRef, limit int) ([]domain.OperationID, error) {
	close(store.entered)
	select {
	case <-ctx.Done():
		close(store.canceled)
		<-store.release
	case <-store.release:
	}
	return store.OperationStore.DescriptorFailures(ctx, scope, limit)
}

type startupStoreCloser struct {
	io.Closer
	calls atomic.Int32
}

func (closer *startupStoreCloser) Close() error {
	closer.calls.Add(1)
	return closer.Closer.Close()
}

func TestStopDuringStartupHonorsDeadline(t *testing.T) {
	for _, workerOnly := range []bool{false, true} {
		name := "HTTP"
		if workerOnly {
			name = "worker"
		}
		t.Run(name, func(t *testing.T) {
			config := DefaultConfig()
			config.Workspace = t.TempDir()
			config.ListenAddr = "127.0.0.1:0"
			host, err := New(t.Context(), Options{Config: config, Maintainer: schedulerAcceptedMaintainer{}})
			if err != nil {
				t.Fatal(err)
			}
			store := &startupInspectionStore{OperationStore: host.operations, entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
			host.scheduler.operations = store
			closer := &startupStoreCloser{Closer: host.closer}
			host.closer = closer
			startupCtx, cancelStartup := context.WithCancel(t.Context())
			defer cancelStartup()
			released := false
			defer func() {
				cancelStartup()
				if !released {
					close(store.release)
				}
				_ = host.Stop(context.Background())
			}()
			started := make(chan error, 1)
			go func() {
				if workerOnly {
					started <- host.StartOperationWorker(startupCtx)
				} else {
					started <- host.Start(startupCtx)
				}
			}()
			select {
			case <-store.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("startup did not inspect the durable store")
			}
			stopCtx, cancelStop := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancelStop()
			stopped := make(chan error, 1)
			go func() { stopped <- host.Stop(stopCtx) }()
			select {
			case err = <-stopped:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("shutdown deadline: %v", err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("Stop exceeded its deadline while startup held the store read")
			}
			select {
			case <-store.canceled:
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown did not cancel startup inspection")
			}
			if host.Ready() || closer.calls.Load() != 0 {
				t.Fatalf("ready=%t store closes=%d while startup is live", host.Ready(), closer.calls.Load())
			}
			if _, err = host.ReconcileHierarchy(t.Context()); !errors.Is(err, ErrHostClosed) {
				t.Fatalf("new admission after shutdown: %v", err)
			}
			close(store.release)
			released = true
			select {
			case err = <-started:
				if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrHostClosed) {
					t.Fatalf("startup outcome after shutdown: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("startup did not exit after releasing inspection")
			}
			if host.Ready() {
				t.Fatal("late startup marked a stopped host ready")
			}
			if err = host.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = host.Stop(t.Context()); err != nil || closer.calls.Load() != 1 {
				t.Fatalf("shutdown retry=%v store closes=%d", err, closer.calls.Load())
			}
			if err = host.Start(t.Context()); !errors.Is(err, ErrHostClosed) {
				t.Fatalf("HTTP restart after shutdown: %v", err)
			}
			if err = host.StartOperationWorker(t.Context()); !errors.Is(err, ErrHostClosed) {
				t.Fatalf("worker restart after shutdown: %v", err)
			}
		})
	}
}

func TestStopDuringStartupPreventsLateReadiness(t *testing.T) {
	for _, workerOnly := range []bool{false, true} {
		name := "HTTP"
		if workerOnly {
			name = "worker"
		}
		t.Run(name, func(t *testing.T) {
			config := DefaultConfig()
			config.Workspace = t.TempDir()
			config.ListenAddr = "127.0.0.1:0"
			host, err := New(t.Context(), Options{Config: config, Maintainer: schedulerAcceptedMaintainer{}})
			if err != nil {
				t.Fatal(err)
			}
			store := &startupInspectionStore{OperationStore: host.operations, entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
			host.scheduler.operations = store
			closer := &startupStoreCloser{Closer: host.closer}
			host.closer = closer
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			released := false
			defer func() {
				if !released {
					close(store.release)
				}
				_ = host.Stop(context.Background())
			}()
			started := make(chan error, 1)
			go func() {
				if workerOnly {
					started <- host.StartOperationWorker(ctx)
				} else {
					started <- host.Start(ctx)
				}
			}()
			select {
			case <-store.entered:
			case <-ctx.Done():
				t.Fatal("startup did not inspect the store")
			}
			stopped := make(chan error, 1)
			go func() { stopped <- host.Stop(ctx) }()
			select {
			case <-host.slots.stopping:
			case <-ctx.Done():
				t.Fatal("shutdown did not close admission during startup")
			}
			if closer.calls.Load() != 0 {
				t.Fatal("shutdown closed the store while startup was still reading it")
			}
			close(store.release)
			released = true
			select {
			case err = <-started:
				if !errors.Is(err, ErrHostClosed) {
					t.Fatalf("late startup outcome: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("startup did not finish after releasing inspection")
			}
			select {
			case err = <-stopped:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("shutdown did not join startup")
			}
			if host.Ready() || closer.calls.Load() != 1 {
				t.Fatalf("ready=%t store closes=%d after joining startup", host.Ready(), closer.calls.Load())
			}
		})
	}
}
