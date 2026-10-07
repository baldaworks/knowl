package storetest

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const operatorConfiguredSourceID knowl.SourceID = "configured-wiki"

// RunOperatorOperations checks immutable pagination and stored configured ownership.
func RunOperatorOperations(t *testing.T, store app.OperationStore, scope knowl.ScopeRef) {
	t.Helper()
	lister, ok := store.(app.OperationLister)
	if !ok {
		t.Fatal("store lacks optional operation listing")
	}
	ctx := t.Context()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var expected []knowl.OperatorOperationSummary
	for i, name := range []string{"whole", "fraction", "tie-a", "tie-b", "offset", "legacy", "A", "z", "é"} {
		at := base
		switch i {
		case 1:
			at = at.Add(100 * time.Millisecond)
		case 2, 3, 6, 7, 8:
			at = at.Add(200 * time.Millisecond)
		case 4:
			at = at.Add(time.Microsecond).In(time.FixedZone("fixture", 3600))
		}
		key, meta := Fixture(scope, name, at)
		meta.AcceptedSource.SourceDocument.URI = (&url.URL{Scheme: "file", Path: "/srv/wiki/" + name + ".md"}).String()
		if i == 5 {
			meta.AcceptedSource.SourceDocument = knowl.SourceDocument{}
		}
		r, err := store.Reserve(ctx, key, meta)
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, knowl.OperatorOperationSummary{ID: r.ID, Kind: knowl.WorkSourceMaintenance, Status: knowl.StatusReceived, SourceID: meta.AcceptedSource.SourceDocument.SourceID, CreatedAt: at.UTC(), UpdatedAt: r.UpdatedAt.UTC()})
	}
	sort.Slice(expected, func(i, j int) bool {
		if expected[i].CreatedAt.Equal(expected[j].CreatedAt) {
			return expected[i].ID > expected[j].ID
		}
		return expected[i].CreatedAt.After(expected[j].CreatedAt)
	})
	key, meta := Fixture(childScope(scope, "foreign"), "foreign", base.Add(time.Hour))
	foreign, err := store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatal(err)
	}
	first, err := lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if first.NextKey == "" || len(first.Items) != 2 {
		t.Fatalf("first page: %+v", first)
	}
	if err := store.Fail(ctx, expected[0].ID, knowl.Failure{Class: "fixture"}); err != nil {
		t.Fatal(err)
	}
	got := append([]knowl.OperatorOperationSummary{}, first.Items...)
	continuation := first.NextKey
	for continuation != "" {
		page, err := lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 2, Continuation: app.OperatorContinuation{Key: continuation}}})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page.Items...)
		continuation = page.NextKey
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("chronological pagination got %+v want %+v", got, expected)
	}
	page, err := lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 100}, SourceID: operatorConfiguredSourceID, Status: knowl.StatusReceived})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != len(expected)-2 || page.NextKey != "" {
		t.Fatalf("configured/status filter: %+v", page)
	}
	filteredWant := page.Items
	var filteredGot []knowl.OperatorOperationSummary
	continuation = ""
	for {
		filtered, err := lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 2, Continuation: app.OperatorContinuation{Key: continuation}}, SourceID: operatorConfiguredSourceID, Status: knowl.StatusReceived})
		if err != nil {
			t.Fatal(err)
		}
		filteredGot = append(filteredGot, filtered.Items...)
		continuation = filtered.NextKey
		if continuation == "" {
			break
		}
	}
	if !reflect.DeepEqual(filteredGot, filteredWant) {
		t.Fatalf("filtered pagination: got %+v want %+v", filteredGot, filteredWant)
	}
	for _, item := range page.Items {
		if item.SourceID != operatorConfiguredSourceID || item.Status != knowl.StatusReceived {
			t.Fatalf("filtered item: %+v", item)
		}
	}
	page, err = lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 100}, SourceID: "legacy"})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("invented source: %+v %v", page, err)
	}
	foreignKey, err := app.EncodeOperatorOperationPosition(app.OperatorOperationPosition{CreatedAt: meta.CreatedAt.UTC(), OperationID: foreign.ID})
	if err != nil {
		t.Fatal(err)
	}
	page, err = lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 100, Continuation: app.OperatorContinuation{Key: foreignKey}}})
	if err != nil || len(page.Items) != len(expected) {
		t.Fatalf("trusted scope: %+v %v", page, err)
	}
	for _, limit := range []int{0, -1, 101} {
		_, err := lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: limit}})
		if !errors.Is(err, app.ErrOperatorLimitInvalid) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	_, err = lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 2, Continuation: app.OperatorContinuation{Key: "invalid"}}})
	if !errors.Is(err, app.ErrOperatorCursorInvalid) {
		t.Fatalf("cursor: %v", err)
	}
	// Enrichment of an existing legacy reservation must update the relational owner.
	key, meta = Fixture(scope, "legacy", base)
	if _, err := store.Reserve(ctx, key, meta); err != nil {
		t.Fatal(err)
	}
	page, err = lister.ListOperations(ctx, scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 100}, SourceID: operatorConfiguredSourceID})
	if err != nil || len(page.Items) != len(expected) {
		t.Fatalf("enrichment: %+v %v", page, err)
	}
}

// OperatorHistory records durable values that additive migrations must preserve.
type OperatorHistory struct {
	ID                                                                     string
	Attempt, WorkAttempt, RetryAttempt, ManualRetryCount, RetrievalAttempt int
	CreatedAt, UpdatedAt, Status, Document, Execution, Plan                string
	Context, Correction, Retrieval                                         *string
}

