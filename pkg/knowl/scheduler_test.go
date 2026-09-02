package knowl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	testProviderFailureClass  = "provider"
	testProviderFailureReason = "provider_run"
)

func TestTerminalRouterDispatchesByDurableWorkKind(t *testing.T) {
	t.Parallel()

	sourceCalls := 0
	hierarchyCalls := 0
	router := terminalRouter{
		source: runnerFunc(func(_ context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
			sourceCalls++
			return app.IngestResult{Operation: claim.Operation}, nil
		}),
		hierarchy: runnerFunc(func(_ context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
			hierarchyCalls++
			return app.IngestResult{Operation: claim.Operation}, nil
		}),
	}
	legacy := schedulerClaim("legacy-source")
	legacy.Descriptor.Kind = ""
	if _, err := router.RunToTerminal(context.Background(), legacy); err != nil {
		t.Fatalf("route legacy source: %v", err)
	}
	hierarchy := schedulerClaim("hierarchy")
	hierarchy.Descriptor.Kind = domain.WorkHierarchy
	if _, err := router.RunToTerminal(context.Background(), hierarchy); err != nil {
		t.Fatalf("route hierarchy: %v", err)
	}
	if sourceCalls != 1 || hierarchyCalls != 1 {
		t.Fatalf("route calls = source %d, hierarchy %d", sourceCalls, hierarchyCalls)
	}
	unknown := schedulerClaim("unknown")
	unknown.Descriptor.Kind = domain.WorkKind("unknown")
	if _, err := router.RunToTerminal(context.Background(), unknown); !errors.Is(err, app.ErrExecutionDescriptorUnavailable) {
		t.Fatalf("unknown route error = %v, want descriptor unavailable", err)
	}
	if _, err := (terminalRouter{source: router.source}).RunToTerminal(context.Background(), hierarchy); !errors.Is(err, app.ErrMaintainerUnavailable) {
		t.Fatalf("missing hierarchy runner error = %v, want maintainer unavailable", err)
	}
}

func TestSchedulerLogsSafeMaintenanceCorrelation(t *testing.T) {
	var output bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&output)
	t.Cleanup(func() { log.Logger = previous })

	claim := schedulerClaim("logged")
	claim.Descriptor.Source.SourceDocument = domain.SourceDocument{
		SourceID: "engineering", DocumentID: "architecture.md", Revision: "revision-1",
		URI: "https://credential:secret@example.test/architecture.md",
	}
	claim.Descriptor.Schema.Content = []byte("prompt-secret-body")
	store := &schedulerStore{claims: []domain.WorkClaim{claim}}
	scheduler := newTestScheduler(t, store, runnerFunc(func(_ context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		claim.Operation.Status = domain.StatusFailed
		claim.Operation.Failure = &domain.Failure{Class: testProviderFailureClass, OperationID: string(claim.Operation.ID)}
		return app.IngestResult{Operation: claim.Operation}, schedulerSafeDetailError{}
	}), schedulerOptions{claimBatch: 1})
	scheduler.cycle(context.Background())

	encoded := output.String()
	for _, required := range []string{
		"engineering", "architecture.md", "revision-1", string(claim.Operation.ID), testProviderFailureClass,
		`content validation failed for \"wiki/concepts/orphan.md\" (catalog.reconciliation_required)`,
	} {
		if !strings.Contains(encoded, required) {
			t.Errorf("maintenance log missing %q: %s", required, encoded)
		}
	}
	for _, secret := range []string{"credential", "secret", "prompt-secret-body", "provider-secret-detail"} {
		if strings.Contains(encoded, secret) {
			t.Errorf("maintenance log leaked %q: %s", secret, encoded)
		}
	}
}

