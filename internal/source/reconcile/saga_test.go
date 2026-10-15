package reconcile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"

	sqlitestore "github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
)

const (
	sagaDocPath    = "docs/doc.bin"
	sagaMirrorPath = "wiki/sources/engineering/docs/doc.bin"
)

// flakySourceContent injects bounded failures into staging and commit while
// counting every invocation around the real filesystem workspace.
type flakySourceContent struct {
	app.SourceContentStore
	stageFailures  int
	commitFailures int
	faultErr       error
	stageCalls     int
	commitCalls    int
}

func (f *flakySourceContent) StageSourcePlan(ctx context.Context, plan knowl.SourceMutationPlan) (knowl.StagedSourceMutation, error) {
	f.stageCalls++
	if f.stageFailures > 0 {
		f.stageFailures--
		return knowl.StagedSourceMutation{}, f.faultErr
	}
	return f.SourceContentStore.StageSourcePlan(ctx, plan)
}

func (f *flakySourceContent) CommitSource(ctx context.Context, staged knowl.StagedSourceMutation) (knowl.ContentCommit, error) {
	f.commitCalls++
	if f.commitFailures > 0 {
		f.commitFailures--
		return knowl.ContentCommit{}, f.faultErr
	}
	return f.SourceContentStore.CommitSource(ctx, staged)
}

// flakyFinalizeState fails finalization a bounded number of times.
type flakyFinalizeState struct {
	app.SourceStateStore
	finalizeFailures int
	finalizeCalls    int
}

type flakyReadyLifecycle struct {
	app.SourceLifecycleStore
	markFailures int
	markCalls    int
}

type cancelAfterFinalizeState struct {
	app.SourceStateStore
	cancel context.CancelFunc
}

func (s *cancelAfterFinalizeState) FinalizeSync(ctx context.Context, finalization app.SyncFinalization) (knowl.SyncRun, error) {
	run, err := s.SourceStateStore.FinalizeSync(ctx, finalization)
	if err == nil {
		s.cancel()
	}
	return run, err
}

func (f *flakyReadyLifecycle) MarkSourceLifecycleReady(ctx context.Context, update app.SourceLifecycleReadyUpdate) (knowl.SourceLifecycle, error) {
	f.markCalls++
	if f.markFailures > 0 {
		f.markFailures--
		return knowl.SourceLifecycle{}, errors.New("readiness fault")
	}
	return f.SourceLifecycleStore.MarkSourceLifecycleReady(ctx, update)
}

func (f *flakyFinalizeState) FinalizeSync(ctx context.Context, finalization app.SyncFinalization) (knowl.SyncRun, error) {
	f.finalizeCalls++
	if f.finalizeFailures > 0 {
		f.finalizeFailures--
		return knowl.SyncRun{}, errors.New("finalize fault")
	}
	return f.SourceStateStore.FinalizeSync(ctx, finalization)
}

// changedListing seeds one finalized document and queues an update listing.
func changedListing(t *testing.T, harness *stageHarness) (seededRef, updatedRef knowl.DocumentRef) {
	t.Helper()
	harness.seedFinalized(t, []seededDoc{{path: sagaDocPath, body: "before", legacyMirror: true}})
	updatedRef = harness.descriptor(sagaDocPath, "after")
	harness.adapter.script(updatedRef.ExternalID, "after")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{updatedRef}, ""))
	return knowl.DocumentRef{ExternalID: sagaDocPath, Revision: sha256Hex("before"), Path: sagaDocPath}, updatedRef
}

func TestSagaStageFailureStaysPreparedAndRecoveryConverges(t *testing.T) {
	harness := newStageHarness(t, nil)
	faults := &flakySourceContent{SourceContentStore: harness.workspace, stageFailures: 1, faultErr: errors.New(secretInjection)}
	harness.service.sourceContent = faults
	seededRef, _ := changedListing(t, harness)

	parked, err := harness.sync(t)
	if !strings.Contains(classOf(err), classStaging) || strings.Contains(err.Error(), secretInjection) {
		t.Fatalf("stage failure = %v, want redacted staging class", err)
	}
	if parked.Run.Status != knowl.SyncStatusPrepared || parked.Run.FailureClass != "" {
		t.Fatalf("parked run = %#v; must stay nonterminal without a failure field", parked.Run)
	}
	inventory, invErr := harness.service.sourceContent.SourceDigests(context.Background(), harness.scope, harness.sourceID, 16)
	if invErr != nil || len(inventory) != 1 || inventory[0].Digest != sha256Hex("before") {
		t.Fatalf("canonical mutated during failed stage = %#v, %v", inventory, invErr)
	}

	faults.stageFailures = 0
	recovered, recErr := harness.service.Recover(context.Background(), harness.scope, []knowl.Source{harness.source(knowl.SourceFlavorMarkdown)})
	if recErr != nil || len(recovered) != 1 || recovered[0].Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("recovery = %#v, %v", recovered, recErr)
	}
	if faults.stageCalls != 2 || faults.commitCalls != 1 {
		t.Fatalf("calls = %d/%d; want one retry and one commit", faults.stageCalls, faults.commitCalls)
	}
	assertCanonicalConverged(t, harness, "after")
	_ = seededRef
}