// OperatorMigrationHarness controls a real migration upgrade and raw legacy fixtures.
type OperatorMigrationHarness struct {
	Store     app.OperationStore
	Scope     knowl.ScopeRef
	Downgrade func(*testing.T)
	Upgrade   func(*testing.T)
	Document  func(*testing.T, knowl.OperationID, string)
	History   func(*testing.T) []OperatorHistory
}

// RunOperatorMigration verifies validated backfill without losing durable history.
func RunOperatorMigration(t *testing.T, h OperatorMigrationHarness) {
	t.Helper()
	ctx := t.Context()
	key, meta := Fixture(h.Scope, "retained", time.Unix(50, 123456789).UTC())
	r, err := h.Store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := h.Store.ClaimOperation(ctx, h.Scope, r.ID, futureLease("operator-upgrade"))
	if err != nil {
		t.Fatal(err)
	}
	writer := h.Store.(app.OperationContextReportStore)
	if err := writer.SaveOperationContextReport(ctx, h.Scope, r.ID, claim.Operation.WorkAttempt, knowl.OperationContextReport{Version: 1, WorkAttempt: claim.Operation.WorkAttempt, Outcome: knowl.ContextSelectionFailed}); err != nil {
		t.Fatal(err)
	}
	zero := 0
	correction := knowl.OperationCorrectionReport{Version: 1, WorkAttempt: claim.Operation.WorkAttempt, MaxOutputBytes: app.MaxCorrectionOutputBytes, DeadlineNanos: int64(app.MaxCorrectionDeadline), Outcome: knowl.CorrectionProviderFailed, Turns: &zero, Corrections: &zero, OutputBytes: &zero}
	if err := h.Store.(app.OperationCorrectionReportStore).SaveOperationCorrectionReport(ctx, h.Scope, r.ID, claim.Operation.WorkAttempt, correction); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.(app.RetrievalReportStore).SaveRetrievalReport(ctx, h.Scope, r.ID, claim.Operation.WorkAttempt, knowl.RetrievalReport{Requested: knowl.RetrievalLexical, Effective: knowl.RetrievalLexical}); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.SavePlan(ctx, r.ID, knowl.PlanSummary{Digest: strings.Repeat("b", 64), FileCount: 2}); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.MarkApplying(ctx, r.ID, knowl.Lease{Token: "operator-apply", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	before, err := h.Store.Operation(ctx, h.Scope, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := h.Store.Execution(ctx, h.Scope, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	identity := knowl.OperationIdentity{Scope: h.Scope, Kind: knowl.WorkHierarchy, Subject: "hierarchy-v1", Revision: meta.Schema.Digest, Digest: strings.Repeat("c", 64)}
	id, err := app.OperationIDForIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	hierarchy := knowl.ExecutionDescriptor{OperationID: id, Kind: identity.Kind, Schema: meta.Schema, Hierarchy: &knowl.HierarchyExecutionDescriptor{SnapshotDigest: identity.Digest, PlannerVersion: identity.Subject}}
	if _, err := h.Store.ReserveOperation(ctx, identity, hierarchy); err != nil {
		t.Fatal(err)
	}
	var corrupt []knowl.OperationID
	for i := range 105 {
		key, meta := Fixture(h.Scope, fmt.Sprintf("legacy-%03d", i), time.Unix(60, int64(i)*1000).UTC())
		meta.AcceptedSource.SourceDocument = knowl.SourceDocument{}
		value, err := h.Store.Reserve(ctx, key, meta)
		if err != nil {
			t.Fatal(err)
		}
		if i < 7 {
			corrupt = append(corrupt, value.ID)
		}
	}
	h.Downgrade(t)
	for i, payload := range []string{"{", `{"source_id":"configured-wiki"}`, `{"source_id":"bad/owner","document_id":"a","revision":"1","uri":"file:///a"}`, strings.Repeat("x", 128<<10), `null`, `{"source_id":"configured-wiki","document_id":"a","revision":"1","uri":"file:///a","unknown":1}`, `{"source_id":"configured-wiki","document_id":"a","revision":"1","uri":"relative"}`} {
		h.Document(t, corrupt[i], payload)
	}
	history := h.History(t)
	h.Upgrade(t)
	h.Upgrade(t) // The normal provider must be idempotent.
	if after := h.History(t); !reflect.DeepEqual(after, history) {
		t.Fatal("upgrade altered durable operation history")
	}
	after, err := h.Store.Operation(ctx, h.Scope, r.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("operation retention: %+v %v", after, err)
	}
	execution, err := h.Store.Execution(ctx, h.Scope, r.ID)
	if err != nil || !reflect.DeepEqual(execution, descriptor) {
		t.Fatalf("execution retention: %+v %v", execution, err)
	}
	lister := h.Store.(app.OperationLister)
	page, err := lister.ListOperations(ctx, h.Scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 100}, SourceID: operatorConfiguredSourceID})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != r.ID {
		t.Fatalf("validated source backfill: %+v %v", page, err)
	}
	var got []knowl.OperatorOperationSummary
	var continuation string
	for {
		page, err := lister.ListOperations(ctx, h.Scope, app.OperatorOperationReadOptions{OperatorReadOptions: app.OperatorReadOptions{Limit: 100, Continuation: app.OperatorContinuation{Key: continuation}}})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page.Items...)
		continuation = page.NextKey
		if continuation == "" {
			break
		}
	}
	if len(got) != 107 {
		t.Fatalf("legacy inventory = %d want 107", len(got))
	}
	for _, item := range got {
		if item.ID == r.ID {
			continue
		}
		if item.SourceID != "" {
			t.Fatalf("invented legacy/hierarchy association: %+v", item)
		}
	}
	if after := h.History(t); !reflect.DeepEqual(after, history) {
		t.Fatal("listing mutated durable history")
	}
}
