package knowl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const maintenanceTestPagePath = "wiki/entities/late.md"
const maintenanceTestSourceID = "ordinary-source"

// A lifecycle transition must invalidate a queued ordinary publication, including
// after restart; accepted immutable raw evidence alone is no longer authority.
func TestOrdinaryMaintenanceRejectsRetiredOwner(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "fresh"}[fresh], func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			workspace, err := newMaintenanceTestContent(filepath.Join(root, "workspace"))
			if err != nil {
				t.Fatal(err)
			}
			if err := workspace.Init(); err != nil {
				t.Fatal(err)
			}
			store, err := sqlite.Open(ctx, filepath.Join(root, "operations.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}()
			if err := bindMaintenanceTest(workspace, store, newHostRetirementCoordinator()); err != nil {
				t.Fatal(err)
			}
			owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "retiring-source"}
			desired := []app.DesiredSourceOwner{{Owner: owner, ConfigDigest: strings.Repeat("a", 64)}}
			now := time.Date(2026, 10, 15, 1, 0, 0, 0, time.UTC)
			if err := store.ReconcileSourceLifecycles(ctx, app.SourceLifecycleReconcileRequest{Scope: testScope, Desired: desired, AuthorityGeneration: app.SourceLifecycleGeneration(desired), UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			ref := acceptStructuredWorkspaceSource(t, workspace, owner.SourceID, "page.md", "v1")
			writeRootCatalogTargets(t, workspace, "entities/late.md")
			schema, err := workspace.Schema(ctx, testScope)
			if err != nil {
				t.Fatal(err)
			}
			plan := knowl.ValidatedEditPlan{OperationID: "ordinary-retirement", Scope: testScope, SchemaDigest: schema.Digest, RequiredSourceRef: ref, SourceRefs: []string{ref}, Edits: []knowl.FileEdit{{Path: maintenanceTestPagePath, Content: validWorkspacePage("entities/late", "Late", ref, "")}}}
			var staged knowl.StagedChange
			if !fresh {
				staged, err = workspace.StagePlan(ctx, plan)
				if err != nil {
					t.Fatal(err)
				}
			}
			authority, err := store.SourceLifecycleAuthority(ctx, testScope)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ReconcileSourceLifecycles(ctx, app.SourceLifecycleReconcileRequest{Scope: testScope, Desired: []app.DesiredSourceOwner{}, AuthorityGeneration: app.SourceLifecycleGeneration(nil), ExpectedAuthorityEpoch: authority.Epoch, UpdatedAt: now.Add(time.Second)}); err != nil {
				t.Fatal(err)
			}
			if fresh {
				_, err = workspace.StagePlan(ctx, plan)
			} else {
				_, err = workspace.Commit(ctx, staged)
			}
			if !errors.Is(err, contentfs.ErrContentInvalid) {
				t.Fatalf("publication error = %v, want lifecycle rejection", err)
			}
			var invalid *app.MaintenanceAuthorityError
			if !errors.As(err, &invalid) || invalid.Diagnostics[0].Code != app.CitationSourceRetiringCode || invalid.Diagnostics[0].Path != maintenanceTestPagePath {
				t.Fatalf("structured rejection = %#v", invalid)
			}
			if _, readErr := os.Stat(filepath.Join(workspace.Root(), maintenanceTestPagePath)); !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("canonical mutation: %v", readErr)
			}
		})
	}
}

func TestOrdinaryMaintenanceActiveReplayAndRestart(t *testing.T) {
	workspace, plan, reader := ordinaryAuthorityFixture(t)
	staged, err := workspace.StagePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := workspace.Commit(t.Context(), staged)
	if err != nil {
		t.Fatal(err)
	}
	for owner, state := range reader.states {
		state.State = knowl.SourceLifecycleDeleted
		reader.states[owner] = state
	}
	reopened, err := newMaintenanceTestContent(workspace.Root())
	if err != nil {
		t.Fatal(err)
	}
	if err := bindMaintenanceTest(reopened, reader, newHostRetirementCoordinator()); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	replay, err := reopened.Commit(t.Context(), staged)
	if err != nil || replay.Generation != commit.Generation {
		t.Fatalf("committed replay = %#v, %v", replay, err)
	}
}

