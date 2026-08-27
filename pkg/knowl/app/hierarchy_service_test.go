package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	hierarchyArchitectureCatalog = "wiki/catalogs/architecture/index.md"
	hierarchyProductCatalog      = "wiki/catalogs/product/index.md"
	testArchitectureType         = "architecture"
)

func TestHierarchyServiceReconcilesCatalogsWithoutChangingSourcePages(t *testing.T) {
	ctx := context.Background()
	workspace, store := hierarchyWorkflow(t)
	maintainer := &hierarchyMaintainer{}
	service := newHierarchyService(t, workspace, store, store, maintainer, time.Minute)

	before := hierarchyProtectedFiles(t, workspace)
	reservation, err := service.Reserve(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("reserve hierarchy: %v", err)
	}
	if reservation.Kind != knowl.WorkHierarchy || !reservation.New {
		t.Fatalf("reservation = %#v, want new hierarchy work", reservation)
	}
	if maintainer.calls() != 0 {
		t.Fatalf("provider calls during reservation = %d, want 0", maintainer.calls())
	}

	result, err := service.RunToTerminal(ctx, claimReady(t, store, testSourceScope))
	if err != nil {
		t.Fatalf("run hierarchy: %v", err)
	}
	if result.Operation.Status != knowl.StatusCommitted || result.Commit == nil {
		t.Fatalf("result = %#v, want committed content", result)
	}
	if maintainer.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", maintainer.calls())
	}
	assertHierarchyProtectedFiles(t, workspace, before)

	root := readWorkspaceFile(t, workspace, "wiki/index.md")
	if !strings.Contains(root, "/catalogs/architecture/index.md") || !strings.Contains(root, "/catalogs/product/index.md") ||
		strings.Contains(root, "/entities/one.md") || strings.Contains(root, "/entities/two.md") {
		t.Fatalf("root is not a catalog-only hierarchy:\n%s", root)
	}
	architecture := readWorkspaceFile(t, workspace, hierarchyArchitectureCatalog)
	product := readWorkspaceFile(t, workspace, hierarchyProductCatalog)
	if !strings.Contains(architecture, "/entities/one.md") || !strings.Contains(product, "/entities/two.md") {
		t.Fatalf("catalog memberships are incomplete:\narchitecture:\n%s\nproduct:\n%s", architecture, product)
	}

	digestBeforeReplay, err := workspace.HierarchySnapshotDigest(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("digest committed hierarchy: %v", err)
	}
	replayReservation, err := service.Reserve(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("reserve converged hierarchy: %v", err)
	}
	if !replayReservation.New || replayReservation.ID == reservation.ID {
		t.Fatalf("converged reservation = %#v, want a new snapshot identity", replayReservation)
	}
	replay, err := service.RunToTerminal(ctx, claimReady(t, store, testSourceScope))
	if err != nil {
		t.Fatalf("run converged hierarchy: %v", err)
	}
	if replay.Operation.Status != knowl.StatusCommitted || replay.Commit != nil {
		t.Fatalf("converged result = %#v, want committed no-op", replay)
	}
	digestAfterReplay, err := workspace.HierarchySnapshotDigest(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("digest converged hierarchy: %v", err)
	}
	if digestAfterReplay != digestBeforeReplay {
		t.Fatalf("no-op changed canonical snapshot: %q -> %q", digestBeforeReplay, digestAfterReplay)
	}
	assertHierarchyProtectedFiles(t, workspace, before)
}

