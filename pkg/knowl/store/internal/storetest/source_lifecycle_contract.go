package storetest

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
	lifecycleCursorA          = "wiki/a.md"
	lifecycleCursorZ          = "wiki/z.md"
	lifecycleHistoricalSource = "historical"
)

// SourceLifecycleHarness binds the shared exact-owner lifecycle contract to one backend.
type SourceLifecycleHarness struct {
	Store      app.SourceLifecycleStore
	Legacy     app.SourceStateStore
	OpenPeer   func(t *testing.T) app.SourceLifecycleStore
	IsConflict func(error) bool
	Scope      knowl.ScopeRef
}

// RunSourceLifecycleContract verifies the backend-neutral exact-owner state machine.
func RunSourceLifecycleContract(t *testing.T, harness SourceLifecycleHarness) {
	t.Helper()
	ctx := context.Background()
	base := time.Unix(1_800_000_000, 0).UTC()
	const sourceID = knowl.SourceID("fastronome-wiki")
	filesystem := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: sourceID}
	git := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: sourceID}
	configA, configB := strings.Repeat("a", 64), strings.Repeat("b", 64)

	legacyRun := knowl.SyncRun{
		ID: "lifecycle-bootstrap", Scope: harness.Scope, SourceID: sourceID,
		ConfigDigest: configA, Status: knowl.SyncStatusScanning, StartedAt: base, UpdatedAt: base,
	}
	if _, _, err := harness.Legacy.BeginSync(ctx, app.BeginSyncRequest{Run: legacyRun, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatalf("create historical source: %v", err)
	}
	if _, err := harness.Store.SourceLifecycle(ctx, harness.Scope, filesystem); !errors.Is(err, app.ErrSourceLifecycleUnowned) {
		t.Fatalf("pre-bootstrap lookup error = %v, want unowned", err)
	}

	authorityA := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: harness.Scope, UpdatedAt: base.Add(time.Second),
		Desired: []app.DesiredSourceOwner{{Owner: filesystem, ConfigDigest: configA}},
	})
	generationA := authorityA.Generation
	active := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem)
	if active.State != knowl.SourceLifecycleActive || active.ConfigDigest != configA ||
		active.AuthorityGeneration != generationA || active.AuthorityEpoch != authorityA.Epoch ||
		active.ActivatedEpoch != authorityA.Epoch || active.Ready {
		t.Fatalf("bootstrapped lifecycle = %#v", active)
	}
	firstActivationEpoch := active.ActivatedEpoch

	if _, err := harness.Store.MarkSourceLifecycleReady(ctx, app.SourceLifecycleReadyUpdate{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationA,
		AuthorityEpoch: authorityA.Epoch, ActivatedEpoch: active.ActivatedEpoch,
		ConfigDigest: configA, ReadyAt: base.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("mark bootstrapped owner ready: %v", err)
	}
	lateRun := knowl.SyncRun{
		ID: "lifecycle-late-bootstrap", Scope: harness.Scope, SourceID: "late-source",
		ConfigDigest: configA, Status: knowl.SyncStatusScanning, StartedAt: base.Add(2 * time.Second), UpdatedAt: base.Add(2 * time.Second),
	}
	if _, _, err := harness.Legacy.BeginSync(ctx, app.BeginSyncRequest{Run: lateRun, Type: knowl.SourceTypeGit, RepositoryIdentity: strings.Repeat("9", 64)}); err != nil {
		t.Fatalf("create late historical source: %v", err)
	}
	replayedA := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: harness.Scope, UpdatedAt: base.Add(2 * time.Second),
		Desired: []app.DesiredSourceOwner{{Owner: filesystem, ConfigDigest: configA}},
	})
	if replayedA != authorityA {
		t.Fatalf("repeated initial authority = %#v, want %#v", replayedA, authorityA)
	}
	lateOwner := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: lateRun.SourceID}
	if _, err := harness.Store.SourceLifecycle(ctx, harness.Scope, lateOwner); !errors.Is(err, app.ErrSourceLifecycleUnowned) {
		t.Fatalf("late legacy owner error = %v, want unowned", err)
	}

	authorityB := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: harness.Scope, ExpectedAuthorityEpoch: authorityA.Epoch, UpdatedAt: base.Add(3 * time.Second),
		Desired: []app.DesiredSourceOwner{{Owner: git, ConfigDigest: configA}},
	})
	generationB := authorityB.Generation
	retiring := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem)
	activeGit := requireLifecycle(t, ctx, harness.Store, harness.Scope, git)
	firstGitActivatedAt := activeGit.ActivatedAt
	firstGitActivatedGeneration := activeGit.ActivatedGeneration
	firstGitActivatedEpoch := activeGit.ActivatedEpoch
	if retiring.State != knowl.SourceLifecycleRetiring || retiring.RetirementGeneration != generationB ||
		retiring.RetirementEpoch != authorityB.Epoch || retiring.ReplacementPending != 1 || retiring.Ready ||
		activeGit.State != knowl.SourceLifecycleActive || activeGit.RetirementGeneration != "" {
		t.Fatalf("independent replacement = filesystem %#v, Git %#v", retiring, activeGit)
	}
	replayedB := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: harness.Scope, ExpectedAuthorityEpoch: authorityA.Epoch, UpdatedAt: base.Add(3 * time.Second),
		Desired: []app.DesiredSourceOwner{{Owner: git, ConfigDigest: configA}},
	})
	if replayedB != authorityB {
		t.Fatalf("repeated inventory authority = %#v, want %#v", replayedB, authorityB)
	}
	staleA := app.SourceLifecycleReconcileRequest{
		Scope: harness.Scope, ExpectedAuthorityEpoch: authorityA.Epoch, UpdatedAt: base.Add(4 * time.Second),
		Desired: []app.DesiredSourceOwner{{Owner: filesystem, ConfigDigest: configA}},
	}
	staleA.AuthorityGeneration = app.SourceLifecycleGeneration(staleA.Desired)
	if err := harness.Store.ReconcileSourceLifecycles(ctx, staleA); !isLifecycleConflict(harness, err) {
		t.Fatalf("stale inventory replay error = %v, want conflict", err)
	}

	readyUpdate := app.SourceLifecycleReadyUpdate{
		Scope: harness.Scope, Owner: git, AuthorityGeneration: generationB,
		AuthorityEpoch: authorityB.Epoch, ActivatedEpoch: activeGit.ActivatedEpoch,
		ConfigDigest: configA, ReadyAt: base.Add(5 * time.Second),
	}
	if _, err := harness.Store.MarkSourceLifecycleReady(ctx, readyUpdate); err != nil {
		t.Fatalf("mark Git ready: %v", err)
	}
	if readyRetirement := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem); readyRetirement.ReplacementPending != 0 {
		t.Fatalf("ready replacement pending = %d, want zero", readyRetirement.ReplacementPending)
	}
	if _, err := harness.Store.MarkSourceLifecycleReady(ctx, readyUpdate); err != nil {
		t.Fatalf("replay Git readiness: %v", err)
	}
	if replayedRetirement := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem); replayedRetirement.ReplacementPending != 0 {
		t.Fatalf("replayed replacement pending = %d, want zero", replayedRetirement.ReplacementPending)
	}
	authorityC := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: harness.Scope, ExpectedAuthorityEpoch: authorityB.Epoch, UpdatedAt: base.Add(6 * time.Second),
		Desired: []app.DesiredSourceOwner{{Owner: git, ConfigDigest: configB}},
	})
	generationC := authorityC.Generation
	activeGit = requireLifecycle(t, ctx, harness.Store, harness.Scope, git)
	if activeGit.Ready || activeGit.ConfigDigest != configB || activeGit.ActivatedGeneration != firstGitActivatedGeneration ||
		activeGit.ActivatedEpoch != firstGitActivatedEpoch || !activeGit.ActivatedAt.Equal(firstGitActivatedAt) {
		t.Fatalf("changed-config lifecycle = %#v", activeGit)
	}
	if staleRetirement := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem); staleRetirement.ReplacementPending != 1 {
		t.Fatalf("stale replacement pending = %d, want one", staleRetirement.ReplacementPending)
	}
	if _, err := harness.Store.MarkSourceLifecycleReady(ctx, app.SourceLifecycleReadyUpdate{
		Scope: harness.Scope, Owner: git, AuthorityGeneration: generationB,
		AuthorityEpoch: authorityB.Epoch, ActivatedEpoch: authorityB.Epoch,
		ConfigDigest: configA, ReadyAt: base.Add(7 * time.Second),
	}); !isLifecycleConflict(harness, err) {
		t.Fatalf("stale readiness error = %v, want conflict", err)
	}
	if _, err := harness.Store.MarkSourceLifecycleReady(ctx, app.SourceLifecycleReadyUpdate{
		Scope: harness.Scope, Owner: git, AuthorityGeneration: generationC,
		AuthorityEpoch: authorityC.Epoch, ActivatedEpoch: activeGit.ActivatedEpoch,
		ConfigDigest: configB, ReadyAt: base.Add(7 * time.Second),
	}); err != nil {
		t.Fatalf("mark changed Git config ready: %v", err)
	}
	if readyRetirement := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem); readyRetirement.ReplacementPending != 0 {
		t.Fatalf("changed replacement pending = %d, want zero", readyRetirement.ReplacementPending)
	}
	retiringBeforePoison := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem)
	if _, err := harness.Store.UpdateSourceRetirement(ctx, app.SourceRetirementUpdate{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationC,
		AuthorityEpoch: authorityC.Epoch, RetirementGeneration: generationB, RetirementEpoch: retiring.RetirementEpoch,
		ExpectedCursor: "", Cursor: lifecycleCursorA, RoundMatches: 1, UpdatedAt: base.Add(5 * time.Second),
	}); !isLifecycleConflict(harness, err) {
		t.Fatalf("regressed scan timestamp error = %v, want conflict", err)
	}
	if afterPoison := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem); afterPoison != retiringBeforePoison {
		t.Fatalf("regressed scan poisoned row = %#v, want %#v", afterPoison, retiringBeforePoison)
	}

	if _, err := harness.Store.UpdateSourceRetirement(ctx, app.SourceRetirementUpdate{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationC,
		AuthorityEpoch: authorityC.Epoch, RetirementEpoch: retiring.RetirementEpoch,
		RetirementGeneration: generationB, ExpectedCursor: "", Cursor: lifecycleCursorA,
		RoundMatches: 1, UpdatedAt: base.Add(8 * time.Second),
	}); err != nil {
		t.Fatalf("advance retirement scan: %v", err)
	}
	if _, err := harness.Store.UpdateSourceRetirement(ctx, app.SourceRetirementUpdate{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationC,
		AuthorityEpoch: authorityC.Epoch, RetirementEpoch: retiring.RetirementEpoch,
		RetirementGeneration: generationB, ExpectedCursor: lifecycleCursorA,
		UpdatedAt: base.Add(9 * time.Second),
	}); err != nil {
		t.Fatalf("restart incomplete verification round: %v", err)
	}
	if _, err := harness.Store.UpdateSourceRetirement(ctx, app.SourceRetirementUpdate{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationC,
		AuthorityEpoch: authorityC.Epoch, RetirementEpoch: retiring.RetirementEpoch,
		RetirementGeneration: generationB, ExpectedCursor: "", Cursor: lifecycleCursorA,
		RoundMatches: 1, UpdatedAt: base.Add(10 * time.Second),
	}); err != nil {
		t.Fatalf("advance restarted retirement scan: %v", err)
	}
	if _, err := harness.Store.FinalizeSourceRetirement(ctx, app.SourceRetirementFinalization{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationC,
		AuthorityEpoch: authorityC.Epoch, RetirementEpoch: retiring.RetirementEpoch,
		RetirementGeneration: generationB, FinalizedAt: base.Add(11 * time.Second),
	}); !isLifecycleConflict(harness, err) {
		t.Fatalf("incomplete scan finalization error = %v, want conflict", err)
	}
	if _, err := harness.Store.UpdateSourceRetirement(ctx, app.SourceRetirementUpdate{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationC,
		AuthorityEpoch: authorityC.Epoch, RetirementEpoch: retiring.RetirementEpoch,
		RetirementGeneration: generationB, ExpectedCursor: lifecycleCursorA, Cursor: lifecycleCursorZ,
		RoundMatches: 1, ScanComplete: true, BlockerCode: "retirement.refs_remaining",
		UpdatedAt: base.Add(12 * time.Second),
	}); err != nil {
		t.Fatalf("complete nonzero scan: %v", err)
	}
	if _, err := harness.Store.FinalizeSourceRetirement(ctx, app.SourceRetirementFinalization{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationC,
		AuthorityEpoch: authorityC.Epoch, RetirementEpoch: retiring.RetirementEpoch,
		RetirementGeneration: generationB, FinalizedAt: base.Add(13 * time.Second),
	}); !isLifecycleConflict(harness, err) {
		t.Fatalf("blocked finalization error = %v, want conflict", err)
	}
	if _, err := harness.Store.UpdateSourceRetirement(ctx, app.SourceRetirementUpdate{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationC,
		AuthorityEpoch: authorityC.Epoch, RetirementEpoch: retiring.RetirementEpoch,
		RetirementGeneration: generationB, ExpectedCursor: lifecycleCursorZ,
		RoundMatches: 0, UpdatedAt: base.Add(14 * time.Second),
	}); err != nil {
		t.Fatalf("reset verification round: %v", err)
	}
	if _, err := harness.Store.UpdateSourceRetirement(ctx, app.SourceRetirementUpdate{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationC,
		AuthorityEpoch: authorityC.Epoch, RetirementEpoch: retiring.RetirementEpoch,
		RetirementGeneration: generationB, ExpectedCursor: "", ScanComplete: true,
		RoundMatches: 0, UpdatedAt: base.Add(15 * time.Second),
	}); err != nil {
		t.Fatalf("complete zero-ref scan: %v", err)
	}
	beforeFinalizationPoison := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem)
	if _, err := harness.Store.FinalizeSourceRetirement(ctx, app.SourceRetirementFinalization{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationC,
		AuthorityEpoch: authorityC.Epoch, RetirementGeneration: generationB, RetirementEpoch: retiring.RetirementEpoch,
		FinalizedAt: base.Add(14 * time.Second),
	}); !isLifecycleConflict(harness, err) {
		t.Fatalf("regressed finalization timestamp error = %v, want conflict", err)
	}
	if afterPoison := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem); afterPoison != beforeFinalizationPoison {
		t.Fatalf("regressed finalization poisoned row = %#v, want %#v", afterPoison, beforeFinalizationPoison)
	}

	peer := harness.OpenPeer(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, store := range []app.SourceLifecycleStore{harness.Store, peer} {
		wait.Add(1)
		go func(store app.SourceLifecycleStore) {
			defer wait.Done()
			<-start
			_, err := store.FinalizeSourceRetirement(ctx, app.SourceRetirementFinalization{
				Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationC,
				AuthorityEpoch: authorityC.Epoch, RetirementEpoch: retiring.RetirementEpoch,
				RetirementGeneration: generationB, FinalizedAt: base.Add(16 * time.Second),
			})
			results <- err
		}(store)
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil && !isLifecycleConflict(harness, err) {
			t.Fatalf("concurrent finalization error = %v", err)
		}
	}
	deleted := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem)
	if deleted.State != knowl.SourceLifecycleDeleted || deleted.DeletedAt.IsZero() {
		t.Fatalf("final lifecycle = %#v", deleted)
	}

	authorityD := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: harness.Scope, ExpectedAuthorityEpoch: authorityC.Epoch, UpdatedAt: base.Add(17 * time.Second),
		Desired: []app.DesiredSourceOwner{{Owner: filesystem, ConfigDigest: configA}},
	})
	generationD := authorityD.Generation
	if generationD != generationA || authorityD.Epoch == authorityA.Epoch {
		t.Fatalf("re-added authority = %#v, initial %#v", authorityD, authorityA)
	}
	readded := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem)
	if readded.State != knowl.SourceLifecycleActive || readded.ActivatedGeneration != generationD ||
		readded.ActivatedEpoch != authorityD.Epoch || readded.RetirementGeneration != generationB ||
		readded.Ready || !readded.DeletedAt.IsZero() {
		t.Fatalf("re-added lifecycle = %#v", readded)
	}
	if _, err := harness.Store.MarkSourceLifecycleReady(ctx, app.SourceLifecycleReadyUpdate{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationA,
		AuthorityEpoch: authorityA.Epoch, ActivatedEpoch: firstActivationEpoch,
		ConfigDigest: configA, ReadyAt: base.Add(18 * time.Second),
	}); !isLifecycleConflict(harness, err) {
		t.Fatalf("delayed readiness error = %v, want conflict", err)
	}
	reopened := requireLifecycle(t, ctx, peer, harness.Scope, filesystem)
	if reopened != readded {
		t.Fatalf("peer lifecycle = %#v, want %#v", reopened, readded)
	}
	beforePoison := readded
	regressedReconcile := app.SourceLifecycleReconcileRequest{
		Scope: harness.Scope, ExpectedAuthorityEpoch: authorityD.Epoch, UpdatedAt: base.Add(14 * time.Second),
		Desired: []app.DesiredSourceOwner{{Owner: git, ConfigDigest: configB}},
	}
	regressedReconcile.AuthorityGeneration = app.SourceLifecycleGeneration(regressedReconcile.Desired)
	if err := harness.Store.ReconcileSourceLifecycles(ctx, regressedReconcile); !isLifecycleConflict(harness, err) {
		t.Fatalf("regressed reconcile timestamp error = %v, want conflict", err)
	}
	if afterPoison := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem); afterPoison != beforePoison {
		t.Fatalf("regressed reconcile poisoned row = %#v, want %#v", afterPoison, beforePoison)
	}
	if _, err := harness.Store.MarkSourceLifecycleReady(ctx, app.SourceLifecycleReadyUpdate{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationD,
		AuthorityEpoch: authorityD.Epoch, ActivatedEpoch: readded.ActivatedEpoch,
		ConfigDigest: configA, ReadyAt: base.Add(14 * time.Second),
	}); !isLifecycleConflict(harness, err) {
		t.Fatalf("regressed readiness timestamp error = %v, want conflict", err)
	}
	if afterPoison := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem); afterPoison != beforePoison {
		t.Fatalf("regressed readiness poisoned row = %#v, want %#v", afterPoison, beforePoison)
	}
	canceledMutation, cancelMutation := context.WithCancel(ctx)
	cancelMutation()
	if _, err := harness.Store.MarkSourceLifecycleReady(canceledMutation, app.SourceLifecycleReadyUpdate{
		Scope: harness.Scope, Owner: filesystem, AuthorityGeneration: generationD,
		AuthorityEpoch: authorityD.Epoch, ActivatedEpoch: readded.ActivatedEpoch,
		ConfigDigest: configA, ReadyAt: base.Add(18 * time.Second),
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled readiness error = %v", err)
	}
	if afterCancel := requireLifecycle(t, ctx, harness.Store, harness.Scope, filesystem); afterCancel != beforePoison {
		t.Fatalf("canceled readiness mutated row = %#v, want %#v", afterCancel, beforePoison)
	}

	rows, err := harness.Store.SourceLifecycles(ctx, harness.Scope, app.SourceLifecycleListOptions{Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("first lifecycle page = %#v, %v", rows, err)
	}
	rows, err = harness.Store.SourceLifecycles(ctx, harness.Scope, app.SourceLifecycleListOptions{After: rows[0].Owner, Limit: 2})
	if err != nil || len(rows) != 1 {
		t.Fatalf("second lifecycle page = %#v, %v", rows, err)
	}

	missingScope := knowl.ScopeRef(string(harness.Scope) + "_missing")
	reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{Scope: missingScope, UpdatedAt: base})
	missing, err := harness.Store.SourceLifecycles(ctx, missingScope, app.SourceLifecycleListOptions{Limit: 10})
	if err != nil || len(missing) != 0 {
		t.Fatalf("empty desired invented lifecycle = %#v, %v", missing, err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := harness.Store.SourceLifecycle(canceled, harness.Scope, filesystem); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup error = %v", err)
	}

	runSourceLifecycleOrderingContract(t, ctx, harness, base.Add(20*time.Second))
	runSourceLifecyclePeerCASContract(t, ctx, harness, base.Add(30*time.Second))
	runHistoricalSourceLifecycleBootstrapContract(t, ctx, harness, base.Add(40*time.Second))
	runReplacementCohortRetentionContract(t, ctx, harness, base.Add(50*time.Second))
	runReplacementTimestampContract(t, ctx, harness, base.Add(60*time.Second))
	runSucceededReadinessRecoveryContract(t, ctx, harness, base.Add(70*time.Second))
}

func runSucceededReadinessRecoveryContract(t *testing.T, ctx context.Context, harness SourceLifecycleHarness, base time.Time) {
	t.Helper()
	for _, test := range []struct {
		name             string
		maintenanceReady bool
		changeConfig     bool
		wantResumable    bool
	}{
		{name: "ready_pending", maintenanceReady: true, wantResumable: true},
		{name: "maintenance_blocked", maintenanceReady: false},
		{name: "stale_config", maintenanceReady: true, changeConfig: true},
	} {
		scope := knowl.ScopeRef(string(harness.Scope) + "_succeeded_" + test.name)
		sourceID := knowl.SourceID("succeeded-" + test.name)
		owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: sourceID}
		configDigest := strings.Repeat("5", 64)
		authority := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
			Scope: scope, UpdatedAt: base,
			Desired: []app.DesiredSourceOwner{{Owner: owner, ConfigDigest: configDigest}},
		})
		run := knowl.SyncRun{
			ID: knowl.SyncRunID("succeeded-" + test.name), Scope: scope, SourceID: sourceID,
			ConfigDigest: configDigest, Status: knowl.SyncStatusScanning, StartedAt: base, UpdatedAt: base,
		}
		if _, _, err := harness.Legacy.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil {
			t.Fatalf("begin %s readiness run: %v", test.name, err)
		}
		prepared := contractPreparedState(t, run.ID, scope, sourceID, "readiness-checkpoint", knowl.SyncCounts{}, nil, base.Add(time.Second))
		if !test.maintenanceReady {
			prepared.MaintenanceReady = false
			digest, err := app.PreparedSyncDigest(prepared)
			if err != nil {
				t.Fatal(err)
			}
			prepared.CandidateDigest = digest
		}
		if _, err := harness.Legacy.PrepareSync(ctx, prepared); err != nil {
			t.Fatalf("prepare %s readiness run: %v", test.name, err)
		}
		transition := app.SyncGeneration{
			RunID: run.ID, Scope: scope, SourceID: sourceID,
			Generation: "readiness-generation", UpdatedAt: base.Add(2 * time.Second),
		}
		if _, err := harness.Legacy.MarkContentCommitted(ctx, transition); err != nil {
			t.Fatal(err)
		}
		if _, err := harness.Legacy.MarkProjected(ctx, transition); err != nil {
			t.Fatal(err)
		}
		if _, err := harness.Legacy.FinalizeSync(ctx, app.SyncFinalization{
			RunID: run.ID, Scope: scope, SourceID: sourceID, CandidateDigest: prepared.CandidateDigest,
			Generation: transition.Generation, Checkpoint: prepared.Checkpoint, FinalizedAt: base.Add(3 * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		if test.changeConfig {
			changed := []app.DesiredSourceOwner{{Owner: owner, ConfigDigest: strings.Repeat("4", 64)}}
			authority = reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
				Scope: scope, Desired: changed, ExpectedAuthorityEpoch: authority.Epoch, UpdatedAt: base.Add(4 * time.Second),
			})
		}
		resumable, err := harness.Legacy.ResumableSyncRuns(ctx, scope, 10)
		if err != nil || (len(resumable) == 1) != test.wantResumable {
			t.Fatalf("%s succeeded resumable = %#v, %v", test.name, resumable, err)
		}
		if !test.wantResumable {
			continue
		}
		active := requireLifecycle(t, ctx, harness.Store, scope, owner)
		if _, err := harness.Store.MarkSourceLifecycleReady(ctx, app.SourceLifecycleReadyUpdate{
			Scope: scope, Owner: owner, AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch,
			ActivatedEpoch: active.ActivatedEpoch, ConfigDigest: configDigest, ReadyAt: base.Add(4 * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		resumable, err = harness.Legacy.ResumableSyncRuns(ctx, scope, 10)
		if err != nil || len(resumable) != 0 {
			t.Fatalf("acknowledged succeeded resumable = %#v, %v", resumable, err)
		}
	}
}

func runReplacementTimestampContract(t *testing.T, ctx context.Context, harness SourceLifecycleHarness, base time.Time) {
	t.Helper()
	scope := knowl.ScopeRef(string(harness.Scope) + "_replacement_timestamp")
	old := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "timestamp-old"}
	addition := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "timestamp-addition"}
	digest := strings.Repeat("6", 64)
	first := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: scope, UpdatedAt: base,
		Desired: []app.DesiredSourceOwner{{Owner: old, ConfigDigest: digest}},
	})
	rollout := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: scope, ExpectedAuthorityEpoch: first.Epoch, UpdatedAt: base.Add(time.Second),
		Desired: []app.DesiredSourceOwner{{Owner: addition, ConfigDigest: digest}},
	})
	retiring := requireLifecycle(t, ctx, harness.Store, scope, old)
	progressAt := base.Add(5 * time.Second)
	if _, err := harness.Store.UpdateSourceRetirement(ctx, app.SourceRetirementUpdate{
		Scope: scope, Owner: old, AuthorityGeneration: rollout.Generation, AuthorityEpoch: rollout.Epoch,
		RetirementGeneration: rollout.Generation, RetirementEpoch: retiring.RetirementEpoch,
		ExpectedCursor: "", Cursor: lifecycleCursorA, RoundMatches: 1, UpdatedAt: progressAt,
	}); err != nil {
		t.Fatalf("advance timestamp retirement: %v", err)
	}
	active := requireLifecycle(t, ctx, harness.Store, scope, addition)
	if _, err := harness.Store.MarkSourceLifecycleReady(ctx, app.SourceLifecycleReadyUpdate{
		Scope: scope, Owner: addition, AuthorityGeneration: rollout.Generation, AuthorityEpoch: rollout.Epoch,
		ActivatedEpoch: active.ActivatedEpoch, ConfigDigest: digest, ReadyAt: base.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("mark timestamp addition ready: %v", err)
	}
	after := requireLifecycle(t, ctx, harness.Store, scope, old)
	if after.ReplacementPending != 0 || !after.UpdatedAt.Equal(progressAt) {
		t.Fatalf("retirement timestamp after readiness = %#v, want pending zero at %s", after, progressAt)
	}
}

