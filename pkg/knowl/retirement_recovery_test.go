package knowl

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	orphanRecoveryNextWindow = "next-window"
	orphanRecoveryStale      = "stale"
	orphanRecoveryUncited    = "uncited"
	orphanRecoveryUnowned    = "unowned"
	orphanRecoveryRaw        = "raw"
)

type orphanRecoveryReserver struct{ pages []knowl.PageID }

func (reserver *orphanRecoveryReserver) Reserve(_ context.Context, _ knowl.ScopeRef, _ knowl.SourceOwner, _ string, page knowl.PageID) (app.OperationReservation, error) {
	reserver.pages = append(reserver.pages, page)
	return app.OperationReservation{Operation: knowl.Operation{ID: "orphan-recovery-test", Kind: knowl.WorkSourceRetirementPage}, New: true}, nil
}

// A source migrated before lifecycle bootstrap is absent rather than retiring.
// Normal advancement must discover it from accepted history, not require an
// adapter alias, synthetic configuration or operator migration flag.
func TestAutomaticRetirementRecoversHistoricalOwner(t *testing.T) {
	for _, fault := range []string{"", orphanRecoveryRaw, orphanRecoveryStale, orphanRecoveryUncited, orphanRecoveryUnowned, orphanRecoveryNextWindow} {
		t.Run(fault, func(t *testing.T) { testAutomaticRetirementRecoversHistoricalOwner(t, fault) })
	}
}

type orphanRecoveryContent struct {
	app.CanonicalProvenanceReader
	app.RetirementContentReader
	fault string
}

func (content *orphanRecoveryContent) ReadSource(ctx context.Context, source knowl.AcceptedSource, limits knowl.ReadLimits) ([]byte, error) {
	if content.fault == orphanRecoveryRaw {
		return []byte("tampered"), nil
	}
	return content.RetirementContentReader.ReadSource(ctx, source, limits)
}
func (content *orphanRecoveryContent) ReadPages(ctx context.Context, scope knowl.ScopeRef, ids []knowl.PageID, limits knowl.ReadLimits) ([]knowl.PageSnapshot, error) {
	pages, err := content.RetirementContentReader.ReadPages(ctx, scope, ids, limits)
	if content.fault == orphanRecoveryStale && len(pages) > 0 {
		pages[0].Digest = strings.Repeat("f", 64)
	}
	return pages, err
}
func (content *orphanRecoveryContent) PageProvenance(ctx context.Context, scope knowl.ScopeRef, owner knowl.SourceOwner, cursor app.CanonicalProvenanceCursor, limits app.CanonicalProvenanceLimits) (app.CanonicalProvenancePage, error) {
	if content.fault == orphanRecoveryUncited {
		return app.CanonicalProvenancePage{EOF: true, Authoritative: true}, nil
	}
	if content.fault == orphanRecoveryUnowned {
		return app.CanonicalProvenancePage{}, &app.CanonicalProvenanceFailure{
			Code: app.CanonicalProvenanceCodeRefUnowned, Cause: app.ErrSourceLifecycleUnowned,
		}
	}
	return content.CanonicalProvenanceReader.PageProvenance(ctx, scope, owner, cursor, limits)
}

