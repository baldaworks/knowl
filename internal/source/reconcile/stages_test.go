package reconcile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/baldaworks/knowl/internal/source/filesystem"
	"github.com/baldaworks/knowl/pkg/knowl/content/fs"
	sqlitestore "github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
)

// scriptedAdapter serves one queued paged listing per synchronization attempt
// while counting every selective Fetch invocation.
type scriptedAdapter struct {
	listings  [][]knowl.DocumentPage
	documents map[knowl.DocumentID][]byte
	failFetch map[knowl.DocumentID]error
	fetches   int
	fetched   map[knowl.DocumentID]int
}

func newScriptedAdapter() *scriptedAdapter {
	return &scriptedAdapter{
		documents: make(map[knowl.DocumentID][]byte),
		failFetch: make(map[knowl.DocumentID]error),
		fetched:   make(map[knowl.DocumentID]int),
	}
}

func (adapter *scriptedAdapter) script(id knowl.DocumentID, body string) {
	adapter.documents[id] = []byte(body)
}

// enqueue lists one attempt's pages in order; an empty NextPageToken ends it.
func (adapter *scriptedAdapter) enqueue(pages ...knowl.DocumentPage) {
	adapter.listings = append(adapter.listings, pages)
}

func (adapter *scriptedAdapter) page(documents []knowl.DocumentRef, next string) knowl.DocumentPage {
	return knowl.DocumentPage{Documents: append([]knowl.DocumentRef(nil), documents...), NextPageToken: next}
}

func (adapter *scriptedAdapter) List(context.Context, knowl.Source, string) (knowl.DocumentPage, error) {
	if len(adapter.listings) == 0 {
		return knowl.DocumentPage{}, nil
	}
	pages := adapter.listings[0]
	page := pages[0]
	if len(pages) == 1 {
		adapter.listings = adapter.listings[1:]
	} else {
		adapter.listings[0] = pages[1:]
	}
	return page, nil
}

func (adapter *scriptedAdapter) Fetch(_ context.Context, _ knowl.Source, ref knowl.DocumentRef) (knowl.Document, error) {
	adapter.fetches++
	adapter.fetched[ref.ExternalID]++
	if failure, scripted := adapter.failFetch[ref.ExternalID]; scripted {
		return knowl.Document{}, failure
	}
	content, known := adapter.documents[ref.ExternalID]
	if !known {
		return knowl.Document{}, fmt.Errorf("unscripted document %q", ref.ExternalID)
	}
	return knowl.Document{
		DocumentRef: ref,
		Title:       "Scripted",
		URI:         "https://wiki.example.test/" + ref.Path,
		MediaType:   mediaTypeFor(ref.Path),
		Content:     content,
	}, nil
}

func mediaTypeFor(path string) string {
	if strings.HasSuffix(path, ".md") {
		return "text/markdown"
	}
	return "application/octet-stream"
}

var _ app.SourceAdapter = (*scriptedAdapter)(nil)

type stageHarness struct {
	service   *Service
	adapter   *scriptedAdapter
	state     app.SourceStateStore
	workspace *fs.Workspace
	scope     knowl.ScopeRef
	sourceID  knowl.SourceID
	queue     *recordingMaintenanceQueue
}

const stageScope = knowl.ScopeRef("stage_contract")

func newStageHarness(t *testing.T, mutate func(*Options)) *stageHarness {
	t.Helper()
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite state store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	workspace, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	adapter := newScriptedAdapter()
	queue := newRecordingMaintenanceQueue()
	options := Options{}
	if mutate != nil {
		mutate(&options)
	}
	harnessClock := time.Unix(1000, 0).UTC()
	var clockMutex sync.Mutex
	options.Clock = func() time.Time {
		clockMutex.Lock()
		defer clockMutex.Unlock()
		harnessClock = harnessClock.Add(time.Second)
		return harnessClock
	}
	runSequence := 0
	var runMutex sync.Mutex
	options.NewRunID = func() knowl.SyncRunID {
		runMutex.Lock()
		defer runMutex.Unlock()
		runSequence++
		return knowl.SyncRunID(fmt.Sprintf("run-%03d", runSequence))
	}
	dependencies := Dependencies{
		Adapters:      map[knowl.SourceType]app.SourceAdapter{knowl.SourceTypeFilesystem: adapter},
		State:         store,
		Content:       workspace,
		SourceContent: workspace,
		Search:        store,
		Maintenance:   queue,
	}
	service, err := NewService(dependencies, options)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return &stageHarness{
		service: service, adapter: adapter, state: store, workspace: workspace,
		scope: stageScope, sourceID: testEngineeringSourceID, queue: queue,
	}
}

func TestMaintenanceReconciliationTrigger(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		previousRevision   string
		currentRevision    string
		previousGeneration string
		currentGeneration  string
		want               string
	}{
		{name: "new revision", previousRevision: "1", currentRevision: "2", previousGeneration: "a", currentGeneration: "a", want: maintenanceTriggerRevision},
		{name: "policy change", previousRevision: "2", currentRevision: "2", previousGeneration: "a", currentGeneration: "b", want: maintenanceTriggerPolicy},
		{name: maintenanceTriggerUnchanged, previousRevision: "2", currentRevision: "2", previousGeneration: "b", currentGeneration: "b", want: maintenanceTriggerUnchanged},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := maintenanceReconciliationTrigger(test.previousRevision, test.currentRevision, test.previousGeneration, test.currentGeneration); got != test.want {
				t.Fatalf("maintenanceReconciliationTrigger() = %q, want %q", got, test.want)
			}
		})
	}
}