func TestConfiguredWorkspaceCannotBypassPrivateHostAuthority(t *testing.T) {
	content, plan, _ := ordinaryAuthorityFixture(t)
	if _, err := content.Workspace.StagePlan(t.Context(), plan); !errors.Is(err, contentfs.ErrPlanConflict) {
		t.Fatalf("raw configured stage bypass=%v", err)
	}
	staged, err := content.StagePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := content.Workspace.Commit(t.Context(), staged); !errors.Is(err, contentfs.ErrPlanConflict) {
		t.Fatalf("raw configured commit bypass=%v", err)
	}
	if _, err := content.Commit(t.Context(), staged); err != nil {
		t.Fatalf("private Host active publication=%v", err)
	}
}

func TestHostAuthorityPreservesUnknownSourceDiagnostic(t *testing.T) {
	content, plan, _ := ordinaryAuthorityFixture(t)
	const missing = "git:missing/doc.md@v1"
	plan.SourceRefs = append(plan.SourceRefs, missing)
	plan.Edits[0].Content = validWorkspacePage("entities/late", "Late", missing, "")
	staged, err := content.StagePlan(t.Context(), plan)
	if !errors.Is(err, contentfs.ErrContentInvalid) || len(staged.Diagnostics) != 1 || staged.Diagnostics[0] != (knowl.MaintenanceDiagnostic{Code: knowl.DiagnosticCitationUnknownSource, Path: maintenanceTestPagePath, Target: missing}) {
		t.Fatalf("missing-source rejection=%#v,%v", staged, err)
	}
}

func TestOrdinaryMaintenanceExactOwnerAndDeclaredRefs(t *testing.T) {
	for _, mode := range []string{"deleted", "unowned", "declared", "emitted", "same-id-other-adapter"} {
		t.Run(mode, func(t *testing.T) {
			workspace, plan, reader := ordinaryAuthorityFixture(t)
			owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: maintenanceTestSourceID}
			state := reader.states[owner]
			wantCode := app.CitationSourceRetiringCode
			switch mode {
			case "deleted":
				state.State = knowl.SourceLifecycleDeleted
				reader.states[owner] = state
			case "unowned":
				delete(reader.states, owner)
				wantCode = app.SourceLifecycleUnownedCode
			case "same-id-other-adapter":
				delete(reader.states, owner)
				state.Owner.Adapter = knowl.SourceAdapterGit
				reader.states[state.Owner] = state
				wantCode = app.SourceLifecycleUnownedCode
			case "declared":
				state.State = knowl.SourceLifecycleRetiring
				reader.states[owner] = state
				plan.Edits[0].Content = validWorkspacePage("entities/late", "Late", "", "")
			case "emitted":
				state.State = knowl.SourceLifecycleRetiring
				reader.states[owner] = state
				plan.SourceRefs = nil
				plan.RequiredSourceRef = ""
			}
			staged, err := workspace.StagePlan(t.Context(), plan)
			if !errors.Is(err, contentfs.ErrContentInvalid) || len(staged.Diagnostics) != 1 || staged.Diagnostics[0] != (knowl.MaintenanceDiagnostic{Code: wantCode, Path: maintenanceTestPagePath, Target: "wiki-filesystem:ordinary-source"}) {
				t.Fatalf("stage=%#v, err=%v", staged, err)
			}
		})
	}
}

func TestOrdinaryMaintenanceRestartRejectsStaleStage(t *testing.T) {
	workspace, plan, reader := ordinaryAuthorityFixture(t)
	staged, err := workspace.StagePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for owner, state := range reader.states {
		state.State = knowl.SourceLifecycleRetiring
		reader.states[owner] = state
	}
	reopened, err := newMaintenanceTestContent(workspace.Root())
	if err != nil {
		t.Fatal(err)
	}
	if err := bindMaintenanceTest(reopened, reader, newHostRetirementCoordinator()); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Commit(t.Context(), staged); !errors.Is(err, contentfs.ErrContentInvalid) {
		t.Fatalf("recovered stale stage error=%v", err)
	}
}