func runReplacementCohortRetentionContract(t *testing.T, ctx context.Context, harness SourceLifecycleHarness, base time.Time) {
	t.Helper()
	scope := knowl.ScopeRef(string(harness.Scope) + "_replacement_cohort")
	old := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "cohort-old"}
	addition := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "cohort-addition"}
	digest := strings.Repeat("d", 64)
	first := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: scope, UpdatedAt: base,
		Desired: []app.DesiredSourceOwner{{Owner: old, ConfigDigest: digest}},
	})
	rollout := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: scope, ExpectedAuthorityEpoch: first.Epoch, UpdatedAt: base.Add(time.Second),
		Desired: []app.DesiredSourceOwner{{Owner: addition, ConfigDigest: digest}},
	})
	retiring := requireLifecycle(t, ctx, harness.Store, scope, old)
	if retiring.ReplacementPending != 1 {
		t.Fatalf("initial cohort pending = %#v, want one", retiring)
	}
	removed := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: scope, ExpectedAuthorityEpoch: rollout.Epoch, UpdatedAt: base.Add(2 * time.Second),
	})
	states, err := harness.Store.SourceLifecycles(ctx, scope, app.SourceLifecycleListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("read removed cohort: %v", err)
	}
	retiring = requireLifecycle(t, ctx, harness.Store, scope, old)
	gate, err := app.EvaluateSourceRetirementReadiness(retiring, states)
	if err != nil || gate.Ready || gate.PendingCount != 1 || len(gate.Pending) != 1 || gate.Pending[0] != addition {
		t.Fatalf("removed cohort gate = %#v, %v", gate, err)
	}
	readdedAuthority := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: scope, ExpectedAuthorityEpoch: removed.Epoch, UpdatedAt: base.Add(3 * time.Second),
		Desired: []app.DesiredSourceOwner{{Owner: addition, ConfigDigest: digest}},
	})
	readded := requireLifecycle(t, ctx, harness.Store, scope, addition)
	if _, err := harness.Store.MarkSourceLifecycleReady(ctx, app.SourceLifecycleReadyUpdate{
		Scope: scope, Owner: addition, AuthorityGeneration: readdedAuthority.Generation,
		AuthorityEpoch: readdedAuthority.Epoch, ActivatedEpoch: readded.ActivatedEpoch,
		ConfigDigest: digest, ReadyAt: base.Add(4 * time.Second),
	}); err != nil {
		t.Fatalf("mark re-added owner ready: %v", err)
	}
	states, err = harness.Store.SourceLifecycles(ctx, scope, app.SourceLifecycleListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("read re-added cohort: %v", err)
	}
	retiring = requireLifecycle(t, ctx, harness.Store, scope, old)
	gate, err = app.EvaluateSourceRetirementReadiness(retiring, states)
	if err != nil || gate.Ready || gate.PendingCount != 1 || len(gate.Pending) != 0 {
		t.Fatalf("re-added cohort gate = %#v, %v", gate, err)
	}
}

