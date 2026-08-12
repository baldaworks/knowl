package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestIngestReviewApplyReplayAndProject(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, maintainer := newWorkflow(t, false, nil)
	content := []byte("source text")
	envelope := sourceEnvelope(content)

	planned, err := service.Ingest(ctx, envelope)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if planned.Operation.Status != knowl.StatusAwaitingReview {
		t.Fatalf("planned status = %q, want awaiting_review", planned.Operation.Status)
	}
	if planned.Commit != nil {
		t.Fatal("review-only ingest committed canonical content")
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki", "entities", "one.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planned page stat = %v, want absent", err)
	}
	if maintainer.calls() != 1 {
		t.Fatalf("maintainer calls after initial ingest = %d, want 1", maintainer.calls())
	}

	applied, err := service.Apply(ctx, envelope.Scope, planned.Operation.ID)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("applied status = %q, want committed", applied.Operation.Status)
	}
	if applied.Commit == nil || len(applied.Commit.Files) != 3 {
		t.Fatalf("commit = %#v, want two pages and log", applied.Commit)
	}
	page, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "entities", "one.md"))
	if err != nil {
		t.Fatalf("read committed page: %v", err)
	}
	if string(page) != string(planPageContent) {
		t.Fatalf("committed page = %q", page)
	}
	logContent, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read commit log: %v", err)
	}
	if !contains(string(logContent), string(planned.Operation.ID)) {
		t.Fatalf("commit log does not cite operation: %q", logContent)
	}
	results, err := store.Search(ctx, envelope.Scope, "One", knowl.ReadLimits{Pages: 5})
	if err != nil {
		t.Fatalf("search projected page: %v", err)
	}
	if len(results) != 1 || results[0].ID != testPageID {
		t.Fatalf("search results = %#v", results)
	}
	if len(results[0].SourceRefs) != 1 || results[0].SourceRefs[0] != testSourceRef {
		t.Fatalf("search source refs = %#v", results[0].SourceRefs)
	}
	links, err := store.Links(ctx, envelope.Scope, testPageID, knowl.ReadLimits{Pages: 5})
	if err != nil {
		t.Fatalf("read projected links: %v", err)
	}
	if len(links) != 1 || links[0].To != "entities/two" {
		t.Fatalf("projected links = %#v", links)
	}

	replay, err := service.Ingest(ctx, envelope)
	if err != nil {
		t.Fatalf("replay ingest: %v", err)
	}
	if replay.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("replay status = %q, want committed", replay.Operation.Status)
	}
	if maintainer.calls() != 1 {
		t.Fatalf("maintainer calls after replay = %d, want 1", maintainer.calls())
	}
	secondLog, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read replay log: %v", err)
	}
	if string(secondLog) != string(logContent) {
		t.Fatal("replay changed the canonical log")
	}
}

func TestIngestCommitsIndexAlongsidePagesAndLog(t *testing.T) {
	ctx := context.Background()
	workspace, _, service, maintainer := newWorkflow(t, false, nil)
	indexPath := filepath.Join(workspace.Root(), "wiki", "index.md")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	maintainer.mu.Lock()
	maintainer.plan.Edits = []knowl.FileEdit{
		{Path: testPagePath, Content: planPageContent},
		{Path: testPageTwoPath, Content: planSupportingContent},
		{Path: "wiki/index.md", ExpectedDigest: digest(indexBefore), Content: append(indexBefore, []byte("\n- entities/one\n")...)},
	}
	maintainer.mu.Unlock()
	planned, err := service.Ingest(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	result, err := service.Apply(ctx, "local", planned.Operation.ID)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Operation.Status != knowl.StatusCommitted || len(result.Commit.Files) != 4 {
		t.Fatalf("index commit result = %#v, want two pages, index, and log", result)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read updated index: %v", err)
	}
	if string(indexAfter) != string(append(indexBefore, []byte("\n- entities/one\n")...)) {
		t.Fatalf("updated index = %q", indexAfter)
	}
}

func TestConcurrentReviewReplayConvergesToOneOperation(t *testing.T) {
	ctx := context.Background()
	_, store, service, maintainer := newWorkflow(t, false, nil)
	envelope := sourceEnvelope([]byte("source text"))
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, ingestErr := service.Ingest(ctx, envelope)
			results <- ingestErr
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent ingest: %v", err)
		}
	}
	operationID := knowl.OperationID("local:fixture:source-1@1#" + digest([]byte("source text"))[:16])
	operation, err := store.Operation(ctx, "local", operationID)
	if err != nil {
		t.Fatalf("read converged operation: %v", err)
	}
	if operation.Status != knowl.StatusAwaitingReview {
		t.Fatalf("converged operation status = %q", operation.Status)
	}
	if maintainer.calls() < 1 {
		t.Fatal("concurrent ingest never invoked maintainer")
	}
}

