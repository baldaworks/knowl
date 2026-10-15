package app_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const retirementCoordinatorScope knowl.ScopeRef = "retirement-coordinator"
const (
	retirementCoordinatorPageA         = "concepts/a"
	retirementCoordinatorPageB         = "concepts/b"
	retirementCoordinatorDiscoveryNext = "discovery-next"
	retirementCoordinatorVerifyNext    = "verify-next"
)

var retirementCoordinatorOwner = knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "legacy"}

type retirementCoordinatorLifecycleStore struct {
	state       knowl.SourceLifecycle
	authority   knowl.SourceLifecycleAuthority
	states      []knowl.SourceLifecycle
	updates     []app.SourceRetirementUpdate
	finalized   []app.SourceRetirementFinalization
	updateError error
}

func (store *retirementCoordinatorLifecycleStore) ReconcileSourceLifecycles(context.Context, app.SourceLifecycleReconcileRequest) error {
	return nil
}

func (store *retirementCoordinatorLifecycleStore) SourceLifecycleAuthority(context.Context, knowl.ScopeRef) (knowl.SourceLifecycleAuthority, error) {
	return store.authority, nil
}

func (store *retirementCoordinatorLifecycleStore) SourceLifecycle(_ context.Context, _ knowl.ScopeRef, owner knowl.SourceOwner) (knowl.SourceLifecycle, error) {
	if owner == store.state.Owner {
		return store.state, nil
	}
	for _, state := range store.states {
		if state.Owner == owner {
			return state, nil
		}
	}
	return knowl.SourceLifecycle{}, app.ErrSourceLifecycleUnowned
}

func (store *retirementCoordinatorLifecycleStore) SourceLifecycles(_ context.Context, _ knowl.ScopeRef, options app.SourceLifecycleListOptions) ([]knowl.SourceLifecycle, error) {
	result := make([]knowl.SourceLifecycle, 0, len(store.states))
	for _, state := range store.states {
		if options.State != "" && state.State != options.State {
			continue
		}
		if options.After != (knowl.SourceOwner{}) && !retirementCoordinatorOwnerAfter(state.Owner, options.After) {
			continue
		}
		result = append(result, state)
		if len(result) == options.Limit {
			break
		}
	}
	return result, nil
}

func (store *retirementCoordinatorLifecycleStore) MarkSourceLifecycleReady(context.Context, app.SourceLifecycleReadyUpdate) (knowl.SourceLifecycle, error) {
	return knowl.SourceLifecycle{}, errors.New("unexpected readiness update")
}

