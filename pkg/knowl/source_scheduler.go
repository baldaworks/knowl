package knowl

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/baldaworks/knowl/internal/source/reconcile"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/rs/zerolog/log"
)

const (
	sourceJitterDivisor    = 5
	sourceAttemptSucceeded = "succeeded"
	sourceAttemptFailed    = "failed"
)

type sourceSyncFunc func(context.Context, domain.ScopeRef, domain.Source) (reconcile.Result, error)
type sourceWaitFunc func(context.Context, time.Duration) bool
type sourceJitterFunc func(time.Duration, domain.SourceID, int) time.Duration

type sourceSchedulerOptions struct {
	now    func() time.Time
	wait   sourceWaitFunc
	jitter sourceJitterFunc
}

func (options sourceSchedulerOptions) normalized() sourceSchedulerOptions {
	if options.now == nil {
		options.now = time.Now
	}
	if options.wait == nil {
		options.wait = waitForSourceDelay
	}
	if options.jitter == nil {
		options.jitter = jitterSourceDelay
	}
	return options
}

type sourceScheduler struct {
	syncSource sourceSyncFunc
	scope      domain.ScopeRef
	sources    []domain.Source
	observer   SourceObserver
	options    sourceSchedulerOptions

	mu            sync.Mutex
	started       bool
	stopping      bool
	cancel        context.CancelFunc
	workers       int
	active        int
	drained       chan struct{}
	drainedClosed bool
}

func newSourceScheduler(syncSource sourceSyncFunc, scope domain.ScopeRef, sources []domain.Source, observer SourceObserver, options sourceSchedulerOptions) (*sourceScheduler, error) {
	if syncSource == nil || scope == "" {
		return nil, fmt.Errorf("source scheduler dependencies are invalid: %w", app.ErrSourceInvalid)
	}
	return &sourceScheduler{
		syncSource: syncSource, scope: scope, sources: cloneSources(sources), observer: observer,
		options: options.normalized(), drained: make(chan struct{}),
	}, nil
}

func (scheduler *sourceScheduler) start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if scheduler.stopping {
		return ErrHostClosed
	}
	if scheduler.started {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	scheduler.cancel = cancel
	scheduler.started = true
	for _, source := range scheduler.sources {
		if !source.Enabled || (!source.Sync.OnStart && source.Sync.Interval <= 0) {
			continue
		}
		scheduler.workers++
		go scheduler.worker(ctx, cloneSource(source))
	}
	return nil
}

func (scheduler *sourceScheduler) stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	scheduler.mu.Lock()
	if !scheduler.stopping {
		scheduler.stopping = true
		if scheduler.cancel != nil {
			scheduler.cancel()
		}
		scheduler.closeDrainedLocked()
	}
	drained := scheduler.drained
	scheduler.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (scheduler *sourceScheduler) run(ctx context.Context, source domain.Source, trigger SourceTrigger) (reconcile.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := scheduler.beginAttempt(); err != nil {
		return reconcile.Result{SourceID: source.ID}, err
	}
	defer scheduler.finishAttempt()
	startedAt := scheduler.options.now()
	result, err := scheduler.syncSource(ctx, scheduler.scope, source)
	duration := scheduler.options.now().Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	attempt := SourceAttempt{
		SourceID: source.ID, Trigger: trigger, Result: sourceAttemptSucceeded, FailureClass: result.FailureClass,
		Changed: result.Changed, Duration: duration,
	}
	if err != nil {
		attempt.Result = sourceAttemptFailed
		if attempt.FailureClass == "" {
			attempt.FailureClass = sourceFailureClass(err)
		}
	}
	scheduler.observe(attempt)
	return result, err
}

func (scheduler *sourceScheduler) worker(ctx context.Context, source domain.Source) {
	defer scheduler.finishWorker()
	retryDelay := source.Sync.RetryInitial
	trigger := SourceTriggerInterval
	if source.Sync.OnStart {
		trigger = SourceTriggerOnStart
	} else if !scheduler.options.wait(ctx, source.Sync.Interval) {
		return
	}
	for {
		_, err := scheduler.run(ctx, source, trigger)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			delay := scheduler.options.jitter(retryDelay, source.ID, retryAttempt(retryDelay, source.Sync.RetryInitial))
			if delay <= 0 {
				delay = time.Nanosecond
			}
			if delay > source.Sync.RetryMaximum {
				delay = source.Sync.RetryMaximum
			}
			if !scheduler.options.wait(ctx, delay) {
				return
			}
			retryDelay = nextSourceRetry(retryDelay, source.Sync.RetryMaximum)
			trigger = SourceTriggerRetry
			continue
		}
		retryDelay = source.Sync.RetryInitial
		if source.Sync.Interval <= 0 || !scheduler.options.wait(ctx, source.Sync.Interval) {
			return
		}
		trigger = SourceTriggerInterval
	}
}

func (scheduler *sourceScheduler) beginAttempt() error {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if scheduler.stopping {
		return ErrHostClosed
	}
	scheduler.active++
	return nil
}

func (scheduler *sourceScheduler) finishAttempt() {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	scheduler.active--
	scheduler.closeDrainedLocked()
}

func (scheduler *sourceScheduler) finishWorker() {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	scheduler.workers--
	scheduler.closeDrainedLocked()
}

func (scheduler *sourceScheduler) closeDrainedLocked() {
	if scheduler.stopping && scheduler.workers == 0 && scheduler.active == 0 && !scheduler.drainedClosed {
		close(scheduler.drained)
		scheduler.drainedClosed = true
	}
}

func (scheduler *sourceScheduler) observe(attempt SourceAttempt) {
	event := log.Info()
	if attempt.Result == sourceAttemptFailed {
		event = log.Warn()
	}
	event.Str("source_id", string(attempt.SourceID)).Str("trigger", string(attempt.Trigger)).
		Str("result", attempt.Result).Str("failure_class", attempt.FailureClass).
		Bool("changed", attempt.Changed).Dur("duration", attempt.Duration).
		Msg("knowl source synchronization attempt")
	if scheduler.observer == nil {
		return
	}
	defer func() {
		if recover() != nil {
			log.Error().Str("class", "source_observer_panic").Msg("knowl source observer failed")
		}
	}()
	scheduler.observer.ObserveSourceAttempt(attempt)
}

func waitForSourceDelay(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func jitterSourceDelay(delay time.Duration, _ domain.SourceID, _ int) time.Duration {
	span := delay / sourceJitterDivisor
	if span <= 0 {
		return delay
	}
	width := int64(span)*2 + 1
	return delay + time.Duration(rand.Int64N(width)-int64(span))
}

func nextSourceRetry(current, maximum time.Duration) time.Duration {
	if current >= maximum || current > maximum/2 {
		return maximum
	}
	return current * 2
}

func retryAttempt(current, initial time.Duration) int {
	attempt := 1
	for initial > 0 && current > initial {
		current /= 2
		attempt++
	}
	return attempt
}

func sourceFailureClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case errors.Is(err, reconcile.ErrSyncInProgress):
		return "in_progress"
	case errors.Is(err, app.ErrSourceInvalid), errors.Is(err, app.ErrSourceNotFound):
		return "invalid"
	case errors.Is(err, ErrHostClosed):
		return "closed"
	default:
		return "failed"
	}
}