type recordingMaintenanceQueue struct {
	requests         []app.AcceptedMaintenanceRequest
	seen             map[knowl.OperationID]struct{}
	err              error
	after            func()
	outcome          app.MaintenanceOutcome
	generation       string
	policyGeneration string
}

func newRecordingMaintenanceQueue() *recordingMaintenanceQueue {
	return &recordingMaintenanceQueue{seen: make(map[knowl.OperationID]struct{})}
}

func (queue *recordingMaintenanceQueue) ReserveAccepted(_ context.Context, request app.AcceptedMaintenanceRequest) (app.MaintenanceReservation, error) {
	id := knowl.OperationID(app.SourceRefKey(request.Source))
	if request.PreviousMaintenanceRevision == request.Source.Version.Version && request.PreviousOperationID == id {
		return app.MaintenanceReservation{
			OperationID: id, Generation: request.PreviousGeneration, Outcome: app.MaintenanceConverged,
		}, nil
	}
	queue.requests = append(queue.requests, request)
	if queue.err != nil {
		return app.MaintenanceReservation{}, queue.err
	}
	if queue.after != nil {
		queue.after()
		queue.after = nil
	}
	_, replayed := queue.seen[id]
	queue.seen[id] = struct{}{}
	outcome := queue.outcome
	if outcome == "" {
		outcome = app.MaintenanceQueued
		if replayed {
			outcome = app.MaintenanceReplayed
		}
	}
	generation := queue.generation
	if generation == "" {
		generation = strings.Repeat("f", 64)
	}
	policyGeneration := queue.policyGeneration
	if policyGeneration == "" {
		policyGeneration = generation
	}
	return app.MaintenanceReservation{
		OperationID: id, Generation: generation, PolicyGeneration: policyGeneration,
		Outcome: outcome, Replayed: replayed,
	}, nil
}

func (harness *stageHarness) source(flavor string) knowl.Source {
	return knowl.Source{
		ID: harness.sourceID, Type: knowl.SourceTypeFilesystem, Enabled: true,
		Config: knowl.SourceConfig{Filesystem: &knowl.FilesystemSourceConfig{
			Root: "/data/engineering", Include: []string{"**/*"}, Flavor: flavor,
			URIBase: "https://wiki.example.test/",
		}},
	}
}

func (harness *stageHarness) descriptor(path, body string) knowl.DocumentRef {
	return knowl.DocumentRef{
		ExternalID: knowl.DocumentID(path), Revision: sha256Hex(body), Path: path,
		Metadata: map[string]string{"kind": kindFor(path)},
	}
}

func (harness *stageHarness) sync(t *testing.T) (Result, error) {
	t.Helper()
	result, err := harness.service.SyncSource(context.Background(), harness.scope, harness.source(knowl.SourceFlavorMarkdown))
	return result, err
}

func kindFor(path string) string {
	if strings.HasSuffix(path, ".md") {
		return "markdown"
	}
	return "asset"
}

func sha256Hex(content string) string {
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	return sum
}

func TestStageInitialSyncClassifiesAddsAndPreparesWithoutCanonicalWrites(t *testing.T) {
	harness := newStageHarness(t, nil)
	first := harness.descriptor("docs/one.md", "# One\n")
	second := harness.descriptor("docs/two.md", "# Two\n")
	asset := harness.descriptor("assets/logo.bin", "\x00\x01binary")
	harness.adapter.script(first.ExternalID, "# One\n")
	harness.adapter.script(second.ExternalID, "# Two\n")
	harness.adapter.script(asset.ExternalID, "\x00\x01binary")
	harness.adapter.enqueue(
		harness.adapter.page([]knowl.DocumentRef{first, asset}, "page-2"),
		harness.adapter.page([]knowl.DocumentRef{second}, ""),
	)

	result, err := harness.sync(t)
	requireSyncSuccess(t, result, err)
	if !strings.HasPrefix(string(result.Run.ID), "run-") || result.Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("finalized run = %#v", result.Run)
	}
	if harness.adapter.fetches != 3 {
		t.Fatalf("fetches = %d, want one per added descriptor", harness.adapter.fetches)
	}
	read, err := harness.state.PreparedSync(context.Background(), harness.scope, result.Run.ID)
	if err != nil {
		t.Fatalf("PreparedSync() error = %v", err)
	}
	if read.Counts != (knowl.SyncCounts{Added: 3}) || len(read.Documents) != 3 || read.Checkpoint == "" {
		t.Fatalf("prepared read = %#v", read)
	}
	head, headErr := harness.state.DocumentState(context.Background(), harness.scope, harness.sourceID, first.ExternalID)
	if headErr != nil || head.Deleted || head.Revision != first.Revision {
		t.Fatalf("finalized head = %#v, %v", head, headErr)
	}
	for _, document := range read.Documents {
		if document.Action != app.SyncDocumentActive || document.State.MirrorPath != "" || document.State.MirrorDigest != "" ||
			document.State.LastSeenRunID != result.Run.ID || document.State.AcceptedSource.Version.Digest == "" {
			t.Fatalf("candidate = %#v", document)
		}
		textual := strings.HasSuffix(string(document.State.DocumentID), ".md")
		if textual != (document.State.MaintenanceRevision == document.State.Revision && document.State.MaintenanceOperationID != "") {
			t.Fatalf("maintenance state = %#v", document.State)
		}
	}
	canonical, err := harness.service.sourceContent.SourceDigests(context.Background(), harness.scope, harness.sourceID, 16)
	if err != nil || len(canonical) != 0 {
		t.Fatalf("canonical source inventory after commit = %#v, %v; want no mirrors", canonical, err)
	}
	if len(harness.queue.requests) != 2 {
		t.Fatalf("maintenance requests = %d, want one per textual document", len(harness.queue.requests))
	}
	if !result.Changed {
		t.Fatal("initial adds must claim canonical change")
	}
}

