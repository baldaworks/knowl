package reconcile

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testScope       = knowl.ScopeRef("reconcile_contract")
	otherScope      = knowl.ScopeRef("reconcile_other")
	secretInjection = "postgres://operator:password@db.example/knowl?token=bearer-secret"

	testSourceBadID = "Bad"
	testSourceMike  = "mike"
	testSourceZulu  = "zulu"

	testSourceFailing = "failing"
	testSourceAlpha   = "alpha"
)

func newTestService(t *testing.T, mutate func(*Dependencies, *Options)) *Service {
	t.Helper()
	dependencies := validDependencies()
	options := Options{
		Clock:    func() time.Time { return time.Unix(1, 0).UTC() },
		NewRunID: func() knowl.SyncRunID { return "run-test" },
	}
	if mutate != nil {
		mutate(&dependencies, &options)
	}
	service, err := NewService(dependencies, options)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func validDependencies() Dependencies {
	return Dependencies{
		Adapters:      map[knowl.SourceType]app.SourceAdapter{knowl.SourceTypeFilesystem: stubAdapter{}},
		Normalizer:    stubNormalizer{},
		State:         &stubState{},
		Content:       &stubContent{},
		SourceContent: &stubSourceContent{},
		Search:        &stubSearch{},
	}
}

func testSource(id knowl.SourceID) knowl.Source {
	return knowl.Source{
		ID: id, Type: knowl.SourceTypeFilesystem, Enabled: true,
		Config: knowl.SourceConfig{Filesystem: &knowl.FilesystemSourceConfig{Root: "/sources/" + string(id)}},
	}
}

func TestNewServiceValidatesDependenciesAndOptions(t *testing.T) {
	for name, dependencies := range map[string]Dependencies{
		"nil adapters":      {Adapters: nil},
		"empty adapters":    {Adapters: map[knowl.SourceType]app.SourceAdapter{}},
		"nil adapter value": {Adapters: map[knowl.SourceType]app.SourceAdapter{knowl.SourceTypeFilesystem: nil}},
		"missing filesystem": {
			Adapters: map[knowl.SourceType]app.SourceAdapter{"git": stubAdapter{}}, Normalizer: stubNormalizer{},
			State: &stubState{}, Content: &stubContent{}, SourceContent: &stubSourceContent{}, Search: &stubSearch{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewService(dependencies, Options{}); !errors.Is(err, app.ErrSourceInvalid) {
				t.Fatalf("NewService() error = %v, want invalid", err)
			}
		})
	}
	for name, mutate := range map[string]func(*Dependencies){
		"nil normalizer":     func(d *Dependencies) { d.Normalizer = nil },
		"nil state store":    func(d *Dependencies) { d.State = nil },
		"nil content":        func(d *Dependencies) { d.Content = nil },
		"nil source content": func(d *Dependencies) { d.SourceContent = nil },
		"nil search":         func(d *Dependencies) { d.Search = nil },
	} {
		t.Run(name, func(t *testing.T) {
			dependencies := validDependencies()
			mutate(&dependencies)
			if _, err := NewService(dependencies, Options{}); !errors.Is(err, app.ErrSourceInvalid) {
				t.Fatalf("NewService() error = %v, want invalid", err)
			}
		})
	}
	for _, bound := range []struct {
		name   string
		mutate func(*Options)
	}{
		{"negative sources", func(o *Options) { o.MaxSyncAllSources = -1 }},
		{"over ceiling sources", func(o *Options) { o.MaxSyncAllSources = 1001 }},
		{"over ceiling pages", func(o *Options) { o.MaxScanPages = 1001 }},
		{"over ceiling documents", func(o *Options) { o.MaxScanDocuments = 1001 }},
		{"over ceiling mutations", func(o *Options) { o.MaxMutations = 2049 }},
		{"over ceiling raw bytes", func(o *Options) { o.MaxRawBytes = 64<<20 + 1 }},
		{"over ceiling recovery", func(o *Options) { o.MaxRecoveryRuns = 1001 }},
	} {
		t.Run(bound.name, func(t *testing.T) {
			options := Options{}
			bound.mutate(&options)
			if _, err := NewService(validDependencies(), options); !errors.Is(err, app.ErrSourceInvalid) {
				t.Fatalf("NewService() error = %v, want invalid", err)
			}
		})
	}
	service := newTestService(t, nil)
	if service.options.MaxSyncAllSources != maxSyncAllSources || service.options.MaxScanPages != maxScanPages ||
		service.options.MaxScanDocuments != maxScanDocuments || service.options.MaxMutations != maxPlanMutations ||
		service.options.MaxRawBytes != maxRawBytes || service.options.MaxRecoveryRuns != maxRecoveryRuns {
		t.Fatalf("defaults not applied: %#v", service.options)
	}
}

func TestSyncSourceValidationOrdering(t *testing.T) {
	var calls int
	service := newTestService(t, nil)
	service.stageEngine = func(context.Context, knowl.ScopeRef, app.SourceAdapter, knowl.Source) (Result, error) {
		calls++
		return Result{}, nil
	}
	disabled := testSource(testEngineeringSourceID)
	disabled.Enabled = false
	for _, test := range []struct {
		name   string
		scope  knowl.ScopeRef
		source knowl.Source
	}{
		{name: "blank scope", scope: " ", source: testSource(testEngineeringSourceID)},
		{name: "invalid source", scope: testScope, source: knowl.Source{ID: testSourceBadID, Type: knowl.SourceTypeFilesystem, Enabled: true}},
		{name: "disabled source", scope: testScope, source: disabled},
		{name: "unknown type", scope: testScope, source: knowl.Source{
			ID: testEngineeringSourceID, Type: "git", Enabled: true,
			Config: knowl.SourceConfig{Filesystem: &knowl.FilesystemSourceConfig{Root: "/s"}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := service.SyncSource(context.Background(), test.scope, test.source)
			if !errors.Is(err, app.ErrSourceInvalid) {
				t.Fatalf("SyncSource() error = %v, want invalid", err)
			}
			if result.FailureClass != "invalid" || result.SourceID != test.source.ID {
				t.Fatalf("result = %#v", result)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("engine invoked %d times during validation failures", calls)
	}
	if _, err := service.SyncSource(context.Background(), testScope, testSource(testEngineeringSourceID)); err != nil {
		t.Fatalf("composed engine error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("engine calls = %d, want 1", calls)
	}
}

func TestLeasesAreExactScopedNonblockingAndAlwaysReleased(t *testing.T) {
	ctx := context.Background()
	releaseBlocking := make(chan struct{})
	engineEntered := make(chan struct{})
	var enteredOnce sync.Once
	service := newTestService(t, nil)
	service.stageEngine = func(engineCtx context.Context, _ knowl.ScopeRef, _ app.SourceAdapter, source knowl.Source) (Result, error) {
		switch source.ID {
		case "blocking":
			enteredOnce.Do(func() { close(engineEntered) })
			<-releaseBlocking
			return Result{Changed: true}, nil
		case "panic":
			panic("engine explosion")
		case "canceled":
			<-engineCtx.Done()
			return Result{}, engineCtx.Err()
		default:
			return Result{Changed: true}, nil
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := service.SyncSource(ctx, testScope, testSource("blocking"))
		if err != nil || !result.Changed {
			t.Errorf("blocking sync = %#v, %v", result, err)
		}
	}()
	<-engineEntered
	if result, err := service.SyncSource(ctx, testScope, testSource("blocking")); !errors.Is(err, ErrSyncInProgress) || result.FailureClass != "in_progress" {
		t.Fatalf("overlap result = %#v, %v; want in progress", result, err)
	}
	if _, err := service.SyncSource(ctx, otherScope, testSource("scope-probe")); err != nil {
		t.Fatalf("different scope blocked by foreign lease: %v", err)
	}
	if _, err := service.SyncSource(ctx, testScope, testSource("independent")); err != nil {
		t.Fatalf("independent source blocked by foreign lease: %v", err)
	}
	close(releaseBlocking)
	<-done
	if _, err := service.SyncSource(ctx, testScope, testSource("blocking")); err != nil {
		t.Fatalf("lease not released after success: %v", err)
	}

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("panicking engine did not panic")
			}
		}()
		_, _ = service.SyncSource(ctx, testScope, testSource("panic"))
	}()
	if held := service.leaseHeld(testScope, "panic"); held {
		t.Fatal("lease not released after panic")
	}

	canceled, cancel := context.WithCancel(ctx)
	blockedDone := make(chan error, 1)
	go func() {
		_, err := service.SyncSource(canceled, testScope, testSource("canceled"))
		blockedDone <- err
	}()
	service.waitLeaseHeld(testScope, "canceled", t)
	cancel()
	if err := <-blockedDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled engine error = %v", err)
	}
	if held := service.leaseHeld(testScope, "canceled"); held {
		t.Fatal("lease not released after cancellation")
	}
}

func (service *Service) leaseHeld(scope knowl.ScopeRef, id knowl.SourceID) bool {
	service.leaseMu.Lock()
	defer service.leaseMu.Unlock()
	_, held := service.leases[leaseKey{scope: scope, source: id}]
	return held
}

func (service *Service) waitLeaseHeld(scope knowl.ScopeRef, id knowl.SourceID, t *testing.T) {
	t.Helper()
	for attempt := 0; attempt < 2000; attempt++ {
		if service.leaseHeld(scope, id) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("lease was never acquired")
}

func TestSyncAllAggregatesDeterministicallyWithoutSecrets(t *testing.T) {
	service := newTestService(t, nil)
	service.stageEngine = func(_ context.Context, _ knowl.ScopeRef, _ app.SourceAdapter, source knowl.Source) (Result, error) {
		if source.ID == "failing" {
			return Result{}, errors.New(secretInjection)
		}
		return Result{Run: knowl.SyncRun{ID: knowl.SyncRunID("run-" + string(source.ID)), SourceID: source.ID}, Changed: true}, nil
	}
	disabled := testSource("disabled")
	disabled.Enabled = false
	sources := []knowl.Source{
		testSource(testSourceZulu), testSource(testSourceAlpha), testSource(testSourceMike),
		testSource(testSourceFailing), disabled, testSource(testSourceAlpha),
	}
	all, err := service.SyncAll(context.Background(), testScope, sources)
	if !errors.Is(err, ErrSyncPartial) || strings.Contains(err.Error(), secretInjection) {
		t.Fatalf("SyncAll() error = %v, want redacted partial", err)
	}
	wantOrder := []knowl.SourceID{testSourceAlpha, testSourceFailing, testSourceMike, testSourceZulu}
	if len(all.Results) != len(wantOrder) {
		t.Fatalf("results = %#v", all.Results)
	}
	for index, result := range all.Results {
		if result.SourceID != wantOrder[index] {
			t.Fatalf("result order = %#v", all.Results)
		}
	}
	for _, result := range all.Results {
		if result.SourceID == testSourceFailing {
			if result.FailureClass != classInternal || strings.Contains(result.FailureClass, secretInjection) {
				t.Fatalf("failure class = %q", result.FailureClass)
			}
			continue
		}
		if result.FailureClass != "" || !result.Changed {
			t.Fatalf("successful result = %#v", result)
		}
	}
	repeat, repeatErr := service.SyncAll(context.Background(), testScope, sources)
	if !errors.Is(repeatErr, ErrSyncPartial) || len(repeat.Results) != len(all.Results) {
		t.Fatalf("repeat aggregate = %#v, %v", repeat, repeatErr)
	}
	empty, emptyErr := service.SyncAll(context.Background(), testScope, nil)
	if emptyErr != nil || len(empty.Results) != 0 {
		t.Fatalf("empty aggregate = %#v, %v", empty, emptyErr)
	}
	if _, err := service.SyncAll(context.Background(), " ", sources); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("blank scope error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.SyncAll(canceled, testScope, sources); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled aggregate error = %v", err)
	}
}

func TestRecoverRestoresReadinessFirstAndConvergesPersistedStages(t *testing.T) {
	ctx := context.Background()
	harness := newStageHarness(t, nil)

	var mutex sync.Mutex
	var order []string
	content := &recordingContent{ContentStore: harness.service.content}
	state := &recordingState{SourceStateStore: harness.service.state}
	content.onRecover = func() { mutex.Lock(); order = append(order, "recover"); mutex.Unlock() }
	state.onResumable = func() { mutex.Lock(); order = append(order, "resumable"); mutex.Unlock() }
	search := &flakySearch{SearchIndex: harness.service.search}
	harness.service.content = content
	harness.service.state = state
	harness.service.search = search

	body := "\x00seed-body"
	harness.seedFinalized(t, []seededDoc{{path: "docs/base.bin", body: body}})
	changed := harness.descriptor("docs/next.bin", "next-body")
	harness.adapter.script(changed.ExternalID, "next-body")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{changed}, ""))
	search.failures = 1
	search.failureErr = errors.New(secretInjection)
	parked, parkErr := harness.sync(t)
	if parkErr == nil || parked.Run.Status != knowl.SyncStatusContentCommitted {
		t.Fatalf("parked sync = %#v, %v; want nonterminal content_committed", parked, parkErr)
	}
	if got := classOf(parkErr); got != classProjection || strings.Contains(parkErr.Error(), secretInjection) {
		t.Fatalf("parked error = %v, want redacted %q class", parkErr, classProjection)
	}

	driftSource := harness.source(knowl.SourceFlavorMarkdown)
	driftSource.ID = "drift"
	now := time.Unix(5000, 0).UTC()
	if _, _, err := harness.state.BeginSync(ctx, app.BeginSyncRequest{Run: knowl.SyncRun{
		ID: "run-drift", Scope: harness.scope, SourceID: driftSource.ID,
		ConfigDigest: strings.Repeat("9", 64), Status: knowl.SyncStatusScanning,
		StartedAt: now, UpdatedAt: now,
	}, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatalf("seed drift run: %v", err)
	}

	results, recErr := harness.service.Recover(ctx, harness.scope, []knowl.Source{driftSource, harness.source(knowl.SourceFlavorMarkdown)})
	if !errors.Is(recErr, ErrSyncPartial) {
		t.Fatalf("recover error = %v, want partial", recErr)
	}
	mutex.Lock()
	copied := append([]string(nil), order...)
	mutex.Unlock()
	recoverIndex := -1
	for index, step := range copied {
		if step == "recover" {
			recoverIndex = index
			break
		}
	}
	if recoverIndex < 0 || recoverIndex+1 >= len(copied) || copied[recoverIndex+1] != "resumable" {
		t.Fatalf("readiness order = %v; want resumable inspection immediately after recovery", copied)
	}
	if len(results) != 2 || results[0].SourceID != "drift" || results[1].SourceID != harness.sourceID {
		t.Fatalf("results order = %#v", results)
	}
	if results[0].FailureClass != classScan {
		t.Fatalf("drift result = %#v, want scan failure class", results[0])
	}
	if results[1].FailureClass != "" || results[1].Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("engineering result = %#v, want convergence", results[1])
	}
	head, headErr := harness.state.DocumentState(ctx, harness.scope, harness.sourceID, changed.ExternalID)
	if headErr != nil || head.Deleted || head.Revision != sha256Hex("next-body") {
		t.Fatalf("converged head = %#v, %v", head, headErr)
	}
	if search.calls != 2 || search.last == nil || search.last.Snapshot.Scope != harness.scope {
		t.Fatalf("projection calls = %d last = %#v", search.calls, search.last)
	}

	clean, cleanErr := harness.service.Recover(ctx, harness.scope, []knowl.Source{harness.source(knowl.SourceFlavorMarkdown)})
	if cleanErr != nil || len(clean) != 0 {
		t.Fatalf("clean recover = %#v, %v; want nothing pending", clean, cleanErr)
	}
	beforeCalls := content.recoverCalls
	if _, err := harness.service.Recover(ctx, harness.scope, []knowl.Source{{ID: testSourceBadID}}); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("invalid source error = %v", err)
	}
	if content.recoverCalls != beforeCalls {
		t.Fatal("workspace readiness ran despite invalid input")
	}
	content.injectedErr = errors.New(secretInjection)
	_, redacted := harness.service.Recover(ctx, harness.scope, []knowl.Source{harness.source(knowl.SourceFlavorMarkdown)})
	if !errors.Is(redacted, ErrRecoveryFailed) || strings.Contains(redacted.Error(), secretInjection) {
		t.Fatalf("redacted recovery failure = %v", redacted)
	}
}

