package knowl

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type freshDiagnosticPlanner struct {
	plan  knowl.ModelEditPlan
	cause error
}

const diagnosticOwnerTarget = string(knowl.SourceAdapterFilesystem) + ":" + string(maintenanceTestSourceID)

func (planner freshDiagnosticPlanner) Plan(context.Context, knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	return planner.plan, planner.cause
}

func TestFreshIngestPlanFailurePersistsStructuredDiagnostic(t *testing.T) {
	content, _, _ := ordinaryAuthorityFixture(t)
	want := []knowl.MaintenanceDiagnostic{{Code: app.CitationSourceRetiringCode, Path: maintenanceTestPagePath, Target: diagnosticOwnerTarget}}
	primary := &app.MaintenanceAuthorityError{Diagnostics: want}
	service, store, reservation := freshDiagnosticRunner(t, content, content, freshDiagnosticPlanner{cause: primary}, nil)
	result, err := service.RunToTerminal(t.Context(), diagnosticClaim(t, store))
	if !errors.Is(err, primary) {
		t.Fatalf("plan failure=%v", err)
	}
	assertFailureDiagnostics(t, store, reservation.ID, result, want)
}

func TestFreshIngestFailurePersistsLifecycleDiagnostic(t *testing.T) {
	for _, state := range []knowl.SourceLifecycleState{knowl.SourceLifecycleRetiring, knowl.SourceLifecycleDeleted} {
		t.Run(string(state), func(t *testing.T) {
			content, plan, reader := ordinaryAuthorityFixture(t)
			service, store, reservation := freshDiagnosticRunner(t, content, content, freshDiagnosticPlanner{plan: modelDiagnosticPlan(plan)}, nil)
			before, err := content.Snapshot(t.Context(), testScope)
			if err != nil {
				t.Fatal(err)
			}
			for owner, lifecycle := range reader.states {
				lifecycle.State = state
				reader.states[owner] = lifecycle
			}
			result, runErr := service.RunToTerminal(t.Context(), diagnosticClaim(t, store))
			if !errors.Is(runErr, app.ErrMaintenanceAuthorityRejected) {
				t.Fatalf("run error=%v", runErr)
			}
			want := []knowl.MaintenanceDiagnostic{{Code: app.CitationSourceRetiringCode, Path: maintenanceTestPagePath, Target: diagnosticOwnerTarget}}
			assertFailureDiagnostics(t, store, reservation.ID, result, want)
			after, err := content.Snapshot(t.Context(), testScope)
			before.CapturedAt, after.CapturedAt = time.Time{}, time.Time{}
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("canonical page changed: before=%#v after=%#v err=%v", before, after, err)
			}
		})
	}
}