func TestStageRepeatSyncIsConvergentWithZeroFetchAndZeroMutations(t *testing.T) {
	harness := newStageHarness(t, nil)
	body := "\x00binary-seed"
	harness.seedFinalized(t, []seededDoc{{path: "docs/page.bin", body: body}})

	ref := harness.descriptor("docs/page.bin", body)
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	second, err := harness.sync(t)
	requireSyncSuccess(t, second, err)
	if harness.adapter.fetches != 0 {
		t.Fatalf("fetches = %d, want zero for unchanged descriptors", harness.adapter.fetches)
	}
	secondPrepared, err := harness.state.PreparedSync(context.Background(), harness.scope, second.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secondPrepared.Counts != (knowl.SyncCounts{Unchanged: 1}) {
		t.Fatalf("repeat counts = %#v", secondPrepared.Counts)
	}
	if second.Run.Checkpoint != scanCheckpoint([]knowl.DocumentRef{ref}) {
		t.Fatalf("checkpoint = %q", second.Run.Checkpoint)
	}
	if second.Changed {
		t.Fatal("unchanged sync claimed canonical change")
	}
}

func TestStageSameRevisionUnderAnotherAdapterFetchesIndependentEvidence(t *testing.T) {
	harness := newStageHarness(t, nil)
	body := "# Independent Git evidence\n"
	harness.seedFinalized(t, []seededDoc{{path: "docs/page.md", body: body}})
	ref := harness.descriptor("docs/page.md", body)
	harness.adapter.script(ref.ExternalID, body)
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	harness.service.adapters[knowl.SourceTypeGit] = harness.adapter
	gitSource := knowl.Source{
		ID: harness.sourceID, Type: knowl.SourceTypeGit, Enabled: true,
		Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
			Remote: "https://example.test/wiki.git", Ref: "main", RefKind: knowl.GitRefKindBranch,
		}},
		ConfigDigest: strings.Repeat("2", 64),
	}

	result, err := harness.service.SyncSource(context.Background(), harness.scope, gitSource)
	requireSyncSuccess(t, result, err)
	if harness.adapter.fetches != 1 {
		t.Fatalf("Git replacement fetches = %d, want one independent fetch", harness.adapter.fetches)
	}
	head, err := harness.state.DocumentState(context.Background(), harness.scope, harness.sourceID, ref.ExternalID)
	if err != nil {
		t.Fatalf("read Git document head: %v", err)
	}
	wantSource := knowl.SourceRef{Adapter: gitAdapterName, ID: string(harness.sourceID) + "/" + string(ref.ExternalID)}
	if head.AcceptedSource.Source != wantSource || head.AcceptedSource.Version.Version != ref.Revision ||
		head.MaintenanceOperationID != knowl.OperationID(app.SourceRefKey(head.AcceptedSource)) {
		t.Fatalf("Git document head = %#v, want independent accepted identity %#v", head, wantSource)
	}
}

func TestStageMarksExactLifecycleReadyOnlyAfterSuccessfulFinalization(t *testing.T) {
	harness := newStageHarness(t, nil)
	source, lifecycle := prepareStageLifecycle(t, harness)
	ref := harness.descriptor("docs/ready.md", "# Ready\n")
	harness.adapter.script(ref.ExternalID, "# Ready\n")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))

	result, err := harness.service.SyncSource(context.Background(), harness.scope, source)
	requireSyncSuccess(t, result, err)
	state, err := lifecycle.SourceLifecycle(context.Background(), harness.scope, knowl.SourceOwner{
		Adapter: knowl.SourceAdapterFilesystem, SourceID: harness.sourceID,
	})
	if err != nil || !state.Ready || state.ConfigDigest != source.ConfigDigest {
		t.Fatalf("source readiness = %#v, %v", state, err)
	}
}

func TestMarkSourceReadyIsIdempotentForCurrentConfig(t *testing.T) {
	harness := newStageHarness(t, nil)
	source, lifecycle := prepareStageLifecycle(t, harness)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: harness.sourceID}
	current, err := lifecycle.SourceLifecycle(context.Background(), harness.scope, owner)
	if err != nil {
		t.Fatalf("read active lifecycle: %v", err)
	}
	readyAt := time.Unix(2_000, 0).UTC()
	ready, err := lifecycle.MarkSourceLifecycleReady(context.Background(), app.SourceLifecycleReadyUpdate{
		Scope: harness.scope, Owner: owner, AuthorityGeneration: current.AuthorityGeneration,
		AuthorityEpoch: current.AuthorityEpoch, ActivatedEpoch: current.ActivatedEpoch,
		ConfigDigest: source.ConfigDigest, ReadyAt: readyAt,
	})
	if err != nil {
		t.Fatalf("prepare ready lifecycle: %v", err)
	}
	harness.service.options.Clock = func() time.Time { return readyAt.Add(-time.Second) }

	err = harness.service.markSourceReady(context.Background(), source, knowl.SyncRun{
		Scope: harness.scope, ConfigDigest: source.ConfigDigest,
	})
	if err != nil {
		t.Fatalf("repeat current-config readiness: %v", err)
	}
	after, err := lifecycle.SourceLifecycle(context.Background(), harness.scope, owner)
	if err != nil || after != ready {
		t.Fatalf("idempotent readiness = %#v, %v; want %#v", after, err, ready)
	}
}

