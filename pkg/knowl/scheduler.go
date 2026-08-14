package knowl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/rs/zerolog/log"
)

const (
	// Scheduler work is deliberately bounded per cycle. Production scans every
	// five seconds and renews a 30-second work lease at one-third of its term.
	defaultSchedulerBatch       = 16
	defaultSchedulerScan        = 5 * time.Second
	defaultSchedulerWorkLease   = 30 * time.Second
	descriptorUnavailableClass  = "descriptor_unavailable"
	schedulerScanFailureClass   = "scheduler_scan"
	schedulerRunnerFailureClass = "runner_interrupted"
)

type terminalRunner interface {
	RunToTerminal(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, error)
}

type schedulerOptions struct {
	wakeSize               int
	claimBatch             int
	descriptorFailureBatch int
	scanInterval           time.Duration
	workLeaseDuration      time.Duration
	now                    func() time.Time
	newToken               func() (string, error)
	scanTicks              <-chan time.Time
	renewTicks             <-chan time.Time
}

type operationScheduler struct {
	operations app.OperationStore
	runner     terminalRunner
	scope      domain.ScopeRef
	options    schedulerOptions
	wake       chan struct{}
	stopClaims chan struct{}

	mu          sync.Mutex
	cancel      context.CancelFunc
	started     bool
	stopped     bool
	startErr    error
	initialDone chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
}

func newOperationScheduler(operations app.OperationStore, runner terminalRunner, scope domain.ScopeRef, options schedulerOptions) (*operationScheduler, error) {
	if operations == nil || runner == nil || scope == "" {
		return nil, fmt.Errorf("scheduler operations, runner, and scope are required")
	}
	options = normalizeSchedulerOptions(options)
	return &operationScheduler{
		operations: operations,
		runner:     runner,
		scope:      scope,
		options:    options,
		wake:       make(chan struct{}, options.wakeSize),
		stopClaims: make(chan struct{}),
	}, nil
}

func normalizeSchedulerOptions(options schedulerOptions) schedulerOptions {
	if options.wakeSize <= 0 {
		options.wakeSize = 1
	}
	if options.claimBatch <= 0 {
		options.claimBatch = defaultSchedulerBatch
	}
	if options.descriptorFailureBatch <= 0 {
		options.descriptorFailureBatch = defaultSchedulerBatch
	}
	if options.scanInterval <= 0 {
		options.scanInterval = defaultSchedulerScan
	}
	if options.workLeaseDuration <= 0 {
		options.workLeaseDuration = defaultSchedulerWorkLease
	}
	if options.now == nil {
		options.now = func() time.Time { return time.Now().UTC() }
	}
	if options.newToken == nil {
		options.newToken = randomWorkToken
	}
	return options
}

// Wake records a best-effort edge for durable work. It is intentionally
// non-blocking and carries no execution inputs.
func (scheduler *operationScheduler) Wake(domain.OperationID) {
	if scheduler == nil {
		return
	}
	select {
	case scheduler.wake <- struct{}{}:
	default:
	}
}

