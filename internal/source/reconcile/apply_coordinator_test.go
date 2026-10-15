package reconcile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestPreparedSourceSyncWaitsForPublicationAndRecovers(t *testing.T) {
	harness := newStageHarness(t, nil)
	changedListing(t, harness)
	original := harness.service
	gate := &syncApplyCoordinator{occupied: make(chan struct{}, 1), requested: make(chan struct{}, 1)}
	gate.occupied <- struct{}{}
	coordinator := &allowFirstApplyCoordinator{afterFirst: gate}
	service, err := NewService(Dependencies{Adapters: original.adapters, State: original.state, Content: original.content, SourceContent: original.sourceContent, Search: original.search, Maintenance: original.maintenance, ApplyCoordinator: coordinator}, original.options)
	if err != nil {
		t.Fatal(err)
	}
	harness.service = service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type completion struct {
		result Result
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		result, err := service.SyncSource(ctx, harness.scope, harness.source(knowl.SourceFlavorMarkdown))
		done <- completion{result, err}
	}()
	select {
	case <-gate.requested:
	case <-ctx.Done():
		t.Fatal("source sync bypassed publication coordinator")
	}
	select {
	case value := <-done:
		t.Fatalf("sync escaped occupied publication gate: %+v", value)
	default:
	}
	cancel()
	stopped := <-done
	if !errors.Is(stopped.err, context.Canceled) || stopped.result.Run.Status != knowl.SyncStatusPrepared {
		t.Fatalf("canceled publication=%+v %v", stopped.result, stopped.err)
	}
	inventory, err := service.sourceContent.SourceDigests(t.Context(), harness.scope, harness.sourceID, 16)
	if err != nil || len(inventory) != 1 || inventory[0].Digest != sha256Hex("before") {
		t.Fatalf("waiting sync changed canonical source: %+v %v", inventory, err)
	}
	<-gate.occupied
	recovered, err := service.Recover(t.Context(), harness.scope, []knowl.Source{harness.source(knowl.SourceFlavorMarkdown)})
	if err != nil || len(recovered) != 1 || recovered[0].Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("publication recovery=%+v %v", recovered, err)
	}
	assertCanonicalConverged(t, harness, "after")
}

func TestSourceAcceptanceWaitsForFinalProvenanceFence(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		setup func(*testing.T, *stageHarness)
	}{
		{
			name: "create",
			setup: func(t *testing.T, harness *stageHarness) {
				ref := harness.descriptor("docs/new.md", "# New\n")
				harness.adapter.script(ref.ExternalID, "# New\n")
				harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
			},
		},
		{
			name: "enrichment",
			setup: func(t *testing.T, harness *stageHarness) {
				body := "# Existing\n"
				harness.seedFinalized(t, []seededDoc{{path: "docs/existing.md", body: body, missingMaintenance: true, legacyRaw: true}})
				ref := harness.descriptor("docs/existing.md", body)
				harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			harness := newStageHarness(t, nil)
			scenario.setup(t, harness)
			gate := &syncApplyCoordinator{occupied: make(chan struct{}, 1), requested: make(chan struct{}, 2)}
			harness.service = coordinatedStageService(t, harness.service, gate)
			owner, cursor, limits := prepareReconcileZeroProvenanceContinuation(t, harness)
			release, err := gate.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			<-gate.requested
			type completion struct {
				result Result
				err    error
			}
			done := make(chan completion, 1)
			go func() {
				result, syncErr := harness.service.SyncSource(ctx, harness.scope, harness.source(knowl.SourceFlavorMarkdown))
				done <- completion{result: result, err: syncErr}
			}()
			select {
			case <-gate.requested:
			case value := <-done:
				release()
				t.Fatalf("source acceptance bypassed finalization fence: %+v", value)
			case <-ctx.Done():
				release()
				t.Fatal(ctx.Err())
			}

			final, err := harness.workspace.PageProvenance(ctx, harness.scope, owner, cursor, limits)
			if err != nil || !final.EOF || !final.Authoritative || final.Returned != 0 {
				release()
				t.Fatalf("final provenance = %#v, %v", final, err)
			}
			select {
			case value := <-done:
				release()
				t.Fatalf("source acceptance crossed finalization fence: %+v", value)
			default:
			}
			release()
			completed := <-done
			requireSyncSuccess(t, completed.result, completed.err)
			_, err = harness.workspace.PageProvenance(ctx, harness.scope, owner, cursor, limits)
			assertReconcileProvenanceRestart(t, err)
		})
	}
}

func TestCanceledSourceAcceptanceWaitDoesNotPublish(t *testing.T) {
	harness := newStageHarness(t, nil)
	ref := harness.descriptor("docs/new.md", "# New\n")
	harness.adapter.script(ref.ExternalID, "# New\n")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	gate := &syncApplyCoordinator{occupied: make(chan struct{}, 1), requested: make(chan struct{}, 2)}
	harness.service = coordinatedStageService(t, harness.service, gate)
	owner, cursor, limits := prepareReconcileZeroProvenanceContinuation(t, harness)
	release, err := gate.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	<-gate.requested
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, syncErr := harness.service.SyncSource(ctx, harness.scope, harness.source(knowl.SourceFlavorMarkdown))
		done <- syncErr
	}()
	select {
	case <-gate.requested:
	case <-time.After(5 * time.Second):
		t.Fatal("source acceptance did not request publication coordinator")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled source acceptance = %v", err)
	}
	final, err := harness.workspace.PageProvenance(t.Context(), harness.scope, owner, cursor, limits)
	if err != nil || !final.EOF || !final.Authoritative {
		t.Fatalf("canceled source acceptance changed publication = %#v, %v", final, err)
	}
}