func (store *retirementCoordinatorLifecycleStore) UpdateSourceRetirement(_ context.Context, update app.SourceRetirementUpdate) (knowl.SourceLifecycle, error) {
	store.updates = append(store.updates, update)
	if store.updateError != nil {
		return knowl.SourceLifecycle{}, store.updateError
	}
	if update.ExpectedCursor != store.state.ScanCursor {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	store.state.ScanCursor = update.Cursor
	store.state.ScanRoundMatches = update.RoundMatches
	store.state.ScanComplete = update.ScanComplete
	store.state.BlockerCode = update.BlockerCode
	store.state.UpdatedAt = update.UpdatedAt
	store.replaceState()
	return store.state, nil
}

func (store *retirementCoordinatorLifecycleStore) FinalizeSourceRetirement(_ context.Context, finalization app.SourceRetirementFinalization) (knowl.SourceLifecycle, error) {
	store.finalized = append(store.finalized, finalization)
	if store.state.State != knowl.SourceLifecycleRetiring || !store.state.ScanComplete || store.state.ScanRoundMatches != 0 {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleConflict
	}
	store.state.State = knowl.SourceLifecycleDeleted
	store.state.DeletedAt = finalization.FinalizedAt
	store.state.UpdatedAt = finalization.FinalizedAt
	store.replaceState()
	return store.state, nil
}

func (store *retirementCoordinatorLifecycleStore) replaceState() {
	for index := range store.states {
		if store.states[index].Owner == store.state.Owner {
			store.states[index] = store.state
		}
	}
}

type retirementCoordinatorPager struct {
	pages map[app.CanonicalProvenanceCursor][]app.CanonicalProvenancePage
	calls []app.CanonicalProvenanceCursor
}

func (pager *retirementCoordinatorPager) PageProvenance(_ context.Context, _ knowl.ScopeRef, _ knowl.SourceOwner, cursor app.CanonicalProvenanceCursor, _ app.CanonicalProvenanceLimits) (app.CanonicalProvenancePage, error) {
	pager.calls = append(pager.calls, cursor)
	pages := pager.pages[cursor]
	if len(pages) == 0 {
		return app.CanonicalProvenancePage{}, errors.New("unexpected provenance cursor")
	}
	page := pages[0]
	pager.pages[cursor] = pages[1:]
	return page, nil
}

type retirementCoordinatorReserver struct {
	ids       []knowl.PageID
	failOn    knowl.PageID
	operation map[knowl.PageID]knowl.OperationID
}

func (reserver *retirementCoordinatorReserver) Reserve(_ context.Context, _ knowl.ScopeRef, _ knowl.SourceOwner, _ string, pageID knowl.PageID) (app.OperationReservation, error) {
	reserver.ids = append(reserver.ids, pageID)
	if pageID == reserver.failOn {
		return app.OperationReservation{}, errors.New("reserve failed")
	}
	id := knowl.OperationID("retire:" + string(pageID))
	if existing := reserver.operation[pageID]; existing != "" {
		id = existing
	} else {
		reserver.operation[pageID] = id
	}
	return app.OperationReservation{Operation: knowl.Operation{ID: id, Kind: knowl.WorkSourceRetirementPage, Status: knowl.StatusReceived}}, nil
}

type retirementCoordinatorOperations struct {
	records []app.RetirementOperationRecord
	retried []knowl.OperationID
}

func (store *retirementCoordinatorOperations) RetirementOperations(_ context.Context, request app.RetirementOperationReadRequest) (app.RetirementOperationPage, error) {
	page := app.RetirementOperationPage{Items: make([]app.RetirementOperationRecord, 0)}
	for _, record := range store.records {
		if record.Owner != request.Owner || record.RetirementGeneration != request.RetirementGeneration || string(record.Operation.ID) <= string(request.After) {
			continue
		}
		page.Items = append(page.Items, record)
		if len(page.Items) == request.Limit {
			break
		}
	}
	if len(page.Items) == request.Limit {
		page.Next = page.Items[len(page.Items)-1].Operation.ID
	}
	return page, nil
}

func (store *retirementCoordinatorOperations) RetryRetirementOperation(_ context.Context, scope knowl.ScopeRef, id knowl.OperationID, generation string, now time.Time) (bool, error) {
	for index := range store.records {
		record := &store.records[index]
		if record.Operation.ID != id || record.RetirementGeneration != generation {
			continue
		}
		if record.Operation.Status != knowl.StatusFailed {
			return false, nil
		}
		record.Operation.Status = knowl.StatusReceived
		record.Operation.Failure = nil
		record.Operation.Diagnostics = nil
		record.Operation.ReadyAt = now
		record.Operation.UpdatedAt = now
		record.Operation.ManualRetryCount++
		store.retried = append(store.retried, id)
		return true, nil
	}
	return false, nil
}

type retirementCoordinatorProjection struct{ pending map[knowl.OperationID]bool }

func (projection retirementCoordinatorProjection) RetirementProjectionPending(_ context.Context, _ knowl.ScopeRef, id knowl.OperationID) (bool, error) {
	return projection.pending[id], nil
}

type retirementCoordinatorFence struct{ held, acquisitions int }

func (fence *retirementCoordinatorFence) Acquire(context.Context) (func(), error) {
	fence.held++
	fence.acquisitions++
	return func() { fence.held-- }, nil
}

func TestRetirementCoordinatorReservesWholeWindowBeforeCursorCAS(t *testing.T) {
	fixture := newRetirementCoordinatorFixture()
	window := app.CanonicalProvenancePage{
		Items:      []app.CanonicalPageProvenance{{ID: retirementCoordinatorPageA}, {ID: retirementCoordinatorPageB}},
		NextCursor: "next", Returned: 2,
	}
	fixture.pager.pages[""] = []app.CanonicalProvenancePage{window, window}
	fixture.reserver.failOn = retirementCoordinatorPageB

	_, err := fixture.coordinator().AdvanceOwner(t.Context(), retirementCoordinatorOwner)
	if err == nil {
		t.Fatal("AdvanceOwner() succeeded despite incomplete reservation")
	}
	if fixture.lifecycle.state.ScanCursor != "" || len(fixture.lifecycle.updates) != 0 {
		t.Fatalf("cursor advanced before all reservations: state=%#v updates=%#v", fixture.lifecycle.state, fixture.lifecycle.updates)
	}

	fixture.reserver.failOn = ""
	result, err := fixture.coordinator().AdvanceOwner(t.Context(), retirementCoordinatorOwner)
	if err != nil || result.Reserved != 2 || fixture.lifecycle.state.ScanCursor != "next" {
		t.Fatalf("replayed window = %#v, err=%v, state=%#v", result, err, fixture.lifecycle.state)
	}
	if got := fixture.reserver.ids; !slices.Equal(got, []knowl.PageID{retirementCoordinatorPageA, retirementCoordinatorPageB, retirementCoordinatorPageA, retirementCoordinatorPageB}) {
		t.Fatalf("reservation replay order = %#v", got)
	}
}

func TestRetirementCoordinatorReplacementGateBlocksDiscovery(t *testing.T) {
	fixture := newRetirementCoordinatorFixture()
	fixture.lifecycle.state.ReplacementPending = 1
	fixture.lifecycle.state.BlockerCode = app.RetirementReplacementNotReadyCode
	fixture.lifecycle.states[0] = fixture.lifecycle.state

	result, err := fixture.coordinator().AdvanceOwner(t.Context(), retirementCoordinatorOwner)
	if !errors.Is(err, app.ErrSourceReplacementNotReady) || result.BlockerCode != app.RetirementReplacementNotReadyCode {
		t.Fatalf("replacement gate = %#v, %v", result, err)
	}
	if len(fixture.pager.calls) != 0 || len(fixture.reserver.ids) != 0 {
		t.Fatalf("blocked replacement touched discovery: calls=%#v reservations=%#v", fixture.pager.calls, fixture.reserver.ids)
	}
}

func TestRetirementCoordinatorRotatesBoundedOwnerWindows(t *testing.T) {
	fixture := newRetirementCoordinatorFixture()
	fixture.lifecycle.states = make([]knowl.SourceLifecycle, 129)
	for index := range fixture.lifecycle.states {
		state := fixture.lifecycle.state
		state.Owner.SourceID = knowl.SourceID(fmt.Sprintf("legacy-%03d", index))
		state.ReplacementPending = 1
		fixture.lifecycle.states[index] = state
	}
	fixture.lifecycle.state = fixture.lifecycle.states[0]
	coordinator := fixture.coordinator()

	first, err := coordinator.Advance(t.Context())
	if err != nil || len(first) != 128 || first[0].Owner != fixture.lifecycle.states[0].Owner || first[127].Owner != fixture.lifecycle.states[127].Owner {
		t.Fatalf("first owner window = %d results, first=%+v last=%+v, err=%v", len(first), first[0].Owner, first[len(first)-1].Owner, err)
	}
	second, err := coordinator.Advance(t.Context())
	if err != nil || len(second) != 1 || second[0].Owner != fixture.lifecycle.states[128].Owner {
		t.Fatalf("second owner window = %+v, err=%v", second, err)
	}
}

func TestRetirementCoordinatorRestartsVerificationAndFinalizesAfterMultiWindowZero(t *testing.T) {
	fixture := newRetirementCoordinatorFixture()
	fixture.pager.pages[""] = []app.CanonicalProvenancePage{
		{Items: []app.CanonicalPageProvenance{{ID: retirementCoordinatorPageA}}, NextCursor: retirementCoordinatorDiscoveryNext, Returned: 1},
		{NextCursor: retirementCoordinatorVerifyNext, Examined: 32},
	}
	fixture.pager.pages[retirementCoordinatorDiscoveryNext] = []app.CanonicalProvenancePage{{EOF: true, Authoritative: true, Examined: 1}}
	fixture.pager.pages[retirementCoordinatorVerifyNext] = []app.CanonicalProvenancePage{
		{EOF: true, Authoritative: true, Examined: 1},
		{EOF: true, Authoritative: true, Examined: 1},
	}

	coordinator := fixture.coordinator()
	first, err := coordinator.AdvanceOwner(t.Context(), retirementCoordinatorOwner)
	if err != nil || first.Reserved != 1 || fixture.lifecycle.state.ScanCursor != retirementCoordinatorDiscoveryNext {
		t.Fatalf("first discovery = %#v, %v, state=%#v", first, err, fixture.lifecycle.state)
	}
	fixture.operations.records = []app.RetirementOperationRecord{{
		Operation: knowl.Operation{ID: "retire:concepts/a", Kind: knowl.WorkSourceRetirementPage, Status: knowl.StatusCommitted},
		Owner:     retirementCoordinatorOwner, RetirementGeneration: fixture.lifecycle.state.RetirementGeneration, PageID: retirementCoordinatorPageA,
	}}
	second, err := coordinator.AdvanceOwner(t.Context(), retirementCoordinatorOwner)
	if err != nil || !second.DiscoveryEOF || fixture.lifecycle.state.ScanRoundMatches != 1 {
		t.Fatalf("discovery EOF = %#v, %v, state=%#v", second, err, fixture.lifecycle.state)
	}
	third, err := coordinator.AdvanceOwner(t.Context(), retirementCoordinatorOwner)
	if err != nil || !third.VerificationRestarted || fixture.lifecycle.state.ScanComplete || fixture.lifecycle.state.ScanCursor != "" || fixture.lifecycle.state.ScanRoundMatches != 0 {
		t.Fatalf("verification reset = %#v, %v, state=%#v", third, err, fixture.lifecycle.state)
	}
	fourth, err := coordinator.AdvanceOwner(t.Context(), retirementCoordinatorOwner)
	if err != nil || fixture.lifecycle.state.ScanCursor != retirementCoordinatorVerifyNext || fixture.lifecycle.state.ScanComplete {
		t.Fatalf("verification window = %#v, %v, state=%#v", fourth, err, fixture.lifecycle.state)
	}
	final, err := coordinator.AdvanceOwner(t.Context(), retirementCoordinatorOwner)
	if err != nil || !final.Finalized || fixture.lifecycle.state.State != knowl.SourceLifecycleDeleted || len(fixture.lifecycle.finalized) != 1 {
		t.Fatalf("finalization = %#v, %v, state=%#v", final, err, fixture.lifecycle.state)
	}
	if fixture.fence.acquisitions != 1 || fixture.fence.held != 0 {
		t.Fatalf("final fence acquisitions=%d held=%d", fixture.fence.acquisitions, fixture.fence.held)
	}
	if got := fixture.pager.calls; !slices.Equal(got, []app.CanonicalProvenanceCursor{"", retirementCoordinatorDiscoveryNext, "", retirementCoordinatorVerifyNext, retirementCoordinatorVerifyNext}) {
		t.Fatalf("pager cursors = %#v", got)
	}
}

func TestRetirementCoordinatorStatusAndTargetedRetry(t *testing.T) {
	fixture := newRetirementCoordinatorFixture()
	now := fixture.now
	fixture.lifecycle.state.ScanComplete = true
	fixture.lifecycle.state.ScanRoundMatches = 2
	fixture.lifecycle.state.ScanCursor = "opaque-internal-path"
	fixture.lifecycle.states[0] = fixture.lifecycle.state
	fixture.operations.records = []app.RetirementOperationRecord{
		{Operation: knowl.Operation{ID: "a", Kind: knowl.WorkSourceRetirementPage, Status: knowl.StatusFailed, Diagnostics: []knowl.MaintenanceDiagnostic{{Code: app.RetirementPageStaleCode, Path: "wiki/concepts/a.md"}}, UpdatedAt: now}, Owner: retirementCoordinatorOwner, RetirementGeneration: fixture.lifecycle.state.RetirementGeneration, PageID: retirementCoordinatorPageA},
		{Operation: knowl.Operation{ID: "b", Kind: knowl.WorkSourceRetirementPage, Status: knowl.StatusApplying, UpdatedAt: now}, Owner: retirementCoordinatorOwner, RetirementGeneration: fixture.lifecycle.state.RetirementGeneration, PageID: retirementCoordinatorPageB},
		{Operation: knowl.Operation{ID: "c", Kind: knowl.WorkSourceRetirementPage, Status: knowl.StatusCommitted, UpdatedAt: now}, Owner: retirementCoordinatorOwner, RetirementGeneration: fixture.lifecycle.state.RetirementGeneration, PageID: "concepts/c"},
	}
	fixture.projection.pending = map[knowl.OperationID]bool{"b": true}
	coordinator := fixture.coordinator()

	status, err := coordinator.Status(t.Context(), retirementCoordinatorOwner)
	if err != nil {
		t.Fatal(err)
	}
	if status.Owner != retirementCoordinatorOwner || status.State != knowl.SourceLifecycleRetiring || !status.Discovery.EOF || status.Discovery.RemainingReferences != 2 ||
		len(status.Discovery.Cursor) != 64 || status.Discovery.Cursor == fixture.lifecycle.state.ScanCursor || status.BlockerCode != app.RetirementWorkFailedCode ||
		status.Operations.Failed != 1 || status.Operations.Applying != 1 || status.Operations.ProjectionPending != 1 || status.Operations.Committed != 1 ||
		len(status.Diagnostics) != 1 || status.Diagnostics[0].Path != "wiki/concepts/a.md" || status.DiagnosticsTruncated {
		t.Fatalf("retirement status = %#v", status)
	}

	retry, err := coordinator.Retry(t.Context(), app.SourceRetirementRetryRequest{Owner: retirementCoordinatorOwner, PageID: retirementCoordinatorPageA})
	if err != nil || retry.Matched != 1 || retry.Requeued != 1 || retry.Truncated || !slices.Equal(retry.OperationIDs, []knowl.OperationID{"a"}) {
		t.Fatalf("targeted retry = %#v, %v", retry, err)
	}
	if !slices.Equal(fixture.operations.retried, []knowl.OperationID{"a"}) || fixture.operations.records[2].Operation.Status != knowl.StatusCommitted {
		t.Fatalf("retry mutations = %#v records=%#v", fixture.operations.retried, fixture.operations.records)
	}
}

type retirementCoordinatorFixture struct {
	now        time.Time
	lifecycle  *retirementCoordinatorLifecycleStore
	pager      *retirementCoordinatorPager
	reserver   *retirementCoordinatorReserver
	operations *retirementCoordinatorOperations
	projection retirementCoordinatorProjection
	fence      *retirementCoordinatorFence
}

func newRetirementCoordinatorFixture() *retirementCoordinatorFixture {
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	generation := strings.Repeat("a", 64)
	state := knowl.SourceLifecycle{
		Scope: retirementCoordinatorScope, Owner: retirementCoordinatorOwner, State: knowl.SourceLifecycleRetiring,
		ConfigDigest: strings.Repeat("b", 64), AuthorityGeneration: generation, AuthorityEpoch: 2,
		ActivatedGeneration: strings.Repeat("c", 64), ActivatedEpoch: 1,
		RetirementGeneration: generation, RetirementEpoch: 2,
		ActivatedAt: now.Add(-2 * time.Hour), RetiringAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute),
	}
	return &retirementCoordinatorFixture{
		now: now,
		lifecycle: &retirementCoordinatorLifecycleStore{
			state: state, states: []knowl.SourceLifecycle{state},
			authority: knowl.SourceLifecycleAuthority{Scope: retirementCoordinatorScope, Generation: generation, Epoch: 2, UpdatedAt: now.Add(-time.Minute)},
		},
		pager:      &retirementCoordinatorPager{pages: make(map[app.CanonicalProvenanceCursor][]app.CanonicalProvenancePage)},
		reserver:   &retirementCoordinatorReserver{operation: make(map[knowl.PageID]knowl.OperationID)},
		operations: &retirementCoordinatorOperations{},
		projection: retirementCoordinatorProjection{pending: make(map[knowl.OperationID]bool)},
		fence:      &retirementCoordinatorFence{},
	}
}

func (fixture *retirementCoordinatorFixture) coordinator() *app.SourceRetirementCoordinator {
	coordinator, err := app.NewSourceRetirementCoordinator(app.SourceRetirementCoordinatorDependencies{
		Scope: fixture.lifecycle.state.Scope, Lifecycles: fixture.lifecycle, Provenance: fixture.pager,
		Pages: fixture.reserver, Operations: fixture.operations, Projection: fixture.projection,
		ApplyCoordinator: fixture.fence, Now: func() time.Time { return fixture.now },
	})
	if err != nil {
		panic(err)
	}
	return coordinator
}

func retirementCoordinatorOwnerAfter(owner, after knowl.SourceOwner) bool {
	return owner.Adapter > after.Adapter || owner.Adapter == after.Adapter && owner.SourceID > after.SourceID
}