func TestSagaCommitFailureStaysPreparedAndReplaysReceipt(t *testing.T) {
	harness := newStageHarness(t, nil)
	faults := &flakySourceContent{SourceContentStore: harness.workspace, commitFailures: 1, faultErr: errors.New("commit fault")}
	harness.service.sourceContent = faults
	_, _ = changedListing(t, harness)

	parked, err := harness.sync(t)
	if !strings.Contains(classOf(err), classCommit) {
		t.Fatalf("commit failure = %v, want commit class", err)
	}
	if parked.Run.Status != knowl.SyncStatusPrepared {
		t.Fatalf("parked status = %q; want prepared", parked.Run.Status)
	}
	faults.commitFailures = 0
	recovered, recErr := harness.service.Recover(context.Background(), harness.scope, []knowl.Source{harness.source(knowl.SourceFlavorMarkdown)})
	if recErr != nil || recovered[0].Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("recovery = %#v, %v", recovered, recErr)
	}
	if faults.commitCalls != 2 {
		t.Fatalf("commit calls = %d, want exactly one replay", faults.commitCalls)
	}
	assertCanonicalConverged(t, harness, "after")
}

func TestSagaFinalizeFailureLeavesProjectedAndRecoveryFinalizes(t *testing.T) {
	harness := newStageHarness(t, nil)
	state := &flakyFinalizeState{SourceStateStore: harness.service.state, finalizeFailures: 1}
	harness.service.state = state
	_, _ = changedListing(t, harness)

	parked, err := harness.sync(t)
	if !strings.Contains(classOf(err), classState) {
		t.Fatalf("finalize failure = %v, want state class", err)
	}
	run, readErr := harness.state.SyncRun(context.Background(), harness.scope, parked.Run.ID)
	if readErr != nil || run.Status != knowl.SyncStatusProjected {
		t.Fatalf("parked run = %#v, %v; want projected nonterminal", run, readErr)
	}
	recovered, recErr := harness.service.Recover(context.Background(), harness.scope, []knowl.Source{harness.source(knowl.SourceFlavorMarkdown)})
	if recErr != nil || recovered[0].Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("recovery = %#v, %v", recovered, recErr)
	}
	if state.finalizeCalls != 2 {
		t.Fatalf("finalize calls = %d, want one retry", state.finalizeCalls)
	}
}

func TestSagaReadinessFailureAfterFinalizeRecoversWithoutNewSync(t *testing.T) {
	harness := newStageHarness(t, nil)
	source, lifecycle := prepareStageLifecycle(t, harness)
	faults := &flakyReadyLifecycle{SourceLifecycleStore: lifecycle, markFailures: 1}
	harness.service.sourceLifecycle = faults
	ref := harness.descriptor("docs/ready-after-restart.md", "# Ready after restart\n")
	harness.adapter.script(ref.ExternalID, "# Ready after restart\n")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))

	parked, err := harness.service.SyncSource(context.Background(), harness.scope, source)
	if !strings.Contains(classOf(err), classState) || parked.Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("post-finalize readiness failure = %#v, %v", parked, err)
	}
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: harness.sourceID}
	state, err := lifecycle.SourceLifecycle(context.Background(), harness.scope, owner)
	if err != nil || state.Ready {
		t.Fatalf("parked lifecycle = %#v, %v", state, err)
	}
	fetches := harness.adapter.fetches
	restarted, err := NewService(Dependencies{
		Adapters: harness.service.adapters, State: harness.state, Content: harness.workspace,
		SourceContent: harness.workspace, Search: harness.service.search, Maintenance: harness.queue,
		Lifecycle: lifecycle,
	}, harness.service.options)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.Recover(context.Background(), harness.scope, []knowl.Source{source})
	if err != nil || len(recovered) != 1 || recovered[0].Run.ID != parked.Run.ID || recovered[0].Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("readiness recovery = %#v, %v", recovered, err)
	}
	state, err = lifecycle.SourceLifecycle(context.Background(), harness.scope, owner)
	if err != nil || !state.Ready || harness.adapter.fetches != fetches {
		t.Fatalf("recovered lifecycle = %#v, %v; fetches=%d want %d", state, err, harness.adapter.fetches, fetches)
	}
	again, err := restarted.Recover(context.Background(), harness.scope, []knowl.Source{source})
	if err != nil || len(again) != 0 {
		t.Fatalf("idempotent readiness recovery = %#v, %v", again, err)
	}
}