func runHistoricalSourceLifecycleBootstrapContract(t *testing.T, ctx context.Context, harness SourceLifecycleHarness, at time.Time) {
	t.Helper()
	config := strings.Repeat("7", 64)
	for _, fixture := range []struct {
		name    string
		desired []app.DesiredSourceOwner
	}{
		{name: "empty"},
		{name: "replacement", desired: []app.DesiredSourceOwner{{
			Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: lifecycleHistoricalSource}, ConfigDigest: strings.Repeat("8", 64),
		}}},
	} {
		scope := knowl.ScopeRef(string(harness.Scope) + "_bootstrap_" + fixture.name)
		run := knowl.SyncRun{
			ID: knowl.SyncRunID("lifecycle-bootstrap-" + fixture.name), Scope: scope, SourceID: lifecycleHistoricalSource,
			ConfigDigest: config, Status: knowl.SyncStatusScanning, StartedAt: at, UpdatedAt: at,
		}
		if _, _, err := harness.Legacy.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil {
			t.Fatalf("create %s historical source: %v", fixture.name, err)
		}
		authority := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
			Scope: scope, Desired: fixture.desired, UpdatedAt: at.Add(time.Second),
		})
		oldOwner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: lifecycleHistoricalSource}
		oldState := requireLifecycle(t, ctx, harness.Store, scope, oldOwner)
		wantOld := knowl.SourceLifecycle{
			Scope: scope, Owner: oldOwner, State: knowl.SourceLifecycleRetiring, ConfigDigest: config,
			AuthorityGeneration: authority.Generation, AuthorityEpoch: 1,
			ActivatedGeneration: authority.Generation, ActivatedEpoch: 1,
			RetirementGeneration: authority.Generation, RetirementEpoch: 1,
			ReplacementPending: len(fixture.desired),
			ActivatedAt:        at, RetiringAt: at.Add(time.Second), UpdatedAt: at.Add(time.Second),
		}
		if oldState != wantOld {
			t.Fatalf("%s historical lifecycle = %#v, want %#v", fixture.name, oldState, wantOld)
		}
		if len(fixture.desired) == 0 {
			continue
		}
		newState := requireLifecycle(t, ctx, harness.Store, scope, fixture.desired[0].Owner)
		wantNew := knowl.SourceLifecycle{
			Scope: scope, Owner: fixture.desired[0].Owner, State: knowl.SourceLifecycleActive,
			ConfigDigest:        fixture.desired[0].ConfigDigest,
			AuthorityGeneration: authority.Generation, AuthorityEpoch: 1,
			ActivatedGeneration: authority.Generation, ActivatedEpoch: 1,
			ActivatedAt: at.Add(time.Second), UpdatedAt: at.Add(time.Second),
		}
		if newState != wantNew {
			t.Fatalf("replacement active lifecycle = %#v, want %#v", newState, wantNew)
		}
	}

	scope := knowl.ScopeRef(string(harness.Scope) + "_bootstrap_config_change")
	run := knowl.SyncRun{
		ID: "lifecycle-bootstrap-config-change", Scope: scope, SourceID: lifecycleHistoricalSource,
		ConfigDigest: config, Status: knowl.SyncStatusScanning, StartedAt: at, UpdatedAt: at,
	}
	if _, _, err := harness.Legacy.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatalf("create config-change historical source: %v", err)
	}
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: lifecycleHistoricalSource}
	desired := []app.DesiredSourceOwner{{Owner: owner, ConfigDigest: strings.Repeat("8", 64)}}
	authority := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: scope, Desired: desired, UpdatedAt: at.Add(time.Second),
	})
	state := requireLifecycle(t, ctx, harness.Store, scope, owner)
	if state.State != knowl.SourceLifecycleActive || state.ConfigDigest != desired[0].ConfigDigest ||
		state.AuthorityGeneration != authority.Generation || state.AuthorityEpoch != authority.Epoch ||
		state.ActivatedGeneration != authority.Generation || state.ActivatedEpoch != authority.Epoch || state.Ready {
		t.Fatalf("config-change historical lifecycle = %#v", state)
	}
}