func TestDefaultRunIDIsBoundedHexadecimal(t *testing.T) {
	generated := defaultRunID()
	if len(generated) != 32 {
		t.Fatalf("default run ID length = %d", len(generated))
	}
	for _, character := range generated {
		if !strings.ContainsRune("0123456789abcdef", character) {
			t.Fatalf("default run ID %q is not hexadecimal", generated)
		}
	}
}

type stubAdapter struct{}

func (stubAdapter) List(context.Context, knowl.Source, string) (knowl.DocumentPage, error) {
	return knowl.DocumentPage{}, nil
}

func (stubAdapter) Fetch(context.Context, knowl.Source, knowl.DocumentRef) (knowl.Document, error) {
	return knowl.Document{}, nil
}

type stubNormalizer struct{}

func (stubNormalizer) NormalizeSource(context.Context, app.SourceNormalizationInput) (app.SourceNormalizationResult, error) {
	return app.SourceNormalizationResult{}, nil
}

type stubState struct {
	app.SourceStateStore
	resumableFunc func(int) ([]knowl.SyncRun, error)
}

func (stub *stubState) ResumableSyncRuns(ctx context.Context, scope knowl.ScopeRef, limit int) ([]knowl.SyncRun, error) {
	if stub.resumableFunc != nil {
		return stub.resumableFunc(limit)
	}
	return nil, nil
}