func TestSchedulerSchedulesTransientRetryThenSucceeds(t *testing.T) {
	var output bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&output)
	t.Cleanup(func() { log.Logger = previous })

	first := schedulerClaim("transient-recovery")
	first.Operation.WorkAttempt = 1
	first.Operation.RetryAttempt = 1
	store := &schedulerStore{claims: []domain.WorkClaim{first}}
	calls := 0
	scheduler := newTestScheduler(t, store, runnerFunc(func(_ context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		calls++
		if calls == 1 {
			return app.IngestResult{Operation: claim.Operation}, schedulerClassifiedError{
				message: "provider credential and prompt body", class: testProviderFailureClass, reason: testProviderFailureReason, retryable: true,
			}
		}
		claim.Operation.Status = domain.StatusCommitted
		return app.IngestResult{Operation: claim.Operation}, nil
	}), schedulerOptions{retryInitialDelay: 30 * time.Second, retryMaximumDelay: 5 * time.Minute})
	scheduler.cycle(context.Background())

	retries := store.recordedRetries()
	if len(retries) != 1 {
		t.Fatalf("scheduled retries = %#v, want one", retries)
	}
	wantReadyAt := scheduler.options.now().UTC().Add(scheduler.retryDelay(first.Operation.ID, first.Operation.RetryAttempt))
	if retries[0].id != first.Operation.ID || retries[0].token != "test-token" || retries[0].failure.Class != testProviderFailureClass ||
		retries[0].failure.Reason != testProviderFailureReason || !retries[0].readyAt.Equal(wantReadyAt) {
		t.Fatalf("scheduled retry = %#v, want ready at %s", retries[0], wantReadyAt)
	}
	encoded := output.String()
	for _, required := range []string{testProviderFailureReason, "work_attempt", "retry_attempt", "next_retry_at"} {
		if !strings.Contains(encoded, required) {
			t.Errorf("retry log missing %q: %s", required, encoded)
		}
	}
	for _, secret := range []string{"credential", "prompt body"} {
		if strings.Contains(encoded, secret) {
			t.Errorf("retry log leaked %q: %s", secret, encoded)
		}
	}

	second := first
	second.Operation.WorkAttempt = 2
	second.Operation.RetryAttempt = 2
	store.addClaim(second)
	scheduler.cycle(context.Background())
	if calls != 2 || len(store.recordedRetries()) != 1 || len(store.recordedClaimFailures()) != 0 {
		t.Fatalf("recovered retry calls=%d schedules=%#v failures=%#v", calls, store.recordedRetries(), store.recordedClaimFailures())
	}
}

func TestSchedulerExhaustsTransientRetryBudget(t *testing.T) {
	claim := schedulerClaim("transient-exhausted")
	claim.Operation.WorkAttempt = 7
	claim.Operation.RetryAttempt = defaultRetryAttempts
	store := &schedulerStore{claims: []domain.WorkClaim{claim}}
	scheduler := newTestScheduler(t, store, runnerFunc(func(_ context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		return app.IngestResult{Operation: claim.Operation}, schedulerClassifiedError{
			message: "sensitive provider outage", class: testProviderFailureClass, reason: testProviderFailureReason, retryable: true,
		}
	}), schedulerOptions{})
	scheduler.cycle(context.Background())

	if retries := store.recordedRetries(); len(retries) != 0 {
		t.Fatalf("exhausted operation scheduled retries: %#v", retries)
	}
	failures := store.recordedClaimFailures()
	if len(failures) != 1 || failures[0].id != claim.Operation.ID || failures[0].failure.Class != testProviderFailureClass || failures[0].failure.Reason != testProviderFailureReason {
		t.Fatalf("exhausted claim failures = %#v", failures)
	}
}