func TestFailedSourceAcceptanceReleasesFenceWithoutPublication(t *testing.T) {
	harness := newStageHarness(t, nil)
	ref := harness.descriptor("docs/new.md", "# New\n")
	harness.adapter.script(ref.ExternalID, "# New\n")
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref}, ""))
	owner, cursor, limits := prepareReconcileZeroProvenanceContinuation(t, harness)
	gate := &syncApplyCoordinator{occupied: make(chan struct{}, 1), requested: make(chan struct{}, 2)}
	content := &failingAcceptContent{ContentStore: harness.service.content}
	service, err := NewService(Dependencies{
		ApplyCoordinator: gate,
		Adapters:         harness.service.adapters, State: harness.service.state, Content: content,
		SourceContent: harness.service.sourceContent, Search: harness.service.search,
		Maintenance: harness.service.maintenance, Lifecycle: harness.service.sourceLifecycle,
	}, harness.service.options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SyncSource(t.Context(), harness.scope, harness.source(knowl.SourceFlavorMarkdown))
	if !errors.Is(err, errTestSourceAcceptance) || content.calls != 1 {
		t.Fatalf("failed source acceptance = %v, calls = %d", err, content.calls)
	}
	final, err := harness.workspace.PageProvenance(t.Context(), harness.scope, owner, cursor, limits)
	if err != nil || !final.EOF || !final.Authoritative {
		t.Fatalf("failed source acceptance changed publication = %#v, %v", final, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	release, err := gate.Acquire(ctx)
	if err != nil {
		t.Fatalf("failed source acceptance retained coordinator: %v", err)
	}
	release()
}

func coordinatedStageService(t *testing.T, original *Service, coordinator app.ApplyCoordinator) *Service {
	t.Helper()
	service, err := NewService(Dependencies{
		ApplyCoordinator: coordinator,
		Adapters:         original.adapters, State: original.state, Content: original.content,
		SourceContent: original.sourceContent, Search: original.search,
		Maintenance: original.maintenance, Lifecycle: original.sourceLifecycle,
	}, original.options)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func prepareReconcileZeroProvenanceContinuation(t *testing.T, harness *stageHarness) (knowl.SourceOwner, app.CanonicalProvenanceCursor, app.CanonicalProvenanceLimits) {
	t.Helper()
	content := []byte("accepted provenance")
	accepted, err := harness.workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{
		Scope:   harness.scope,
		Source:  knowl.SourceRef{Adapter: string(knowl.SourceAdapterGit), ID: "active/document"},
		Version: knowl.SourceVersion{Version: "v1", Digest: sha256Hex(string(content))}, MediaType: "text/markdown",
		SourceDocument: knowl.SourceDocument{SourceID: "active", DocumentID: "document", Revision: "v1", URI: "https://example.test/active/document"},
		Content:        content,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := app.SourceRefKey(accepted)
	for _, id := range []string{"concepts/a", "concepts/b"} {
		path := filepath.Join(harness.workspace.Root(), "wiki", filepath.FromSlash(id)+".md")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		page := fmt.Sprintf("---\ntype: concept\ntitle: %s\nknowl:\n  id: %s\n  source_refs:\n    - %q\n---\n# %s\n", id, id, ref, id)
		if err := os.WriteFile(path, []byte(page), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "retiring"}
	limits := app.CanonicalProvenanceLimits{Returned: 1, Examined: 1, PathBytes: 2_048, PageBytes: 1 << 20, TotalBytes: 4 << 20, SourceRefs: 8, RoundPages: 100}
	first, err := harness.workspace.PageProvenance(t.Context(), harness.scope, owner, "", limits)
	if err != nil || first.EOF || first.Authoritative || first.NextCursor == "" {
		t.Fatalf("initial provenance = %#v, %v", first, err)
	}
	return owner, first.NextCursor, limits
}

func assertReconcileProvenanceRestart(t *testing.T, err error) {
	t.Helper()
	var failure *app.CanonicalProvenanceFailure
	if !errors.As(err, &failure) || failure.Code != app.CanonicalProvenanceCodeRestart || !errors.Is(err, app.ErrCanonicalProvenanceRestart) {
		t.Fatalf("provenance restart = %#v, %v", failure, err)
	}
}

var errTestSourceAcceptance = errors.New("test source acceptance failure")

type failingAcceptContent struct {
	app.ContentStore
	calls int
}

func (content *failingAcceptContent) AcceptSource(context.Context, knowl.SourceEnvelope) (knowl.AcceptedSource, error) {
	content.calls++
	return knowl.AcceptedSource{}, errTestSourceAcceptance
}

type syncApplyCoordinator struct{ occupied, requested chan struct{} }

// allowFirstApplyCoordinator lets the test's accepted-source publication
// complete, then parks the later canonical finalize section on afterFirst.
type allowFirstApplyCoordinator struct {
	afterFirst app.ApplyCoordinator
	calls      int
}

func (coordinator *allowFirstApplyCoordinator) Acquire(ctx context.Context) (func(), error) {
	coordinator.calls++
	if coordinator.calls == 1 {
		return func() {}, nil
	}
	return coordinator.afterFirst.Acquire(ctx)
}

func (gate *syncApplyCoordinator) Acquire(ctx context.Context) (func(), error) {
	select {
	case gate.requested <- struct{}{}:
	default:
	}
	select {
	case gate.occupied <- struct{}{}:
		return func() { <-gate.occupied }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
