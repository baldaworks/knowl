package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestApplyCoordinatorOrdersCompleteProjectionTails(t *testing.T) {
	workspace, store, _, _ := newWorkflow(t, false, nil)
	gate := newTestApplyCoordinator()
	index := &delayedApplyIndex{SearchIndex: store, entered: make(chan struct{}), resume: make(chan struct{}, 1)}
	service, err := app.NewIngestService(workspace, store, index, applyFactMaintainer{}, app.IngestOptions{AutoApply: true, ApplyCoordinator: gate})
	if err != nil {
		t.Fatal(err)
	}
	// Bound completion under race/coverage load without charging fixture setup.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	var workers sync.WaitGroup
	defer func() { cancel(); close(index.resume); workers.Wait() }()
	firstDone := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, err := service.Ingest(ctx, applyEnvelope("source-1"))
		firstDone <- err
	}()
	select {
	case <-index.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-gate.requests:
	case <-ctx.Done():
		t.Fatal("first apply bypassed coordinator")
	}
	secondDone := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, err := service.Ingest(ctx, applyEnvelope("source-2"))
		secondDone <- err
	}()
	select {
	case <-gate.requests:
	case <-ctx.Done():
		t.Fatal("second apply bypassed coordinator")
	}
	if index.calls.Load() != 1 {
		t.Fatal("projection overtook preceding publication")
	}
	select {
	case err := <-secondDone:
		t.Fatalf("second apply escaped occupied gate: %v", err)
	default:
	}
	// Release the first projection without closing the channel twice in cleanup.
	index.resume <- struct{}{}
	for _, done := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	snapshot, err := workspace.Snapshot(ctx, testSourceScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pages) != 2 || index.calls.Load() != 2 {
		t.Fatalf("canonical pages=%d projections=%d", len(snapshot.Pages), index.calls.Load())
	}
	if err := store.CheckProjection(ctx, snapshot); err != nil {
		t.Fatalf("older full snapshot published last: %v", err)
	}
}