func TestIngestRejectsStaleReviewedPlan(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, _ := newWorkflow(t, false, nil, func(schema knowl.SchemaDocument) knowl.ModelEditPlan {
		return knowl.ModelEditPlan{
			SchemaDigest: schema.Digest,
			SourceRefs:   []string{testSourceRef},
			Edits:        []knowl.FileEdit{{Path: "wiki/entities/stale.md", ExpectedDigest: digest([]byte("before")), Content: []byte("---\nid: entities/stale\ntitle: Stale\ntype: entity\nsource_refs:\n  - " + testSourceRef + "\n---\n# Stale\n\nafter\n")}},
		}
	})
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "entities", "stale.md"), []byte("before"), 0o600); err != nil {
		t.Fatalf("write stale fixture: %v", err)
	}
	planned, err := service.Ingest(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("ingest stale plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "entities", "stale.md"), []byte("human edit"), 0o600); err != nil {
		t.Fatalf("write human edit: %v", err)
	}
	_, err = service.Apply(ctx, "local", planned.Operation.ID)
	if !errors.Is(err, contentfs.ErrPrecondition) {
		t.Fatalf("stale apply error = %v, want precondition", err)
	}
	operation, err := store.Operation(ctx, "local", planned.Operation.ID)
	if err != nil {
		t.Fatalf("read failed operation: %v", err)
	}
	if operation.Status != knowl.StatusFailed {
		t.Fatalf("stale operation status = %q, want failed", operation.Status)
	}
	content, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "entities", "stale.md"))
	if err != nil {
		t.Fatalf("read human edit: %v", err)
	}
	if string(content) != "human edit" {
		t.Fatalf("human edit was overwritten: %q", content)
	}
}

