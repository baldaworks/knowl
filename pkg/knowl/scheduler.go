package knowl

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
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
	defaultRetryAttempts        = 3
	defaultRetryInitialDelay    = 30 * time.Second
	defaultRetryMaximumDelay    = 5 * time.Minute
	descriptorUnavailableClass  = "descriptor_unavailable"
	schedulerScanFailureClass   = "scheduler_scan"
	schedulerRunnerFailureClass = "runner_interrupted"
)

type terminalRunner interface {
	RunToTerminal(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, error)
}

// DrainResult summarizes maintenance operations processed during a drain cycle.
type DrainResult struct {
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Retried   int `json:"retried"`
	Total     int `json:"total"`
}

type terminalRouter struct {
	source    terminalRunner
	hierarchy terminalRunner
}

func (router terminalRouter) RunToTerminal(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
	switch claim.Descriptor.Kind {
	case domain.WorkHierarchy:
		if router.hierarchy == nil {
			return app.IngestResult{}, app.ErrMaintainerUnavailable
		}
		return router.hierarchy.RunToTerminal(ctx, claim)
	case "", domain.WorkSourceMaintenance:
		return router.source.RunToTerminal(ctx, claim)
	default:
		return app.IngestResult{}, app.ErrExecutionDescriptorUnavailable
	}
}