// start launches the scoped drain loop and waits only for its bounded initial
// durable inspection, not for maintenance completion.
func (scheduler *operationScheduler) start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	scheduler.mu.Lock()
	if scheduler.started {
		done := scheduler.initialDone
		scheduler.mu.Unlock()
		select {
		case <-done:
			scheduler.mu.Lock()
			err := scheduler.startErr
			scheduler.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if scheduler.stopped {
		scheduler.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	scheduler.cancel = cancel
	scheduler.started = true
	scheduler.initialDone = make(chan struct{})
	initial := make(chan error, 1)
	scheduler.wg.Add(1)
	go scheduler.run(runCtx, initial)
	scheduler.mu.Unlock()

	select {
	case err := <-initial:
		if err != nil {
			cancel()
		}
		scheduler.completeStart(err)
		return err
	case <-ctx.Done():
		cancel()
		scheduler.completeStart(ctx.Err())
		return ctx.Err()
	}
}

func (scheduler *operationScheduler) completeStart(err error) {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	scheduler.startErr = err
	close(scheduler.initialDone)
}

func (scheduler *operationScheduler) stop(ctx context.Context) error {
	if scheduler == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	scheduler.mu.Lock()
	scheduler.stopped = true
	scheduler.stopOnce.Do(func() { close(scheduler.stopClaims) })
	cancel := scheduler.cancel
	scheduler.mu.Unlock()

	finished := make(chan struct{})
	go func() {
		scheduler.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		if cancel != nil {
			cancel()
		}
		return ctx.Err()
	}
}

func (scheduler *operationScheduler) run(ctx context.Context, initial chan<- error) {
	defer scheduler.wg.Done()
	ready, err := scheduler.inspect(ctx)
	if err == nil && ready {
		scheduler.Wake("")
	}
	initial <- err
	close(initial)
	if err != nil {
		return
	}
	scanTicks, stopScanTicks := scheduler.ticks(scheduler.options.scanTicks, scheduler.options.scanInterval)
	defer stopScanTicks()
	for {
		select {
		case <-ctx.Done():
			return
		case <-scheduler.stopClaims:
			return
		case <-scheduler.wake:
			scheduler.cycle(ctx)
		case <-scanTicks:
			scheduler.cycle(ctx)
		}
	}
}

func (scheduler *operationScheduler) inspect(ctx context.Context) (bool, error) {
	failures, err := scheduler.operations.DescriptorFailures(ctx, scheduler.scope, scheduler.options.descriptorFailureBatch)
	if err != nil {
		return false, err
	}
	for _, id := range failures {
		if err := scheduler.operations.Fail(ctx, id, domain.Failure{Class: descriptorUnavailableClass, OperationID: string(id)}); err != nil {
			return false, err
		}
	}
	ready, err := scheduler.operations.ResumeReady(ctx, scheduler.scope, scheduler.options.claimBatch)
	if err != nil {
		return false, err
	}
	return len(ready) > 0, nil
}

func (scheduler *operationScheduler) cycle(ctx context.Context) {
	if scheduler.isStopping() {
		return
	}
	ready, err := scheduler.inspect(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Error().Str("class", schedulerScanFailureClass).Msg("knowl scheduler scan failed")
		}
		return
	}
	if !ready {
		return
	}
	claimed := 0
	for claimed < scheduler.options.claimBatch && ctx.Err() == nil && !scheduler.isStopping() {
		claim, claimErr := scheduler.claim(ctx)
		if errors.Is(claimErr, app.ErrNoReadyOperation) {
			return
		}
		if claimErr != nil {
			if !errors.Is(claimErr, context.Canceled) {
				log.Error().Str("class", schedulerScanFailureClass).Msg("knowl scheduler claim failed")
			}
			return
		}
		claimed++
		result, runErr := scheduler.runClaim(ctx, claim)
		if runErr != nil {
			failureClass := schedulerRunnerFailureClass
			if result.Operation.Failure != nil {
				failureClass = result.Operation.Failure.Class
			}
			log.Error().Str("class", failureClass).Str("operation_id", string(claim.Operation.ID)).Msg("knowl operation runner stopped")
		}
	}
	if claimed == scheduler.options.claimBatch && ctx.Err() == nil && !scheduler.isStopping() {
		scheduler.Wake("")
	}
}

func (scheduler *operationScheduler) isStopping() bool {
	select {
	case <-scheduler.stopClaims:
		return true
	default:
		return false
	}
}

func (scheduler *operationScheduler) claim(ctx context.Context) (domain.WorkClaim, error) {
	token, err := scheduler.options.newToken()
	if err != nil {
		return domain.WorkClaim{}, fmt.Errorf("create work lease: %w", err)
	}
	now := scheduler.options.now().UTC()
	return scheduler.operations.ClaimReady(ctx, scheduler.scope, domain.WorkLease{
		Token: token, ExpiresAt: now.Add(scheduler.options.workLeaseDuration),
	})
}

func (scheduler *operationScheduler) runClaim(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
	executionCtx, cancelExecution := context.WithCancel(ctx)
	renewCtx, stopRenewal := context.WithCancel(ctx)
	renewalDone := make(chan struct{})
	go func() {
		defer close(renewalDone)
		scheduler.renew(renewCtx, cancelExecution, claim)
	}()
	result, err := scheduler.runner.RunToTerminal(executionCtx, claim)
	stopRenewal()
	<-renewalDone
	cancelExecution()
	return result, err
}

func (scheduler *operationScheduler) renew(ctx context.Context, cancelExecution context.CancelFunc, claim domain.WorkClaim) {
	interval := scheduler.options.workLeaseDuration / 3
	if interval <= 0 {
		interval = time.Nanosecond
	}
	ticks, stopTicks := scheduler.ticks(scheduler.options.renewTicks, interval)
	defer stopTicks()
	currentToken := claim.Lease.Token
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			token, err := scheduler.options.newToken()
			if err != nil {
				cancelExecution()
				return
			}
			now := scheduler.options.now().UTC()
			next := domain.WorkLease{Token: token, ExpiresAt: now.Add(scheduler.options.workLeaseDuration)}
			if err := scheduler.operations.RenewClaim(ctx, scheduler.scope, claim.Operation.ID, currentToken, next); err != nil {
				cancelExecution()
				return
			}
			currentToken = next.Token
		}
	}
}

func (scheduler *operationScheduler) ticks(injected <-chan time.Time, interval time.Duration) (<-chan time.Time, func()) {
	if injected != nil {
		return injected, func() {}
	}
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

func randomWorkToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("read random work lease token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