func TestSagaRegressedClockFinalizesAndRecoversReadiness(t *testing.T) {
	harness := newStageHarness(t, nil)
	source, lifecycle := prepareStageLifecycle(t, harness)
	started := time.Unix(1001, 0).UTC()
	regressed := time.Unix(900, 0).UTC()
	clockCalls := 0
	harness.service.options.Clock = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return started
		}
		return regressed
	}
	faults := &flakyReadyLifecycle{SourceLifecycleStore: lifecycle, markFailures: 1}
	harness.service.sourceLifecycle = faults
	ref := harness.descriptor("docs/regressed-clock.md", "# Regressed clock\n")
	harness.adapter.script(ref.ExternalID, "# Regressed clock\n")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))

	parked, err := harness.service.SyncSource(context.Background(), harness.scope, source)
	if !strings.Contains(classOf(err), classState) || parked.Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("regressed-clock readiness fault = %#v, %v", parked, err)
	}
	stored, err := harness.state.SyncRun(context.Background(), harness.scope, parked.Run.ID)
	if err != nil || stored.UpdatedAt.Before(stored.StartedAt) || stored.CompletedAt.Before(stored.StartedAt) {
		t.Fatalf("regressed-clock stored run = %#v, %v", stored, err)
	}

	restarted, err := NewService(Dependencies{
		Adapters: harness.service.adapters, State: harness.state, Content: harness.workspace,
		SourceContent: harness.workspace, Search: harness.service.search, Maintenance: harness.queue,
		Lifecycle: lifecycle,
	}, harness.service.options)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.Recover(context.Background(), harness.scope, []knowl.Source{source})
	if err != nil || len(recovered) != 1 || recovered[0].Run.ID != parked.Run.ID {
		t.Fatalf("regressed-clock readiness recovery = %#v, %v", recovered, err)
	}
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: harness.sourceID}
	state, err := lifecycle.SourceLifecycle(context.Background(), harness.scope, owner)
	if err != nil || !state.Ready || state.UpdatedAt.Before(stored.CompletedAt) {
		t.Fatalf("regressed-clock lifecycle = %#v, %v; want ready at or after %v", state, err, stored.CompletedAt)
	}
}

func TestSagaReadinessAcknowledgmentIgnoresCallerCancellationAfterFinalize(t *testing.T) {
	harness := newStageHarness(t, nil)
	source, lifecycle := prepareStageLifecycle(t, harness)
	clockCalls := 0
	harness.service.options.Clock = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return time.Unix(1001, 0).UTC()
		}
		return time.Unix(900, 0).UTC()
	}
	ctx, cancel := context.WithCancel(context.Background())
	harness.service.state = &cancelAfterFinalizeState{SourceStateStore: harness.state, cancel: cancel}
	ref := harness.descriptor("docs/canceled-ready.md", "# Canceled ready\n")
	harness.adapter.script(ref.ExternalID, "# Canceled ready\n")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))

	result, err := harness.service.SyncSource(ctx, harness.scope, source)
	if err != nil || result.Run.Status != knowl.SyncStatusSucceeded || ctx.Err() == nil {
		t.Fatalf("canceled-after-finalize result = %#v, %v; context=%v", result, err, ctx.Err())
	}
	state, err := lifecycle.SourceLifecycle(context.Background(), harness.scope, knowl.SourceOwner{
		Adapter: knowl.SourceAdapterFilesystem, SourceID: harness.sourceID,
	})
	if err != nil || !state.Ready {
		t.Fatalf("durable canceled readiness = %#v, %v", state, err)
	}
}