func TestSchedulerSchedulesRetryWithRenewedLeaseToken(t *testing.T) {
	renewTicks := make(chan time.Time, 1)
	claim := schedulerClaim("renewed-retry")
	claim.Operation.WorkAttempt = 1
	claim.Operation.RetryAttempt = 1
	store := &schedulerStore{claims: []domain.WorkClaim{claim}, renewCalls: make(chan struct{}, 1)}
	started := make(chan struct{})
	release := make(chan struct{})
	tokens := []string{"initial-token", "renewed-token"}
	var tokenMu sync.Mutex
	scheduler := newTestScheduler(t, store, runnerFunc(func(_ context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		close(started)
		<-release
		return app.IngestResult{Operation: claim.Operation}, schedulerClassifiedError{
			message: "provider unavailable", class: testProviderFailureClass, reason: testProviderFailureReason, retryable: true,
		}
	}), schedulerOptions{
		renewTicks: renewTicks,
		newToken: func() (string, error) {
			tokenMu.Lock()
			defer tokenMu.Unlock()
			if len(tokens) == 0 {
				return "unused-token", nil
			}
			token := tokens[0]
			tokens = tokens[1:]
			return token, nil
		},
	})
	done := make(chan struct{})
	go func() {
		scheduler.cycle(context.Background())
		close(done)
	}()
	<-started
	renewTicks <- time.Now()
	select {
	case <-store.renewCalls:
	case <-time.After(time.Second):
		t.Fatal("retry fixture did not renew its lease")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retry fixture did not finish")
	}
	retries := store.recordedRetries()
	if len(retries) != 1 || retries[0].token != "renewed-token" {
		t.Fatalf("retry lease token = %#v, want renewed token", retries)
	}
}

func TestSchedulerRetryDelayIsDeterministicExponentialAndBounded(t *testing.T) {
	scheduler := &operationScheduler{options: normalizeSchedulerOptions(schedulerOptions{})}
	first := scheduler.retryDelay("operation-a", 1)
	if first < defaultRetryInitialDelay || first > defaultRetryInitialDelay+defaultRetryInitialDelay/5 {
		t.Fatalf("first retry delay = %s", first)
	}
	if repeat := scheduler.retryDelay("operation-a", 1); repeat != first {
		t.Fatalf("repeat retry delay = %s, want %s", repeat, first)
	}
	second := scheduler.retryDelay("operation-a", 2)
	if second < 2*defaultRetryInitialDelay || second > 2*defaultRetryInitialDelay+(2*defaultRetryInitialDelay)/5 {
		t.Fatalf("second retry delay = %s", second)
	}
	if capped := scheduler.retryDelay("operation-a", 20); capped != defaultRetryMaximumDelay {
		t.Fatalf("capped retry delay = %s, want %s", capped, defaultRetryMaximumDelay)
	}
}

func TestSchedulerWakeIsNonBlockingAndCoalesced(t *testing.T) {
	scheduler := newTestScheduler(t, &schedulerStore{}, runnerFunc(func(context.Context, domain.WorkClaim) (app.IngestResult, error) {
		t.Fatal("empty scheduler invoked runner")
		return app.IngestResult{}, nil
	}), schedulerOptions{wakeSize: 1})

	finished := make(chan struct{})
	go func() {
		for index := 0; index < 100; index++ {
			scheduler.Wake(domain.OperationID("ignored"))
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("full wake channel blocked the caller")
	}
	if got := len(scheduler.wake); got != 1 {
		t.Fatalf("coalesced wake count = %d, want 1", got)
	}
}

func TestSchedulerStartWaitsForInspectionNotExecution(t *testing.T) {
	store := &schedulerStore{claims: []domain.WorkClaim{schedulerClaim("initial")}}
	started := make(chan struct{})
	release := make(chan struct{})
	runner := runnerFunc(func(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		close(started)
		select {
		case <-release:
			claim.Operation.Status = domain.StatusCommitted
			return app.IngestResult{Operation: claim.Operation}, nil
		case <-ctx.Done():
			return app.IngestResult{Operation: claim.Operation}, ctx.Err()
		}
	})
	scheduler := newTestScheduler(t, store, runner, schedulerOptions{})
	if err := scheduler.start(context.Background()); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("initial durable work was not scheduled")
	}
	close(release)
	stopScheduler(t, scheduler)
}

