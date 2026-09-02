package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// SourceHarness supplies one backend implementation to the shared source-state contract.
type SourceHarness struct {
	Store      app.SourceStateStore
	OpenPeer   func(t *testing.T) app.SourceStateStore
	IsConflict func(error) bool
	Scope      knowl.ScopeRef
}

// RunSourceContract verifies backend-neutral source reconciliation semantics.
func RunSourceContract(t *testing.T, harness SourceHarness) {
	t.Helper()
	ctx := context.Background()
	scope := harness.Scope
	if scope == "" {
		scope = "source_contract"
	}
	const (
		sourceID   = knowl.SourceID("engineering")
		otherID    = knowl.SourceID("operations")
		document   = knowl.DocumentID("architecture/auth.md")
		generation = "workspace-generation-1"
		checkpoint = "checkpoint-1"
		changed    = "changed"
	)
	base := time.Unix(100, 0).UTC()
	if harness.OpenPeer != nil {
		runConcurrentBeginContract(t, ctx, harness, scope, base.Add(-20*time.Second))
	}
	run := newContractRun(scope, sourceID, "run-1", base)
	created, replay, err := harness.Store.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem})
	if err != nil || replay || created.Status != knowl.SyncStatusScanning {
		t.Fatalf("BeginSync() = %#v, %v, %v", created, replay, err)
	}
	replayed, replay, err := harness.Store.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem})
	if err != nil || !replay || replayed.ID != created.ID {
		t.Fatalf("BeginSync() replay = %#v, %v, %v", replayed, replay, err)
	}
	conflicting := run
	conflicting.SourceID = otherID
	if _, _, err := harness.Store.BeginSync(ctx, app.BeginSyncRequest{Run: conflicting, Type: knowl.SourceTypeFilesystem}); !errors.Is(err, app.ErrSyncConflict) && (harness.IsConflict == nil || !harness.IsConflict(err)) {
		t.Fatalf("BeginSync() conflict = %v", err)
	}
	secret := "postgres://operator:password@database.example/knowl?token=bearer-secret"
	if _, err := harness.Store.FailSync(ctx, scope, run.ID, secret, base.Add(time.Second)); !errors.Is(err, app.ErrSourceInvalid) || strings.Contains(err.Error(), secret) {
		t.Fatalf("FailSync() secret-shaped class error = %q", err)
	}
	statusAfterRejectedFailure, err := harness.Store.SourceStatus(ctx, scope, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	statusJSON, err := json.Marshal(statusAfterRejectedFailure)
	if err != nil {
		t.Fatal(err)
	}
	if statusAfterRejectedFailure.Status != knowl.SyncStatusScanning || strings.Contains(string(statusJSON), secret) {
		t.Fatalf("status after rejected failure = %s", statusJSON)
	}
	oversizedPage := make([]knowl.DocumentRef, 1001)
	for index := range oversizedPage {
		id := knowl.DocumentID(fmt.Sprintf("oversized/%04d.md", index))
		oversizedPage[index] = knowl.DocumentRef{ExternalID: id, Revision: "1", Path: string(id)}
	}
	if _, err := harness.Store.RecordScanPage(ctx, app.ScanPageRecord{RunID: run.ID, Scope: scope, SourceID: sourceID, Documents: oversizedPage, RecordedAt: base.Add(time.Second)}); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("RecordScanPage() oversized error = %v", err)
	}
	afterOversized, err := harness.Store.SyncRun(ctx, scope, run.ID)
	if err != nil || afterOversized.NextPageToken != "" || !afterOversized.UpdatedAt.Equal(base) {
		t.Fatalf("run after oversized page = %#v, %v", afterOversized, err)
	}

	ref := knowl.DocumentRef{ExternalID: document, Revision: testSourceRevision, Path: string(document)}
	progressed, err := harness.Store.RecordScanPage(ctx, app.ScanPageRecord{
		RunID: run.ID, Scope: scope, SourceID: sourceID, Documents: []knowl.DocumentRef{ref}, RecordedAt: base.Add(time.Second),
	})
	if err != nil || !progressed.UpdatedAt.Equal(base.Add(time.Second)) {
		t.Fatalf("RecordScanPage() = %#v, %v", progressed, err)
	}
	state := contractDocumentState(scope, sourceID, document, run.ID, testSourceRevision, base)
	rejectedDigest := strings.Repeat("a", 64)
	if _, err := harness.Store.PrepareSync(ctx, app.PreparedSyncState{
		RunID: run.ID, Scope: scope, SourceID: sourceID, CompleteScan: true, Checkpoint: checkpoint,
		Counts: knowl.SyncCounts{Added: -1}, CandidateDigest: rejectedDigest, PreparedAt: base.Add(2 * time.Second),
	}); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("PrepareSync() invalid counts error = %v", err)
	}
	afterInvalidCounts, err := harness.Store.SyncRun(ctx, scope, run.ID)
	if err != nil || afterInvalidCounts.Status != knowl.SyncStatusScanning || afterInvalidCounts.Counts != (knowl.SyncCounts{}) {
		t.Fatalf("run after invalid counts = %#v, %v", afterInvalidCounts, err)
	}
	prepared := contractPreparedState(t, run.ID, scope, sourceID, checkpoint, knowl.SyncCounts{Added: 1},
		[]app.PreparedDocumentState{{Action: app.SyncDocumentActive, State: state}}, base.Add(2*time.Second))
	if _, err := harness.Store.PrepareSync(ctx, func() app.PreparedSyncState {
		nonCanonical := prepared
		nonCanonical.CandidateDigest = rejectedDigest
		return nonCanonical
	}()); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("PrepareSync() non-canonical digest error = %v", err)
	}
	afterNonCanonical, err := harness.Store.SyncRun(ctx, scope, run.ID)
	if err != nil || afterNonCanonical.Status != knowl.SyncStatusScanning || afterNonCanonical.Checkpoint != "" {
		t.Fatalf("run after non-canonical digest = %#v, %v", afterNonCanonical, err)
	}
	preparedRun, err := harness.Store.PrepareSync(ctx, prepared)
	if err != nil || preparedRun.Status != knowl.SyncStatusPrepared {
		t.Fatalf("PrepareSync() = %#v, %v", preparedRun, err)
	}
	if _, err := harness.Store.DocumentState(ctx, scope, sourceID, document); !errors.Is(err, app.ErrSourceNotFound) {
		t.Fatalf("DocumentState() before finalize = %v, want not found", err)
	}
	if _, err := harness.Store.PrepareSync(ctx, func() app.PreparedSyncState {
		return contractPreparedState(t, run.ID, scope, sourceID, checkpoint, knowl.SyncCounts{Added: 1},
			[]app.PreparedDocumentState{{Action: app.SyncDocumentActive, State: func() knowl.DocumentState {
				altered := state
				altered.Revision = changed
				altered.AcceptedSource.Version.Version = changed
				altered.AcceptedSource.SourceDocument.Revision = changed
				altered.MaintenanceRevision = changed
				altered.MaintenanceOperationID = "operation-changed"
				return altered
			}()}}, base.Add(2*time.Second))
	}()); !errors.Is(err, app.ErrSyncConflict) && (harness.IsConflict == nil || !harness.IsConflict(err)) {
		t.Fatalf("PrepareSync() incompatible replay = %v, want conflict", err)
	}
	committed, err := harness.Store.MarkContentCommitted(ctx, app.SyncGeneration{RunID: run.ID, Scope: scope, SourceID: sourceID, Generation: generation, UpdatedAt: base.Add(3 * time.Second)})
	if err != nil || committed.Status != knowl.SyncStatusContentCommitted {
		t.Fatalf("MarkContentCommitted() = %#v, %v", committed, err)
	}
	projected, err := harness.Store.MarkProjected(ctx, app.SyncGeneration{RunID: run.ID, Scope: scope, SourceID: sourceID, Generation: generation, UpdatedAt: base.Add(4 * time.Second)})
	if err != nil || projected.Status != knowl.SyncStatusProjected {
		t.Fatalf("MarkProjected() = %#v, %v", projected, err)
	}
	finalization := app.SyncFinalization{RunID: run.ID, Scope: scope, SourceID: sourceID, CandidateDigest: prepared.CandidateDigest, Generation: generation, Checkpoint: checkpoint, Counts: knowl.SyncCounts{Added: 1}, FinalizedAt: base.Add(5 * time.Second)}
	finalized, err := harness.Store.FinalizeSync(ctx, finalization)
	if err != nil || finalized.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("FinalizeSync() = %#v, %v", finalized, err)
	}
	if replayed, err := harness.Store.FinalizeSync(ctx, finalization); err != nil || replayed != finalized {
		t.Fatalf("FinalizeSync() replay = %#v, %v", replayed, err)
	}
	active, err := harness.Store.DocumentState(ctx, scope, sourceID, document)
	if err != nil || active.Deleted || active.Revision != testSourceRevision || active.AcceptedSource.ManifestRef != "raw/manifest-1.json" ||
		active.MaintenanceRevision != active.Revision || active.MaintenanceOperationID == "" {
		t.Fatalf("DocumentState() = %#v, %v", active, err)
	}
	status, err := harness.Store.SourceStatus(ctx, scope, sourceID)
	if err != nil || status.Checkpoint != checkpoint || status.LastAttemptRunID != run.ID || status.LastSuccessfulRunID != run.ID || status.Counts != (knowl.SyncCounts{Added: 1}) || !status.CreatedAt.Equal(base) || !status.LastAttemptAt.Equal(base.Add(5*time.Second)) || !status.LastSuccessfulAt.Equal(base.Add(5*time.Second)) || !status.UpdatedAt.Equal(base.Add(5*time.Second)) {
		t.Fatalf("SourceStatus() = %#v, %v", status, err)
	}

	otherRun := newContractRun(scope, otherID, "run-other", base.Add(10*time.Second))
	if _, _, err := harness.Store.BeginSync(ctx, app.BeginSyncRequest{Run: otherRun, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	otherState := contractDocumentState(scope, otherID, document, otherRun.ID, "other-revision", base)
	finishContractRun(t, ctx, harness.Store, otherRun, otherState, "other-generation", base.Add(11*time.Second))
	if got, err := harness.Store.DocumentState(ctx, scope, otherID, document); err != nil || got.Revision != "other-revision" {
		t.Fatalf("other source state = %#v, %v", got, err)
	}

	deleteRun := newContractRun(scope, sourceID, "run-delete", base.Add(20*time.Second))
	if _, _, err := harness.Store.BeginSync(ctx, app.BeginSyncRequest{Run: deleteRun, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	tombstone := active
	tombstone.LastSeenRunID = deleteRun.ID
	tombstone.Deleted = true
	tombstone.DeletedAt = base.Add(21 * time.Second)
	finishContractRun(t, ctx, harness.Store, deleteRun, tombstone, "delete-generation", base.Add(21*time.Second))
	if visible, err := harness.Store.DocumentStates(ctx, scope, sourceID, app.DocumentListOptions{}); err != nil || len(visible) != 0 {
		t.Fatalf("active DocumentStates() = %#v, %v", visible, err)
	}
	deleted, err := harness.Store.DocumentStates(ctx, scope, sourceID, app.DocumentListOptions{IncludeDeleted: true})
	if err != nil || len(deleted) != 1 || !deleted[0].Deleted || deleted[0].AcceptedSource.ManifestRef != active.AcceptedSource.ManifestRef {
		t.Fatalf("deleted DocumentStates() = %#v, %v", deleted, err)
	}
	if harness.OpenPeer != nil {
		reopened := harness.OpenPeer(t)
		reopenedState, stateErr := reopened.DocumentState(ctx, scope, sourceID, document)
		reopenedStatus, statusErr := reopened.SourceStatus(ctx, scope, sourceID)
		if stateErr != nil || statusErr != nil || !reopenedState.Deleted || reopenedState.AcceptedSource.ManifestRef != active.AcceptedSource.ManifestRef || reopenedState.MaintenanceOperationID != active.MaintenanceOperationID || reopenedStatus.LastSuccessfulRunID != deleteRun.ID || reopenedStatus.Counts != (knowl.SyncCounts{Deleted: 1}) || !reopenedStatus.CreatedAt.Equal(base) || !reopenedStatus.LastAttemptAt.Equal(base.Add(23*time.Second)) || !reopenedStatus.LastSuccessfulAt.Equal(base.Add(23*time.Second)) {
			t.Fatalf("reopened source state = %#v/%#v, errors = %v/%v", reopenedState, reopenedStatus, stateErr, statusErr)
		}
		runConcurrentGenerationContract(t, ctx, harness, scope, sourceID, base.Add(25*time.Second))
	}

	runRecoveryReadsContract(t, ctx, harness, scope, base.Add(26*time.Second))

	failureRun := newContractRun(scope, sourceID, "run-failure", base.Add(30*time.Second))
	if _, _, err := harness.Store.BeginSync(ctx, app.BeginSyncRequest{Run: failureRun, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	failed, err := harness.Store.FailSync(ctx, scope, failureRun.ID, "adapter_unavailable", base.Add(31*time.Second))
	if err != nil || failed.FailureClass != "adapter_unavailable" {
		t.Fatalf("FailSync() = %#v, %v", failed, err)
	}
	failedStatus, err := harness.Store.SourceStatus(ctx, scope, sourceID)
	if err != nil || failedStatus.Status != knowl.SyncStatusFailed || failedStatus.Counts != (knowl.SyncCounts{}) || !failedStatus.LastAttemptAt.Equal(base.Add(31*time.Second)) || !failedStatus.LastSuccessfulAt.Equal(base.Add(29*time.Second)) || !failedStatus.UpdatedAt.Equal(base.Add(31*time.Second)) {
		t.Fatalf("SourceStatus() after failure = %#v, %v", failedStatus, err)
	}
	resumable, err := harness.Store.ResumableSyncRuns(ctx, scope, 100)
	if err != nil || len(resumable) != 0 {
		t.Fatalf("ResumableSyncRuns() = %#v, %v", resumable, err)
	}
}

func runConcurrentBeginContract(t *testing.T, ctx context.Context, harness SourceHarness, scope knowl.ScopeRef, at time.Time) {
	t.Helper()
	type result struct {
		run    knowl.SyncRun
		replay bool
		err    error
	}
	runPair := func(requests [2]app.BeginSyncRequest) [2]result {
		t.Helper()
		stores := [2]app.SourceStateStore{harness.Store, harness.OpenPeer(t)}
		start := make(chan struct{})
		results := make(chan result, 2)
		var wait sync.WaitGroup
		for index, store := range stores {
			wait.Add(1)
			go func(store app.SourceStateStore, request app.BeginSyncRequest) {
				defer wait.Done()
				<-start
				run, replay, err := store.BeginSync(ctx, request)
				results <- result{run: run, replay: replay, err: err}
			}(store, requests[index])
		}
		close(start)
		wait.Wait()
		close(results)
		var outcomes [2]result
		index := 0
		for outcome := range results {
			outcomes[index] = outcome
			index++
		}
		return outcomes
	}

	identical := newContractRun(scope, "concurrent-identical", "run-concurrent-identical", at)
	outcomes := runPair([2]app.BeginSyncRequest{
		{Run: identical, Type: knowl.SourceTypeFilesystem},
		{Run: identical, Type: knowl.SourceTypeFilesystem},
	})
	successes, replays := 0, 0
	for _, outcome := range outcomes {
		if outcome.err != nil || outcome.run.ID != identical.ID {
			t.Fatalf("concurrent identical BeginSync() = %#v, %v", outcome, outcome.err)
		}
		successes++
		if outcome.replay {
			replays++
		}
	}
	if successes != 2 || replays != 1 {
		t.Fatalf("concurrent identical begins successes = %d, replays = %d", successes, replays)
	}
	if _, err := harness.Store.FailSync(ctx, scope, identical.ID, "contract_cleanup", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	first := newContractRun(scope, "concurrent-first", "run-concurrent-conflict", at.Add(2*time.Second))
	second := first
	second.SourceID = "concurrent-second"
	outcomes = runPair([2]app.BeginSyncRequest{
		{Run: first, Type: knowl.SourceTypeFilesystem},
		{Run: second, Type: knowl.SourceTypeFilesystem},
	})
	successes, conflicts := 0, 0
	for _, outcome := range outcomes {
		switch {
		case outcome.err == nil:
			successes++
		case errors.Is(outcome.err, app.ErrSyncConflict):
			conflicts++
		default:
			t.Fatalf("concurrent incompatible BeginSync() returned non-contract error: %v", outcome.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent incompatible begins successes = %d, conflicts = %d", successes, conflicts)
	}
	if _, err := harness.Store.FailSync(ctx, scope, first.ID, "contract_cleanup", at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func runConcurrentGenerationContract(t *testing.T, ctx context.Context, harness SourceHarness, scope knowl.ScopeRef, sourceID knowl.SourceID, at time.Time) {
	t.Helper()
	const (
		runID      = knowl.SyncRunID("run-concurrent-generation")
		checkpoint = "concurrent-checkpoint"
	)
	run := newContractRun(scope, sourceID, runID, at)
	if _, _, err := harness.Store.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	prepared := contractPreparedState(t, runID, scope, sourceID, checkpoint, knowl.SyncCounts{}, nil, at.Add(time.Second))
	if _, err := harness.Store.PrepareSync(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	peer := harness.OpenPeer(t)
	type result struct {
		generation string
		err        error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	stores := []app.SourceStateStore{harness.Store, peer}
	generations := []string{"generation-a", "generation-b"}
	var wait sync.WaitGroup
	for index, store := range stores {
		wait.Add(1)
		go func(store app.SourceStateStore, generation string) {
			defer wait.Done()
			<-start
			_, err := store.MarkContentCommitted(ctx, app.SyncGeneration{RunID: runID, Scope: scope, SourceID: sourceID, Generation: generation, UpdatedAt: at.Add(2 * time.Second)})
			results <- result{generation: generation, err: err}
		}(store, generations[index])
	}
	close(start)
	wait.Wait()
	close(results)
	winner := ""
	conflicts := 0
	for outcome := range results {
		if outcome.err == nil {
			if winner != "" {
				t.Fatalf("two incompatible generations succeeded: %q and %q", winner, outcome.generation)
			}
			winner = outcome.generation
			continue
		}
		if errors.Is(outcome.err, app.ErrSyncConflict) || (harness.IsConflict != nil && harness.IsConflict(outcome.err)) {
			conflicts++
			continue
		}
		t.Fatalf("concurrent generation error = %v", outcome.err)
	}
	if winner == "" || conflicts != 1 {
		t.Fatalf("concurrent generations winner = %q, conflicts = %d", winner, conflicts)
	}
	transition := app.SyncGeneration{RunID: runID, Scope: scope, SourceID: sourceID, Generation: winner, UpdatedAt: at.Add(3 * time.Second)}
	if _, err := harness.Store.MarkProjected(ctx, transition); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.Store.FinalizeSync(ctx, app.SyncFinalization{RunID: runID, Scope: scope, SourceID: sourceID, CandidateDigest: prepared.CandidateDigest, Generation: winner, Checkpoint: checkpoint, FinalizedAt: at.Add(4 * time.Second)}); err != nil {
		t.Fatal(err)
	}
}

func newContractRun(scope knowl.ScopeRef, sourceID knowl.SourceID, id knowl.SyncRunID, at time.Time) knowl.SyncRun {
	return knowl.SyncRun{ID: id, Scope: scope, SourceID: sourceID, ConfigDigest: strings.Repeat("1", 64), Status: knowl.SyncStatusScanning, StartedAt: at, UpdatedAt: at}
}

func contractDocumentState(scope knowl.ScopeRef, sourceID knowl.SourceID, documentID knowl.DocumentID, runID knowl.SyncRunID, revision string, at time.Time) knowl.DocumentState {
	return knowl.DocumentState{Scope: scope, SourceID: sourceID, DocumentID: documentID, Revision: revision,
		AcceptedSource:      knowl.AcceptedSource{Scope: scope, Source: knowl.SourceRef{Adapter: "wiki-filesystem", ID: string(sourceID) + "/" + string(documentID)}, Version: knowl.SourceVersion{Version: revision, Digest: strings.Repeat("d", 64)}, MediaType: "text/markdown", SourceDocument: knowl.SourceDocument{SourceID: sourceID, DocumentID: documentID, Revision: revision, URI: "https://wiki.example.test/" + string(documentID)}, ManifestRef: "raw/manifest-1.json"},
		MaintenanceRevision: revision, MaintenanceOperationID: knowl.OperationID("operation-" + string(sourceID) + "-" + revision),
		MirrorPath: "wiki/sources/" + string(sourceID) + "/" + string(documentID), MirrorDigest: strings.Repeat("e", 64), LastSeenRunID: runID, CreatedAt: at, UpdatedAt: at}
}

func finishContractRun(t *testing.T, ctx context.Context, store app.SourceStateStore, run knowl.SyncRun, state knowl.DocumentState, generation string, at time.Time) {
	t.Helper()
	action := app.SyncDocumentActive
	counts := knowl.SyncCounts{Added: 1}
	if state.Deleted {
		action, counts = app.SyncDocumentTombstone, knowl.SyncCounts{Deleted: 1}
	}
	prepared := contractPreparedState(t, run.ID, run.Scope, run.SourceID, string(run.ID)+"-checkpoint", counts,
		[]app.PreparedDocumentState{{Action: action, State: state}}, at)
	if _, err := store.PrepareSync(ctx, prepared); err != nil {
		t.Fatalf("prepare helper run: %v", err)
	}
	transition := app.SyncGeneration{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, Generation: generation, UpdatedAt: at.Add(time.Second)}
	if _, err := store.MarkContentCommitted(ctx, transition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkProjected(ctx, transition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeSync(ctx, app.SyncFinalization{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, CandidateDigest: prepared.CandidateDigest, Generation: generation, Checkpoint: prepared.Checkpoint, Counts: counts, FinalizedAt: at.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
}

func contractPreparedState(t *testing.T, runID knowl.SyncRunID, scope knowl.ScopeRef, sourceID knowl.SourceID, checkpoint string, counts knowl.SyncCounts, documents []app.PreparedDocumentState, preparedAt time.Time) app.PreparedSyncState {
	t.Helper()
	prepared := app.PreparedSyncState{
		RunID: runID, Scope: scope, SourceID: sourceID, CompleteScan: true, Checkpoint: checkpoint,
		Counts: counts, Documents: documents, PreparedAt: preparedAt,
	}
	digest, err := app.PreparedSyncDigest(prepared)
	if err != nil {
		t.Fatalf("canonical prepared digest: %v", err)
	}
	prepared.CandidateDigest = digest
	return prepared
}

func runRecoveryReadsContract(t *testing.T, ctx context.Context, harness SourceHarness, scope knowl.ScopeRef, at time.Time) {
	t.Helper()
	const (
		recoverySource = knowl.SourceID("recovery")
		documentB      = knowl.DocumentID("docs/b.md")
		documentA      = knowl.DocumentID("docs/a.md")
		documentC      = knowl.DocumentID("docs/c.md")
	)
	run := newContractRun(scope, recoverySource, "run-recovery", at)
	if _, _, err := harness.Store.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := harness.Store.ScanDocuments(canceled, scope, run.ID, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ScanDocuments() error = %v", err)
	}
	if _, err := harness.Store.ScanDocuments(ctx, scope, run.ID, 0); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("ScanDocuments() zero limit error = %v", err)
	}
	if _, err := harness.Store.ScanDocuments(ctx, scope, run.ID, 1001); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("ScanDocuments() over-limit error = %v", err)
	}
	if _, err := harness.Store.ScanDocuments(ctx, scope, "missing-run", 10); !errors.Is(err, app.ErrSyncRunNotFound) {
		t.Fatalf("ScanDocuments() unknown run error = %v", err)
	}
	firstPage := []knowl.DocumentRef{
		{ExternalID: documentB, Revision: "revision-b", Path: string(documentB)},
		{ExternalID: documentA, Revision: "revision-a", Path: string(documentA)},
	}
	if _, err := harness.Store.RecordScanPage(ctx, app.ScanPageRecord{RunID: run.ID, Scope: scope, SourceID: recoverySource, Documents: firstPage, NextPageToken: "token-2", RecordedAt: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	secondPage := []knowl.DocumentRef{{ExternalID: documentC, Revision: "revision-c", Path: string(documentC)}}
	if _, err := harness.Store.RecordScanPage(ctx, app.ScanPageRecord{RunID: run.ID, Scope: scope, SourceID: recoverySource, ExpectedPageToken: "token-2", Documents: secondPage, RecordedAt: at.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	scanned, err := harness.Store.ScanDocuments(ctx, scope, run.ID, 100)
	if err != nil || len(scanned) != 3 || scanned[0].ExternalID != documentB || scanned[1].ExternalID != documentA || scanned[2].ExternalID != documentC {
		t.Fatalf("ScanDocuments() = %#v, %v", scanned, err)
	}
	bounded, err := harness.Store.ScanDocuments(ctx, scope, run.ID, 2)
	if err != nil || len(bounded) != 2 || bounded[0].ExternalID != "docs/b.md" || bounded[1].ExternalID != "docs/a.md" {
		t.Fatalf("bounded ScanDocuments() = %#v, %v", bounded, err)
	}
	if _, err := harness.Store.PreparedSync(ctx, scope, run.ID); !errors.Is(err, app.ErrSyncStateTransition) {
		t.Fatalf("PreparedSync() before prepare error = %v, want state transition", err)
	}
	states := make([]app.PreparedDocumentState, 0, len(scanned))
	for index, ref := range scanned {
		state := contractDocumentState(scope, recoverySource, ref.ExternalID, run.ID, ref.Revision, at.Add(3*time.Second))
		action := app.SyncDocumentActive
		if index == len(scanned)-1 {
			// Exercise durable tombstone reconstruction in both backends.
			action = app.SyncDocumentTombstone
			state.Deleted = true
			state.DeletedAt = at.Add(3 * time.Second)
			state.MirrorPath = ""
			state.MirrorDigest = ""
		}
		states = append(states, app.PreparedDocumentState{Action: action, State: state})
	}
	counts := knowl.SyncCounts{Added: int64(len(states) - 1), Deleted: 1}
	prepared := contractPreparedState(t, run.ID, scope, recoverySource, "recovery-checkpoint", counts, states, at.Add(3*time.Second))
	if _, err := harness.Store.PrepareSync(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	assertPreparedRead(t, harness.Store, prepared)
	if harness.OpenPeer != nil {
		reopened := harness.OpenPeer(t)
		reopenedScan, scanErr := reopened.ScanDocuments(ctx, scope, run.ID, 100)
		if scanErr != nil || len(reopenedScan) != len(scanned) {
			t.Fatalf("reopened ScanDocuments() = %#v, %v", reopenedScan, scanErr)
		}
		for index := range scanned {
			if reopenedScan[index].ExternalID != scanned[index].ExternalID || reopenedScan[index].Revision != scanned[index].Revision || reopenedScan[index].Path != scanned[index].Path {
				t.Fatalf("reopened descriptor %d = %#v, want %#v", index, reopenedScan[index], scanned[index])
			}
		}
		assertPreparedRead(t, reopened, prepared)
	}
	if _, err := harness.Store.FailSync(ctx, scope, run.ID, "contract_cleanup", at.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.Store.PreparedSync(ctx, scope, run.ID); !errors.Is(err, app.ErrSyncStateTransition) {
		t.Fatalf("PreparedSync() after failure error = %v, want state transition", err)
	}
}

func assertPreparedRead(t *testing.T, store app.SourceStateStore, prepared app.PreparedSyncState) {
	t.Helper()
	read, err := store.PreparedSync(context.Background(), prepared.Scope, prepared.RunID)
	if err != nil {
		t.Fatalf("PreparedSync() error = %v", err)
	}
	if read.RunID != prepared.RunID || read.Scope != prepared.Scope || read.SourceID != prepared.SourceID ||
		read.Checkpoint != prepared.Checkpoint || read.Counts != prepared.Counts || read.CandidateDigest != prepared.CandidateDigest {
		t.Fatalf("PreparedSync() header = %#v, want digest %q", read, prepared.CandidateDigest)
	}
	expected, err := app.NormalizePreparedDocuments(prepared.Scope, prepared.SourceID, prepared.RunID, prepared.Documents)
	if err != nil {
		t.Fatalf("normalize expected candidates: %v", err)
	}
	if len(read.Documents) != len(expected) {
		t.Fatalf("PreparedSync() documents = %d, want %d", len(read.Documents), len(expected))
	}
	for index, document := range expected {
		restored := read.Documents[index]
		state := restored.State
		source := document.State
		if restored.Action != document.Action || state.DocumentID != source.DocumentID || state.Revision != source.Revision ||
			state.AcceptedSource != source.AcceptedSource || state.MaintenanceRevision != source.MaintenanceRevision ||
			state.MaintenanceOperationID != source.MaintenanceOperationID || state.MirrorPath != source.MirrorPath ||
			state.MirrorDigest != source.MirrorDigest || state.LastSeenRunID != source.LastSeenRunID ||
			state.Deleted != source.Deleted || !state.DeletedAt.Equal(source.DeletedAt) {
			t.Fatalf("PreparedSync() document %d = %#v, want %#v", index, restored, document)
		}
	}
}