func TestSagaStaleSucceededRunCannotReadyChangedConfig(t *testing.T) {
	harness := newStageHarness(t, nil)
	source, lifecycle := prepareStageLifecycle(t, harness)
	harness.service.sourceLifecycle = &flakyReadyLifecycle{SourceLifecycleStore: lifecycle, markFailures: 1}
	ref := harness.descriptor("docs/stale-succeeded.md", "# Stale succeeded\n")
	harness.adapter.script(ref.ExternalID, "# Stale succeeded\n")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	parked, err := harness.service.SyncSource(context.Background(), harness.scope, source)
	if err == nil || parked.Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("park stale succeeded run = %#v, %v", parked, err)
	}
	authority, err := lifecycle.SourceLifecycleAuthority(context.Background(), harness.scope)
	if err != nil {
		t.Fatal(err)
	}
	changed := source
	changed.ConfigDigest = strings.Repeat("c", 64)
	desired := []app.DesiredSourceOwner{{
		Owner:        knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: source.ID},
		ConfigDigest: changed.ConfigDigest,
	}}
	if err := lifecycle.ReconcileSourceLifecycles(context.Background(), app.SourceLifecycleReconcileRequest{
		Scope: harness.scope, Desired: desired, AuthorityGeneration: app.SourceLifecycleGeneration(desired),
		ExpectedAuthorityEpoch: authority.Epoch, UpdatedAt: harness.service.options.Clock(),
	}); err != nil {
		t.Fatal(err)
	}
	recovered, err := harness.service.Recover(context.Background(), harness.scope, []knowl.Source{changed})
	if err != nil || len(recovered) != 0 {
		t.Fatalf("stale succeeded recovery = %#v, %v", recovered, err)
	}
	state, err := lifecycle.SourceLifecycle(context.Background(), harness.scope, desired[0].Owner)
	if err != nil || state.Ready || state.ConfigDigest != changed.ConfigDigest {
		t.Fatalf("changed-config readiness = %#v, %v", state, err)
	}
}