func TestSchedulerRepeatedStartReturnsInitialInspectionFailure(t *testing.T) {
	wantErr := errors.New("inspection unavailable")
	store := &schedulerStore{descriptorErr: wantErr}
	scheduler := newTestScheduler(t, store, runnerFunc(func(context.Context, domain.WorkClaim) (app.IngestResult, error) {
		t.Fatal("failed initial inspection invoked runner")
		return app.IngestResult{}, nil
	}), schedulerOptions{})
	if err := scheduler.start(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("first start error = %v, want inspection failure", err)
	}
	if err := scheduler.start(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("repeated start error = %v, want same inspection failure", err)
	}
	stopScheduler(t, scheduler)
}

func TestSchedulerPeriodicScanRecoversLostWake(t *testing.T) {
	scanTicks := make(chan time.Time, 1)
	store := &schedulerStore{}
	completed := make(chan domain.OperationID, 1)
	scheduler := newTestScheduler(t, store, runnerFunc(func(_ context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		claim.Operation.Status = domain.StatusCommitted
		completed <- claim.Operation.ID
		return app.IngestResult{Operation: claim.Operation}, nil
	}), schedulerOptions{scanTicks: scanTicks})
	if err := scheduler.start(context.Background()); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	store.addClaim(schedulerClaim("periodic"))
	scanTicks <- time.Now()
	select {
	case id := <-completed:
		if id != "periodic" {
			t.Fatalf("completed operation = %q, want periodic", id)
		}
	case <-time.After(time.Second):
		t.Fatal("periodic scan did not recover lost wake")
	}
	stopScheduler(t, scheduler)
}