type stubContent struct {
	app.ContentStore
	recoverFunc  func()
	injectedErr  error
	recoverCalls int
}

func (stub *stubContent) Recover(context.Context) ([]knowl.RecoveryResult, error) {
	stub.recoverCalls++
	if stub.recoverFunc != nil {
		if err := stub.injectedErr; err != nil {
			return nil, err
		}
		stub.recoverFunc()
	}
	return nil, nil
}

type stubSourceContent struct {
	app.SourceContentStore
}

type stubSearch struct {
	app.SearchIndex
}

// recordingContent records workspace readiness invocations around the real store.
type recordingContent struct {
	app.ContentStore
	onRecover    func()
	recoverCalls int
	injectedErr  error
}

func (r *recordingContent) Recover(ctx context.Context) ([]knowl.RecoveryResult, error) {
	r.recoverCalls++
	if r.onRecover != nil {
		r.onRecover()
	}
	if r.injectedErr != nil {
		return nil, r.injectedErr
	}
	return r.ContentStore.Recover(ctx)
}

// recordingState records resumable-run inspections around the real store.
type recordingState struct {
	app.SourceStateStore
	onResumable func()
}

func (r *recordingState) ResumableSyncRuns(ctx context.Context, scope knowl.ScopeRef, limit int) ([]knowl.SyncRun, error) {
	if r.onResumable != nil {
		r.onResumable()
	}
	return r.SourceStateStore.ResumableSyncRuns(ctx, scope, limit)
}