func TestHierarchyServiceRejectsStaleSnapshotBeforeProviderPlanning(t *testing.T) {
	ctx := context.Background()
	workspace, store := hierarchyWorkflow(t)
	maintainer := &hierarchyMaintainer{}
	service := newHierarchyService(t, workspace, store, store, maintainer, time.Minute)

	reservation, err := service.Reserve(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("reserve hierarchy: %v", err)
	}
	rootBefore := readWorkspaceFile(t, workspace, "wiki/index.md")
	pagePath := filepath.Join(workspace.Root(), filepath.FromSlash(testPagePath))
	page, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatalf("read page before concurrent edit: %v", err)
	}
	if err := os.WriteFile(pagePath, append(page, []byte("\nconcurrent edit\n")...), 0o600); err != nil {
		t.Fatalf("write concurrent edit: %v", err)
	}

	result, err := service.RunToTerminal(ctx, claimReady(t, store, testSourceScope))
	if !errors.Is(err, app.ErrHierarchyDigestMismatch) {
		t.Fatalf("run stale hierarchy error = %v, want digest mismatch", err)
	}
	if result.Operation.Status != knowl.StatusFailed || result.Operation.ID != reservation.ID {
		t.Fatalf("stale operation = %#v, want failed reservation", result.Operation)
	}
	if maintainer.calls() != 0 {
		t.Fatalf("provider calls for stale snapshot = %d, want 0", maintainer.calls())
	}
	if rootAfter := readWorkspaceFile(t, workspace, "wiki/index.md"); rootAfter != rootBefore {
		t.Fatal("stale hierarchy attempt changed the root catalog")
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(hierarchyArchitectureCatalog))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale hierarchy catalog stat = %v, want absent", err)
	}
}

func TestHierarchyServiceAcceptsExcerptTruncatedAfterWhitespace(t *testing.T) {
	ctx := context.Background()
	workspace, store := hierarchyWorkflow(t)
	content := "---\nid: entities/one\ntitle: One\ntype: architecture\nsource_refs:\n  - " + testSourceRef + "\n---\n# One\n\n" + strings.Repeat("x", 4089) + " y\n"
	if err := os.WriteFile(filepath.Join(workspace.Root(), filepath.FromSlash(testPagePath)), []byte(content), 0o600); err != nil {
		t.Fatalf("write boundary excerpt page: %v", err)
	}

	service := newHierarchyService(t, workspace, store, store, &hierarchyMaintainer{}, time.Minute)
	result, err := service.Reconcile(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("reconcile boundary excerpt: %v", err)
	}
	if result.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("boundary excerpt result = %#v, want committed", result.Operation)
	}
}