func TestConcurrentPreparedSourcePlansFailSafely(t *testing.T) {
	workspace, store, _, _ := newWorkflow(t, false, nil)
	gate := newTestApplyCoordinator()
	var calls atomic.Int32
	service, err := app.NewIngestService(workspace, store, store, applyFactMaintainer{calls: &calls}, app.IngestOptions{ApplyCoordinator: gate})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Preview(t.Context(), applyEnvelope("source-1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Preview(t.Context(), applyEnvelope("source-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(t.Context(), testSourceScope, first.Operation.ID); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki/log.md"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(t.Context(), testSourceScope, second.Operation.ID)
	if !errors.Is(err, contentfs.ErrPrecondition) || result.Operation.Status != knowl.StatusFailed || result.Operation.Failure == nil || result.Operation.Failure.Reason != "precondition_failed" {
		t.Fatalf("stale apply=%+v err=%v", result, err)
	}
	after, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki/log.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed stale apply changed log")
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki/entities/source-1.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki/entities/source-2.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale factual page committed: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("conflict replanned source %d times", calls.Load())
	}
	logConflictObservation(t, "source-source-conflict", calls.Load(), 0, result.Operation)
}

func TestCanceledApplyCoordinatorWaitCannotCommit(t *testing.T) {
	workspace, store, _, _ := newWorkflow(t, false, nil)
	gate := newTestApplyCoordinator()
	service, err := app.NewIngestService(workspace, store, store, applyFactMaintainer{}, app.IngestOptions{ApplyCoordinator: gate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.Preview(t.Context(), applyEnvelope("source-1"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := gate.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for len(gate.requests) > 0 {
		<-gate.requests
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := service.Apply(ctx, testSourceScope, preview.Operation.ID); done <- err }()
	select {
	case <-gate.requests:
	case <-ctx.Done():
		t.Fatal("apply bypassed occupied coordinator")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled apply=%v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki/entities/source-1.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled apply committed: %v", err)
	}
}

func TestSubmitAcceptSourceWaitsForFinalProvenanceFence(t *testing.T) {
	gate := newTestApplyCoordinator()
	workspace, _, service, _ := newWorkflowWithOptions(t, app.IngestOptions{ApplyCoordinator: gate}, nil)
	owner, cursor, limits := prepareZeroProvenanceContinuation(t, workspace)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	release, err := gate.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release = sync.OnceFunc(release)
	var workers sync.WaitGroup
	defer func() { cancel(); release(); workers.Wait() }()
	<-gate.requests
	done := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, submitErr := service.Submit(ctx, sourceEnvelope([]byte("coordinated source publication")))
		done <- submitErr
	}()
	select {
	case <-gate.requests:
	case err := <-done:
		release()
		t.Fatalf("source acceptance bypassed finalization fence: %v", err)
	case <-ctx.Done():
		release()
		t.Fatal(ctx.Err())
	}

	final, err := workspace.PageProvenance(ctx, testSourceScope, owner, cursor, limits)
	if err != nil || !final.EOF || !final.Authoritative || final.Returned != 0 {
		release()
		t.Fatalf("final provenance = %#v, %v", final, err)
	}
	select {
	case err := <-done:
		release()
		t.Fatalf("source acceptance crossed finalization fence: %v", err)
	default:
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, err = workspace.PageProvenance(ctx, testSourceScope, owner, cursor, limits)
	assertCanonicalProvenanceRestart(t, err)
}

func TestCanceledSubmitWaitDoesNotPublishAcceptedSource(t *testing.T) {
	gate := newTestApplyCoordinator()
	workspace, _, service, _ := newWorkflowWithOptions(t, app.IngestOptions{ApplyCoordinator: gate}, nil)
	owner, cursor, limits := prepareZeroProvenanceContinuation(t, workspace)
	release, err := gate.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	<-gate.requests
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, submitErr := service.Submit(ctx, sourceEnvelope([]byte("canceled source publication")))
		done <- submitErr
	}()
	select {
	case <-gate.requests:
	case <-time.After(5 * time.Second):
		t.Fatal("submit did not request publication coordinator")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled submit error = %v", err)
	}
	final, err := workspace.PageProvenance(t.Context(), testSourceScope, owner, cursor, limits)
	if err != nil || !final.EOF || !final.Authoritative {
		t.Fatalf("canceled submit changed publication = %#v, %v", final, err)
	}
}

func TestSubmitReleasesCoordinatorWhenAcceptSourceFails(t *testing.T) {
	gate := newTestApplyCoordinator()
	workspace, store, _, maintainer := newWorkflow(t, false, nil)
	content := &rejectingAcceptStore{Workspace: workspace}
	service, err := app.NewIngestService(content, store, store, maintainer, app.IngestOptions{ApplyCoordinator: gate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Submit(t.Context(), sourceEnvelope([]byte("rejected publication"))); err == nil || content.acceptCalls != 1 {
		t.Fatalf("failed source acceptance = %v, calls = %d", err, content.acceptCalls)
	}
	select {
	case <-gate.requests:
	default:
		t.Fatal("failed source acceptance bypassed coordinator")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	release, err := gate.Acquire(ctx)
	if err != nil {
		t.Fatalf("failed source acceptance retained coordinator: %v", err)
	}
	release()
}

type testApplyCoordinator struct {
	gate     chan struct{}
	requests chan struct{}
}

func prepareZeroProvenanceContinuation(t *testing.T, workspace *contentfs.Workspace) (knowl.SourceOwner, app.CanonicalProvenanceCursor, app.CanonicalProvenanceLimits) {
	t.Helper()
	content := []byte("accepted provenance")
	digest := sha256.Sum256(content)
	accepted, err := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{
		Scope:   testSourceScope,
		Source:  knowl.SourceRef{Adapter: string(knowl.SourceAdapterGit), ID: "active/document"},
		Version: knowl.SourceVersion{Version: "v1", Digest: fmt.Sprintf("%x", digest)}, MediaType: "text/markdown",
		SourceDocument: knowl.SourceDocument{SourceID: "active", DocumentID: "document", Revision: "v1", URI: "https://example.test/active/document"},
		Content:        content,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := app.SourceRefKey(accepted)
	for _, id := range []string{retirementCoordinatorPageA, retirementCoordinatorPageB} {
		path := filepath.Join(workspace.Root(), "wiki", filepath.FromSlash(id)+".md")
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
	first, err := workspace.PageProvenance(t.Context(), testSourceScope, owner, "", limits)
	if err != nil || first.EOF || first.Authoritative || first.NextCursor == "" {
		t.Fatalf("initial provenance = %#v, %v", first, err)
	}
	return owner, first.NextCursor, limits
}

func assertCanonicalProvenanceRestart(t *testing.T, err error) {
	t.Helper()
	var failure *app.CanonicalProvenanceFailure
	if !errors.As(err, &failure) || failure.Code != app.CanonicalProvenanceCodeRestart || !errors.Is(err, app.ErrCanonicalProvenanceRestart) {
		t.Fatalf("provenance restart = %#v, %v", failure, err)
	}
}

func newTestApplyCoordinator() *testApplyCoordinator {
	return &testApplyCoordinator{gate: make(chan struct{}, 1), requests: make(chan struct{}, 8)}
}
func (gate *testApplyCoordinator) Acquire(ctx context.Context) (func(), error) {
	select {
	case gate.requests <- struct{}{}:
	default:
	}
	select {
	case gate.gate <- struct{}{}:
		return func() { <-gate.gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type delayedApplyIndex struct {
	app.SearchIndex
	calls           atomic.Int32
	entered, resume chan struct{}
}

func (index *delayedApplyIndex) Project(ctx context.Context, commit knowl.ContentCommit) error {
	if index.calls.Add(1) == 1 {
		close(index.entered)
		select {
		case <-index.resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return index.SearchIndex.Project(ctx, commit)
}

type applyFactMaintainer struct{ calls *atomic.Int32 }

func (maintainer applyFactMaintainer) Plan(_ context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	if maintainer.calls != nil {
		maintainer.calls.Add(1)
	}
	id := input.Source.Source.ID
	ref := app.SourceRefKey(input.Source)
	content := fmt.Sprintf("---\ntype: entity\ntitle: %s\nknowl:\n  id: entities/%s\n  source_refs: [%s]\n---\n# %s\n\nRecorded fact.\n", id, id, ref, id)
	plan := knowl.ModelEditPlan{SchemaDigest: input.Schema.Digest, SourceRefs: []string{ref}, Edits: []knowl.FileEdit{{Path: "wiki/entities/" + id + ".md", Content: []byte(content)}}}
	return withRootCatalog(input, plan), nil
}
func applyEnvelope(id string) knowl.SourceEnvelope {
	envelope := sourceEnvelope([]byte("independent recorded fact"))
	envelope.Source.ID = id
	return envelope
}

func TestOverlappingSourceAndHierarchyPreserveFirstCommit(t *testing.T) {
	workspace, store := hierarchyWorkflow(t)
	gate := newTestApplyCoordinator()
	maintainer := &blockedApplyHierarchy{ready: make(chan struct{}), resume: make(chan struct{}, 1)}
	hierarchy, err := app.NewHierarchyService(workspace, store, store, maintainer, app.HierarchyOptions{ApplyCoordinator: gate})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	var workers sync.WaitGroup
	defer func() { cancel(); close(maintainer.resume); workers.Wait() }()
	type completed struct {
		result app.IngestResult
		err    error
	}
	done := make(chan completed, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		result, err := hierarchy.Reconcile(ctx, testSourceScope)
		done <- completed{result, err}
	}()
	select {
	case <-maintainer.ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var calls atomic.Int32
	source, err := app.NewIngestService(workspace, store, store, applyFactMaintainer{calls: &calls}, app.IngestOptions{AutoApply: true, ApplyCoordinator: gate})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := source.Ingest(ctx, applyEnvelope("new-source"))
	if err != nil || committed.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("source while hierarchy infers=%+v %v", committed, err)
	}
	before, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki/log.md"))
	if err != nil {
		t.Fatal(err)
	}
	maintainer.resume <- struct{}{}
	var stale completed
	select {
	case stale = <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !errors.Is(stale.err, contentfs.ErrPrecondition) || stale.result.Operation.Status != knowl.StatusFailed || stale.result.Operation.Failure == nil || stale.result.Operation.Failure.Reason != "precondition_failed" {
		t.Fatalf("stale hierarchy=%+v %v", stale.result, stale.err)
	}
	after, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki/log.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("stale hierarchy changed committed log")
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki/entities/new-source.md")); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Validate(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || maintainer.calls.Load() != 1 {
		t.Fatalf("conflict calls source%d hierarchy%d", calls.Load(), maintainer.calls.Load())
	}
	logConflictObservation(t, "source-hierarchy-conflict", calls.Load(), maintainer.calls.Load(), stale.result.Operation)
}

type blockedApplyHierarchy struct {
	ready, resume chan struct{}
	delegate      hierarchyMaintainer
	calls         atomic.Int32
}

func (maintainer *blockedApplyHierarchy) PlanHierarchy(ctx context.Context, input knowl.HierarchyInput) (knowl.HierarchyModelPlan, error) {
	maintainer.calls.Add(1)
	plan, err := maintainer.delegate.PlanHierarchy(ctx, input)
	if err != nil {
		return plan, err
	}
	close(maintainer.ready)
	select {
	case <-maintainer.resume:
		return plan, nil
	case <-ctx.Done():
		return knowl.HierarchyModelPlan{}, ctx.Err()
	}
}

func TestHierarchyPublicationWaitCoversApplyAndNoOp(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(fmt.Sprintf("change_%v", change), func(t *testing.T) {
			workspace, store, _, _ := newWorkflow(t, false, nil)
			gate := newTestApplyCoordinator()
			service, err := app.NewHierarchyService(workspace, store, store, preserveApplyHierarchy{}, app.HierarchyOptions{ApplyCoordinator: gate})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Reconcile(t.Context(), testSourceScope); err != nil {
				t.Fatal(err)
			}
			// A fresh planner identity avoids terminal replay of the seed operation.
			service, err = app.NewHierarchyService(workspace, store, store, preserveApplyHierarchy{change: change}, app.HierarchyOptions{ApplyCoordinator: gate, PlannerVersion: "publication-next"})
			if err != nil {
				t.Fatal(err)
			}
			release, err := gate.Acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			for len(gate.requests) > 0 {
				<-gate.requests
			}
			before, err := os.ReadFile(filepath.Join(workspace.Root(), testRootCatalogPath))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := service.Reconcile(ctx, testSourceScope); done <- err }()
			select {
			case <-gate.requests:
			case <-ctx.Done():
				t.Fatal("hierarchy publication bypassed coordinator")
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("hierarchy cancellation=%v", err)
			}
			after, err := os.ReadFile(filepath.Join(workspace.Root(), testRootCatalogPath))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("hierarchy changed canonical graph while publication blocked")
			}
		})
	}
}

type preserveApplyHierarchy struct{ change bool }

func (maintainer preserveApplyHierarchy) PlanHierarchy(_ context.Context, input knowl.HierarchyInput) (knowl.HierarchyModelPlan, error) {
	plan := knowl.HierarchyModelPlan{SchemaDigest: input.SchemaDigest, SnapshotDigest: input.SnapshotDigest}
	for _, catalog := range input.Catalogs {
		title := catalog.Title
		if maintainer.change && catalog.Path == testRootCatalogPath {
			title += " updated"
		}
		plan.Catalogs = append(plan.Catalogs, knowl.HierarchyCatalogSpec{Path: catalog.Path, Title: title, Children: catalog.Children})
	}
	return plan, nil
}

func logConflictObservation(t *testing.T, id string, sourceCalls, hierarchyCalls int32, operation knowl.Operation) {
	t.Helper()
	encoded, err := json.Marshal(struct {
		CaseID         string                `json:"case_id"`
		SourceCalls    int32                 `json:"source_calls"`
		HierarchyCalls int32                 `json:"hierarchy_calls"`
		Status         knowl.OperationStatus `json:"status"`
		FailureClass   string                `json:"failure_class"`
		FailureReason  string                `json:"failure_reason"`
		Outcome        string                `json:"outcome"`
	}{id, sourceCalls, hierarchyCalls, operation.Status, operation.Failure.Class, operation.Failure.Reason, "met"})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(encoded))
}