func TestOrdinaryMaintenanceRevalidatesApplicableAuthority(t *testing.T) {
	for _, change := range []string{"unrelated", "config", "reactivated"} {
		t.Run(change, func(t *testing.T) {
			workspace, plan, reader := ordinaryAuthorityFixture(t)
			staged, err := workspace.StagePlan(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			reader.authority.Epoch = 2
			reader.authority.Generation = strings.Repeat("c", 64)
			for owner, state := range reader.states {
				state.AuthorityEpoch = 2
				state.AuthorityGeneration = reader.authority.Generation
				if change == "config" {
					state.ConfigDigest = strings.Repeat("d", 64)
				}
				if change == "reactivated" {
					state.ActivatedEpoch = 2
				}
				reader.states[owner] = state
			}
			_, err = workspace.Commit(t.Context(), staged)
			if change == "unrelated" {
				if err != nil {
					t.Fatalf("unrelated inventory change rejected: %v", err)
				}
				return
			}
			var failure *app.MaintenanceAuthorityError
			if !errors.As(err, &failure) || failure.Diagnostics[0].Code != app.CitationSourceAuthorityChangedCode {
				t.Fatalf("changed applicable authority error=%v", err)
			}
		})
	}
}

func TestOrdinaryMaintenanceCapturedSessionCannotBypassReleasedFence(t *testing.T) {
	workspace, _, _ := ordinaryAuthorityFixture(t)
	ctx, release, err := workspace.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	blockedRelease, err := workspace.coordinator.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blockedRelease()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := workspace.acquire(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("captured session bypassed fence: %v", err)
	}
}

type maintenanceReadFailure struct {
	app.SourceLifecycleReader
	failure   error
	ownerRead bool
}

func (reader maintenanceReadFailure) SourceLifecycleAuthority(ctx context.Context, scope knowl.ScopeRef) (knowl.SourceLifecycleAuthority, error) {
	if !reader.ownerRead {
		return knowl.SourceLifecycleAuthority{}, reader.failure
	}
	return reader.SourceLifecycleReader.SourceLifecycleAuthority(ctx, scope)
}
func (reader maintenanceReadFailure) SourceLifecycle(ctx context.Context, scope knowl.ScopeRef, owner knowl.SourceOwner) (knowl.SourceLifecycle, error) {
	return knowl.SourceLifecycle{}, reader.failure
}

func TestOrdinaryMaintenanceLifecycleReadFailsClosed(t *testing.T) {
	for _, ownerRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "authority", true: "owner"}[ownerRead], func(t *testing.T) {
			workspace, plan, reader := ordinaryAuthorityFixture(t)
			reopened, err := newMaintenanceTestContent(workspace.Root())
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("lifecycle unavailable")
			if err := bindMaintenanceTest(reopened, maintenanceReadFailure{SourceLifecycleReader: reader, failure: failure, ownerRead: ownerRead}, newHostRetirementCoordinator()); err != nil {
				t.Fatal(err)
			}
			staged, err := reopened.StagePlan(t.Context(), plan)
			if !errors.Is(err, failure) || staged.Digest != "" {
				t.Fatalf("stage=%#v,error=%v", staged, err)
			}
		})
	}
}

type unexpectedMaintenancePlanner struct{ t *testing.T }

func (planner unexpectedMaintenancePlanner) Plan(context.Context, knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	planner.t.Error("durable stage was unexpectedly replanned")
	return knowl.ModelEditPlan{}, app.ErrMaintainerUnavailable
}