func TestSchedulerRestartProcessesAcceptedSourceReservation(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	storePath := filepath.Join(workspace.Root(), "state.db")
	firstStore, err := sqlite.Open(ctx, storePath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	maintainer := schedulerAcceptedMaintainer{}
	firstService, err := app.NewIngestService(workspace, firstStore, firstStore, maintainer, app.IngestOptions{AutoApply: true})
	if err != nil {
		t.Fatalf("new first ingest service: %v", err)
	}
	content := []byte("accepted source survives scheduler restart")
	sum := sha256.Sum256(content)
	envelope := domain.SourceEnvelope{
		Scope: "local", Source: domain.SourceRef{Adapter: "wiki-filesystem", ID: "configured/docs/page.md"},
		Version: domain.SourceVersion{Version: "rev-1", Digest: hex.EncodeToString(sum[:])}, MediaType: "text/markdown",
		SourceDocument: domain.SourceDocument{SourceID: "configured", DocumentID: "docs/page.md", Revision: "rev-1", URI: "file:///srv/wiki/docs/page.md"},
		Content:        content,
	}
	accepted, err := workspace.AcceptSource(ctx, envelope)
	if err != nil {
		t.Fatalf("accept source: %v", err)
	}
	reservation, err := firstService.ReserveAccepted(ctx, app.AcceptedMaintenanceRequest{
		Source: accepted, SourceDocument: envelope.SourceDocument, ContentType: envelope.MediaType,
	})
	if err != nil {
		t.Fatalf("reserve accepted source: %v", err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restartedStore, err := sqlite.Open(ctx, storePath)
	if err != nil {
		t.Fatalf("reopen durable store: %v", err)
	}
	t.Cleanup(func() { _ = restartedStore.Close() })
	restartedService, err := app.NewIngestService(workspace, restartedStore, restartedStore, maintainer, app.IngestOptions{AutoApply: true})
	if err != nil {
		t.Fatalf("new restarted ingest service: %v", err)
	}
	scheduler, err := newOperationScheduler(restartedStore, restartedService, envelope.Scope, schedulerOptions{})
	if err != nil {
		t.Fatalf("new restarted scheduler: %v", err)
	}
	if err := scheduler.start(ctx); err != nil {
		t.Fatalf("start restarted scheduler: %v", err)
	}
	defer stopScheduler(t, scheduler)

	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		operation, readErr := restartedStore.Operation(ctx, envelope.Scope, reservation.OperationID)
		if readErr == nil && operation.Status == domain.StatusCommitted {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("restarted scheduler operation = %#v, err = %v", operation, readErr)
		case <-ticker.C:
		}
	}
	descriptor, err := restartedStore.Execution(ctx, envelope.Scope, reservation.OperationID)
	if err != nil {
		t.Fatalf("read restarted descriptor: %v", err)
	}
	if descriptor.Source.SourceDocument != envelope.SourceDocument {
		t.Fatalf("restarted source document = %#v, want %#v", descriptor.Source.SourceDocument, envelope.SourceDocument)
	}
}

func TestSchedulerBoundsCyclesAndRetriggersReadyWork(t *testing.T) {
	store := &schedulerStore{claims: []domain.WorkClaim{
		schedulerClaim("one"), schedulerClaim("two"), schedulerClaim("three"),
	}}
	completed := make(chan domain.OperationID, 3)
	scheduler := newTestScheduler(t, store, runnerFunc(func(_ context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		claim.Operation.Status = domain.StatusCommitted
		completed <- claim.Operation.ID
		return app.IngestResult{Operation: claim.Operation}, nil
	}), schedulerOptions{claimBatch: 2})
	if err := scheduler.start(context.Background()); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	for index := 0; index < 3; index++ {
		select {
		case <-completed:
		case <-time.After(time.Second):
			t.Fatalf("completed %d operations, want 3", index)
		}
	}
	stopScheduler(t, scheduler)
	if got := store.maxClaimCallsBetweenInspections(); got > 2 {
		t.Fatalf("claim calls in one cycle = %d, want at most 2", got)
	}
}

func TestSchedulerClassifiesLegacyDescriptorsBeforeRunning(t *testing.T) {
	store := &schedulerStore{descriptorFailures: []domain.OperationID{"legacy-a", "legacy-b"}}
	scheduler := newTestScheduler(t, store, runnerFunc(func(context.Context, domain.WorkClaim) (app.IngestResult, error) {
		t.Fatal("legacy descriptor reached runner")
		return app.IngestResult{}, nil
	}), schedulerOptions{})
	if err := scheduler.start(context.Background()); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	defer stopScheduler(t, scheduler)
	failures := store.recordedFailures()
	if len(failures) != 2 {
		t.Fatalf("legacy failures = %#v, want 2", failures)
	}
	for _, failure := range failures {
		if failure.Class != descriptorUnavailableClass {
			t.Fatalf("legacy failure = %#v, want redacted descriptor class", failure)
		}
	}
}

func TestSchedulerRenewsLongRunningClaim(t *testing.T) {
	renewTicks := make(chan time.Time, 1)
	store := &schedulerStore{claims: []domain.WorkClaim{schedulerClaim("long")}, renewCalls: make(chan struct{}, 1)}
	started := make(chan struct{})
	release := make(chan struct{})
	scheduler := newTestScheduler(t, store, runnerFunc(func(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		close(started)
		select {
		case <-release:
			claim.Operation.Status = domain.StatusCommitted
			return app.IngestResult{Operation: claim.Operation}, nil
		case <-ctx.Done():
			return app.IngestResult{Operation: claim.Operation}, ctx.Err()
		}
	}), schedulerOptions{renewTicks: renewTicks})
	if err := scheduler.start(context.Background()); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	<-started
	renewTicks <- time.Now()
	select {
	case <-store.renewCalls:
	case <-time.After(time.Second):
		t.Fatal("long-running claim was not renewed")
	}
	close(release)
	stopScheduler(t, scheduler)
}

func TestSchedulerRenewalLossCancelsWithoutFailOrRelease(t *testing.T) {
	renewTicks := make(chan time.Time, 1)
	store := &schedulerStore{
		claims:     []domain.WorkClaim{schedulerClaim("lost")},
		renewErr:   app.ErrWorkLeaseConflict,
		renewCalls: make(chan struct{}, 1),
	}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	scheduler := newTestScheduler(t, store, runnerFunc(func(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return app.IngestResult{Operation: claim.Operation}, ctx.Err()
	}), schedulerOptions{renewTicks: renewTicks})
	if err := scheduler.start(context.Background()); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	<-started
	renewTicks <- time.Now()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("renewal loss did not cancel execution")
	}
	stopScheduler(t, scheduler)
	if failures := store.recordedFailures(); len(failures) != 0 {
		t.Fatalf("renewal loss recorded terminal failures: %#v", failures)
	}
	if store.releaseCount() != 0 {
		t.Fatalf("renewal loss released work lease %d times, want expiry", store.releaseCount())
	}
	if len(store.recordedRetries()) != 0 || len(store.recordedClaimFailures()) != 0 {
		t.Fatalf("renewal loss changed retry state: retries=%#v failures=%#v", store.recordedRetries(), store.recordedClaimFailures())
	}
}

func TestSchedulerShutdownStopsClaimsAndAllowsBoundedCompletion(t *testing.T) {
	store := &schedulerStore{claims: []domain.WorkClaim{schedulerClaim("active"), schedulerClaim("later")}}
	started := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan domain.OperationID, 1)
	scheduler := newTestScheduler(t, store, runnerFunc(func(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		if claim.Operation.ID == "active" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return app.IngestResult{Operation: claim.Operation}, ctx.Err()
			}
		}
		claim.Operation.Status = domain.StatusCommitted
		completed <- claim.Operation.ID
		return app.IngestResult{Operation: claim.Operation}, nil
	}), schedulerOptions{})
	if err := scheduler.start(context.Background()); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	<-started
	stopped := make(chan error, 1)
	go func() { stopped <- scheduler.stop(context.Background()) }()
	select {
	case <-scheduler.stopClaims:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not stop new claims")
	}
	select {
	case err := <-stopped:
		t.Fatalf("shutdown returned before active completion: %v", err)
	default:
	}
	close(release)
	select {
	case id := <-completed:
		if id != "active" {
			t.Fatalf("completed operation = %q, want active", id)
		}
	case <-time.After(time.Second):
		t.Fatal("active operation did not complete during shutdown")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("stop scheduler: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after active completion")
	}
	if remaining := store.claimCount(); remaining != 1 {
		t.Fatalf("remaining durable claims = %d, want later work unclaimed", remaining)
	}
}