type schedulerOptions struct {
	wakeSize               int
	claimBatch             int
	descriptorFailureBatch int
	scanInterval           time.Duration
	workLeaseDuration      time.Duration
	retryAttempts          int
	retryInitialDelay      time.Duration
	retryMaximumDelay      time.Duration
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
	if options.retryAttempts <= 0 {
		options.retryAttempts = defaultRetryAttempts
	}
	if options.retryInitialDelay <= 0 {
		options.retryInitialDelay = defaultRetryInitialDelay
	}
	if options.retryMaximumDelay <= 0 {
		options.retryMaximumDelay = defaultRetryMaximumDelay
	}
	if options.retryMaximumDelay < options.retryInitialDelay {
		options.retryMaximumDelay = options.retryInitialDelay
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

// Drain synchronously claims and executes all ready operations until none remain
// or context expires. It does not start background tickers or goroutines.
func (scheduler *operationScheduler) Drain(ctx context.Context) (DrainResult, error) {
	if scheduler == nil {
		return DrainResult{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result DrainResult
	for {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if scheduler.isStopping() {
			return result, nil
		}
		ready, err := scheduler.inspect(ctx)
		if err != nil {
			return result, err
		}
		if !ready {
			return result, nil
		}
		claim, claimErr := scheduler.claim(ctx)
		if errors.Is(claimErr, app.ErrNoReadyOperation) {
			return result, nil
		}
		if claimErr != nil {
			return result, claimErr
		}

		opResult, leaseToken, runErr := scheduler.runClaim(ctx, claim)
		nextRetryAt, transitionErr := scheduler.handleTransientFailure(ctx, claim, leaseToken, &opResult, runErr)
		result.Total++

		failureClass := ""
		failureReason := ""
		failureDetail := ""
		event := log.Info()
		if runErr != nil {
			event = log.Error()
			failureClass = schedulerRunnerFailureClass
			if failure, ok := app.ClassifyExecutionFailure(runErr); ok {
				failureClass = failure.Class
				failureReason = failure.Reason
			}
			if opResult.Operation.Failure != nil && app.ValidateSafeFailure(*opResult.Operation.Failure, false) {
				failureClass = opResult.Operation.Failure.Class
				failureReason = opResult.Operation.Failure.Reason
			}
			var detailed interface{ SafeDetail() string }
			if errors.As(runErr, &detailed) {
				failureDetail = strings.TrimSpace(detailed.SafeDetail())
			}
		}

		switch opResult.Operation.Status {
		case domain.StatusCommitted:
			result.Completed++
		case domain.StatusFailed:
			result.Failed++
		default:
			if !nextRetryAt.IsZero() {
				result.Retried++
			}
		}

		event.Str("class", failureClass).Str("operation_id", string(claim.Operation.ID)).
			Str("maintenance_status", string(opResult.Operation.Status)).
			Int("work_attempt", claim.Operation.WorkAttempt).
			Int("retry_attempt", claim.Operation.RetryAttempt)
		if failureReason != "" {
			event.Str("reason", failureReason)
		}
		if failureDetail != "" {
			event.Str("detail", failureDetail)
		}
		if !nextRetryAt.IsZero() {
			event.Time("next_retry_at", nextRetryAt)
		}
		if transitionErr != nil && !errors.Is(transitionErr, context.Canceled) {
			event.Bool("retry_transition_failed", true)
		}
		document := claim.Descriptor.Source.SourceDocument
		if document != (domain.SourceDocument{}) {
			event.Str("source_id", string(document.SourceID)).Str("document_id", string(document.DocumentID)).
				Str("revision", document.Revision)
		}
		event.Msg("knowl maintenance operation drain")
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
		result, leaseToken, runErr := scheduler.runClaim(ctx, claim)
		nextRetryAt, transitionErr := scheduler.handleTransientFailure(ctx, claim, leaseToken, &result, runErr)
		failureClass := ""
		failureReason := ""
		failureDetail := ""
		event := log.Info()
		if runErr != nil {
			event = log.Error()
			failureClass = schedulerRunnerFailureClass
			if failure, ok := app.ClassifyExecutionFailure(runErr); ok {
				failureClass = failure.Class
				failureReason = failure.Reason
			}
			if result.Operation.Failure != nil && app.ValidateSafeFailure(*result.Operation.Failure, false) {
				failureClass = result.Operation.Failure.Class
				failureReason = result.Operation.Failure.Reason
			}
			var detailed interface{ SafeDetail() string }
			if errors.As(runErr, &detailed) {
				failureDetail = strings.TrimSpace(detailed.SafeDetail())
			}
		}
		event.Str("class", failureClass).Str("operation_id", string(claim.Operation.ID)).
			Str("maintenance_status", string(result.Operation.Status)).
			Int("work_attempt", claim.Operation.WorkAttempt).
			Int("retry_attempt", claim.Operation.RetryAttempt)
		if failureReason != "" {
			event.Str("reason", failureReason)
		}
		if failureDetail != "" {
			event.Str("detail", failureDetail)
		}
		if !nextRetryAt.IsZero() {
			event.Time("next_retry_at", nextRetryAt)
		}
		if transitionErr != nil && !errors.Is(transitionErr, context.Canceled) {
			event.Bool("retry_transition_failed", true)
		}
		document := claim.Descriptor.Source.SourceDocument
		if document != (domain.SourceDocument{}) {
			event.Str("source_id", string(document.SourceID)).Str("document_id", string(document.DocumentID)).
				Str("revision", document.Revision)
		}
		event.Msg("knowl maintenance operation")
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

func (scheduler *operationScheduler) runClaim(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, string, error) {
	executionCtx, cancelExecution := context.WithCancel(ctx)
	stopRenewal := make(chan struct{})
	renewalDone := make(chan string, 1)
	go func() {
		renewalDone <- scheduler.renew(ctx, stopRenewal, cancelExecution, claim)
	}()
	result, err := scheduler.runner.RunToTerminal(executionCtx, claim)
	close(stopRenewal)
	leaseToken := <-renewalDone
	cancelExecution()
	return result, leaseToken, err
}

func (scheduler *operationScheduler) renew(ctx context.Context, stop <-chan struct{}, cancelExecution context.CancelFunc, claim domain.WorkClaim) string {
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
			return currentToken
		case <-stop:
			return currentToken
		case <-ticks:
			token, err := scheduler.options.newToken()
			if err != nil {
				cancelExecution()
				return ""
			}
			now := scheduler.options.now().UTC()
			next := domain.WorkLease{Token: token, ExpiresAt: now.Add(scheduler.options.workLeaseDuration)}
			if err := scheduler.operations.RenewClaim(ctx, scheduler.scope, claim.Operation.ID, currentToken, next); err != nil {
				cancelExecution()
				return ""
			}
			currentToken = next.Token
		}
	}
}

func (scheduler *operationScheduler) handleTransientFailure(
	ctx context.Context,
	claim domain.WorkClaim,
	leaseToken string,
	result *app.IngestResult,
	runErr error,
) (time.Time, error) {
	failureInfo, classified := app.ClassifyExecutionFailure(runErr)
	if !classified || !failureInfo.Retryable || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) || ctx.Err() != nil {
		return time.Time{}, nil
	}
	failure := domain.Failure{
		Class: failureInfo.Class, Reason: failureInfo.Reason, OperationID: string(claim.Operation.ID),
	}
	result.Operation = claim.Operation
	result.Operation.Failure = &failure
	if claim.Operation.RetryAttempt >= scheduler.options.retryAttempts {
		if err := scheduler.operations.FailClaim(ctx, scheduler.scope, claim.Operation.ID, leaseToken, failure); err != nil {
			return time.Time{}, err
		}
		result.Operation.Status = domain.StatusFailed
		return time.Time{}, nil
	}
	readyAt := scheduler.options.now().UTC().Add(scheduler.retryDelay(claim.Operation.ID, claim.Operation.RetryAttempt))
	if err := scheduler.operations.ScheduleRetry(ctx, scheduler.scope, claim.Operation.ID, leaseToken, failure, readyAt); err != nil {
		return time.Time{}, err
	}
	result.Operation.ReadyAt = readyAt
	return readyAt, nil
}

func (scheduler *operationScheduler) retryDelay(id domain.OperationID, attempt int) time.Duration {
	delay := scheduler.options.retryInitialDelay
	for step := 1; step < attempt && delay < scheduler.options.retryMaximumDelay; step++ {
		if delay > scheduler.options.retryMaximumDelay/2 {
			delay = scheduler.options.retryMaximumDelay
			break
		}
		delay *= 2
	}
	if delay >= scheduler.options.retryMaximumDelay {
		return scheduler.options.retryMaximumDelay
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(id))
	var encodedAttempt [8]byte
	binary.LittleEndian.PutUint64(encodedAttempt[:], uint64(max(attempt, 0)))
	_, _ = hash.Write(encodedAttempt[:])
	jitterRange := delay / 5
	if jitterRange <= 0 {
		return delay
	}
	jitter := time.Duration(hash.Sum64() % uint64(jitterRange+1))
	if delay+jitter > scheduler.options.retryMaximumDelay {
		return scheduler.options.retryMaximumDelay
	}
	return delay + jitter
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
