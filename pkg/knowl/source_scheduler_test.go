package knowl

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/source/reconcile"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const schedulerTestSourceID = domain.SourceID("source")

func TestSourceSchedulerOnStartRetryResetAndInterval(t *testing.T) {
	var calls atomic.Int64
	waits := newScriptedSourceWait()
	observer := &recordingSourceObserver{events: make(chan SourceAttempt, 8)}
	source := domain.Source{
		ID: schedulerTestSourceID, Type: domain.SourceTypeFilesystem, Enabled: true,
		Sync: domain.SourceSyncPolicy{OnStart: true, Interval: 10 * time.Minute, RetryInitial: time.Second, RetryMaximum: 4 * time.Second},
	}
	clock := &incrementingSourceClock{}
	scheduler, err := newSourceScheduler(func(_ context.Context, _ domain.ScopeRef, source domain.Source) (reconcile.Result, error) {
		call := calls.Add(1)
		result := reconcile.Result{SourceID: source.ID}
		if call <= 4 {
			result.FailureClass = "adapter"
			return result, errors.New("secret adapter detail")
		}
		result.Changed = call == 3
		return result, nil
	}, "local", []domain.Source{source}, observer, sourceSchedulerOptions{
		now: clock.now, wait: waits.wait, jitter: func(delay time.Duration, _ domain.SourceID, _ int) time.Duration { return delay },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.start(context.Background()); err != nil {
		t.Fatal(err)
	}

	requireSourceAttempt(t, observer.events, SourceTriggerOnStart, "failed", "adapter")
	waits.requireAndRelease(t, time.Second)
	requireSourceAttempt(t, observer.events, SourceTriggerRetry, "failed", "adapter")
	waits.requireAndRelease(t, 2*time.Second)
	requireSourceAttempt(t, observer.events, SourceTriggerRetry, "failed", "adapter")
	waits.requireAndRelease(t, 4*time.Second)
	requireSourceAttempt(t, observer.events, SourceTriggerRetry, "failed", "adapter")
	waits.requireAndRelease(t, 4*time.Second)
	requireSourceAttempt(t, observer.events, SourceTriggerRetry, "succeeded", "")
	waits.requireAndRelease(t, 10*time.Minute)
	requireSourceAttempt(t, observer.events, SourceTriggerInterval, "succeeded", "")

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.stop(stopCtx); err != nil {
		t.Fatalf("stop scheduler: %v", err)
	}
	if got := calls.Load(); got != 6 {
		t.Fatalf("sync calls = %d, want 6", got)
	}
}

func TestSourceSchedulerRunsSourcesIndependentlyAndSkipsInertSources(t *testing.T) {
	started := make(chan domain.SourceID, 2)
	release := make(chan struct{})
	sources := []domain.Source{
		{ID: "alpha", Enabled: true, Sync: domain.SourceSyncPolicy{OnStart: true, RetryInitial: time.Second, RetryMaximum: time.Second}},
		{ID: "beta", Enabled: true, Sync: domain.SourceSyncPolicy{OnStart: true, RetryInitial: time.Second, RetryMaximum: time.Second}},
		{ID: "disabled", Enabled: false, Sync: domain.SourceSyncPolicy{OnStart: true, RetryInitial: time.Second, RetryMaximum: time.Second}},
		{ID: "inert", Enabled: true, Sync: domain.SourceSyncPolicy{RetryInitial: time.Second, RetryMaximum: time.Second}},
	}
	scheduler, err := newSourceScheduler(func(ctx context.Context, _ domain.ScopeRef, source domain.Source) (reconcile.Result, error) {
		started <- source.ID
		select {
		case <-release:
			return reconcile.Result{SourceID: source.ID}, nil
		case <-ctx.Done():
			return reconcile.Result{SourceID: source.ID, FailureClass: "canceled"}, ctx.Err()
		}
	}, "local", sources, nil, sourceSchedulerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	seen := map[domain.SourceID]bool{}
	for range 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-time.After(time.Second):
			t.Fatal("independent source did not start")
		}
	}
	if !seen["alpha"] || !seen["beta"] || seen["disabled"] || seen["inert"] {
		t.Fatalf("started sources = %#v", seen)
	}
	close(release)
	if err := scheduler.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSourceSchedulerStopBoundsActiveManualAttemptAndObserverPanic(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	scheduler, err := newSourceScheduler(func(context.Context, domain.ScopeRef, domain.Source) (reconcile.Result, error) {
		close(started)
		<-release
		return reconcile.Result{SourceID: schedulerTestSourceID}, nil
	}, "local", nil, panickingSourceObserver{}, sourceSchedulerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := scheduler.run(context.Background(), domain.Source{ID: schedulerTestSourceID}, SourceTriggerManual)
		done <- runErr
	}()
	<-started
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scheduler.stop(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("bounded stop error = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("manual attempt: %v", err)
	}
	if err := scheduler.stop(context.Background()); err != nil {
		t.Fatalf("repeat stop: %v", err)
	}
	if _, err := scheduler.run(context.Background(), domain.Source{ID: schedulerTestSourceID}, SourceTriggerManual); !errors.Is(err, ErrHostClosed) {
		t.Fatalf("post-stop run error = %v", err)
	}
}

func TestSourceRetryAndJitterStayWithinBounds(t *testing.T) {
	const base = 10 * time.Second
	for range 1000 {
		got := jitterSourceDelay(base, schedulerTestSourceID, 1)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("jittered delay = %s, want within 20%%", got)
		}
	}
	if got := nextSourceRetry(3*time.Second, 4*time.Second); got != 4*time.Second {
		t.Fatalf("capped retry = %s, want 4s", got)
	}
}

type scriptedSourceWait struct {
	calls    chan time.Duration
	releases chan struct{}
}

func newScriptedSourceWait() *scriptedSourceWait {
	return &scriptedSourceWait{calls: make(chan time.Duration), releases: make(chan struct{})}
}

func (waiter *scriptedSourceWait) wait(ctx context.Context, delay time.Duration) bool {
	select {
	case waiter.calls <- delay:
	case <-ctx.Done():
		return false
	}
	select {
	case <-waiter.releases:
		return true
	case <-ctx.Done():
		return false
	}
}

func (waiter *scriptedSourceWait) requireAndRelease(t *testing.T, want time.Duration) {
	t.Helper()
	select {
	case got := <-waiter.calls:
		if got != want {
			t.Fatalf("wait = %s, want %s", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("missing wait %s", want)
	}
	waiter.releases <- struct{}{}
}

type recordingSourceObserver struct {
	events chan SourceAttempt
}

func (observer *recordingSourceObserver) ObserveSourceAttempt(attempt SourceAttempt) {
	observer.events <- attempt
}

type panickingSourceObserver struct{}

func (panickingSourceObserver) ObserveSourceAttempt(SourceAttempt) { panic("secret observer panic") }

type incrementingSourceClock struct {
	mu    sync.Mutex
	value time.Time
}

func (clock *incrementingSourceClock) now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.value = clock.value.Add(time.Millisecond)
	return clock.value
}

func requireSourceAttempt(t *testing.T, events <-chan SourceAttempt, trigger SourceTrigger, result, failure string) {
	t.Helper()
	select {
	case event := <-events:
		if event.Trigger != trigger || event.Result != result || event.FailureClass != failure || event.Duration != time.Millisecond {
			t.Fatalf("attempt = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatalf("missing %s attempt", trigger)
	}
}