func TestSchedulerShutdownDeadlineCancelsActiveExecution(t *testing.T) {
	store := &schedulerStore{claims: []domain.WorkClaim{schedulerClaim("active")}}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	scheduler := newTestScheduler(t, store, runnerFunc(func(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return app.IngestResult{Operation: claim.Operation}, ctx.Err()
	}), schedulerOptions{})
	if err := scheduler.start(context.Background()); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	<-started
	stopCtx, cancelStop := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- scheduler.stop(stopCtx) }()
	select {
	case <-cancelled:
		t.Fatal("shutdown canceled active execution before its bound expired")
	default:
	}
	cancelStop()
	if err := <-stopped; !errors.Is(err, context.Canceled) {
		t.Fatalf("stop error = %v, want canceled bound", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("expired shutdown bound did not cancel active execution")
	}
	if len(store.recordedRetries()) != 0 || len(store.recordedClaimFailures()) != 0 {
		t.Fatalf("shutdown cancellation changed retry state: retries=%#v failures=%#v", store.recordedRetries(), store.recordedClaimFailures())
	}
}

type runnerFunc func(context.Context, domain.WorkClaim) (app.IngestResult, error)

type schedulerAcceptedMaintainer struct{}

func (schedulerAcceptedMaintainer) Plan(_ context.Context, input domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	return domain.ModelEditPlan{
		SchemaDigest: input.Schema.Digest,
		SourceRefs:   []string{app.SourceRefKey(input.Source)},
		Rationale:    "record accepted source maintenance",
	}, nil
}

func (run runnerFunc) RunToTerminal(ctx context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
	return run(ctx, claim)
}

