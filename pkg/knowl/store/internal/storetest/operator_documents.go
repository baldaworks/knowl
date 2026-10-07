package storetest

import (
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// RunOperatorDocuments verifies saved heads, maintenance outcomes and bytewise continuation.
func RunOperatorDocuments(t *testing.T, store app.OperationStore, scope knowl.ScopeRef) {
	t.Helper()
	lister, ok := store.(app.SourceDocumentLister)
	if !ok {
		t.Fatal("store lacks optional document listing")
	}
	source := store.(app.SourceStateStore)
	ctx := t.Context()
	base := time.Unix(200, 0).UTC()
	const sourceID knowl.SourceID = "document-owner"
	const firstDocument = "A.md"
	const failedDocument = "a.md"
	const deletedDocument = "gone.md"
	const missingDocument = "missing.md"
	run := newContractRun(scope, sourceID, knowl.SyncRunID("operator-documents-"+string(scope)), base)
	if _, _, err := source.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	ids := []knowl.DocumentID{firstDocument, failedDocument, deletedDocument, missingDocument, "z.md", "é.md"}
	foreignKey, foreignMeta := Fixture(childScope(scope, "foreign"), "foreign-maintenance", base)
	foreignOperation, err := store.Reserve(ctx, foreignKey, foreignMeta)
	if err != nil {
		t.Fatal(err)
	}
	var documents []app.PreparedDocumentState
	operations := make(map[knowl.DocumentID]knowl.OperationID)
	for _, id := range ids {
		state := contractDocumentState(scope, sourceID, id, run.ID, testSourceRevision, base)
		state.AcceptedSource.ManifestRef = "raw/private-manifest-canary.json"
		state.AcceptedSource.SourceDocument.URI = (&url.URL{Scheme: "file", Path: "/private/uri-canary/" + string(id)}).String()
		state.MirrorPath = "wiki/private-mirror-canary/" + string(id)
		if id == missingDocument {
			state.MaintenanceOperationID = foreignOperation.ID
		}
		if id != missingDocument {
			key, meta := Fixture(scope, string(id), base)
			meta.AcceptedSource = state.AcceptedSource
			key.Source, key.Version = meta.AcceptedSource.Source, meta.AcceptedSource.Version
			meta.Key = key
			reserved, err := store.Reserve(ctx, key, meta)
			if err != nil {
				t.Fatal(err)
			}
			state.MaintenanceOperationID = reserved.ID
			operations[id] = reserved.ID
			if id == failedDocument {
				if err := store.Fail(ctx, reserved.ID, knowl.Failure{Class: testProviderFailureClass, Reason: "private-failure-canary"}); err != nil {
					t.Fatal(err)
				}
			}
		}
		action := app.SyncDocumentActive
		if id == deletedDocument {
			action = app.SyncDocumentTombstone
			state.Deleted = true
			state.DeletedAt = base
		}
		documents = append(documents, app.PreparedDocumentState{Action: action, State: state})
	}
	prepared := contractPreparedState(t, run.ID, scope, sourceID, "private-checkpoint-canary", knowl.SyncCounts{Added: 5, Deleted: 1}, documents, base.Add(time.Second))
	finalizeOperatorDocuments(t, source, run, prepared, base.Add(2*time.Second))
	before, err := source.DocumentStates(ctx, scope, sourceID, app.DocumentListOptions{Limit: 100, IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	// A partial later scan cannot turn an omitted saved document into a deletion.
	later := newContractRun(scope, sourceID, knowl.SyncRunID("operator-partial-"+string(scope)), base.Add(10*time.Second))
	if _, _, err := source.BeginSync(ctx, app.BeginSyncRequest{Run: later, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RecordScanPage(ctx, app.ScanPageRecord{RunID: later.ID, Scope: scope, SourceID: sourceID, Documents: []knowl.DocumentRef{{ExternalID: firstDocument, Path: firstDocument, Revision: "next"}}, NextPageToken: "partial", RecordedAt: base.Add(11 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.FailSync(ctx, scope, later.ID, testProviderFailureClass, base.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	statusBefore, err := source.SourceStatus(ctx, scope, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if statusBefore.Status != knowl.SyncStatusFailed || !statusBefore.LastSuccessfulAt.Equal(base.Add(4*time.Second)) {
		t.Fatalf("retained success: %+v", statusBefore)
	}
	// Other configured owners and other scopes must never leak into this inventory.
	for _, other := range []struct {
		scope  knowl.ScopeRef
		source knowl.SourceID
	}{{scope, "other-owner"}, {childScope(scope, "foreign"), sourceID}} {
		foreign := newContractRun(other.scope, other.source, knowl.SyncRunID("operator-foreign-"+string(other.scope)+string(other.source)), base)
		if _, _, err := source.BeginSync(ctx, app.BeginSyncRequest{Run: foreign, Type: knowl.SourceTypeFilesystem}); err != nil {
			t.Fatal(err)
		}
		finishContractRun(t, ctx, source, foreign, contractDocumentState(other.scope, other.source, "foreign.md", foreign.ID, testSourceRevision, base), "foreign-generation", base.Add(time.Second))
	}
	operationBefore := make(map[knowl.OperationID]knowl.Operation)
	for _, id := range operations {
		operation, err := store.Operation(ctx, scope, id)
		if err != nil {
			t.Fatal(err)
		}
		operationBefore[id] = operation
	}
	var got []knowl.OperatorDocumentSummary
	key := ""
	for range 5 {
		page, err := lister.ListSourceDocuments(ctx, scope, sourceID, app.OperatorReadOptions{Limit: 2, Continuation: app.OperatorContinuation{Key: key}})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 2 {
			t.Fatalf("unbounded page: %+v", page)
		}
		got = append(got, page.Items...)
		key = page.NextKey
		if key == "" {
			break
		}
	}
	if key != "" || len(got) != len(ids) {
		t.Fatalf("continuation failed: %+v key=%q", got, key)
	}
	for i, item := range got {
		if item.ID != ids[i] || item.Revision != testSourceRevision || item.Deleted != (item.ID == deletedDocument) {
			t.Fatalf("saved state %d: %+v", i, item)
		}
		// Parse the public contract; allowlisted fields are also the secret-canary boundary.
		payload, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(payload, &fields); err != nil {
			t.Fatal(err)
		}
		allowed := map[string]bool{"id": true, "revision": true, "accepted_revision": true, "maintenance_revision": true, "maintenance_operation_id": true, "maintenance_status": true, "deleted": true, "updated_at": true}
		for field := range fields {
			if !allowed[field] {
				t.Fatalf("unexpected public field %q", field)
			}
		}
		if fields["accepted_revision"] != testSourceRevision {
			t.Fatalf("accepted revision missing: %+v", fields)
		}
		wantStatus := string(knowl.StatusReceived)
		if item.ID == failedDocument {
			wantStatus = string(knowl.StatusFailed)
		}
		if item.ID == missingDocument {
			if _, exists := fields["maintenance_status"]; exists {
				t.Fatalf("invented operation status: %+v", fields)
			}
		} else if fields["maintenance_status"] != wantStatus || item.MaintenanceOperationID != operations[item.ID] {
			t.Fatalf("durable maintenance association: %+v", fields)
		}
		assertOperatorCanaries(t, fields)
	}
	for _, limit := range []int{-1, 0, 101} {
		if _, err := lister.ListSourceDocuments(ctx, scope, sourceID, app.OperatorReadOptions{Limit: limit}); !errors.Is(err, app.ErrOperatorLimitInvalid) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	for _, position := range []string{"../bad", strings.Repeat("a", 4097)} {
		if _, err := lister.ListSourceDocuments(ctx, scope, sourceID, app.OperatorReadOptions{Limit: 1, Continuation: app.OperatorContinuation{Key: position}}); !errors.Is(err, app.ErrOperatorCursorInvalid) {
			t.Fatalf("position: %v", err)
		}
	}
	empty, err := lister.ListSourceDocuments(ctx, scope, "never-run", app.OperatorReadOptions{Limit: 100})
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.NextKey != "" {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	after, err := source.DocumentStates(ctx, scope, sourceID, app.DocumentListOptions{Limit: 100, IncludeDeleted: true})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("read mutated documents: %v", err)
	}
	for id, before := range operationBefore {
		after, err := store.Operation(ctx, scope, id)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("read mutated operation %q: %v", id, err)
		}
	}
	statusAfter, err := source.SourceStatus(ctx, scope, sourceID)
	if err != nil || !reflect.DeepEqual(statusBefore, statusAfter) {
		t.Fatalf("read mutated source: %v", err)
	}
}

func finalizeOperatorDocuments(t *testing.T, store app.SourceStateStore, run knowl.SyncRun, prepared app.PreparedSyncState, at time.Time) {
	t.Helper()
	if _, err := store.PrepareSync(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	transition := app.SyncGeneration{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, Generation: "operator-document-generation", UpdatedAt: at}
	if _, err := store.MarkContentCommitted(t.Context(), transition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkProjected(t.Context(), transition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeSync(t.Context(), app.SyncFinalization{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, CandidateDigest: prepared.CandidateDigest, Generation: transition.Generation, Checkpoint: prepared.Checkpoint, Counts: prepared.Counts, FinalizedAt: at.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
}

func assertOperatorCanaries(t *testing.T, value any) {
	t.Helper()
	switch value := value.(type) {
	case string:
		for _, canary := range []string{"raw/private-manifest-canary.json", "private-failure-canary", "private-checkpoint-canary"} {
			if value == canary {
				t.Fatalf("private value serialized: %q", value)
			}
		}
	case map[string]any:
		for _, child := range value {
			assertOperatorCanaries(t, child)
		}
	case []any:
		for _, child := range value {
			assertOperatorCanaries(t, child)
		}
	}
}