func TestStageDocumentTombstoneDoesNotRetireConfiguredSource(t *testing.T) {
	harness := newStageHarness(t, nil)
	source, lifecycle := prepareStageLifecycle(t, harness)
	ref := harness.descriptor("docs/removed.md", "# Removed\n")
	harness.adapter.script(ref.ExternalID, "# Removed\n")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	result, err := harness.service.SyncSource(context.Background(), harness.scope, source)
	requireSyncSuccess(t, result, err)

	harness.adapter.enqueue(harness.adapter.page(nil, ""))
	result, err = harness.service.SyncSource(context.Background(), harness.scope, source)
	requireSyncSuccess(t, result, err)
	document, err := harness.state.DocumentState(context.Background(), harness.scope, harness.sourceID, ref.ExternalID)
	if err != nil || !document.Deleted {
		t.Fatalf("document tombstone = %#v, %v", document, err)
	}
	state, err := lifecycle.SourceLifecycle(context.Background(), harness.scope, knowl.SourceOwner{
		Adapter: knowl.SourceAdapterFilesystem, SourceID: harness.sourceID,
	})
	if err != nil || state.State != knowl.SourceLifecycleActive || !state.Ready {
		t.Fatalf("configured source lifecycle = %#v, %v", state, err)
	}
}

func TestStageFailedReservationLeavesLifecycleNotReady(t *testing.T) {
	harness := newStageHarness(t, nil)
	source, lifecycle := prepareStageLifecycle(t, harness)
	ref := harness.descriptor("docs/blocked.md", "# Blocked\n")
	harness.adapter.script(ref.ExternalID, "# Blocked\n")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	harness.queue.err = errors.New("reservation unavailable")

	if _, err := harness.service.SyncSource(context.Background(), harness.scope, source); err == nil {
		t.Fatal("SyncSource() succeeded despite failed maintenance reservation")
	}
	state, err := lifecycle.SourceLifecycle(context.Background(), harness.scope, knowl.SourceOwner{
		Adapter: knowl.SourceAdapterFilesystem, SourceID: harness.sourceID,
	})
	if err != nil || state.Ready {
		t.Fatalf("failed-sync readiness = %#v, %v", state, err)
	}
}

func TestStageManualMaintenanceGateLeavesLifecycleNotReady(t *testing.T) {
	for _, test := range []struct {
		name             string
		generation       string
		policyGeneration string
	}{
		{name: "exhausted current policy", generation: strings.Repeat("f", 64), policyGeneration: strings.Repeat("f", 64)},
		{name: "stale policy", generation: strings.Repeat("e", 64), policyGeneration: strings.Repeat("f", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newStageHarness(t, nil)
			source, lifecycle := prepareStageLifecycle(t, harness)
			ref := harness.descriptor("docs/manual-gate.md", "# Manual gate\n")
			harness.adapter.script(ref.ExternalID, "# Manual gate\n")
			harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
			harness.queue.outcome = app.MaintenanceManualGate
			harness.queue.generation = test.generation
			harness.queue.policyGeneration = test.policyGeneration

			result, err := harness.service.SyncSource(context.Background(), harness.scope, source)
			requireSyncSuccess(t, result, err)
			prepared, err := harness.state.PreparedSync(context.Background(), harness.scope, result.Run.ID)
			if err != nil {
				t.Fatalf("read prepared sync: %v", err)
			}
			state, err := lifecycle.SourceLifecycle(context.Background(), harness.scope, knowl.SourceOwner{
				Adapter: knowl.SourceAdapterFilesystem, SourceID: harness.sourceID,
			})
			if err != nil || state.Ready || prepared.MaintenanceReady {
				t.Fatalf("manual-gated readiness = lifecycle %#v prepared %#v, %v", state, prepared, err)
			}
			resumable, err := harness.state.ResumableSyncRuns(context.Background(), harness.scope, 10)
			if err != nil || len(resumable) != 0 {
				t.Fatalf("manual-gated resumable runs = %#v, %v", resumable, err)
			}
		})
	}
}

func TestStageStaleConfigCannotOpenLifecycleReadiness(t *testing.T) {
	harness := newStageHarness(t, nil)
	source, lifecycle := prepareStageLifecycle(t, harness)
	ref := harness.descriptor("docs/stale.md", "# Stale\n")
	harness.adapter.script(ref.ExternalID, "# Stale\n")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	changedDigest := strings.Repeat("c", 64)
	harness.queue.after = func() {
		authority, err := lifecycle.SourceLifecycleAuthority(context.Background(), harness.scope)
		if err != nil {
			t.Errorf("read lifecycle authority: %v", err)
			return
		}
		desired := []app.DesiredSourceOwner{{
			Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: harness.sourceID}, ConfigDigest: changedDigest,
		}}
		err = lifecycle.ReconcileSourceLifecycles(context.Background(), app.SourceLifecycleReconcileRequest{
			Scope: harness.scope, Desired: desired, AuthorityGeneration: app.SourceLifecycleGeneration(desired),
			ExpectedAuthorityEpoch: authority.Epoch, UpdatedAt: time.Unix(995, 0).UTC(),
		})
		if err != nil {
			t.Errorf("change lifecycle config: %v", err)
		}
	}

	result, err := harness.service.SyncSource(context.Background(), harness.scope, source)
	if !errors.Is(err, app.ErrSourceLifecycleConflict) || result.Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("stale-config sync = %#v, %v", result, err)
	}
	state, err := lifecycle.SourceLifecycle(context.Background(), harness.scope, knowl.SourceOwner{
		Adapter: knowl.SourceAdapterFilesystem, SourceID: harness.sourceID,
	})
	if err != nil || state.Ready || state.ConfigDigest != changedDigest {
		t.Fatalf("stale-config readiness = %#v, %v", state, err)
	}
}