type schedulerStore struct {
	app.OperationStore
	mu                 sync.Mutex
	claims             []domain.WorkClaim
	descriptorFailures []domain.OperationID
	descriptorErr      error
	failures           []domain.Failure
	renewErr           error
	renewCalls         chan struct{}
	releases           int
	retries            []schedulerRetry
	claimFailures      []schedulerClaimFailure
	claimCalls         int
	inspectionCalls    []int
}

type schedulerRetry struct {
	id      domain.OperationID
	token   string
	failure domain.Failure
	readyAt time.Time
}

type schedulerClaimFailure struct {
	id      domain.OperationID
	token   string
	failure domain.Failure
}

func (store *schedulerStore) addClaim(claim domain.WorkClaim) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claims = append(store.claims, claim)
}

func (store *schedulerStore) DescriptorFailures(context.Context, domain.ScopeRef, int) ([]domain.OperationID, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.inspectionCalls = append(store.inspectionCalls, store.claimCalls)
	store.claimCalls = 0
	if store.descriptorErr != nil {
		return nil, store.descriptorErr
	}
	return append([]domain.OperationID(nil), store.descriptorFailures...), nil
}

func (store *schedulerStore) ResumeReady(context.Context, domain.ScopeRef, int) ([]domain.OperationID, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	ids := make([]domain.OperationID, 0, len(store.claims))
	for _, claim := range store.claims {
		ids = append(ids, claim.Operation.ID)
	}
	return ids, nil
}

func (store *schedulerStore) ClaimReady(_ context.Context, _ domain.ScopeRef, lease domain.WorkLease) (domain.WorkClaim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claimCalls++
	if len(store.claims) == 0 {
		return domain.WorkClaim{}, app.ErrNoReadyOperation
	}
	claim := store.claims[0]
	store.claims = store.claims[1:]
	claim.Lease = lease
	return claim, nil
}

func (store *schedulerStore) RenewClaim(context.Context, domain.ScopeRef, domain.OperationID, string, domain.WorkLease) error {
	store.mu.Lock()
	err := store.renewErr
	calls := store.renewCalls
	store.mu.Unlock()
	if calls != nil {
		select {
		case calls <- struct{}{}:
		default:
		}
	}
	return err
}

func (store *schedulerStore) ReleaseClaim(context.Context, domain.ScopeRef, domain.OperationID, string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.releases++
	return nil
}

func (store *schedulerStore) ScheduleRetry(_ context.Context, _ domain.ScopeRef, id domain.OperationID, token string, failure domain.Failure, readyAt time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.retries = append(store.retries, schedulerRetry{id: id, token: token, failure: failure, readyAt: readyAt})
	return nil
}

func (store *schedulerStore) FailClaim(_ context.Context, _ domain.ScopeRef, id domain.OperationID, token string, failure domain.Failure) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claimFailures = append(store.claimFailures, schedulerClaimFailure{id: id, token: token, failure: failure})
	return nil
}

func (store *schedulerStore) Fail(_ context.Context, id domain.OperationID, failure domain.Failure) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failures = append(store.failures, failure)
	for index, candidate := range store.descriptorFailures {
		if candidate == id {
			store.descriptorFailures = append(store.descriptorFailures[:index], store.descriptorFailures[index+1:]...)
			break
		}
	}
	return nil
}

func (store *schedulerStore) recordedFailures() []domain.Failure {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]domain.Failure(nil), store.failures...)
}

func (store *schedulerStore) recordedRetries() []schedulerRetry {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]schedulerRetry(nil), store.retries...)
}

func (store *schedulerStore) recordedClaimFailures() []schedulerClaimFailure {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]schedulerClaimFailure(nil), store.claimFailures...)
}

func (store *schedulerStore) releaseCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.releases
}

func (store *schedulerStore) claimCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.claims)
}

func (store *schedulerStore) maxClaimCallsBetweenInspections() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	maximum := store.claimCalls
	for _, count := range store.inspectionCalls {
		if count > maximum {
			maximum = count
		}
	}
	return maximum
}

