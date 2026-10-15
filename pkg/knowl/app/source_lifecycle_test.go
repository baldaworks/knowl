package app_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	invalidLifecycleDigest = "not-a-digest"
	lifecycleTestScope     = "local"
)

func TestSourceOwnerFromTypeKeepsAdaptersIndependent(t *testing.T) {
	t.Parallel()
	const sourceID = knowl.SourceID("fastronome-wiki")

	filesystem, err := app.SourceOwnerFromType(knowl.SourceTypeFilesystem, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	git, err := app.SourceOwnerFromType(knowl.SourceTypeGit, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if filesystem != (knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: sourceID}) {
		t.Fatalf("filesystem owner = %#v", filesystem)
	}
	if git != (knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: sourceID}) {
		t.Fatalf("Git owner = %#v", git)
	}
	if filesystem == git {
		t.Fatal("filesystem and Git owners collapsed")
	}
}

func TestNormalizeSourceLifecycleReconcileRejectsInvalidAuthority(t *testing.T) {
	t.Parallel()
	request := app.SourceLifecycleReconcileRequest{
		Scope: lifecycleTestScope,
		Desired: []app.DesiredSourceOwner{{
			Owner:        knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "fastronome-wiki"},
			ConfigDigest: strings.Repeat("b", 64),
		}},
		UpdatedAt: time.Unix(1, 0).UTC(),
	}
	request.AuthorityGeneration = app.SourceLifecycleGeneration(request.Desired)
	if _, err := app.NormalizeSourceLifecycleReconcile(request); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	request.AuthorityGeneration = invalidLifecycleDigest
	if _, err := app.NormalizeSourceLifecycleReconcile(request); !errors.Is(err, app.ErrSourceLifecycleInvalid) {
		t.Fatalf("invalid authority error = %v", err)
	}
	request.AuthorityGeneration = app.SourceLifecycleGeneration(request.Desired)
	request.Desired[0].Owner.Adapter = "filesystem"
	if _, err := app.NormalizeSourceLifecycleReconcile(request); !errors.Is(err, app.ErrSourceLifecycleInvalid) {
		t.Fatalf("invalid adapter error = %v", err)
	}
}

func TestValidateSourceLifecycleRejectsRegressedTransitionTimes(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_830_000_000, 0).UTC()
	state := knowl.SourceLifecycle{
		Scope: lifecycleTestScope, Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "timeline"},
		State: knowl.SourceLifecycleRetiring, ConfigDigest: strings.Repeat("a", 64),
		AuthorityGeneration: strings.Repeat("b", 64), AuthorityEpoch: 2,
		ActivatedGeneration: strings.Repeat("a", 64), ActivatedEpoch: 1,
		RetirementGeneration: strings.Repeat("b", 64), RetirementEpoch: 2,
		ActivatedAt: base, RetiringAt: base.Add(-time.Second), UpdatedAt: base.Add(time.Second),
	}
	if err := app.ValidateSourceLifecycle(state); !errors.Is(err, app.ErrSourceLifecycleInvalid) {
		t.Fatalf("regressed retiring time error = %v", err)
	}
	state.RetiringAt = base
	state.State = knowl.SourceLifecycleDeleted
	state.ScanComplete = true
	state.DeletedAt = base.Add(-time.Second)
	if err := app.ValidateSourceLifecycle(state); !errors.Is(err, app.ErrSourceLifecycleInvalid) {
		t.Fatalf("regressed deleted time error = %v", err)
	}
}

func TestValidateSourceLifecycleRejectsImpossibleEpochOrder(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_830_000_000, 0).UTC()
	state := knowl.SourceLifecycle{
		Scope: lifecycleTestScope, Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "epochs"},
		State: knowl.SourceLifecycleActive, ConfigDigest: strings.Repeat("a", 64),
		AuthorityGeneration: strings.Repeat("b", 64), AuthorityEpoch: 1,
		ActivatedGeneration: strings.Repeat("a", 64), ActivatedEpoch: 2,
		ActivatedAt: base, UpdatedAt: base,
	}
	if err := app.ValidateSourceLifecycle(state); !errors.Is(err, app.ErrSourceLifecycleInvalid) {
		t.Fatalf("activation after authority error = %v", err)
	}

	state.State = knowl.SourceLifecycleRetiring
	state.AuthorityEpoch = 3
	state.RetirementGeneration = strings.Repeat("c", 64)
	state.RetirementEpoch = 1
	state.RetiringAt = base
	if err := app.ValidateSourceLifecycle(state); !errors.Is(err, app.ErrSourceLifecycleInvalid) {
		t.Fatalf("retirement before activation error = %v", err)
	}
}

func TestSourceLifecycleMutationValidationRejectsImpossibleEpochOrder(t *testing.T) {
	t.Parallel()
	digest := strings.Repeat("a", 64)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "epochs"}
	at := time.Unix(1_830_000_000, 0).UTC()
	if err := app.ValidateSourceLifecycleReadyUpdate(app.SourceLifecycleReadyUpdate{
		Scope: lifecycleTestScope, Owner: owner, AuthorityGeneration: digest, AuthorityEpoch: 1,
		ActivatedEpoch: 2, ConfigDigest: digest, ReadyAt: at,
	}); !errors.Is(err, app.ErrSourceLifecycleInvalid) {
		t.Fatalf("readiness epoch order error = %v", err)
	}
	if err := app.ValidateSourceRetirementUpdate(app.SourceRetirementUpdate{
		Scope: lifecycleTestScope, Owner: owner, AuthorityGeneration: digest, AuthorityEpoch: 1,
		RetirementGeneration: digest, RetirementEpoch: 2, UpdatedAt: at,
	}); !errors.Is(err, app.ErrSourceLifecycleInvalid) {
		t.Fatalf("retirement update epoch order error = %v", err)
	}
	if err := app.ValidateSourceRetirementFinalization(app.SourceRetirementFinalization{
		Scope: lifecycleTestScope, Owner: owner, AuthorityGeneration: digest, AuthorityEpoch: 1,
		RetirementGeneration: digest, RetirementEpoch: 2, FinalizedAt: at,
	}); !errors.Is(err, app.ErrSourceLifecycleInvalid) {
		t.Fatalf("retirement finalization epoch order error = %v", err)
	}
}