func TestFreshIngestEmptySafeSubsetPreservesPageDiagnostic(t *testing.T) {
	content, plan, _ := ordinaryAuthorityFixture(t)
	oldRef := acceptStructuredWorkspaceSource(t, content, maintenanceTestSourceID, "old.md", "v1")
	baseline := plan
	baseline.SourceRefs = append(append([]string(nil), plan.SourceRefs...), oldRef)
	baseline.Edits = []knowl.FileEdit{{Path: maintenanceTestPagePath, Content: validWorkspacePage("entities/late", "Late", plan.SourceRefs[0]+"\n  - "+oldRef, "old support")}}
	staged, err := content.StagePlan(t.Context(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := content.Commit(t.Context(), staged); err != nil {
		t.Fatal(err)
	}
	plan.Edits[0].ExpectedDigest = digestBytes(baseline.Edits[0].Content)
	service, store, reservation := freshDiagnosticRunner(t, content, content, freshDiagnosticPlanner{plan: modelDiagnosticPlan(plan)}, nil)
	result, runErr := service.RunToTerminal(t.Context(), diagnosticClaim(t, store))
	if !errors.Is(runErr, contentfs.ErrContentInvalid) {
		t.Fatalf("run error=%v", runErr)
	}
	operation, err := store.Operation(t.Context(), testScope, reservation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(operation.Diagnostics) != 1 {
		t.Fatalf("page reasons lost: %#v", operation.Diagnostics)
	}
	if result.Operation.Status != knowl.StatusFailed || !reflect.DeepEqual(result.Operation.Diagnostics, operation.Diagnostics) {
		t.Fatalf("result=%#v", result.Operation)
	}
	if details := app.PublicOperationDetails(operation); len(details.Warnings) != 0 || details.WarningsOmitted != 1 {
		t.Fatalf("public omission=%#v", details)
	}
	if operation.Diagnostics[0] != (knowl.MaintenanceDiagnostic{Code: "citation.lineage_removed", Path: maintenanceTestPagePath}) {
		t.Fatalf("diagnostic=%#v", operation.Diagnostics)
	}
}

type diagnosticStageContent struct {
	app.ContentStore
	diagnostics []knowl.MaintenanceDiagnostic
	cause       error
	cancel      context.CancelFunc
	delegate    bool
	commitCause error
}

func (content *diagnosticStageContent) Commit(ctx context.Context, staged knowl.StagedChange) (knowl.ContentCommit, error) {
	if content.commitCause != nil {
		return knowl.ContentCommit{}, content.commitCause
	}
	return content.ContentStore.Commit(ctx, staged)
}

func TestIngestApplyFailureDoesNotDuplicateBoundedPlanWarnings(t *testing.T) {
	content, plan, _ := ordinaryAuthorityFixture(t)
	var body strings.Builder
	want := make([]knowl.MaintenanceDiagnostic, 0, 64)
	for i := range 64 {
		fmt.Fprintf(&body, "[Missing](missing-%02d.md)\n", i)
		want = append(want, knowl.MaintenanceDiagnostic{Code: knowl.DiagnosticLinkUnresolved, Path: maintenanceTestPagePath, Target: fmt.Sprintf("entities/missing-%02d", i)})
	}
	plan.Edits[0].Content = validWorkspacePage("entities/late", "Late", plan.SourceRefs[0], body.String())
	primary := errors.New("injected commit rejection")
	stage := &diagnosticStageContent{ContentStore: content, delegate: true, commitCause: primary}
	service, store, reservation := freshDiagnosticRunner(t, content, stage, freshDiagnosticPlanner{plan: modelDiagnosticPlan(plan)}, nil)
	result, err := service.RunToTerminal(t.Context(), diagnosticClaim(t, store))
	if !errors.Is(err, primary) {
		t.Fatalf("commit failure=%v", err)
	}
	assertFailureDiagnostics(t, store, reservation.ID, result, want)
}

func TestIngestStructuredApplyFailureKeepsPlanAndFailureDiagnosticsSeparate(t *testing.T) {
	for _, test := range []struct {
		name      string
		warnings  int
		overlap   bool
		authority bool
		tooMany   bool
	}{
		{name: "review-32-plus-1", warnings: 32, authority: true},
		{name: "boundary-63-plus-1", warnings: 63, authority: true},
		{name: "boundary-overlapping-carrier", warnings: 63, overlap: true, authority: true},
		{name: "all-64-overlap", warnings: 64, overlap: true},
		{name: "union-over-limit", warnings: 64, authority: true, tooMany: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			content, plan, _ := ordinaryAuthorityFixture(t)
			var body strings.Builder
			want := make([]knowl.MaintenanceDiagnostic, 0, test.warnings+1)
			for i := range test.warnings {
				fmt.Fprintf(&body, "[Missing](missing-%02d.md)\n", i)
				want = append(want, knowl.MaintenanceDiagnostic{Code: knowl.DiagnosticLinkUnresolved, Path: maintenanceTestPagePath, Target: fmt.Sprintf("entities/missing-%02d", i)})
			}
			plan.Edits[0].Content = validWorkspacePage("entities/late", "Late", plan.SourceRefs[0], body.String())
			var failureDiagnostics []knowl.MaintenanceDiagnostic
			if test.overlap {
				failureDiagnostics = append(failureDiagnostics, want...)
			}
			if test.authority {
				diagnostic := knowl.MaintenanceDiagnostic{Code: app.CitationSourceRetiringCode, Path: maintenanceTestPagePath, Target: diagnosticOwnerTarget}
				failureDiagnostics = append(failureDiagnostics, diagnostic)
				if !test.tooMany {
					want = append(want, diagnostic)
				}
			}
			primary := &app.MaintenanceAuthorityError{Diagnostics: failureDiagnostics}
			stage := &diagnosticStageContent{ContentStore: content, delegate: true, commitCause: primary}
			service, store, reservation := freshDiagnosticRunner(t, content, stage, freshDiagnosticPlanner{plan: modelDiagnosticPlan(plan)}, nil)
			result, runErr := service.RunToTerminal(t.Context(), diagnosticClaim(t, store))
			if !errors.Is(runErr, primary) {
				t.Fatalf("primary failure lost: %v", runErr)
			}
			want, err := app.NormalizeMaintenanceDiagnostics(want)
			if err != nil {
				t.Fatal(err)
			}
			if test.tooMany {
				operation, readErr := store.Operation(t.Context(), testScope, reservation.ID)
				if !errors.Is(runErr, app.ErrMaintenanceDiagnosticInvalid) || readErr != nil || operation.Status != knowl.StatusApplying || !reflect.DeepEqual(operation.Diagnostics, want) {
					t.Fatalf("overflow operation=%#v read=%v run=%v", operation, readErr, runErr)
				}
				if details := app.PublicOperationDetails(operation); !reflect.DeepEqual(details.Warnings, want) {
					t.Fatalf("overflow public=%#v", details)
				}
				return
			}
			assertFailureDiagnostics(t, store, reservation.ID, result, want)
		})
	}
}

func (content *diagnosticStageContent) StagePlan(ctx context.Context, plan knowl.ValidatedEditPlan) (knowl.StagedChange, error) {
	if content.delegate {
		return content.ContentStore.StagePlan(ctx, plan)
	}
	if content.cancel != nil {
		content.cancel()
	}
	return knowl.StagedChange{Diagnostics: content.diagnostics}, content.cause
}

func TestFreshIngestRetryReplacesFailureDiagnosticsAndKeepsCurrentPlanWarnings(t *testing.T) {
	content, plan, _ := ordinaryAuthorityFixture(t)
	plan.Edits[0].Content = validWorkspacePage("entities/late", "Late", plan.SourceRefs[0], "[Missing](missing.md)")
	stage := &diagnosticStageContent{ContentStore: content, cause: errors.New("injected rejection"), diagnostics: []knowl.MaintenanceDiagnostic{{Code: knowl.DiagnosticCatalogDependencyReject, Path: testIndexPath, Target: maintenanceTestPagePath}}}
	service, store, reservation := freshDiagnosticRunner(t, content, stage, freshDiagnosticPlanner{plan: modelDiagnosticPlan(plan)}, nil)
	linkDiagnosticRetrySource(t, store, reservation)
	for attempt := range 3 {
		switch attempt {
		case 1:
			stage.diagnostics = []knowl.MaintenanceDiagnostic{{Code: app.CitationSourceRetiringCode, Path: maintenanceTestPagePath, Target: diagnosticOwnerTarget}}
		case 2:
			stage.diagnostics = []knowl.MaintenanceDiagnostic{}
		}
		result, err := service.RunToTerminal(t.Context(), diagnosticClaim(t, store))
		if !errors.Is(err, stage.cause) {
			t.Fatalf("attempt %d error=%v", attempt, err)
		}
		assertFailureDiagnostics(t, store, reservation.ID, result, stage.diagnostics)
		retried, err := store.RetrySourceMaintenance(t.Context(), app.SourceMaintenanceRetryRequest{Scope: testScope, SourceID: maintenanceTestSourceID, FailureClasses: []string{result.Operation.Failure.Class}, MaintenanceGeneration: reservation.Key.MaintenanceGeneration, Schema: reservation.Descriptor.Schema})
		if err != nil || retried.Requeued != 1 {
			t.Fatalf("retry=%#v err=%v", retried, err)
		}
	}
	stale := stage.diagnostics
	stage.delegate = true
	result, err := service.RunToTerminal(t.Context(), diagnosticClaim(t, store))
	if err != nil || result.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("repaired operation=%#v err=%v", result.Operation, err)
	}
	want := []knowl.MaintenanceDiagnostic{{Code: knowl.DiagnosticLinkUnresolved, Path: maintenanceTestPagePath, Target: "entities/missing"}}
	operation, err := store.Operation(t.Context(), testScope, reservation.ID)
	if err != nil || !reflect.DeepEqual(operation.Diagnostics, want) {
		t.Fatalf("current warnings=%#v err=%v", operation.Diagnostics, err)
	}
	if err := store.SaveOperationDiagnostics(t.Context(), testScope, reservation.ID, 1, stale); !errors.Is(err, app.ErrWorkLeaseConflict) {
		t.Fatalf("stale saver error=%v", err)
	}
}

func linkDiagnosticRetrySource(t *testing.T, store *sqlite.Store, reservation app.OperationReservation) {
	t.Helper()
	now := time.Now().UTC()
	run := knowl.SyncRun{ID: "diagnostic-retry", Scope: testScope, SourceID: maintenanceTestSourceID, ConfigDigest: strings.Repeat("a", 64), Status: knowl.SyncStatusScanning, StartedAt: now, UpdatedAt: now}
	if _, _, err := store.BeginSync(t.Context(), app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	source := reservation.Descriptor.Source
	state := knowl.DocumentState{Scope: testScope, SourceID: maintenanceTestSourceID, DocumentID: source.SourceDocument.DocumentID, Revision: source.Version.Version, AcceptedSource: source, MaintenanceRevision: source.Version.Version, MaintenanceOperationID: reservation.ID, MaintenanceGeneration: reservation.Key.MaintenanceGeneration, MirrorPath: "wiki/sources/ordinary-source/page.md", MirrorDigest: source.Version.Digest, LastSeenRunID: run.ID, CreatedAt: now, UpdatedAt: now}
	prepared := app.PreparedSyncState{RunID: run.ID, Scope: testScope, SourceID: maintenanceTestSourceID, CompleteScan: true, MaintenanceReady: true, Checkpoint: string(run.ID), Counts: knowl.SyncCounts{Added: 1}, Documents: []app.PreparedDocumentState{{Action: app.SyncDocumentActive, State: state}}, PreparedAt: now}
	digest, err := app.PreparedSyncDigest(prepared)
	if err != nil {
		t.Fatal(err)
	}
	prepared.CandidateDigest = digest
	if _, err := store.PrepareSync(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	transition := app.SyncGeneration{RunID: run.ID, Scope: testScope, SourceID: maintenanceTestSourceID, Generation: string(run.ID), UpdatedAt: now}
	if _, err := store.MarkContentCommitted(t.Context(), transition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkProjected(t.Context(), transition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeSync(t.Context(), app.SyncFinalization{RunID: run.ID, Scope: testScope, SourceID: maintenanceTestSourceID, CandidateDigest: digest, Generation: transition.Generation, Checkpoint: prepared.Checkpoint, Counts: prepared.Counts, FinalizedAt: now}); err != nil {
		t.Fatal(err)
	}
}

type diagnosticOperationStore struct {
	*sqlite.Store
	saveErr error
}

func (store *diagnosticOperationStore) SaveOperationDiagnostics(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, attempt int, diagnostics []knowl.MaintenanceDiagnostic) error {
	if store.saveErr != nil {
		return store.saveErr
	}
	return store.Store.SaveOperationDiagnostics(ctx, scope, id, attempt, diagnostics)
}

func TestFreshIngestFailureDiagnosticsBoundedAndSaverFailureRetryable(t *testing.T) {
	content, plan, _ := ordinaryAuthorityFixture(t)
	primary := errors.New("injected stage rejection")
	stage := &diagnosticStageContent{ContentStore: content, cause: primary}
	for i := range 64 {
		stage.diagnostics = append(stage.diagnostics, knowl.MaintenanceDiagnostic{Code: knowl.DiagnosticCatalogDependencyReject, Path: fmt.Sprintf("wiki/entities/page-%02d.md", i), Target: maintenanceTestPagePath})
	}
	// The same diagnostics may be returned in the staged result and the error.
	// Their union still fits the 64-diagnostic bound.
	stage.cause = errors.Join(primary, &app.MaintenanceAuthorityError{Diagnostics: stage.diagnostics})
	saverFailure := errors.New("injected diagnostic persistence failure")
	operations := &diagnosticOperationStore{saveErr: saverFailure}
	service, store, reservation := freshDiagnosticRunner(t, content, stage, freshDiagnosticPlanner{plan: modelDiagnosticPlan(plan)}, operations)
	claim := diagnosticClaim(t, store)
	result, runErr := service.RunToTerminal(t.Context(), claim)
	if !errors.Is(runErr, primary) || !errors.Is(runErr, saverFailure) {
		t.Fatalf("primary/save error=%v", runErr)
	}
	operation, err := store.Operation(t.Context(), testScope, reservation.ID)
	if err != nil || operation.Status != knowl.StatusReceived || result.Operation.Status != knowl.StatusReceived {
		t.Fatalf("premature terminal state: %#v %v", operation, err)
	}
	operations.saveErr = nil
	result, runErr = service.RunToTerminal(t.Context(), claim)
	if !errors.Is(runErr, primary) {
		t.Fatalf("retry error=%v", runErr)
	}
	assertFailureDiagnostics(t, store, reservation.ID, result, stage.diagnostics)
}

func TestFreshIngestFailurePersistsDespiteCanceledCaller(t *testing.T) {
	content, plan, _ := ordinaryAuthorityFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	primary := errors.New("injected stage rejection")
	want := []knowl.MaintenanceDiagnostic{{Code: knowl.DiagnosticCatalogDependencyReject, Path: testIndexPath, Target: maintenanceTestPagePath}}
	stage := &diagnosticStageContent{ContentStore: content, cause: primary, diagnostics: want, cancel: cancel}
	service, store, reservation := freshDiagnosticRunner(t, content, stage, freshDiagnosticPlanner{plan: modelDiagnosticPlan(plan)}, nil)
	result, runErr := service.RunToTerminal(ctx, diagnosticClaim(t, store))
	if !errors.Is(runErr, primary) {
		t.Fatalf("run error=%v", runErr)
	}
	assertFailureDiagnostics(t, store, reservation.ID, result, want)
}

func modelDiagnosticPlan(plan knowl.ValidatedEditPlan) knowl.ModelEditPlan {
	return knowl.ModelEditPlan{SchemaDigest: plan.SchemaDigest, SourceRefs: plan.SourceRefs, Edits: plan.Edits}
}

type diagnosticContextIndex struct {
	app.SearchIndex
	ids []knowl.PageID
}

func (index diagnosticContextIndex) SelectContext(context.Context, knowl.ScopeRef, knowl.SourceSummary, knowl.ReadLimits) ([]knowl.PageID, error) {
	return index.ids, nil
}

func freshDiagnosticRunner(t *testing.T, content *hostMaintenanceContent, serviceContent app.ContentStore, planner app.Maintainer, operations *diagnosticOperationStore) (*app.IngestService, *sqlite.Store, app.OperationReservation) {
	t.Helper()
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var operationStore app.OperationStore = store
	if operations != nil {
		operations.Store = store
		operationStore = operations
	}
	snapshot, err := content.Snapshot(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	index := diagnosticContextIndex{SearchIndex: store}
	for _, page := range snapshot.Pages {
		index.ids = append(index.ids, page.ID)
	}
	service, err := app.NewIngestService(serviceContent, operationStore, index, planner, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	schema, generation, err := service.CurrentMaintenancePolicy(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := content.Inspect(t.Context(), testScope)
	if err != nil || len(inspection.RawSources) == 0 {
		t.Fatalf("accepted sources=%#v err=%v", inspection, err)
	}
	source := inspection.RawSources[0].Source
	for _, raw := range inspection.RawSources {
		if raw.Source.SourceDocument.DocumentID == "page.md" {
			source = raw.Source
		}
	}
	key := knowl.OperationKey{Scope: testScope, Source: source.Source, Version: source.Version, MaintenanceGeneration: generation}
	reservation, err := store.Reserve(t.Context(), key, knowl.OperationMeta{Key: key, AcceptedSource: source, Schema: schema, SchemaDigest: schema.Digest, MaintenanceGeneration: generation, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, reservation
}

func diagnosticClaim(t *testing.T, store *sqlite.Store) knowl.WorkClaim {
	t.Helper()
	claim, err := store.ClaimReady(t.Context(), testScope, knowl.WorkLease{Token: "fresh-diagnostics", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func assertFailureDiagnostics(t *testing.T, store *sqlite.Store, id knowl.OperationID, result app.IngestResult, want []knowl.MaintenanceDiagnostic) {
	t.Helper()
	operation, err := store.Operation(t.Context(), testScope, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []knowl.Operation{operation, result.Operation} {
		if value.Status != knowl.StatusFailed || !reflect.DeepEqual(value.Diagnostics, want) {
			t.Fatalf("failure diagnostics=%#v want=%#v status=%s", value.Diagnostics, want, value.Status)
		}
		if details := app.PublicOperationDetails(value); !reflect.DeepEqual(details.Warnings, want) {
			t.Fatalf("public warnings=%#v", details.Warnings)
		}
	}
}