func prepareStageLifecycle(t *testing.T, harness *stageHarness) (knowl.Source, app.SourceLifecycleStore) {
	t.Helper()
	source := harness.source(knowl.SourceFlavorMarkdown)
	digest, err := app.SourceConfigDigest(source)
	if err != nil {
		t.Fatalf("source config digest: %v", err)
	}
	source.ConfigDigest = digest
	lifecycle, ok := harness.state.(app.SourceLifecycleStore)
	if !ok {
		t.Fatal("stage state store does not expose source lifecycle")
	}
	desired := []app.DesiredSourceOwner{{
		Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: harness.sourceID}, ConfigDigest: digest,
	}}
	if err := lifecycle.ReconcileSourceLifecycles(context.Background(), app.SourceLifecycleReconcileRequest{
		Scope: harness.scope, Desired: desired, AuthorityGeneration: app.SourceLifecycleGeneration(desired),
		UpdatedAt: time.Unix(990, 0).UTC(),
	}); err != nil {
		t.Fatalf("prepare source lifecycle: %v", err)
	}
	harness.service.sourceLifecycle = lifecycle
	return source, lifecycle
}

func TestStageBackfillsMaintenanceFromRawWithoutFetch(t *testing.T) {
	harness := newStageHarness(t, nil)
	body := "# Existing\n"
	harness.seedFinalized(t, []seededDoc{{path: "docs/existing.md", body: body, missingMaintenance: true, legacyRaw: true}})
	ref := harness.descriptor("docs/existing.md", body)
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))

	result, err := harness.sync(t)
	requireSyncSuccess(t, result, err)
	if harness.adapter.fetches != 0 || len(harness.queue.requests) != 1 || !result.Changed {
		t.Fatalf("backfill = fetches:%d requests:%d changed:%v", harness.adapter.fetches, len(harness.queue.requests), result.Changed)
	}
	head, err := harness.state.DocumentState(context.Background(), harness.scope, harness.sourceID, ref.ExternalID)
	if err != nil || head.MaintenanceRevision != ref.Revision || head.MaintenanceOperationID == "" ||
		head.AcceptedSource.SourceDocument.SourceID != harness.sourceID || head.AcceptedSource.SourceDocument.DocumentID != ref.ExternalID {
		t.Fatalf("backfilled head = %#v, %v", head, err)
	}
	inspection, err := harness.workspace.Inspect(context.Background(), harness.scope)
	if err != nil || len(inspection.RawSources) != 1 || inspection.RawSources[0].Source.SourceDocument != head.AcceptedSource.SourceDocument {
		t.Fatalf("backfilled raw manifest = %#v, %v", inspection.RawSources, err)
	}
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	repeated, err := harness.sync(t)
	requireSyncSuccess(t, repeated, err)
	if repeated.Changed || harness.adapter.fetches != 0 || len(harness.queue.requests) != 1 {
		t.Fatalf("repeated backfill = %#v fetches:%d requests:%d", repeated, harness.adapter.fetches, len(harness.queue.requests))
	}
}

func TestStageMaintenanceReservationFailureRetriesSafely(t *testing.T) {
	var logOutput bytes.Buffer
	previousLogger := log.Logger
	log.Logger = zerolog.New(&logOutput)
	t.Cleanup(func() { log.Logger = previousLogger })

	harness := newStageHarness(t, nil)
	ref := harness.descriptor("docs/new.md", "# New\n")
	harness.adapter.script(ref.ExternalID, "# New\n")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	reservationCause := errors.New(secretInjection)
	harness.queue.err = reservationCause

	failed, err := harness.sync(t)
	if !strings.Contains(classOf(err), classMaintenance) || strings.Contains(err.Error(), secretInjection) || !errors.Is(err, reservationCause) {
		t.Fatalf("reservation failure = %v", err)
	}
	harness.assertFailedRun(t, failed.Run.ID, classMaintenance)
	if _, stateErr := harness.state.DocumentState(context.Background(), harness.scope, harness.sourceID, ref.ExternalID); !errors.Is(stateErr, app.ErrSourceNotFound) {
		t.Fatalf("failed reservation published state: %v", stateErr)
	}
	if len(harness.queue.requests) != 1 {
		t.Fatalf("failed reservation requests = %d", len(harness.queue.requests))
	}

	harness.queue.err = nil
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	retried, err := harness.sync(t)
	requireSyncSuccess(t, retried, err)
	if len(harness.queue.requests) != 2 {
		t.Fatalf("retry reservations = %d", len(harness.queue.requests))
	}
	head, err := harness.state.DocumentState(context.Background(), harness.scope, harness.sourceID, ref.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	encodedLogs := logOutput.String()
	for _, required := range []string{string(harness.sourceID), string(ref.ExternalID), ref.Revision, string(head.MaintenanceOperationID), classMaintenance} {
		if !strings.Contains(encodedLogs, required) {
			t.Errorf("maintenance reservation logs missing %q: %s", required, encodedLogs)
		}
	}
	if strings.Contains(encodedLogs, secretInjection) || strings.Contains(encodedLogs, "# New") {
		t.Fatalf("maintenance reservation logs leaked source or cause: %s", encodedLogs)
	}
}

func TestStageUpdateIsSelectiveAndDeletePreparesTombstoneSafely(t *testing.T) {
	harness := newStageHarness(t, nil)
	harness.seedFinalized(t, []seededDoc{
		{path: "docs/kept.bin", body: "kept"},
		{path: "docs/removed.bin", body: "removed"},
		{path: "docs/updated.bin", body: "before"},
	})
	beforeFetches := harness.adapter.fetches

	changedBody := "after"
	keptRef := harness.descriptor("docs/kept.bin", "kept")
	updatedAfter := harness.descriptor("docs/updated.bin", changedBody)
	harness.adapter.script(updatedAfter.ExternalID, changedBody)
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{keptRef, updatedAfter}, ""))

	result, err := harness.sync(t)
	requireSyncSuccess(t, result, err)
	if harness.adapter.fetches != beforeFetches+1 {
		t.Fatalf("fetch delta = %d, want exactly one selective update fetch", harness.adapter.fetches-beforeFetches)
	}
	prepared, err := harness.state.PreparedSync(context.Background(), harness.scope, result.Run.ID)
	if err != nil {
		t.Fatalf("prepared read = %v", err)
	}
	if prepared.Counts != (knowl.SyncCounts{Updated: 1, Unchanged: 1, Deleted: 1}) {
		t.Fatalf("update counts = %#v", prepared.Counts)
	}
	var tombstoned, refreshed bool
	for _, document := range prepared.Documents {
		switch document.State.DocumentID {
		case "docs/removed.bin":
			tombstoned = document.Action == app.SyncDocumentTombstone && document.State.Deleted && !document.State.DeletedAt.IsZero()
		case "docs/updated.bin":
			refreshed = document.Action == app.SyncDocumentActive && document.State.Revision == sha256Hex(changedBody)
		}
	}
	if !tombstoned || !refreshed {
		t.Fatalf("candidates missing tombstone or refresh: %#v", prepared.Documents)
	}
	resumable, err := harness.state.ResumableSyncRuns(context.Background(), harness.scope, 10)
	if err != nil || len(resumable) != 0 {
		t.Fatalf("resumable tails = %#v, %v; want full convergence", resumable, err)
	}
	removedHead, removedErr := harness.state.DocumentState(context.Background(), harness.scope, harness.sourceID, "docs/removed.bin")
	if removedErr != nil || !removedHead.Deleted || removedHead.AcceptedSource.ManifestRef == "" {
		t.Fatalf("tombstone head = %#v, %v; raw history must persist", removedHead, removedErr)
	}
	if _, readErr := harness.service.content.ReadSource(context.Background(), removedHead.AcceptedSource, knowl.ReadLimits{Bytes: 1 << 20}); readErr != nil {
		t.Fatalf("retained raw read = %v", readErr)
	}
}