func testAutomaticRetirementRecoversHistoricalOwner(t *testing.T, fault string) {
	content, err := newMaintenanceTestContent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := content.Init(); err != nil {
		t.Fatal(err)
	}
	ref := acceptStructuredWorkspaceSource(t, content, maintenanceTestSourceID, "page.md", "v1")
	writeRootCatalogTargets(t, content, "entities/late.md")
	schema, err := content.Schema(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	plan := knowl.ValidatedEditPlan{OperationID: "historical-page", Scope: testScope, SchemaDigest: schema.Digest, RequiredSourceRef: ref, SourceRefs: []string{ref}, Edits: []knowl.FileEdit{{Path: maintenanceTestPagePath, Content: validWorkspacePage("entities/late", "Late", ref, "Historical facts.")}}}
	stage, err := content.Workspace.StagePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := content.Workspace.Commit(t.Context(), stage); err != nil {
		t.Fatal(err)
	}
	if fault == orphanRecoveryNextWindow {
		writeRootCatalogTargets(t, content, "entities/early.md", "entities/late.md")
		otherRef := acceptStructuredWorkspaceSource(t, content, maintenanceTestSourceID, "other.md", "v1")
		early := knowl.ValidatedEditPlan{OperationID: "historical-early", Scope: testScope, SchemaDigest: schema.Digest, RequiredSourceRef: otherRef, SourceRefs: []string{otherRef}, Edits: []knowl.FileEdit{{Path: "wiki/entities/early.md", Content: validWorkspacePage("entities/early", "Early", otherRef, "Other historical facts.")}}}
		earlyStage, err := content.Workspace.StagePlan(t.Context(), early)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = content.Workspace.Commit(t.Context(), earlyStage); err != nil {
			t.Fatal(err)
		}
	}
	accepted, err := content.ResolveAcceptedSources(t.Context(), testScope, []string{ref})
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "recovery.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	source := accepted[ref]
	key := knowl.OperationKey{Scope: testScope, Source: source.Source, Version: source.Version}
	if _, err := store.Reserve(t.Context(), key, knowl.OperationMeta{Key: key, AcceptedSource: source, Schema: schema, SchemaDigest: schema.Digest}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	desired := []app.DesiredSourceOwner{{Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "different-current-source"}, ConfigDigest: strings.Repeat("a", 64)}}
	if err := store.ReconcileSourceLifecycles(t.Context(), app.SourceLifecycleReconcileRequest{Scope: testScope, Desired: desired, AuthorityGeneration: app.SourceLifecycleGeneration(desired), UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	authority, err := store.SourceLifecycleAuthority(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkSourceLifecycleReady(t.Context(), app.SourceLifecycleReadyUpdate{Scope: testScope, Owner: desired[0].Owner, AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch, ActivatedEpoch: authority.Epoch, ConfigDigest: desired[0].ConfigDigest, ReadyAt: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: maintenanceTestSourceID}
	if _, err := store.SourceLifecycle(t.Context(), testScope, owner); !errors.Is(err, app.ErrSourceLifecycleUnowned) {
		t.Fatalf("fixture must reproduce missing owner: %v", err)
	}
	reserver := &orphanRecoveryReserver{}
	limits := app.CanonicalProvenanceLimits{}
	if fault == orphanRecoveryNextWindow {
		limits, _ = app.NormalizeCanonicalProvenanceLimits(app.CanonicalProvenanceLimits{})
		limits.Returned = 1
		limits.Examined = 1
	}
	reader := &orphanRecoveryContent{CanonicalProvenanceReader: content.Workspace, RetirementContentReader: content.Workspace, fault: fault}
	coordinator, err := app.NewSourceRetirementCoordinator(app.SourceRetirementCoordinatorDependencies{Scope: testScope, Lifecycles: store, Provenance: reader, Pages: reserver, Operations: store, Projection: content.Workspace, ApplyCoordinator: newHostRetirementCoordinator(), Limits: limits, Now: func() time.Time { return at.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	_, advanceErr := coordinator.Advance(t.Context())
	if fault == orphanRecoveryNextWindow {
		if _, err := store.SourceLifecycle(t.Context(), testScope, owner); !errors.Is(err, app.ErrSourceLifecycleUnowned) {
			t.Fatalf("first nonmatching window recovered owner: %v", err)
		}
		for range 10 {
			_, advanceErr = coordinator.Advance(t.Context())
			if len(reserver.pages) > 0 {
				break
			}
		}
	}
	if fault == orphanRecoveryRaw || fault == orphanRecoveryStale || fault == orphanRecoveryUncited || fault == orphanRecoveryUnowned {
		if fault != orphanRecoveryUncited && fault != orphanRecoveryUnowned && advanceErr == nil {
			t.Fatal("invalid proof accepted")
		}
		if fault == orphanRecoveryUnowned && advanceErr != nil {
			t.Fatalf("unowned historical ref blocked retirement scheduling: %v", advanceErr)
		}
		if _, err := store.SourceLifecycle(t.Context(), testScope, owner); !errors.Is(err, app.ErrSourceLifecycleUnowned) {
			t.Fatalf("invalid proof created owner: %v", err)
		}
		if len(reserver.pages) != 0 {
			t.Fatal("invalid proof scheduled work")
		}
		return
	}
	if advanceErr != nil {
		t.Fatal(advanceErr)
	}
	state, err := store.SourceLifecycle(t.Context(), testScope, owner)
	if err != nil || state.State != knowl.SourceLifecycleRetiring || state.ConfigDigest != "" || state.RecoveryProofDigest == "" || len(reserver.pages) != 1 {
		t.Fatalf("orphan not recovered into normal retirement: %#v pages=%v err=%v", state, reserver.pages, err)
	}
}