func TestHierarchyServiceReplaysCommittedStageAfterOutcomeFailure(t *testing.T) {
	ctx := context.Background()
	workspace, store := hierarchyWorkflow(t)
	maintainer := &hierarchyMaintainer{}
	operations := &failOnceOutcomeStore{OperationStore: store}
	service := newHierarchyService(t, workspace, operations, store, maintainer, time.Nanosecond)

	reservation, err := service.Reserve(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("reserve hierarchy: %v", err)
	}
	claim := claimReady(t, store, testSourceScope)
	first, err := service.RunToTerminal(ctx, claim)
	if !errors.Is(err, errOutcomeUnavailable) || first.Commit == nil {
		t.Fatalf("first run = %#v, %v, want committed content and failed outcome", first, err)
	}
	if err := store.ReleaseClaim(ctx, testSourceScope, reservation.ID, claim.Lease.Token); err != nil {
		t.Fatalf("release first claim: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	replayed, err := service.RunToTerminal(ctx, claimReady(t, store, testSourceScope))
	if err != nil {
		t.Fatalf("replay committed stage: %v", err)
	}
	if replayed.Operation.Status != knowl.StatusCommitted || replayed.Commit == nil {
		t.Fatalf("replayed result = %#v, want committed", replayed)
	}
	if maintainer.calls() != 1 {
		t.Fatalf("provider calls after durable replay = %d, want 1", maintainer.calls())
	}
}

func TestHierarchyServiceReplaysAfterProjectionFailure(t *testing.T) {
	ctx := context.Background()
	workspace, store := hierarchyWorkflow(t)
	maintainer := &hierarchyMaintainer{}
	index := &failOnceProjectionIndex{}
	service := newHierarchyService(t, workspace, store, index, maintainer, time.Nanosecond)

	reservation, err := service.Reserve(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("reserve hierarchy: %v", err)
	}
	claim := claimReady(t, store, testSourceScope)
	first, err := service.RunToTerminal(ctx, claim)
	if !errors.Is(err, app.ErrProjection) || first.Commit == nil || first.Operation.Status == knowl.StatusCommitted {
		t.Fatalf("first projection = %#v, %v, want observable canonical commit with non-terminal operation", first, err)
	}
	if err := store.ReleaseClaim(ctx, testSourceScope, reservation.ID, claim.Lease.Token); err != nil {
		t.Fatalf("release first claim: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	replayed, err := service.RunToTerminal(ctx, claimReady(t, store, testSourceScope))
	if err != nil {
		t.Fatalf("replay after projection failure: %v", err)
	}
	if replayed.Operation.Status != knowl.StatusCommitted || replayed.Commit == nil {
		t.Fatalf("replayed projection result = %#v, want committed", replayed)
	}
	if maintainer.calls() != 1 {
		t.Fatalf("provider calls after projection replay = %d, want 1", maintainer.calls())
	}
}

func TestHierarchyServiceCancellationLeavesReservationRetryable(t *testing.T) {
	workspace, store := hierarchyWorkflow(t)
	maintainer := &hierarchyMaintainer{}
	service := newHierarchyService(t, workspace, store, store, maintainer, time.Minute)
	reservation, err := service.Reserve(context.Background(), testSourceScope)
	if err != nil {
		t.Fatalf("reserve hierarchy: %v", err)
	}
	claim := claimReady(t, store, testSourceScope)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.RunToTerminal(cancelled, claim); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run error = %v, want context canceled", err)
	}
	operation, err := store.Operation(context.Background(), testSourceScope, reservation.ID)
	if err != nil {
		t.Fatalf("read cancelled reservation: %v", err)
	}
	if operation.Status != knowl.StatusReceived || maintainer.calls() != 0 {
		t.Fatalf("cancelled operation = %#v, provider calls = %d", operation, maintainer.calls())
	}
}

func TestHierarchyServiceReconcileClaimsOnlyItsReservedOperation(t *testing.T) {
	ctx := context.Background()
	workspace, store := hierarchyWorkflow(t)
	sourceMaintainer := &countingMaintainer{}
	sourceService, err := app.NewIngestService(workspace, store, store, sourceMaintainer, app.IngestOptions{AutoApply: true})
	if err != nil {
		t.Fatalf("new pending source service: %v", err)
	}
	pendingContent := []byte("unrelated pending source")
	pending, err := sourceService.Submit(ctx, knowl.SourceEnvelope{
		Scope: testSourceScope, Source: knowl.SourceRef{Adapter: testSourceAdapter, ID: "pending-source"},
		Version: knowl.SourceVersion{Version: "1", Digest: digest(pendingContent)}, MediaType: testPlainMediaType, Content: pendingContent,
	})
	if err != nil {
		t.Fatalf("reserve pending source operation: %v", err)
	}

	hierarchyMaintainer := &hierarchyMaintainer{}
	hierarchy := newHierarchyService(t, workspace, store, store, hierarchyMaintainer, time.Minute)
	result, err := hierarchy.Reconcile(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("explicit hierarchy reconcile: %v", err)
	}
	if result.Operation.Kind != knowl.WorkHierarchy || result.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("hierarchy result = %#v", result.Operation)
	}
	pendingOperation, err := store.Operation(ctx, testSourceScope, pending.Operation.ID)
	if err != nil {
		t.Fatalf("read pending source operation: %v", err)
	}
	if pendingOperation.Status != knowl.StatusReceived || sourceMaintainer.calls() != 0 {
		t.Fatalf("unrelated source operation = %#v, provider calls = %d", pendingOperation, sourceMaintainer.calls())
	}
}

type hierarchyMaintainer struct {
	mu      sync.Mutex
	counter int
}

func (maintainer *hierarchyMaintainer) PlanHierarchy(_ context.Context, input knowl.HierarchyInput) (knowl.HierarchyModelPlan, error) {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	maintainer.counter++
	architecture := make([]string, 0, len(input.Pages))
	product := make([]string, 0, len(input.Pages))
	for _, page := range input.Pages {
		if page.Type == testArchitectureType {
			architecture = append(architecture, page.Path)
		} else {
			product = append(product, page.Path)
		}
	}
	return knowl.HierarchyModelPlan{
		SchemaDigest: input.SchemaDigest, SnapshotDigest: input.SnapshotDigest,
		Catalogs: []knowl.HierarchyCatalogSpec{
			{Path: "wiki/index.md", Title: "Knowl", Children: []string{hierarchyArchitectureCatalog, hierarchyProductCatalog}},
			{Path: hierarchyArchitectureCatalog, Title: "Architecture", Children: architecture},
			{Path: hierarchyProductCatalog, Title: "Product", Children: product},
		},
	}, nil
}

func (maintainer *hierarchyMaintainer) calls() int {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	return maintainer.counter
}

func hierarchyWorkflow(t *testing.T) (*contentfs.Workspace, *sqlite.Store) {
	t.Helper()
	workspace, store, ingest, _ := newWorkflowWithOptions(t, app.IngestOptions{AutoApply: true}, nil, func(schema knowl.SchemaDocument) knowl.ModelEditPlan {
		return knowl.ModelEditPlan{
			SchemaDigest: schema.Digest,
			SourceRefs:   []string{testSourceRef},
			Edits: []knowl.FileEdit{
				{Path: testPagePath, Content: []byte("---\nid: entities/one\ntitle: One\ntype: architecture\nsource_refs:\n  - " + testSourceRef + "\n---\n# One\n\nArchitecture body.\n")},
				{Path: testPageTwoPath, Content: []byte("---\nid: entities/two\ntitle: Two\ntype: product\nsource_refs:\n  - " + testSourceRef + "\n---\n# Two\n\nProduct body.\n")},
			},
		}
	})
	if _, err := ingest.Ingest(context.Background(), sourceEnvelope([]byte("hierarchy source"))); err != nil {
		t.Fatalf("seed hierarchy workflow: %v", err)
	}
	return workspace, store
}

func newHierarchyService(t *testing.T, workspace *contentfs.Workspace, operations app.OperationStore, index app.SearchIndex, maintainer app.HierarchyMaintainer, leaseDuration time.Duration) *app.HierarchyService {
	t.Helper()
	service, err := app.NewHierarchyService(workspace, operations, index, maintainer, app.HierarchyOptions{LeaseDuration: leaseDuration})
	if err != nil {
		t.Fatalf("new hierarchy service: %v", err)
	}
	return service
}

func hierarchyProtectedFiles(t *testing.T, workspace *contentfs.Workspace) map[string][]byte {
	t.Helper()
	inspection, err := workspace.Inspect(context.Background(), testSourceScope)
	if err != nil {
		t.Fatalf("inspect hierarchy workspace: %v", err)
	}
	paths := []string{testPagePath, testPageTwoPath}
	for _, raw := range inspection.RawSources {
		paths = append(paths, raw.Path)
	}
	result := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read protected file %q: %v", path, err)
		}
		result[path] = content
	}
	return result
}

func assertHierarchyProtectedFiles(t *testing.T, workspace *contentfs.Workspace, want map[string][]byte) {
	t.Helper()
	for path, expected := range want {
		got, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read protected file %q: %v", path, err)
		}
		if string(got) != string(expected) {
			t.Fatalf("hierarchy reconciliation changed protected file %q", path)
		}
	}
}

func readWorkspaceFile(t *testing.T, workspace *contentfs.Workspace, path string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read workspace file %q: %v", path, err)
	}
	return string(content)
}