func TestStageReappearingDescriptorRerendersStoredRawWithoutFetch(t *testing.T) {
	harness := newStageHarness(t, nil)
	harness.seedFinalized(t, []seededDoc{{path: "docs/phoenix.bin", body: "phoenix", deleted: true}})

	ref := harness.descriptor("docs/phoenix.bin", "phoenix")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	before := harness.adapter.fetches
	reborn, err := harness.sync(t)
	requireSyncSuccess(t, reborn, err)
	if harness.adapter.fetches != before {
		t.Fatalf("reappear fetched %d times, want stored-rerender only", harness.adapter.fetches-before)
	}
	prepared, err := harness.state.PreparedSync(context.Background(), harness.scope, reborn.Run.ID)
	if err != nil || prepared.Counts != (knowl.SyncCounts{Updated: 1}) {
		t.Fatalf("reappear counts = %#v, %v", prepared, err)
	}
	if prepared.Documents[0].Action != app.SyncDocumentActive || prepared.Documents[0].State.Deleted {
		t.Fatalf("reactivated candidate = %#v", prepared.Documents[0])
	}
}

func TestStageFailuresNeverPrepareUnsafeDeletions(t *testing.T) {
	ctx := context.Background()
	t.Run("adapter failure marks run failed atomically", func(t *testing.T) {
		harness := newStageHarness(t, nil)
		ref := harness.descriptor("docs/gone.md", "# Gone\n")
		harness.adapter.failFetch[ref.ExternalID] = errors.New(secretInjection)
		harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
		result, err := harness.sync(t)
		if err == nil || !strings.Contains(classOf(err), "fetch") || strings.Contains(err.Error(), secretInjection) {
			t.Fatalf("failure = %v, want redacted fetch class", err)
		}
		harness.assertFailedRun(t, result.Run.ID, "fetch")
	})
	t.Run("duplicate descriptors rejected", func(t *testing.T) {
		harness := newStageHarness(t, nil)
		ref := harness.descriptor("docs/a.md", "# A\n")
		harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref, ref}, ""))
		result, err := harness.sync(t)
		if !strings.Contains(classOf(err), "scan_invalid") {
			t.Fatalf("duplicate error = %v, want scan class", err)
		}
		harness.assertFailedRun(t, result.Run.ID, "scan_invalid")
	})
	t.Run("document overflow rejected", func(t *testing.T) {
		harness := newStageHarness(t, func(options *Options) { options.MaxScanDocuments = 1 })
		first := harness.descriptor("docs/a.md", "# A\n")
		second := harness.descriptor("docs/b.md", "# B\n")
		harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{first, second}, ""))
		result, err := harness.sync(t)
		if !strings.Contains(classOf(err), "scan_invalid") {
			t.Fatalf("overflow error = %v, want scan class", err)
		}
		harness.assertFailedRun(t, result.Run.ID, "scan_invalid")
	})
	t.Run("non-advancing tokens terminate at the page bound", func(t *testing.T) {
		harness := newStageHarness(t, func(options *Options) { options.MaxScanPages = 3 })
		ref := harness.descriptor("docs/loop.md", "# Loop\n")
		harness.adapter.script(ref.ExternalID, "# Loop\n")
		stuck := harness.adapter.page([]knowl.DocumentRef{ref}, "stuck-token")
		harness.adapter.enqueue(stuck, stuck, stuck)
		result, err := harness.sync(t)
		if !strings.Contains(classOf(err), "scan_invalid") {
			t.Fatalf("looping token error = %v, want scan class", err)
		}
		harness.assertFailedRun(t, result.Run.ID, "scan_invalid")
	})
	t.Run("cancellation leaves heads intact", func(t *testing.T) {
		harness := newStageHarness(t, nil)
		ref := harness.descriptor("docs/keep-active.md", "# Keep\n")
		harness.adapter.script(ref.ExternalID, "# Keep\n")
		harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
		seededResult, seedErr := harness.sync(t)
		requireSyncSuccess(t, seededResult, seedErr) // seed
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := harness.service.SyncSource(canceled, harness.scope, harness.source(knowl.SourceFlavorMarkdown))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled sync error = %v", err)
		}
		head, headErr := harness.state.DocumentState(ctx, harness.scope, harness.sourceID, ref.ExternalID)
		// The saga tail is pending, so finalization never applied any head:
		// absence (not a tombstone) is the intact outcome at this boundary.
		if headErr != nil && !errors.Is(headErr, app.ErrSourceNotFound) {
			t.Fatalf("head read error = %v", headErr)
		}
		if headErr == nil && head.Deleted {
			t.Fatalf("cancellation produced a tombstone: %#v", head)
		}
	})

	t.Run("legacy canonical orphan is removed", func(t *testing.T) {
		harness := newStageHarness(t, nil)
		ref := harness.descriptor("docs/real.md", "# Real\n")
		harness.adapter.script(ref.ExternalID, "# Real\n")
		harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
		target := filepath.Join(harness.workspace.Root(), filepath.FromSlash("wiki/sources/engineering/orphan.bin"))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("orphan"), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := harness.sync(t)
		requireSyncSuccess(t, result, err)
		if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("legacy orphan still exists: %v", statErr)
		}
	})
}