func TestSagaSurvivesProcessRestartAtEveryPersistedStage(t *testing.T) {
	for _, tc := range []struct {
		name             string
		stageFailures    int
		searchFailures   int
		finalizeFailures int
		wantStatus       knowl.SyncStatus
	}{
		{name: "prepared", stageFailures: 1, wantStatus: knowl.SyncStatusPrepared},
		{name: "content_committed", searchFailures: 1, wantStatus: knowl.SyncStatusContentCommitted},
		{name: "projected", finalizeFailures: 1, wantStatus: knowl.SyncStatusProjected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			harness := newStageHarness(t, nil)
			storePath := filepath.Join(t.TempDir(), "restart.sqlite")
			store, err := sqlitestore.Open(ctx, storePath)
			if err != nil {
				t.Fatal(err)
			}
			harness.service.state = store
			harness.state = store
			harness.service.search = store
			switch {
			case tc.stageFailures > 0:
				harness.service.sourceContent = &flakySourceContent{SourceContentStore: harness.workspace, stageFailures: tc.stageFailures, faultErr: errors.New("stage fault")}
			case tc.searchFailures > 0:
				harness.service.search = &flakySearch{SearchIndex: store, failures: tc.searchFailures, failureErr: errors.New("projection fault")}
			case tc.finalizeFailures > 0:
				harness.service.state = &flakyFinalizeState{SourceStateStore: store, finalizeFailures: tc.finalizeFailures}
			}
			_, _ = changedListing(t, harness)
			parked, parkErr := harness.sync(t)
			if parkErr == nil {
				t.Fatalf("fault injection produced success: %#v", parked)
			}
			t.Logf("parked id=%q status=%q errClass=%q", parked.Run.ID, parked.Run.Status, classOf(parkErr))
			runRow, runErr := harness.state.SyncRun(ctx, harness.scope, parked.Run.ID)
			if runErr != nil || runRow.Status != tc.wantStatus {
				t.Fatalf("parked stage = %q/%v, want %q", runRow.Status, runErr, tc.wantStatus)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := sqlitestore.Open(ctx, storePath)
			if err != nil {
				t.Fatalf("reopen store: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			harness.service.state = reopened
			harness.state = reopened
			harness.service.search = reopened
			results, recErr := harness.service.Recover(ctx, harness.scope, []knowl.Source{harness.source(knowl.SourceFlavorMarkdown)})
			if recErr != nil || len(results) != 1 || results[0].Run.Status != knowl.SyncStatusSucceeded || results[0].Run.ID != parked.Run.ID {
				t.Fatalf("restart recovery = %#v, %v", results, recErr)
			}
			assertCanonicalConverged(t, harness, "after")
		})
	}
}

func TestNoOpGenerationIsDeterministicAndSeparated(t *testing.T) {
	inventory := map[string]string{"wiki/sources/engineering/a.md": sha256Hex("a")}
	first := noOpGeneration("run-1", sha256Hex("payload"), inventory)
	if len(first) != 64 {
		t.Fatalf("generation length = %d", len(first))
	}
	if again := noOpGeneration("run-1", sha256Hex("payload"), inventory); again != first {
		t.Fatal("identical inputs produced different no-op generations")
	}
	if other := noOpGeneration("run-2", sha256Hex("payload"), inventory); other == first {
		t.Fatal("different runs shared a no-op generation")
	}
	grown := map[string]string{"wiki/sources/engineering/a.md": sha256Hex("a"), "wiki/sources/engineering/b.md": sha256Hex("b")}
	if other := noOpGeneration("run-1", sha256Hex("payload"), grown); other == first {
		t.Fatal("changed inventory shared a no-op generation")
	}
}

func TestDurableTransitionsIgnoreCallerCancellation(t *testing.T) {
	harness := newStageHarness(t, nil)
	ctx := context.Background()
	now := harness.service.options.Clock()
	runID := harness.service.options.NewRunID()
	ref := harness.descriptor("docs/cancel.bin", "cancel")
	if _, _, err := harness.state.BeginSync(ctx, app.BeginSyncRequest{Run: knowl.SyncRun{
		ID: runID, Scope: harness.scope, SourceID: harness.sourceID,
		ConfigDigest: strings.Repeat("3", 64), Status: knowl.SyncStatusScanning,
		StartedAt: now, UpdatedAt: now,
	}, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	prepared := app.PreparedSyncState{
		RunID: runID, Scope: harness.scope, SourceID: harness.sourceID, CompleteScan: true,
		Checkpoint: scanCheckpoint([]knowl.DocumentRef{ref}), PreparedAt: now.Add(time.Second),
	}
	digest, digestErr := app.PreparedSyncDigest(prepared)
	if digestErr != nil {
		t.Fatal(digestErr)
	}
	prepared.CandidateDigest = digest
	if _, err := harness.state.PrepareSync(ctx, prepared); err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	transition := app.SyncGeneration{
		RunID: runID, Scope: harness.scope, SourceID: harness.sourceID,
		Generation: "cancel-generation", UpdatedAt: harness.service.options.Clock(),
	}
	if err := harness.service.markContentCommitted(canceled, transition); err != nil {
		t.Fatalf("canceled durable commit mark = %v", err)
	}
	if err := harness.service.markProjected(canceled, transition); err != nil {
		t.Fatalf("canceled projection mark = %v", err)
	}
	reread, rereadErr := harness.state.SyncRun(context.Background(), harness.scope, runID)
	if rereadErr != nil || reread.Status != knowl.SyncStatusProjected || reread.ContentGeneration != "cancel-generation" {
		t.Fatalf("canceled-durable run = %#v, %v", reread, rereadErr)
	}
}

func assertCanonicalConverged(t *testing.T, harness *stageHarness, wantBody string) {
	t.Helper()
	inventory, err := harness.service.sourceContent.SourceDigests(context.Background(), harness.scope, harness.sourceID, 16)
	if err != nil || len(inventory) != 0 {
		t.Fatalf("converged inventory = %#v, %v", inventory, err)
	}
	head, headErr := harness.state.DocumentState(context.Background(), harness.scope, harness.sourceID, sagaDocPath)
	if headErr != nil || head.Deleted || head.MirrorPath != "" || head.MirrorDigest != "" {
		t.Fatalf("converged head = %#v, %v", head, headErr)
	}
	if _, fileErr := os.Stat(filepath.Join(harness.workspace.Root(), filepath.FromSlash(sagaMirrorPath))); !errors.Is(fileErr, os.ErrNotExist) {
		t.Fatalf("legacy mirror still exists: %v", fileErr)
	}
	raw, rawErr := harness.service.content.ReadSource(context.Background(), head.AcceptedSource, knowl.ReadLimits{})
	if rawErr != nil || string(raw) != wantBody {
		t.Fatalf("retained raw = %q, %v", raw, rawErr)
	}
}