// flakySearch fails projection a bounded number of times before delegating.
type flakySearch struct {
	app.SearchIndex
	failures   int
	failureErr error
	calls      int
	last       *knowl.ContentCommit
}

func (f *flakySearch) Project(ctx context.Context, commit knowl.ContentCommit) error {
	f.calls++
	f.last = &commit
	if f.failures > 0 {
		f.failures--
		return f.failureErr
	}
	return f.SearchIndex.Project(ctx, commit)
}

func TestSyncAllContinuesAfterEveryFailurePosition(t *testing.T) {
	for _, failing := range []string{"alpha", testSourceMike, testSourceZulu} {
		t.Run(failing, func(t *testing.T) {
			service := newTestService(t, nil)
			service.stageEngine = func(_ context.Context, _ knowl.ScopeRef, _ app.SourceAdapter, source knowl.Source) (Result, error) {
				if string(source.ID) == failing {
					return Result{}, errors.New(secretInjection)
				}
				return Result{Changed: true}, nil
			}
			all, err := service.SyncAll(context.Background(), testScope, []knowl.Source{
				testSource(testSourceZulu), testSource("alpha"), testSource(testSourceMike),
			})
			if !errors.Is(err, ErrSyncPartial) || strings.Contains(err.Error(), secretInjection) {
				t.Fatalf("aggregate error = %v", err)
			}
			if len(all.Results) != 3 {
				t.Fatalf("results = %#v; later sources must still run", all.Results)
			}
			for index, result := range all.Results {
				want := []knowl.SourceID{"alpha", testSourceMike, testSourceZulu}[index]
				if result.SourceID != want {
					t.Fatalf("order = %#v", all.Results)
				}
				if string(result.SourceID) == failing && result.FailureClass != classInternal {
					t.Fatalf("failing class = %q", result.FailureClass)
				}
				if string(result.SourceID) != failing && (result.FailureClass != "" || !result.Changed) {
					t.Fatalf("healthy result = %#v", result)
				}
			}
		})
	}
}