func schedulerClaim(id domain.OperationID) domain.WorkClaim {
	return domain.WorkClaim{Operation: domain.Operation{ID: id, Key: domain.OperationKey{Scope: DefaultScope}, Status: domain.StatusReceived}}
}

type schedulerClassifiedError struct {
	message   string
	class     string
	reason    string
	retryable bool
}

func (failure schedulerClassifiedError) Error() string         { return failure.message }
func (failure schedulerClassifiedError) FailureClass() string  { return failure.class }
func (failure schedulerClassifiedError) FailureReason() string { return failure.reason }
func (failure schedulerClassifiedError) Retryable() bool       { return failure.retryable }

type schedulerSafeDetailError struct{}

func (schedulerSafeDetailError) Error() string {
	return "provider-secret-detail"
}

func (schedulerSafeDetailError) SafeDetail() string {
	return `content validation failed for "wiki/concepts/orphan.md" (catalog.reconciliation_required)`
}

func newTestScheduler(t *testing.T, store app.OperationStore, runner terminalRunner, options schedulerOptions) *operationScheduler {
	t.Helper()
	options.scanInterval = time.Hour
	options.workLeaseDuration = 9 * time.Second
	if options.now == nil {
		options.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	}
	if options.newToken == nil {
		options.newToken = func() (string, error) { return "test-token", nil }
	}
	scheduler, err := newOperationScheduler(store, runner, DefaultScope, options)
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	return scheduler
}

func stopScheduler(t *testing.T, scheduler *operationScheduler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("stop scheduler: %v", err)
	}
}

func TestSchedulerDrain(t *testing.T) {
	t.Parallel()

	c1 := schedulerClaim("op-1")
	c2 := schedulerClaim("op-2")
	c3 := schedulerClaim("op-3")
	store := &schedulerStore{claims: []domain.WorkClaim{c1, c2, c3}}
	executed := make(map[domain.OperationID]bool)
	scheduler := newTestScheduler(t, store, runnerFunc(func(_ context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		executed[claim.Operation.ID] = true
		if claim.Operation.ID == "op-3" {
			claim.Operation.Status = domain.StatusFailed
			claim.Operation.Failure = &domain.Failure{Class: testProviderFailureClass, OperationID: string(claim.Operation.ID)}
			return app.IngestResult{Operation: claim.Operation}, errors.New("non-retryable failure")
		}
		claim.Operation.Status = domain.StatusCommitted
		return app.IngestResult{Operation: claim.Operation}, nil
	}), schedulerOptions{claimBatch: 2})

	result, err := scheduler.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Total != 3 {
		t.Errorf("result.Total = %d, want 3", result.Total)
	}
	if result.Completed != 2 {
		t.Errorf("result.Completed = %d, want 2", result.Completed)
	}
	if result.Failed != 1 {
		t.Errorf("result.Failed = %d, want 1", result.Failed)
	}
	if len(executed) != 3 {
		t.Errorf("executed operations = %d, want 3", len(executed))
	}
	if store.claimCount() != 0 {
		t.Errorf("remaining claims = %d, want 0", store.claimCount())
	}
}

func TestSchedulerDrainContextCanceled(t *testing.T) {
	t.Parallel()

	c1 := schedulerClaim("op-1")
	c2 := schedulerClaim("op-2")
	store := &schedulerStore{claims: []domain.WorkClaim{c1, c2}}
	ctx, cancel := context.WithCancel(context.Background())
	scheduler := newTestScheduler(t, store, runnerFunc(func(_ context.Context, claim domain.WorkClaim) (app.IngestResult, error) {
		cancel()
		claim.Operation.Status = domain.StatusCommitted
		return app.IngestResult{Operation: claim.Operation}, nil
	}), schedulerOptions{})

	result, err := scheduler.Drain(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Drain() error = %v, want context.Canceled", err)
	}
	if result.Completed != 1 {
		t.Errorf("result.Completed = %d, want 1", result.Completed)
	}
}