func TestStageTwoSourcesWithEqualPathsStayIsolated(t *testing.T) {
	harness := newStageHarness(t, nil)
	left := harness.descriptor("shared/page.md", "# Left\n")
	right := harness.descriptor("shared/page.md", "# Right\n")
	harness.adapter.script(left.ExternalID, "# Left\n")

	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{left}, ""))
	leftResult, leftErr := harness.sync(t)
	requireSyncSuccess(t, leftResult, leftErr) // left source

	other := harness.source(knowl.SourceFlavorMarkdown)
	other.ID = "operations"
	otherAdapter := newScriptedAdapter()
	otherAdapter.documents[right.ExternalID] = []byte("# Right\n")
	otherAdapter.enqueue(otherAdapter.page([]knowl.DocumentRef{right}, ""))
	harness.swapAdapter(otherAdapter)

	otherResult, otherErr := harness.service.SyncSource(context.Background(), harness.scope, other)
	requireSyncSuccess(t, otherResult, otherErr)
	otherInventory, err := harness.service.sourceContent.SourceDigests(context.Background(), harness.scope, other.ID, 16)
	if err != nil || len(otherInventory) != 0 {
		t.Fatalf("other inventory = %#v, %v", otherInventory, err)
	}
	leftInventory, err := harness.service.sourceContent.SourceDigests(context.Background(), harness.scope, harness.sourceID, 16)
	if err != nil || len(leftInventory) != 0 {
		t.Fatalf("left inventory = %#v, %v", leftInventory, err)
	}
	prepared, err := harness.state.PreparedSync(context.Background(), harness.scope, knowl.SyncRunID("run-002"))
	if err != nil || len(prepared.Documents) != 1 {
		t.Fatalf("second prepared = %#v, %v", prepared, err)
	}
	if state := prepared.Documents[0].State; state.MirrorPath != "" || state.AcceptedSource.SourceDocument.SourceID != other.ID {
		t.Fatalf("isolated raw state = %#v", state)
	}
}

func (harness *stageHarness) swapAdapter(adapter app.SourceAdapter) {
	harness.service.adapters[knowl.SourceTypeFilesystem] = adapter
}

func (harness *stageHarness) assertFailedRun(t *testing.T, runID knowl.SyncRunID, class string) {
	t.Helper()
	run, err := harness.state.SyncRun(context.Background(), harness.scope, runID)
	if err != nil || run.Status != knowl.SyncStatusFailed || run.FailureClass != class {
		t.Fatalf("failed run = %#v, %v; want class %q", run, err, class)
	}
	if _, err := harness.state.PreparedSync(context.Background(), harness.scope, runID); !errors.Is(err, app.ErrSyncStateTransition) {
		t.Fatalf("failed run prepared read = %v, want transition refusal", err)
	}
}

func classOf(err error) string {
	var staged *stageError
	if errors.As(err, &staged) {
		return staged.class
	}
	return ""
}

// requireSyncSuccess asserts one fully converged synchronization attempt.
func requireSyncSuccess(t *testing.T, result Result, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("sync error = %v", err)
	}
	if result.Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", result.Run.Status)
	}
}

// seededDoc describes one durably finalized historical document head.
type seededDoc struct {
	path               string
	body               string
	deleted            bool
	legacyMirror       bool
	legacyRaw          bool
	missingMaintenance bool
}