func TestQueuedOrdinaryMaintenancePersistsRetiringDiagnostic(t *testing.T) {
	workspace, plan, reader := ordinaryAuthorityFixture(t)
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	accepted, err := workspace.ResolveAcceptedSources(t.Context(), testScope, plan.SourceRefs)
	if err != nil {
		t.Fatal(err)
	}
	source := accepted[plan.SourceRefs[0]]
	schema, err := workspace.Schema(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	generation := strings.Repeat("c", 64)
	key := knowl.OperationKey{Scope: testScope, Source: source.Source, Version: source.Version, MaintenanceGeneration: generation}
	reservation, err := store.Reserve(t.Context(), key, knowl.OperationMeta{Key: key, AcceptedSource: source, Schema: schema, SchemaDigest: schema.Digest, MaintenanceGeneration: generation, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	plan.OperationID = string(reservation.ID)
	if _, err := workspace.StagePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	for owner, state := range reader.states {
		state.State = knowl.SourceLifecycleRetiring
		reader.states[owner] = state
	}
	service, err := app.NewIngestService(workspace, store, store, unexpectedMaintenancePlanner{t: t}, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimReady(t.Context(), testScope, knowl.WorkLease{Token: "authority-test", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.RunToTerminal(t.Context(), claim)
	if !errors.Is(err, contentfs.ErrContentInvalid) {
		t.Fatalf("queued operation error=%v", err)
	}
	operation, err := store.Operation(t.Context(), testScope, reservation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != knowl.StatusFailed || len(operation.Diagnostics) != 1 || operation.Diagnostics[0] != (knowl.MaintenanceDiagnostic{Code: app.CitationSourceRetiringCode, Path: maintenanceTestPagePath, Target: "wiki-filesystem:ordinary-source"}) {
		t.Fatalf("durable operation=%#v", operation)
	}
	details := app.PublicOperationDetails(operation)
	if len(details.Warnings) != 1 || details.Warnings[0] != operation.Diagnostics[0] {
		t.Fatalf("public diagnostics=%#v", details)
	}
}

func TestHierarchyMaintenanceDoesNotRepublishRetiringProvenance(t *testing.T) {
	for _, emitsRef := range []bool{false, true} {
		t.Run(map[bool]string{false: "navigation-only", true: "emitted-ref"}[emitsRef], func(t *testing.T) {
			workspace := newHierarchyWorkspace(t)
			ref := acceptStructuredWorkspaceSource(t, workspace, maintenanceTestSourceID, "page.md", "v1")
			_, _, reader := ordinaryAuthorityFixture(t)
			for owner, state := range reader.states {
				state.State = knowl.SourceLifecycleRetiring
				reader.states[owner] = state
			}
			if err := bindMaintenanceTest(workspace, reader, newHostRetirementCoordinator()); err != nil {
				t.Fatal(err)
			}
			plan := hierarchyStagePlan(t, workspace)
			if emitsRef {
				plan.Mutations[0].Content = validWorkspacePage("entities/late", "Late", ref, "")
			}
			staged, err := workspace.StageHierarchyPlan(t.Context(), testHierarchyOperation, plan)
			if emitsRef {
				var failure *app.MaintenanceAuthorityError
				if !errors.As(err, &failure) || failure.Diagnostics[0].Code != app.CitationSourceRetiringCode || len(staged.Diagnostics) != 1 {
					t.Fatalf("hierarchy provenance = %#v,%v", staged, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := workspace.CommitHierarchy(t.Context(), staged); err != nil {
				t.Fatalf("navigation-only commit rejected: %v", err)
			}
		})
	}
}

func TestRetirementCommitRejectsRemainingEvidenceRetiredAfterStage(t *testing.T) {
	workspace, plan, reader := retirementDeleteFixture(t)
	ref := acceptStructuredWorkspaceSource(t, workspace, "active-evidence", "page.md", "v1")
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "active-evidence"}
	reader.states[owner] = knowl.SourceLifecycle{Scope: testScope, Owner: owner, State: knowl.SourceLifecycleActive, ConfigDigest: strings.Repeat("a", 64), AuthorityGeneration: reader.authority.Generation, AuthorityEpoch: reader.authority.Epoch, ActivatedEpoch: reader.authority.Epoch}
	coordinator := newHostRetirementCoordinator()
	if err := bindMaintenanceTest(workspace, reader, coordinator); err != nil {
		t.Fatal(err)
	}
	plan.SourceRefs = append(plan.SourceRefs, ref)
	for _, edit := range plan.Edits {
		if edit.Path == plan.Retirement.PagePath {
			edit.Action = knowl.SourceMutationWrite
			edit.Content = validWorkspacePage(string(plan.Retirement.PageID), "One", ref, "")
			plan.Edits = []knowl.FileEdit{edit}
			break
		}
	}
	staged, err := workspace.StagePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	before := mustReadHierarchyFile(t, workspace, plan.Retirement.PagePath)
	state := reader.states[owner]
	state.State = knowl.SourceLifecycleRetiring
	reader.states[owner] = state
	_, err = commitRetirementForTest(t.Context(), coordinator, reader, workspace, staged)
	var failure *app.MaintenanceAuthorityError
	if !errors.As(err, &failure) || failure.Diagnostics[0].Code != app.CitationSourceRetiringCode || failure.Diagnostics[0].Target != "wiki-filesystem:active-evidence" {
		t.Fatalf("remaining retiring evidence commit error=%v", err)
	}
	if after := mustReadHierarchyFile(t, workspace, plan.Retirement.PagePath); string(after) != string(before) {
		t.Fatal("retirement republished newly retired remaining evidence")
	}
}

type maintenanceSignaledCoordinator struct {
	*hostRetirementCoordinator
	requests chan struct{}
}

func (coordinator *maintenanceSignaledCoordinator) Acquire(ctx context.Context) (func(), error) {
	coordinator.requests <- struct{}{}
	return coordinator.hostRetirementCoordinator.Acquire(ctx)
}

func TestOrdinaryMaintenanceFenceOrdersPublicationAndZeroFinalization(t *testing.T) {
	for _, ordinaryFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "zero-first", true: "ordinary-first"}[ordinaryFirst], func(t *testing.T) {
			workspace, plan, reader := ordinaryAuthorityFixture(t)
			reopened, err := newMaintenanceTestContent(workspace.Root())
			if err != nil {
				t.Fatal(err)
			}
			coordinator := &maintenanceSignaledCoordinator{hostRetirementCoordinator: newHostRetirementCoordinator(), requests: make(chan struct{}, 4)}
			if err := bindMaintenanceTest(reopened, reader, coordinator); err != nil {
				t.Fatal(err)
			}
			staged, err := reopened.StagePlan(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			markDeleted := func() {
				for owner, state := range reader.states {
					state.State = knowl.SourceLifecycleDeleted
					reader.states[owner] = state
				}
			}
			if !ordinaryFirst {
				release, err := coordinator.Acquire(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				<-coordinator.requests
				done := make(chan error, 1)
				go func() { _, err := reopened.Commit(t.Context(), staged); done <- err }()
				<-coordinator.requests
				markDeleted()
				release()
				if err := <-done; !errors.Is(err, contentfs.ErrContentInvalid) {
					t.Fatalf("stale publication after zero: %v", err)
				}
				return
			}
			projecting := make(chan struct{})
			finishProjection := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				_, err := reopened.RunMaintenanceApply(t.Context(), staged, false, func(context.Context) (bool, error) { return true, nil }, func(context.Context, knowl.ContentCommit) error { close(projecting); <-finishProjection; return nil })
				done <- err
			}()
			<-coordinator.requests
			<-projecting
			finalized := make(chan bool, 1)
			go func() {
				release, err := coordinator.Acquire(t.Context())
				if err != nil {
					finalized <- false
					return
				}
				defer release()
				pages, err := reopened.ReadPages(t.Context(), testScope, []knowl.PageID{"entities/late"}, knowl.ReadLimits{Pages: 1, Bytes: 1 << 20})
				if err == nil && len(pages) == 1 && len(pages[0].SourceRefs) > 0 {
					finalized <- false
					return
				}
				markDeleted()
				finalized <- true
			}()
			<-coordinator.requests
			select {
			case <-finalized:
				t.Fatal("finalization crossed ongoing projection tail")
			default:
			}
			close(finishProjection)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if <-finalized {
				t.Fatal("zero finalization ignored published canonical ref")
			}
		})
	}
}

func ordinaryAuthorityFixture(t *testing.T) (*hostMaintenanceContent, knowl.ValidatedEditPlan, *maintenanceLifecycleReader) {
	t.Helper()
	workspace, err := newMaintenanceTestContent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	ref := acceptStructuredWorkspaceSource(t, workspace, maintenanceTestSourceID, "page.md", "v1")
	writeRootCatalogTargets(t, workspace, "entities/late.md")
	schema, err := workspace.Schema(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: maintenanceTestSourceID}
	generation := strings.Repeat("a", 64)
	reader := &maintenanceLifecycleReader{authority: knowl.SourceLifecycleAuthority{Scope: testScope, Generation: generation, Epoch: 1}, states: map[knowl.SourceOwner]knowl.SourceLifecycle{owner: {Scope: testScope, Owner: owner, State: knowl.SourceLifecycleActive, AuthorityGeneration: generation, AuthorityEpoch: 1, ActivatedEpoch: 1, ConfigDigest: strings.Repeat("b", 64)}}}
	if err := bindMaintenanceTest(workspace, reader, newHostRetirementCoordinator()); err != nil {
		t.Fatal(err)
	}
	plan := knowl.ValidatedEditPlan{OperationID: "ordinary-active", Scope: testScope, SchemaDigest: schema.Digest, RequiredSourceRef: ref, SourceRefs: []string{ref}, Edits: []knowl.FileEdit{{Path: maintenanceTestPagePath, Content: validWorkspacePage("entities/late", "Late", ref, "")}}}
	return workspace, plan, reader
}