func runSourceLifecycleOrderingContract(t *testing.T, ctx context.Context, harness SourceLifecycleHarness, at time.Time) {
	t.Helper()
	scope := knowl.ScopeRef(string(harness.Scope) + "_ordering")
	digest := strings.Repeat("c", 64)
	desired := []app.DesiredSourceOwner{
		{Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "a_z"}, ConfigDigest: digest},
		{Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "a.z"}, ConfigDigest: digest},
		{Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "a-z"}, ConfigDigest: digest},
	}
	reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{Scope: scope, Desired: desired, UpdatedAt: at})
	rows, err := harness.Store.SourceLifecycles(ctx, scope, app.SourceLifecycleListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	want := []knowl.SourceID{"a-z", "a.z", "a_z"}
	if len(rows) != len(want) {
		t.Fatalf("punctuation lifecycle rows = %#v", rows)
	}
	for index := range want {
		if rows[index].Owner.SourceID != want[index] {
			t.Fatalf("punctuation order[%d] = %q, want %q", index, rows[index].Owner.SourceID, want[index])
		}
	}
}

func runSourceLifecyclePeerCASContract(t *testing.T, ctx context.Context, harness SourceLifecycleHarness, at time.Time) {
	t.Helper()
	scope := knowl.ScopeRef(string(harness.Scope) + "_peer_cas")
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "peer-source"}
	first := reconcileLifecycle(t, ctx, harness.Store, app.SourceLifecycleReconcileRequest{
		Scope: scope, UpdatedAt: at,
		Desired: []app.DesiredSourceOwner{{Owner: owner, ConfigDigest: strings.Repeat("d", 64)}},
	})
	requests := []app.SourceLifecycleReconcileRequest{
		{Scope: scope, ExpectedAuthorityEpoch: first.Epoch, UpdatedAt: at.Add(time.Second), Desired: []app.DesiredSourceOwner{{Owner: owner, ConfigDigest: strings.Repeat("e", 64)}}},
		{Scope: scope, ExpectedAuthorityEpoch: first.Epoch, UpdatedAt: at.Add(time.Second), Desired: []app.DesiredSourceOwner{{Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "peer-source"}, ConfigDigest: strings.Repeat("f", 64)}}},
	}
	stores := []app.SourceLifecycleStore{harness.Store, harness.OpenPeer(t)}
	start := make(chan struct{})
	results := make(chan error, len(stores))
	var wait sync.WaitGroup
	for index := range stores {
		request := requests[index]
		request.AuthorityGeneration = app.SourceLifecycleGeneration(request.Desired)
		wait.Add(1)
		go func(store app.SourceLifecycleStore, request app.SourceLifecycleReconcileRequest) {
			defer wait.Done()
			<-start
			results <- store.ReconcileSourceLifecycles(ctx, request)
		}(stores[index], request)
	}
	close(start)
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case isLifecycleConflict(harness, err):
			conflicts++
		default:
			t.Fatalf("peer reconciliation error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("peer reconciliation successes/conflicts = %d/%d", successes, conflicts)
	}
	authority, err := harness.Store.SourceLifecycleAuthority(ctx, scope)
	if err != nil || authority.Epoch != first.Epoch+1 {
		t.Fatalf("peer authority = %#v, %v", authority, err)
	}
}

func reconcileLifecycle(t *testing.T, ctx context.Context, store app.SourceLifecycleStore, request app.SourceLifecycleReconcileRequest) knowl.SourceLifecycleAuthority {
	t.Helper()
	request.AuthorityGeneration = app.SourceLifecycleGeneration(request.Desired)
	if err := store.ReconcileSourceLifecycles(ctx, request); err != nil {
		t.Fatalf("ReconcileSourceLifecycles(): %v", err)
	}
	authority, err := store.SourceLifecycleAuthority(ctx, request.Scope)
	if err != nil {
		t.Fatalf("SourceLifecycleAuthority(): %v", err)
	}
	return authority
}

func requireLifecycle(t *testing.T, ctx context.Context, store app.SourceLifecycleStore, scope knowl.ScopeRef, owner knowl.SourceOwner) knowl.SourceLifecycle {
	t.Helper()
	state, err := store.SourceLifecycle(ctx, scope, owner)
	if err != nil {
		t.Fatalf("SourceLifecycle(%#v): %v", owner, err)
	}
	return state
}

func isLifecycleConflict(harness SourceLifecycleHarness, err error) bool {
	return errors.Is(err, app.ErrSourceLifecycleConflict) || harness.IsConflict != nil && harness.IsConflict(err)
}
