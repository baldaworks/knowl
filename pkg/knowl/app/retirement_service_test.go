package app_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const firstRetirementLeaseToken = "first"

func TestRetirementPageServiceReusesSharedPageOperationAcrossRetiringOwners(t *testing.T) {
	fixture, _, request, _, firstOwner := plannedOldOnlyRetirement(t)
	secondOwner := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "second-retiring-owner"}
	second := retirementLifecycle(secondOwner, knowl.SourceLifecycleRetiring, fixture.authority)
	second.RetirementGeneration = fixture.states[firstOwner].RetirementGeneration
	fixture.states[secondOwner] = second
	content := &retirementOperationContent{
		retirementContentFixture: &fixture.content,
		schema:                   fixture.schema,
		stages:                   make(map[knowl.OperationID]knowl.StagedChange),
	}
	store, err := sqlite.Open(t.Context(), t.TempDir()+"/retirement-shared-page.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := app.NewRetirementPageService(content, store, &retirementProjection{}, fixture.lifecycle(), &retirementOperationCommitter{}, &fixture.maintainer, app.RetirementPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Reserve(t.Context(), request.Scope, firstOwner, second.RetirementGeneration, request.Page.ID)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Reserve(t.Context(), request.Scope, secondOwner, second.RetirementGeneration, request.Page.ID)
	if err != nil {
		t.Fatalf("Reserve() shared page replay error = %v", err)
	}
	if replayed.ID != first.ID || replayed.New {
		t.Fatalf("shared page replay = %#v, want existing %q", replayed, first.ID)
	}
	if replayed.Descriptor.RetirementPage == nil || replayed.Descriptor.RetirementPage.Owner != firstOwner {
		t.Fatalf("shared descriptor = %#v, want original owner %v", replayed.Descriptor.RetirementPage, firstOwner)
	}
	secondOperations, err := store.RetirementOperations(t.Context(), app.RetirementOperationReadRequest{
		Scope: request.Scope, Owner: secondOwner, RetirementGeneration: second.RetirementGeneration, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondOperations.Items) != 1 || secondOperations.Items[0].Operation.ID != first.ID || secondOperations.Items[0].PageID != request.Page.ID {
		t.Fatalf("second owner operations = %#v, want shared page %q", secondOperations, first.ID)
	}
}

func TestRetirementPageServiceReplaysProjectionAfterCanonicalCommit(t *testing.T) {
	fixture, _, request, _, owner := plannedOldOnlyRetirement(t)
	content := &retirementOperationContent{
		retirementContentFixture: &fixture.content,
		schema:                   fixture.schema,
		stages:                   make(map[knowl.OperationID]knowl.StagedChange),
	}
	store, err := sqlite.Open(t.Context(), t.TempDir()+"/retirement-operation.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	index := &retirementProjection{failures: 1}
	committer := &retirementOperationCommitter{}
	service, err := app.NewRetirementPageService(content, store, index, fixture.lifecycle(), committer, &fixture.maintainer, app.RetirementPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := service.Reserve(t.Context(), request.Scope, owner, fixture.states[owner].RetirementGeneration, request.Page.ID)
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	claim, err := store.ClaimOperation(t.Context(), request.Scope, reservation.ID, knowl.WorkLease{Token: firstRetirementLeaseToken, ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.RunToTerminal(t.Context(), claim)
	if !errors.Is(err, app.ErrProjection) || first.Operation.Status != knowl.StatusApplying || committer.calls != 1 || index.calls != 1 {
		t.Fatalf("first RunToTerminal() = %#v, %v; commits=%d projections=%d", first, err, committer.calls, index.calls)
	}
	failed, err := store.Operation(t.Context(), request.Scope, reservation.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantDiagnostic := knowl.MaintenanceDiagnostic{Code: app.RetirementProjectionFailedCode, Path: request.Page.Path}
	if !slices.Contains(failed.Diagnostics, wantDiagnostic) {
		t.Fatalf("projection diagnostics = %#v, want %#v", failed.Diagnostics, wantDiagnostic)
	}
	if err := store.ReleaseClaim(t.Context(), request.Scope, reservation.ID, claim.Lease.Token); err != nil {
		t.Fatal(err)
	}
	replayClaim, err := store.ClaimOperation(t.Context(), request.Scope, reservation.ID, knowl.WorkLease{Token: "replay", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.RunToTerminal(t.Context(), replayClaim)
	if err != nil || replayed.Operation.Status != knowl.StatusCommitted || committer.calls != 1 || index.calls != 2 {
		t.Fatalf("replayed RunToTerminal() = %#v, %v; commits=%d projections=%d", replayed, err, committer.calls, index.calls)
	}
	if !slices.Contains(replayed.Operation.Diagnostics, wantDiagnostic) {
		t.Fatalf("committed replay lost projection diagnostics: %#v", replayed.Operation.Diagnostics)
	}
	if fixture.maintainer.planCalls != 0 {
		t.Fatalf("old-only retirement invoked maintainer %d times", fixture.maintainer.planCalls)
	}
	if len(content.stagePlans) != 1 {
		t.Fatalf("stage plans = %d, want one", len(content.stagePlans))
	}
	if !reflect.DeepEqual(committer.first, committer.last) || !reflect.DeepEqual(index.commits[0], index.commits[1]) {
		t.Fatalf("replay changed durable unit: commits %#v/%#v projections %#v", committer.first, committer.last, index.commits)
	}
}

func TestRetirementPageServiceProjectionReplayDoesNotRevalidateCommittedCanonicalMutation(t *testing.T) {
	fixture, _, request, _, owner := plannedOldOnlyRetirement(t)
	content := &retirementOperationContent{
		retirementContentFixture: &fixture.content,
		schema:                   fixture.schema,
		stages:                   make(map[knowl.OperationID]knowl.StagedChange),
	}
	store, err := sqlite.Open(t.Context(), t.TempDir()+"/retirement-projection-only-replay.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	index := &retirementProjection{failures: 1}
	committer := &retirementOperationCommitter{}
	service, err := app.NewRetirementPageService(content, store, index, fixture.lifecycle(), committer, &fixture.maintainer, app.RetirementPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := service.Reserve(t.Context(), request.Scope, owner, fixture.states[owner].RetirementGeneration, request.Page.ID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimOperation(t.Context(), request.Scope, reservation.ID, knowl.WorkLease{Token: firstRetirementLeaseToken, ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunToTerminal(t.Context(), claim); !errors.Is(err, app.ErrProjection) {
		t.Fatalf("first RunToTerminal() error = %v, want projection failure", err)
	}
	if err := store.ReleaseClaim(t.Context(), request.Scope, reservation.ID, claim.Lease.Token); err != nil {
		t.Fatal(err)
	}

	// Once canonical content is committed, a later lifecycle change must not
	// block replay of the rebuildable projection and terminal outcome.
	committer.err = app.ErrRetirementLifecycleConflict
	replay, err := store.ClaimOperation(t.Context(), request.Scope, reservation.ID, knowl.WorkLease{Token: "replay", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunToTerminal(t.Context(), replay)
	if err != nil || result.Operation.Status != knowl.StatusCommitted || committer.calls != 1 || index.calls != 2 {
		t.Fatalf("projection-only replay = %#v, %v; commits=%d projections=%d", result, err, committer.calls, index.calls)
	}
}

func TestRetirementPageServiceHoldsApplyCoordinatorThroughProjection(t *testing.T) {
	fixture, _, request, _, owner := plannedOldOnlyRetirement(t)
	content := &retirementOperationContent{
		retirementContentFixture: &fixture.content,
		schema:                   fixture.schema,
		stages:                   make(map[knowl.OperationID]knowl.StagedChange),
	}
	store, err := sqlite.Open(t.Context(), t.TempDir()+"/retirement-apply-fence.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	coordinator := newTestApplyCoordinator()
	committer := &coordinatedRetirementOperationCommitter{
		coordinator: coordinator,
		delegate:    &retirementOperationCommitter{},
	}
	index := &blockedRetirementProjection{
		entered: make(chan struct{}),
		resume:  make(chan struct{}),
	}
	service, err := app.NewRetirementPageService(content, store, index, fixture.lifecycle(), committer, &fixture.maintainer, app.RetirementPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := service.Reserve(t.Context(), request.Scope, owner, fixture.states[owner].RetirementGeneration, request.Page.ID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimOperation(t.Context(), request.Scope, reservation.ID, knowl.WorkLease{Token: "apply-fence", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}

	type runResult struct {
		result app.IngestResult
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		result, runErr := service.RunToTerminal(t.Context(), claim)
		done <- runResult{result: result, err: runErr}
	}()
	select {
	case <-index.entered:
	case <-time.After(time.Second):
		close(index.resume)
		t.Fatal("retirement projection did not start")
	}

	waitCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	release, acquireErr := coordinator.Acquire(waitCtx)
	cancel()
	if release != nil {
		release()
	}
	close(index.resume)
	completed := <-done
	if completed.err != nil || completed.result.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("RunToTerminal() = %#v, %v", completed.result, completed.err)
	}
	if !errors.Is(acquireErr, context.DeadlineExceeded) {
		t.Fatalf("competing canonical publication acquired coordinator during retirement projection: %v", acquireErr)
	}
}

func TestRetirementPageServiceReceiptReplayHoldsApplyCoordinatorThroughProjection(t *testing.T) {
	fixture, _, request, _, owner := plannedOldOnlyRetirement(t)
	content := &retirementOperationContent{
		retirementContentFixture: &fixture.content,
		schema:                   fixture.schema,
		stages:                   make(map[knowl.OperationID]knowl.StagedChange),
	}
	store, err := sqlite.Open(t.Context(), t.TempDir()+"/retirement-replay-apply-fence.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	coordinator := newTestApplyCoordinator()
	committer := &coordinatedRetirementOperationCommitter{
		coordinator: coordinator,
		delegate:    &retirementOperationCommitter{},
	}
	firstIndex := &retirementProjection{failures: 1}
	service, err := app.NewRetirementPageService(content, store, firstIndex, fixture.lifecycle(), committer, &fixture.maintainer, app.RetirementPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := service.Reserve(t.Context(), request.Scope, owner, fixture.states[owner].RetirementGeneration, request.Page.ID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimOperation(t.Context(), request.Scope, reservation.ID, knowl.WorkLease{Token: "receipt-first", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunToTerminal(t.Context(), claim); !errors.Is(err, app.ErrProjection) {
		t.Fatalf("first RunToTerminal() error = %v, want projection failure", err)
	}
	if err := store.ReleaseClaim(t.Context(), request.Scope, reservation.ID, claim.Lease.Token); err != nil {
		t.Fatal(err)
	}

	index := &blockedRetirementProjection{entered: make(chan struct{}), resume: make(chan struct{})}
	service, err = app.NewRetirementPageService(content, store, index, fixture.lifecycle(), committer, &fixture.maintainer, app.RetirementPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.ClaimOperation(t.Context(), request.Scope, reservation.ID, knowl.WorkLease{Token: "receipt-replay", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	type runResult struct {
		result app.IngestResult
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		result, runErr := service.RunToTerminal(t.Context(), replay)
		done <- runResult{result: result, err: runErr}
	}()
	select {
	case <-index.entered:
	case <-time.After(time.Second):
		close(index.resume)
		t.Fatal("retirement receipt replay projection did not start")
	}

	waitCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	release, acquireErr := coordinator.Acquire(waitCtx)
	cancel()
	if release != nil {
		release()
	}
	close(index.resume)
	completed := <-done
	if completed.err != nil || completed.result.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("receipt replay RunToTerminal() = %#v, %v", completed.result, completed.err)
	}
	if !errors.Is(acquireErr, context.DeadlineExceeded) {
		t.Fatalf("competing canonical publication acquired coordinator during receipt replay projection: %v", acquireErr)
	}
	if committer.delegate.calls != 1 {
		t.Fatalf("receipt replay canonical commits = %d, want 1", committer.delegate.calls)
	}
}

func TestRetirementPageServiceReplaysOutcomeAfterCanonicalCommit(t *testing.T) {
	fixture, _, request, _, owner := plannedOldOnlyRetirement(t)
	content := &retirementOperationContent{
		retirementContentFixture: &fixture.content,
		schema:                   fixture.schema,
		stages:                   make(map[knowl.OperationID]knowl.StagedChange),
	}
	store, err := sqlite.Open(t.Context(), t.TempDir()+"/retirement-outcome.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	operations := &failOnceOutcomeStore{OperationStore: store}
	index := &retirementProjection{}
	committer := &retirementOperationCommitter{}
	service, err := app.NewRetirementPageService(content, operations, index, fixture.lifecycle(), committer, &fixture.maintainer, app.RetirementPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := service.Reserve(t.Context(), request.Scope, owner, fixture.states[owner].RetirementGeneration, request.Page.ID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimOperation(t.Context(), request.Scope, reservation.ID, knowl.WorkLease{Token: "outcome-first", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.RunToTerminal(t.Context(), claim)
	if !errors.Is(err, errOutcomeUnavailable) || first.Operation.Status != knowl.StatusApplying || first.Commit == nil {
		t.Fatalf("first RunToTerminal() = %#v, %v", first, err)
	}
	if err := store.ReleaseClaim(t.Context(), request.Scope, reservation.ID, claim.Lease.Token); err != nil {
		t.Fatal(err)
	}
	replay, err := store.ClaimOperation(t.Context(), request.Scope, reservation.ID, knowl.WorkLease{Token: "outcome-replay", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunToTerminal(t.Context(), replay)
	if err != nil || result.Operation.Status != knowl.StatusCommitted || result.Commit == nil || committer.calls != 1 || index.calls != 2 || len(content.stagePlans) != 1 {
		t.Fatalf("replayed RunToTerminal() = %#v, %v; commits=%d projections=%d stages=%d", result, err, committer.calls, index.calls, len(content.stagePlans))
	}
}

func TestRetirementPageServicePersistsPlanningDiagnostics(t *testing.T) {
	fixture, _, request, _, owner := plannedOldOnlyRetirement(t)
	content := &retirementOperationContent{
		retirementContentFixture: &fixture.content,
		schema:                   fixture.schema,
		stages:                   make(map[knowl.OperationID]knowl.StagedChange),
	}
	store, err := sqlite.Open(t.Context(), t.TempDir()+"/retirement-planning-diagnostic.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := app.NewRetirementPageService(content, store, &retirementProjection{}, fixture.lifecycle(), &retirementOperationCommitter{}, &fixture.maintainer, app.RetirementPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := service.Reserve(t.Context(), request.Scope, owner, fixture.states[owner].RetirementGeneration, request.Page.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := fixture.states[owner]
	changed.RetirementGeneration = strings.Repeat("e", 64)
	fixture.states[owner] = changed
	claim, err := store.ClaimOperation(t.Context(), request.Scope, reservation.ID, knowl.WorkLease{Token: "planning", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunToTerminal(t.Context(), claim)
	var failure *app.RetirementOperationError
	want := knowl.MaintenanceDiagnostic{Code: app.RetirementLifecycleConflictCode, Path: request.Page.Path}
	if !errors.As(err, &failure) || failure.Code != app.RetirementLifecycleConflictCode || !slices.Contains(result.Operation.Diagnostics, want) {
		t.Fatalf("RunToTerminal() = %#v, %v; want diagnostic %#v", result, err, want)
	}
}

func TestRetirementPageServiceCommitsMissingPageAsIdempotentNoOp(t *testing.T) {
	fixture, _, request, _, owner := plannedOldOnlyRetirement(t)
	content := &retirementOperationContent{
		retirementContentFixture: &fixture.content,
		schema:                   fixture.schema,
		stages:                   make(map[knowl.OperationID]knowl.StagedChange),
		missingPage:              true,
	}
	store, err := sqlite.Open(t.Context(), t.TempDir()+"/retirement-noop.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	index := &retirementProjection{}
	committer := &retirementOperationCommitter{}
	service, err := app.NewRetirementPageService(content, store, index, fixture.lifecycle(), committer, &fixture.maintainer, app.RetirementPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := service.Reserve(t.Context(), request.Scope, owner, fixture.states[owner].RetirementGeneration, request.Page.ID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimOperation(t.Context(), request.Scope, reservation.ID, knowl.WorkLease{Token: "noop", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunToTerminal(t.Context(), claim)
	if err != nil || result.Operation.Status != knowl.StatusCommitted || result.Operation.Plan == nil || result.Operation.Plan.FileCount == nil || *result.Operation.Plan.FileCount != 0 {
		t.Fatalf("RunToTerminal() = %#v, %v", result, err)
	}
	if committer.calls != 0 || index.calls != 0 || len(content.stagePlans) != 0 {
		t.Fatalf("no-op mutated content: commits=%d projections=%d stages=%d", committer.calls, index.calls, len(content.stagePlans))
	}
	receipt, err := service.RunToTerminal(t.Context(), claim)
	if err != nil || receipt.Operation.Status != knowl.StatusCommitted || committer.calls != 0 || index.calls != 0 {
		t.Fatalf("receipt replay = %#v, %v; commits=%d projections=%d", receipt, err, committer.calls, index.calls)
	}
}

func TestRetirementPageServicesPreserveIndependentProgressAndRetryOnlyFailedPage(t *testing.T) {
	failedFixture, _, failedRequest, _, owner := plannedOldOnlyRetirement(t)
	committedFixture := newRetirementFixture(t)
	committedSource := retirementAccepted("wiki-filesystem", "second-old", "second.md", "v1", "text/markdown")
	committedOwner := retirementOwner(committedSource)
	committedFixture.page = retirementPage(t, "concepts/second", []string{app.SourceRefKey(committedSource)}, "Old facts.")
	committedFixture.states[committedOwner] = retirementLifecycle(committedOwner, knowl.SourceLifecycleRetiring, committedFixture.authority)
	committedRequest := committedFixture.request([]knowl.AcceptedSource{committedSource}, committedOwner)

	store, err := sqlite.Open(t.Context(), t.TempDir()+"/retirement-independent.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	failedContent := &retirementOperationContent{
		retirementContentFixture: &failedFixture.content, schema: failedFixture.schema,
		stages: make(map[knowl.OperationID]knowl.StagedChange), stageErr: errors.New("injected stage failure"),
	}
	committedContent := &retirementOperationContent{
		retirementContentFixture: &committedFixture.content, schema: committedFixture.schema,
		stages: make(map[knowl.OperationID]knowl.StagedChange),
	}
	failedCommitter, committedCommitter := &retirementOperationCommitter{}, &retirementOperationCommitter{}
	failedService, err := app.NewRetirementPageService(failedContent, store, &retirementProjection{}, failedFixture.lifecycle(), failedCommitter, &failedFixture.maintainer, app.RetirementPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	committedService, err := app.NewRetirementPageService(committedContent, store, &retirementProjection{}, committedFixture.lifecycle(), committedCommitter, &committedFixture.maintainer, app.RetirementPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	failedReservation, err := failedService.Reserve(t.Context(), failedRequest.Scope, owner, failedFixture.states[owner].RetirementGeneration, failedRequest.Page.ID)
	if err != nil {
		t.Fatal(err)
	}
	committedReservation, err := committedService.Reserve(t.Context(), committedRequest.Scope, committedOwner, committedFixture.states[committedOwner].RetirementGeneration, committedRequest.Page.ID)
	if err != nil {
		t.Fatal(err)
	}
	failedClaim, err := store.ClaimOperation(t.Context(), failedRequest.Scope, failedReservation.ID, knowl.WorkLease{Token: "failed", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	failedResult, runErr := failedService.RunToTerminal(t.Context(), failedClaim)
	var retirementFailure *app.RetirementOperationError
	if !errors.As(runErr, &retirementFailure) || failedResult.Operation.Status != knowl.StatusReceived || failedCommitter.calls != 0 {
		t.Fatalf("failed page = %#v, %v; commits=%d", failedResult, runErr, failedCommitter.calls)
	}
	if !slices.Contains(failedResult.Operation.Diagnostics, knowl.MaintenanceDiagnostic{Code: app.RetirementStagingFailedCode, Path: failedRequest.Page.Path}) {
		t.Fatalf("failed diagnostics = %#v", failedResult.Operation.Diagnostics)
	}
	committedClaim, err := store.ClaimOperation(t.Context(), committedRequest.Scope, committedReservation.ID, knowl.WorkLease{Token: "committed", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	committedResult, err := committedService.RunToTerminal(t.Context(), committedClaim)
	if err != nil || committedResult.Operation.Status != knowl.StatusCommitted || committedCommitter.calls != 1 {
		t.Fatalf("independent page = %#v, %v; commits=%d", committedResult, err, committedCommitter.calls)
	}
	if err := store.ReleaseClaim(t.Context(), failedRequest.Scope, failedReservation.ID, failedClaim.Lease.Token); err != nil {
		t.Fatal(err)
	}
	failedContent.stageErr = nil
	retryClaim, err := store.ClaimOperation(t.Context(), failedRequest.Scope, failedReservation.ID, knowl.WorkLease{Token: "retry", ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := failedService.RunToTerminal(t.Context(), retryClaim)
	if err != nil || retried.Operation.Status != knowl.StatusCommitted || failedCommitter.calls != 1 || committedCommitter.calls != 1 {
		t.Fatalf("retry = %#v, %v; failed commits=%d independent commits=%d", retried, err, failedCommitter.calls, committedCommitter.calls)
	}
}

type retirementOperationContent struct {
	*retirementContentFixture
	schema      knowl.SchemaDocument
	stages      map[knowl.OperationID]knowl.StagedChange
	stagePlans  []knowl.ValidatedEditPlan
	missingPage bool
	stageErr    error
}

func (content *retirementOperationContent) ReadPages(ctx context.Context, scope knowl.ScopeRef, ids []knowl.PageID, limits knowl.ReadLimits) ([]knowl.PageSnapshot, error) {
	if content.missingPage {
		return nil, nil
	}
	return content.retirementContentFixture.ReadPages(ctx, scope, ids, limits)
}

func (content *retirementOperationContent) Schema(context.Context, knowl.ScopeRef) (knowl.SchemaDocument, error) {
	return content.schema, nil
}

func (content *retirementOperationContent) Inspect(context.Context, knowl.ScopeRef) (knowl.WorkspaceInspection, error) {
	catalogs := make([]knowl.PageSnapshot, 0, len(content.catalogs))
	for _, catalog := range content.catalogs {
		catalogs = append(catalogs, cloneRetirementPage(catalog))
	}
	slices.SortFunc(catalogs, func(left, right knowl.PageSnapshot) int {
		if left.Path < right.Path {
			return -1
		}
		if left.Path > right.Path {
			return 1
		}
		return 0
	})
	return knowl.WorkspaceInspection{Scope: retirementTestScope, Catalogs: catalogs}, nil
}

func (content *retirementOperationContent) StagePlan(_ context.Context, plan knowl.ValidatedEditPlan) (knowl.StagedChange, error) {
	if content.stageErr != nil {
		return knowl.StagedChange{}, content.stageErr
	}
	digest, err := app.PlanDigest(plan)
	if err != nil {
		return knowl.StagedChange{}, err
	}
	staged := knowl.StagedChange{
		OperationID: plan.OperationID, Digest: digest, Diagnostics: slices.Clone(plan.Diagnostics),
		Retirement: plan.Retirement,
	}
	for _, edit := range plan.Edits {
		staged.Files = append(staged.Files, edit.Path)
	}
	content.stagePlans = append(content.stagePlans, plan)
	content.stages[knowl.OperationID(plan.OperationID)] = staged
	return staged, nil
}

func (content *retirementOperationContent) LoadStage(_ context.Context, _ knowl.ScopeRef, id knowl.OperationID) (knowl.StagedChange, error) {
	staged, ok := content.stages[id]
	if !ok {
		return knowl.StagedChange{}, app.ErrStageNotFound
	}
	return staged, nil
}

func (content *retirementOperationContent) Snapshot(context.Context, knowl.ScopeRef) (knowl.WorkspaceSnapshot, error) {
	return knowl.WorkspaceSnapshot{Scope: retirementTestScope, SchemaDigest: content.schema.Digest, PageDigests: map[string]string{}}, nil
}

type retirementOperationCommitter struct {
	calls   int
	first   knowl.StagedChange
	last    knowl.StagedChange
	err     error
	receipt *knowl.ContentCommit
}

type coordinatedRetirementOperationCommitter struct {
	coordinator app.ApplyCoordinator
	delegate    *retirementOperationCommitter
}

func (committer *coordinatedRetirementOperationCommitter) RunRetirementApply(
	ctx context.Context,
	staged knowl.StagedChange,
	prepare func(context.Context) (bool, error),
	complete func(context.Context, knowl.ContentCommit) error,
) (knowl.ContentCommit, error) {
	release, err := committer.coordinator.Acquire(ctx)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	defer release()
	return committer.delegate.RunRetirementApply(ctx, staged, prepare, complete)
}

func (committer *coordinatedRetirementOperationCommitter) CommittedRetirement(ctx context.Context, staged knowl.StagedChange) (knowl.ContentCommit, bool, error) {
	return committer.delegate.CommittedRetirement(ctx, staged)
}

func (committer *coordinatedRetirementOperationCommitter) CommitRetirement(ctx context.Context, staged knowl.StagedChange) (knowl.ContentCommit, error) {
	release, err := committer.coordinator.Acquire(ctx)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	defer release()
	return committer.delegate.CommitRetirement(ctx, staged)
}

func (committer *retirementOperationCommitter) CommittedRetirement(_ context.Context, staged knowl.StagedChange) (knowl.ContentCommit, bool, error) {
	if committer.receipt == nil {
		return knowl.ContentCommit{}, false, nil
	}
	if committer.receipt.OperationID != staged.OperationID || committer.receipt.Generation != staged.Digest {
		return knowl.ContentCommit{}, false, app.ErrRetirementLifecycleConflict
	}
	return *committer.receipt, true, nil
}

func (committer *retirementOperationCommitter) CommitRetirement(_ context.Context, staged knowl.StagedChange) (knowl.ContentCommit, error) {
	if committer.err != nil {
		return knowl.ContentCommit{}, committer.err
	}
	committer.calls++
	if committer.calls == 1 {
		committer.first = staged
	}
	committer.last = staged
	commit := knowl.ContentCommit{OperationID: staged.OperationID, Generation: staged.Digest, Files: slices.Clone(staged.Files), Diagnostics: slices.Clone(staged.Diagnostics)}
	committer.receipt = &commit
	return commit, nil
}

func (committer *retirementOperationCommitter) RunRetirementApply(
	ctx context.Context,
	staged knowl.StagedChange,
	prepare func(context.Context) (bool, error),
	complete func(context.Context, knowl.ContentCommit) error,
) (knowl.ContentCommit, error) {
	proceed, err := prepare(ctx)
	if err != nil || !proceed {
		return knowl.ContentCommit{}, err
	}
	commit, committed, err := committer.CommittedRetirement(ctx, staged)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	if !committed {
		commit, err = committer.CommitRetirement(ctx, staged)
		if err != nil {
			return knowl.ContentCommit{}, err
		}
	}
	return commit, complete(ctx, commit)
}

type retirementProjection struct {
	failures int
	calls    int
	commits  []knowl.ContentCommit
}

type blockedRetirementProjection struct {
	retirementProjection
	entered chan struct{}
	resume  chan struct{}
}

func (index *blockedRetirementProjection) Project(ctx context.Context, commit knowl.ContentCommit) error {
	close(index.entered)
	select {
	case <-index.resume:
		return index.retirementProjection.Project(ctx, commit)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*retirementProjection) SelectContext(context.Context, knowl.ScopeRef, knowl.SourceSummary, knowl.ReadLimits) ([]knowl.PageID, error) {
	return nil, nil
}
func (*retirementProjection) Search(context.Context, knowl.ScopeRef, string, knowl.ReadLimits, []knowl.SourceID) ([]knowl.PageReference, error) {
	return nil, nil
}
func (*retirementProjection) Links(context.Context, knowl.ScopeRef, knowl.PageID, knowl.ReadLimits) ([]knowl.LinkReference, error) {
	return nil, nil
}
func (index *retirementProjection) Project(_ context.Context, commit knowl.ContentCommit) error {
	index.calls++
	index.commits = append(index.commits, commit)
	if index.failures > 0 {
		index.failures--
		return errors.New("injected projection failure")
	}
	return nil
}
func (*retirementProjection) Rebuild(context.Context, knowl.WorkspaceSnapshot) error { return nil }