func TestEvaluateSourceRetirementReadinessUsesOnlySameRolloutAdditions(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_900_000_000, 0).UTC()
	retiring := knowl.SourceLifecycle{
		Owner:              knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "old"},
		State:              knowl.SourceLifecycleRetiring,
		RetirementEpoch:    4,
		ReplacementPending: 1,
		RetiringAt:         base,
	}
	first := knowl.SourceLifecycle{
		Owner:          knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "first"},
		State:          knowl.SourceLifecycleActive,
		ActivatedEpoch: 4,
		ActivatedAt:    base,
		Ready:          true,
	}
	staleConfig := knowl.SourceLifecycle{
		Owner:          knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "second"},
		State:          knowl.SourceLifecycleActive,
		ActivatedEpoch: 4,
		ActivatedAt:    base.Add(time.Second),
	}
	unrelatedLater := knowl.SourceLifecycle{
		Owner:          knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "later"},
		State:          knowl.SourceLifecycleActive,
		ActivatedEpoch: 6,
		ActivatedAt:    base,
	}

	blocked, err := app.EvaluateSourceRetirementReadiness(retiring, []knowl.SourceLifecycle{unrelatedLater, staleConfig, first})
	if err != nil {
		t.Fatalf("EvaluateSourceRetirementReadiness() error = %v", err)
	}
	wantPending := []knowl.SourceOwner{staleConfig.Owner}
	if blocked.Ready || blocked.BlockerCode != app.RetirementReplacementNotReadyCode || blocked.PendingCount != 1 || !reflect.DeepEqual(blocked.Pending, wantPending) {
		t.Fatalf("blocked readiness = %#v, want pending %#v", blocked, wantPending)
	}

	staleConfig.Ready = true
	retiring.ReplacementPending = 0
	ready, err := app.EvaluateSourceRetirementReadiness(retiring, []knowl.SourceLifecycle{unrelatedLater, staleConfig, first})
	if err != nil || !ready.Ready || ready.BlockerCode != "" || len(ready.Pending) != 0 {
		t.Fatalf("ready replacement = %#v, %v", ready, err)
	}

	pureDeletion, err := app.EvaluateSourceRetirementReadiness(retiring, []knowl.SourceLifecycle{unrelatedLater})
	if err != nil || !pureDeletion.Ready || len(pureDeletion.Pending) != 0 {
		t.Fatalf("pure-deletion readiness = %#v, %v", pureDeletion, err)
	}
}

func TestEvaluateSourceRetirementReadinessKeepsRemovedCohortBlocked(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_900_000_000, 0).UTC()
	retiring := knowl.SourceLifecycle{
		Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "old"},
		State: knowl.SourceLifecycleRetiring, RetirementEpoch: 4, ReplacementPending: 1, RetiringAt: base,
	}
	addition := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "replacement"}
	removed := knowl.SourceLifecycle{
		Owner: addition, State: knowl.SourceLifecycleRetiring, ActivatedEpoch: 4, RetiringAt: base.Add(time.Second),
	}
	blocked, err := app.EvaluateSourceRetirementReadiness(retiring, []knowl.SourceLifecycle{removed})
	if err != nil || blocked.Ready || blocked.PendingCount != 1 || !reflect.DeepEqual(blocked.Pending, []knowl.SourceOwner{addition}) {
		t.Fatalf("removed-cohort readiness = %#v, %v", blocked, err)
	}

	readded := knowl.SourceLifecycle{Owner: addition, State: knowl.SourceLifecycleActive, ActivatedEpoch: 7, Ready: true, ActivatedAt: base.Add(2 * time.Second)}
	blocked, err = app.EvaluateSourceRetirementReadiness(retiring, []knowl.SourceLifecycle{readded})
	if err != nil || blocked.Ready || blocked.PendingCount != 1 || len(blocked.Pending) != 0 {
		t.Fatalf("re-added cohort readiness = %#v, %v", blocked, err)
	}
}

func TestEvaluateSourceRetirementReadinessOmitsHistoricalBootstrapRemovals(t *testing.T) {
	base := time.Unix(1_900_000_000, 0).UTC()
	retiring := knowl.SourceLifecycle{
		Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "historical-a"},
		State: knowl.SourceLifecycleRetiring, ActivatedEpoch: 8, RetirementEpoch: 8,
		ReplacementPending: 1, RetiringAt: base,
	}
	otherRemoval := knowl.SourceLifecycle{
		Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "historical-b"},
		State: knowl.SourceLifecycleRetiring, ActivatedEpoch: 8, RetirementEpoch: 8,
	}
	addition := knowl.SourceLifecycle{
		Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "replacement"},
		State: knowl.SourceLifecycleActive, ActivatedEpoch: 8, ActivatedAt: base,
	}

	gate, err := app.EvaluateSourceRetirementReadiness(retiring, []knowl.SourceLifecycle{retiring, otherRemoval, addition})
	if err != nil {
		t.Fatalf("EvaluateSourceRetirementReadiness() error = %v", err)
	}
	if gate.Ready || gate.PendingCount != 1 || len(gate.Pending) != 1 || gate.Pending[0] != addition.Owner {
		t.Fatalf("historical bootstrap gate = %#v, want only addition %#v", gate, addition.Owner)
	}
}