// seedFinalized replays a complete prior synchronization through the durable
// store primitives so classification scenarios observe real heads and mirrors.
func (harness *stageHarness) seedFinalized(t *testing.T, docs []seededDoc) {
	t.Helper()
	ctx := context.Background()
	runID := harness.service.options.NewRunID()
	now := harness.service.options.Clock()
	source := harness.source(knowl.SourceFlavorMarkdown)

	type seeded struct {
		ref      knowl.DocumentRef
		accepted knowl.AcceptedSource
		state    knowl.DocumentState
		action   app.SyncDocumentAction
		content  []byte
	}
	refs := make([]knowl.DocumentRef, 0, len(docs))
	entries := make([]seeded, 0, len(docs))
	counts := knowl.SyncCounts{}
	for _, doc := range docs {
		revision := sha256Hex(doc.body)
		ref := knowl.DocumentRef{
			ExternalID: knowl.DocumentID(doc.path), Revision: revision, Path: doc.path,
			Metadata: map[string]string{"kind": kindFor(doc.path)},
		}
		refs = append(refs, ref)
		sourceDocument := knowl.SourceDocument{
			SourceID: harness.sourceID, DocumentID: ref.ExternalID, Revision: revision,
			URI: filesystem.DocumentURI(*source.Config.Filesystem, ref.Path),
		}
		if doc.legacyRaw {
			sourceDocument = knowl.SourceDocument{}
		}
		accepted, err := harness.workspace.AcceptSource(ctx, knowl.SourceEnvelope{
			Scope:          harness.scope,
			Source:         knowl.SourceRef{Adapter: "wiki-filesystem", ID: string(harness.sourceID) + "/" + doc.path},
			Version:        knowl.SourceVersion{Version: revision, Digest: revision},
			MediaType:      mediaTypeFor(doc.path),
			SourceDocument: sourceDocument,
			Content:        []byte(doc.body),
			ReceivedAt:     now,
		})
		if err != nil {
			t.Fatalf("seed accept source: %v", err)
		}
		state := knowl.DocumentState{
			Scope: harness.scope, SourceID: harness.sourceID, DocumentID: ref.ExternalID, Revision: revision,
			AcceptedSource: accepted,
			LastSeenRunID:  runID,
			CreatedAt:      now, UpdatedAt: now,
		}
		content := []byte(nil)
		if doc.legacyMirror {
			state.MirrorPath = "wiki/sources/" + string(harness.sourceID) + "/" + doc.path
			state.MirrorDigest = sha256Hex(doc.body)
			content = []byte(doc.body)
		}
		if strings.HasSuffix(doc.path, ".md") && !doc.deleted && !doc.missingMaintenance {
			state.MaintenanceRevision = revision
			state.MaintenanceOperationID = knowl.OperationID(app.SourceRefKey(accepted))
		}
		action := app.SyncDocumentActive
		if doc.deleted {
			action = app.SyncDocumentTombstone
			state.Deleted = true
			state.DeletedAt = now.Add(time.Second)
			state.MirrorPath = ""
			state.MirrorDigest = ""
			counts.Deleted++
		} else {
			counts.Added++
		}
		entries = append(entries, seeded{ref: ref, accepted: accepted, state: state, action: action, content: content})
	}
	candidates := make([]app.PreparedDocumentState, 0, len(entries))
	for _, entry := range entries {
		if !entry.state.Deleted && entry.state.MirrorPath != "" {
			writeSeedFile(t, harness.workspace, entry.state.MirrorPath, entry.content)
		}
		candidates = append(candidates, app.PreparedDocumentState{Action: entry.action, State: entry.state})
	}
	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].State.DocumentID < candidates[right].State.DocumentID
	})
	prepared := app.PreparedSyncState{
		RunID: runID, Scope: harness.scope, SourceID: harness.sourceID, CompleteScan: true,
		Checkpoint: scanCheckpoint(refs), Counts: counts, Documents: candidates, PreparedAt: now,
	}
	digest, err := app.PreparedSyncDigest(prepared)
	if err != nil {
		t.Fatalf("seed prepared digest: %v", err)
	}
	prepared.CandidateDigest = digest
	if _, _, err := harness.state.BeginSync(ctx, app.BeginSyncRequest{Run: knowl.SyncRun{
		ID: runID, Scope: harness.scope, SourceID: harness.sourceID,
		ConfigDigest: strings.Repeat("1", 64), Status: knowl.SyncStatusScanning,
		StartedAt: now, UpdatedAt: now,
	}, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatalf("seed begin: %v", err)
	}
	recorded, err := harness.state.RecordScanPage(ctx, app.ScanPageRecord{
		RunID: runID, Scope: harness.scope, SourceID: harness.sourceID,
		Documents: refs, RecordedAt: now.Add(time.Second),
	})
	if err != nil || recorded.NextPageToken != "" {
		t.Fatalf("seed scan page = %#v, %v", recorded, err)
	}
	if _, err := harness.state.PrepareSync(ctx, prepared); err != nil {
		t.Fatalf("seed prepare: %v", err)
	}
	generation := "seed-generation-" + string(runID)
	transition := app.SyncGeneration{RunID: runID, Scope: harness.scope, SourceID: harness.sourceID, Generation: generation, UpdatedAt: now.Add(2 * time.Second)}
	if _, err := harness.state.MarkContentCommitted(ctx, transition); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	if _, err := harness.state.MarkProjected(ctx, transition); err != nil {
		t.Fatalf("seed projection: %v", err)
	}
	finalized, err := harness.state.FinalizeSync(ctx, app.SyncFinalization{
		RunID: runID, Scope: harness.scope, SourceID: harness.sourceID,
		CandidateDigest: digest, Generation: generation, Checkpoint: prepared.Checkpoint,
		Counts: counts, FinalizedAt: now.Add(3 * time.Second),
	})
	if err != nil || finalized.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("seed finalize = %#v, %v", finalized, err)
	}
}

func writeSeedFile(t *testing.T, workspace *fs.Workspace, relative string, content []byte) {
	t.Helper()
	target := filepath.Join(workspace.Root(), filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