func TestIngestRejectsStaleSchemaAtApply(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, _ := newWorkflow(t, false, nil)
	planned, err := service.Ingest(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	schemaPath := filepath.Join(workspace.Root(), "schema.md")
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if err := os.WriteFile(schemaPath, append(schema, []byte("\noperator change\n")...), 0o600); err != nil {
		t.Fatalf("change schema: %v", err)
	}
	_, err = service.Apply(ctx, "local", planned.Operation.ID)
	if !errors.Is(err, contentfs.ErrPrecondition) {
		t.Fatalf("stale schema error = %v, want precondition", err)
	}
	operation, err := store.Operation(ctx, "local", planned.Operation.ID)
	if err != nil {
		t.Fatalf("read stale schema operation: %v", err)
	}
	if operation.Status != knowl.StatusFailed {
		t.Fatalf("stale schema status = %q, want failed", operation.Status)
	}
}

func TestProjectionFailureLeavesCanonicalCommitCommitted(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, _ := newWorkflow(t, false, failingIndex{})
	planned, err := service.Ingest(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	_, err = service.Apply(ctx, "local", planned.Operation.ID)
	if !errors.Is(err, app.ErrProjection) {
		t.Fatalf("projection error = %v, want projection error", err)
	}
	operation, err := store.Operation(ctx, "local", planned.Operation.ID)
	if err != nil {
		t.Fatalf("read committed operation: %v", err)
	}
	if operation.Status != knowl.StatusCommitted {
		t.Fatalf("projection failure status = %q, want committed", operation.Status)
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki", "entities", "one.md")); err != nil {
		t.Fatalf("canonical page missing after projection failure: %v", err)
	}
}

func TestAutoApplyIsExplicit(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, maintainer := newWorkflow(t, true, nil)
	_ = workspace
	_ = store
	_ = maintainer
	result, err := service.Ingest(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("auto apply ingest: %v", err)
	}
	if result.Operation.Status != knowl.StatusCommitted || result.Commit == nil {
		t.Fatalf("auto apply result = %#v, want committed result", result)
	}
}

func TestSubmitReservesWithoutPlanningAndMarksReplay(t *testing.T) {
	ctx := context.Background()
	_, _, service, maintainer := newWorkflow(t, false, nil)
	envelope := sourceEnvelope([]byte("source text"))

	first, err := service.Submit(ctx, envelope)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !first.NeedsExecution() || first.Operation.Status != knowl.StatusReceived {
		t.Fatalf("first submission = %#v, want new received operation", first.Operation)
	}
	if maintainer.calls() != 0 {
		t.Fatalf("maintainer calls after submit = %d, want 0", maintainer.calls())
	}

	second, err := service.Submit(ctx, envelope)
	if err != nil {
		t.Fatalf("replay submit: %v", err)
	}
	if second.NeedsExecution() || second.Operation.ID != first.Operation.ID {
		t.Fatalf("replayed submission = %#v, want existing operation", second.Operation)
	}
}

func TestExecuteKeepsReadDeadlineOutOfMaintainerPlan(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(workspace.Root(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	content := &deadlineContentStore{Workspace: workspace}
	maintainer := &deadlineMaintainer{}
	service, err := app.NewIngestService(content, store, store, maintainer, app.IngestOptions{
		ReadLimits: knowl.ReadLimits{Pages: 20, Bytes: 4 << 20, Characters: 32 << 10, Depth: 8, Deadline: time.Second},
	})
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}
	submission, err := service.Submit(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !content.schemaHasDeadline {
		t.Fatal("schema read did not receive the read deadline")
	}
	if _, err := service.Execute(ctx, submission); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !content.sourceHasDeadline {
		t.Fatal("source read did not receive the read deadline")
	}
	if maintainer.hasDeadline {
		t.Fatal("maintainer plan inherited the read deadline")
	}
}

const testSourceRef = "fixture:source-1@1"

var (
	planPageContent       = []byte("---\nid: entities/one\ntitle: One\ntype: entity\nsource_refs:\n  - " + testSourceRef + "\n---\n# One\n\n[[entities/two]]\n")
	planSupportingContent = []byte("---\nid: entities/two\ntitle: Two\ntype: entity\nsource_refs:\n  - " + testSourceRef + "\n---\n# Two\n")
)

type countingMaintainer struct {
	mu      sync.Mutex
	plan    knowl.ModelEditPlan
	factory func(knowl.SchemaDocument) knowl.ModelEditPlan
	counter int
}

type deadlineContentStore struct {
	*contentfs.Workspace
	schemaHasDeadline bool
	sourceHasDeadline bool
}

func (store *deadlineContentStore) Schema(ctx context.Context, scope knowl.ScopeRef) (knowl.SchemaDocument, error) {
	_, store.schemaHasDeadline = ctx.Deadline()
	return store.Workspace.Schema(ctx, scope)
}

func (store *deadlineContentStore) ReadSource(ctx context.Context, source knowl.AcceptedSource, limits knowl.ReadLimits) ([]byte, error) {
	_, store.sourceHasDeadline = ctx.Deadline()
	return store.Workspace.ReadSource(ctx, source, limits)
}

type deadlineMaintainer struct {
	hasDeadline bool
}

func (maintainer *deadlineMaintainer) Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	_, maintainer.hasDeadline = ctx.Deadline()
	return knowl.ModelEditPlan{
		SchemaDigest: input.Schema.Digest,
		SourceRefs:   []string{testSourceRef},
		Edits: []knowl.FileEdit{
			{Path: testPagePath, Content: planPageContent},
			{Path: testPageTwoPath, Content: planSupportingContent},
		},
	}, nil
}

func (maintainer *countingMaintainer) Plan(_ context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	maintainer.counter++
	if maintainer.factory != nil {
		return maintainer.factory(input.Schema), nil
	}
	return maintainer.plan, nil
}

func (maintainer *countingMaintainer) calls() int {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	return maintainer.counter
}

type failingIndex struct{}

func (failingIndex) SelectContext(context.Context, knowl.ScopeRef, knowl.SourceSummary, knowl.ReadLimits) ([]knowl.PageID, error) {
	return nil, nil
}
func (failingIndex) Search(context.Context, knowl.ScopeRef, string, knowl.ReadLimits) ([]knowl.PageReference, error) {
	return nil, nil
}
func (failingIndex) Links(context.Context, knowl.ScopeRef, knowl.PageID, knowl.ReadLimits) ([]knowl.LinkReference, error) {
	return nil, nil
}
func (failingIndex) Project(context.Context, knowl.ContentCommit) error {
	return errors.New("projection unavailable")
}
func (failingIndex) Rebuild(context.Context, knowl.WorkspaceSnapshot) error {
	return errors.New("projection unavailable")
}

func newWorkflow(t *testing.T, autoApply bool, indexOverride app.SearchIndex, factory ...func(knowl.SchemaDocument) knowl.ModelEditPlan) (*contentfs.Workspace, *sqlite.Store, *app.IngestService, *countingMaintainer) {
	t.Helper()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(workspace.Root(), "state.db"))
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	schema, err := workspace.Schema(context.Background(), "local")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	maintainer := &countingMaintainer{plan: knowl.ModelEditPlan{
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testSourceRef},
		Edits: []knowl.FileEdit{
			{Path: testPagePath, Content: planPageContent},
			{Path: "wiki/entities/two.md", Content: planSupportingContent},
		},
	}}
	if len(factory) > 0 && factory[0] != nil {
		maintainer.factory = factory[0]
	}
	if indexOverride == nil {
		indexOverride = store
	}
	service, err := app.NewIngestService(workspace, store, indexOverride, maintainer, app.IngestOptions{AutoApply: autoApply})
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}
	return workspace, store, service, maintainer
}

func sourceEnvelope(content []byte) knowl.SourceEnvelope {
	return knowl.SourceEnvelope{
		Scope:     "local",
		Source:    knowl.SourceRef{Adapter: "fixture", ID: "source-1"},
		Version:   knowl.SourceVersion{Version: "1", Digest: digest(content)},
		MediaType: "text/plain",
		Content:   content,
	}
}

func digest(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func contains(value, want string) bool {
	for index := 0; index+len(want) <= len(value); index++ {
		if value[index:index+len(want)] == want {
			return true
		}
	}
	return false
}