func TestSyncAllRejectsBadInputBeforeAnyWork(t *testing.T) {
	var calls int
	service := newTestService(t, nil)
	service.stageEngine = func(context.Context, knowl.ScopeRef, app.SourceAdapter, knowl.Source) (Result, error) {
		calls++
		return Result{}, nil
	}
	conflictA := testSource("dup")
	conflictA.Config.Filesystem.Root = "/root-a"
	conflictB := testSource("dup")
	conflictB.Config.Filesystem.Root = "/root-b"
	for _, sources := range [][]knowl.Source{
		{testSource("ok"), conflictA, conflictB},
		{{ID: testSourceBadID, Type: knowl.SourceTypeFilesystem, Enabled: true}},
	} {
		if _, err := service.SyncAll(context.Background(), testScope, sources); !errors.Is(err, app.ErrSourceInvalid) {
			t.Fatalf("input validation error = %v, want invalid", err)
		}
	}
	if calls != 0 {
		t.Fatalf("engine ran %d times during invalid input", calls)
	}
	identical := testSource("dup")
	if _, directErr := service.SyncSource(context.Background(), testScope, identical); directErr != nil {
		t.Fatalf("direct sync after validation = %v", directErr)
	}
	deduped, dedupedErr := service.SyncAll(context.Background(), testScope, []knowl.Source{identical, identical})
	if dedupedErr != nil || len(deduped.Results) != 1 {
		t.Fatalf("identical duplicates = %#v, %v; want one attempt", deduped, dedupedErr)
	}
	if calls != 2 { // direct source + single aggregate attempt
		t.Fatalf("engine calls = %d, want 2", calls)
	}
}

func TestInjectedSecretsNeverReachClassesOrMessages(t *testing.T) {
	harness := newStageHarness(t, nil)
	ref := harness.descriptor("docs/secret.md", "# Secret\n")
	harness.adapter.failFetch[ref.ExternalID] = errors.New(secretInjection)
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	result, err := harness.sync(t)
	class := classOf(err)
	vocabulary := map[string]bool{
		classAdapter: true, classScan: true, classFetch: true, classRaw: true,
		classNormalize: true, classStaging: true, classCommit: true,
		classProjection: true, classState: true, classCanceled: true,
		classInvalid: true, classInProgress: true, classInternal: true,
	}
	if !vocabulary[class] || strings.Contains(err.Error(), secretInjection) {
		t.Fatalf("leaked failure = %q / %q", result.FailureClass, err.Error())
	}
}
