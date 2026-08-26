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
		claim.Operation.Failure = &domain.Failure{Class: "provider", OperationID: string(claim.Operation.ID)}
		return app.IngestResult{Operation: claim.Operation}, errors.New("provider-secret-detail")
	}), schedulerOptions{claimBatch: 1})
	scheduler.cycle(context.Background())

	encoded := output.String()
	for _, required := range []string{"engineering", "architecture.md", "revision-1", string(claim.Operation.ID), "provider"} {
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
	claimCalls         int
	inspectionCalls    []int
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

func newTestScheduler(t *testing.T, store app.OperationStore, runner terminalRunner, options schedulerOptions) *operationScheduler {
	t.Helper()
	options.scanInterval = time.Hour
	options.workLeaseDuration = 9 * time.Second
	options.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	options.newToken = func() (string, error) { return "test-token", nil }
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
