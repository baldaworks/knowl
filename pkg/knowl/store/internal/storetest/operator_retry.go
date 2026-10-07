package storetest

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// RunOperatorRetryOperations verifies listing after real generation-changing retries.
func RunOperatorRetryOperations(t *testing.T, store app.OperationStore, scope knowl.ScopeRef) {
	t.Helper()
	ctx := t.Context()
	source := store.(app.SourceStateStore)
	lister := store.(app.OperationLister)
	base := time.Unix(100, 0).UTC()
	oldGeneration, currentGeneration := strings.Repeat("a", 64), strings.Repeat("b", 64)
	run := newContractRun(scope, operatorConfiguredSourceID, "operator-retry-run", base)
	if _, _, err := source.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	var documents []app.PreparedDocumentState
	var oldIDs []knowl.OperationID
	var schema knowl.SchemaDocument
	for i, name := range []string{"retry-a", "retry-b"} {
		key, meta := Fixture(scope, name, base.Add(time.Duration(i)*time.Second))
		key.MaintenanceGeneration = oldGeneration
		meta.Key = key
		meta.MaintenanceGeneration = oldGeneration
		reserved, err := store.Reserve(ctx, key, meta)
		if err != nil {
			t.Fatal(err)
		}
		oldIDs = append(oldIDs, reserved.ID)
		schema = meta.Schema
		if err := store.Fail(ctx, reserved.ID, knowl.Failure{Class: testProviderFailureClass, Reason: testProviderFailureReason}); err != nil {
			t.Fatal(err)
		}
		state := contractDocumentState(scope, operatorConfiguredSourceID, meta.AcceptedSource.SourceDocument.DocumentID, run.ID, key.Version.Version, base)
		state.AcceptedSource = meta.AcceptedSource
		state.MaintenanceRevision = key.Version.Version
		state.MaintenanceOperationID = reserved.ID
		state.MaintenanceGeneration = oldGeneration
		documents = append(documents, app.PreparedDocumentState{Action: app.SyncDocumentActive, State: state})
	}
	prepared := contractPreparedState(t, run.ID, scope, operatorConfiguredSourceID, "retry-checkpoint", knowl.SyncCounts{Added: 2}, documents, base.Add(2*time.Second))
	if _, err := source.PrepareSync(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	transition := app.SyncGeneration{RunID: run.ID, Scope: scope, SourceID: operatorConfiguredSourceID, Generation: "retry-content", UpdatedAt: base.Add(3 * time.Second)}
	if _, err := source.MarkContentCommitted(ctx, transition); err != nil {
		t.Fatal(err)
	}
	if _, err := source.MarkProjected(ctx, transition); err != nil {
		t.Fatal(err)
	}
	if _, err := source.FinalizeSync(ctx, app.SyncFinalization{RunID: run.ID, Scope: scope, SourceID: operatorConfiguredSourceID, CandidateDigest: prepared.CandidateDigest, Generation: transition.Generation, Checkpoint: prepared.Checkpoint, Counts: prepared.Counts, FinalizedAt: base.Add(4 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	before, err := lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	request := app.SourceMaintenanceRetryRequest{Scope: scope, SourceID: operatorConfiguredSourceID, FailureClasses: []string{testProviderFailureClass}, MaintenanceGeneration: currentGeneration, Schema: schema}
	started := time.Now().UTC().Add(-time.Second)
	retried, err := source.RetrySourceMaintenance(ctx, request)
	finished := time.Now().UTC().Add(time.Second)
	if err != nil || retried.Matched != 2 || retried.Requeued != 2 || len(retried.OperationIDs) != 2 {
		t.Fatalf("generation retry: %+v %v", retried, err)
	}
	newIDs := append([]knowl.OperationID{}, retried.OperationIDs...)
	sort.Slice(newIDs, func(i, j int) bool { return newIDs[i] > newIDs[j] })
	t.Run("configured_source_and_status_filters", func(t *testing.T) {
		page, err := lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 100}, SourceID: operatorConfiguredSourceID, Status: knowl.StatusReceived})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 2 {
			t.Fatalf("retry source-filter inventory: %+v", page)
		}
		for i, item := range page.Items {
			if item.ID != newIDs[i] || item.SourceID != operatorConfiguredSourceID || item.CreatedAt.Before(started) || item.CreatedAt.After(finished) || !item.CreatedAt.Equal(item.UpdatedAt) {
				t.Fatalf("new-generation summary: %+v", item)
			}
		}
		historical, err := lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 100}, SourceID: operatorConfiguredSourceID, Status: knowl.StatusFailed})
		if err != nil || len(historical.Items) != 2 {
			t.Fatalf("historical filter: %+v %v", historical, err)
		}
	})
	t.Run("terminating_chronological_pagination", func(t *testing.T) {
		seen := make(map[knowl.OperationID]bool)
		var items []knowl.OperatorOperationSummary
		continuation := ""
		for range 5 {
			page, err := lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 1, Continuation: app.OperatorContinuation{Key: continuation}}})
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range page.Items {
				if seen[item.ID] {
					t.Fatalf("retry pagination repeated %q", item.ID)
				}
				seen[item.ID] = true
				items = append(items, item)
			}
			continuation = page.NextKey
			if continuation == "" {
				break
			}
		}
		if continuation != "" || len(items) != 4 {
			t.Fatalf("retry pagination did not terminate with four rows: %+v", items)
		}
		for i, id := range newIDs {
			if items[i].ID != id {
				t.Fatalf("new generation ordered behind history: %+v", items)
			}
		}
		for _, item := range before.Items {
			if !seen[item.ID] {
				t.Fatalf("historical ID missing: %q", item.ID)
			}
		}
	})
	// A current-generation requeue must preserve the immutable creation tuple.
	target := newIDs[0]
	page, err := lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	var created time.Time
	for _, item := range page.Items {
		if item.ID == target {
			created = item.CreatedAt
		}
	}
	if created.IsZero() {
		t.Fatal("new retry operation missing")
	}
	if err := store.Fail(ctx, target, knowl.Failure{Class: testProviderFailureClass, Reason: testProviderFailureReason}); err != nil {
		t.Fatal(err)
	}
	same, err := source.RetrySourceMaintenance(ctx, request)
	if err != nil || same.Requeued != 1 || len(same.OperationIDs) != 1 || same.OperationIDs[0] != target {
		t.Fatalf("current-generation retry: %+v %v", same, err)
	}
	page, err = lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.ID == target && !item.CreatedAt.Equal(created) {
			t.Fatalf("current-generation retry changed creation tuple: %+v", item)
		}
	}
	replay, err := source.RetrySourceMaintenance(ctx, request)
	if err != nil || replay.Matched != 0 || replay.Requeued != 0 {
		t.Fatalf("retry replay: %+v %v", replay, err)
	}
	for i, id := range oldIDs {
		operation, err := store.Operation(ctx, scope, id)
		if err != nil || operation.Status != knowl.StatusFailed {
			t.Fatalf("old operation %d: %+v %v", i, operation, err)
		}
	}
}
