package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	knowlruntime "github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestHierarchyReconcileCommandBuildsValidHierarchyAndReplaysAsNoOp(t *testing.T) {
	ctx := context.Background()
	fixture := newCommandWorkflowFixture(t, true)
	secondPagePath := "wiki/entities/two.md"
	fixture.maintainer.Result.Edits = []knowl.FileEdit{
		{Path: smokePagePath, Content: []byte("---\nid: entities/one\ntitle: Architecture\ntype: Architecture\nsource_refs:\n  - " + smokeSourceRef + "\n---\n# Architecture\n\nSystem components.\n")},
		{Path: secondPagePath, Content: []byte("---\nid: entities/two\ntitle: Roadmap\ntype: Product\nsource_refs:\n  - " + smokeSourceRef + "\n---\n# Roadmap\n\nProduct milestones.\n")},
	}
	withLocalWorkflowSessionFactory(t, fixture.newSessionFactory(t))
	inputPath := writeJSONFixture(t, knowlapi.IngestRequest{
		Content: pointerTo(smokeSourceText), Origin: pointerTo(smokeSourceID), IdempotencyKey: pointerTo(smokeSourceVersion),
	})
	if _, stderr, err := executeCLICommand(newIngestCommand(), []string{workflowInputFlagUsage, inputPath}, nil); err != nil {
		t.Fatalf("seed flat hierarchy: %v, stderr=%s", err, stderr)
	}

	workspace, err := contentfs.New(fixture.config.Workspace)
	if err != nil {
		t.Fatalf("open hierarchy workspace: %v", err)
	}
	setHierarchyFixturePlan(t, workspace, &fixture, smokePagePath, secondPagePath)
	protected := make(map[string][]byte)
	for _, path := range []string{smokePagePath, secondPagePath} {
		protected[path], err = os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read protected page %q: %v", path, err)
		}
	}
	original := newLocalHierarchySession
	fixture.config.Sources = []knowl.Source{{
		ID: "configured-on-start", Type: knowl.SourceTypeFilesystem, Enabled: true,
		Config: knowl.SourceConfig{Filesystem: &knowl.FilesystemSourceConfig{Root: t.TempDir(), Flavor: knowl.SourceFlavorMarkdown}},
		Sync:   knowl.SourceSyncPolicy{OnStart: true},
	}}
	observer := &hierarchySourceObserver{}
	newLocalHierarchySession = func(ctx context.Context) (localHierarchySession, error) {
		host, hostErr := knowlruntime.New(ctx, knowlruntime.Options{
			Config: fixture.config, Maintainer: fixture.maintainer, SourceObserver: observer,
		})
		return localHierarchySession{Host: host, ShutdownTimeout: fixture.config.ShutdownTimeout}, hostErr
	}
	t.Cleanup(func() { newLocalHierarchySession = original })

	stdout, stderr, err := executeCLICommand(newHierarchyCommand(), []string{hierarchyReconcileCommandName}, nil)
	if err != nil {
		t.Fatalf("reconcile hierarchy: %v, stderr=%s", err, stderr)
	}
	var first hierarchyReconcileResult
	if err := json.Unmarshal([]byte(stdout), &first); err != nil {
		t.Fatalf("decode hierarchy result: %v", err)
	}
	if first.Status != knowl.StatusCommitted || !first.Changed || first.Generation == "" || len(first.Files) < 4 {
		t.Fatalf("hierarchy result = %#v", first)
	}
	if err := workspace.Validate(); err != nil {
		t.Fatalf("validate reconciled hierarchy: %v", err)
	}
	root, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "index.md"))
	if err != nil {
		t.Fatalf("read hierarchy root: %v", err)
	}
	if !strings.Contains(string(root), "/catalogs/architecture/index.md") || !strings.Contains(string(root), "/catalogs/product/index.md") ||
		strings.Contains(string(root), "/entities/one.md") || strings.Contains(string(root), "/entities/two.md") {
		t.Fatalf("root is not nested semantic catalogs:\n%s", root)
	}
	for path, before := range protected {
		after, readErr := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(path)))
		if readErr != nil || string(after) != string(before) {
			t.Fatalf("protected page %q changed: %v", path, readErr)
		}
	}

	setHierarchyFixturePlan(t, workspace, &fixture, smokePagePath, secondPagePath)
	digestBefore, err := workspace.HierarchySnapshotDigest(ctx, fixture.config.Scope)
	if err != nil {
		t.Fatalf("read hierarchy digest before replay: %v", err)
	}
	stdout, stderr, err = executeCLICommand(newHierarchyCommand(), []string{hierarchyReconcileCommandName}, nil)
	if err != nil {
		t.Fatalf("replay hierarchy: %v, stderr=%s", err, stderr)
	}
	var replay hierarchyReconcileResult
	if err := json.Unmarshal([]byte(stdout), &replay); err != nil {
		t.Fatalf("decode hierarchy replay: %v", err)
	}
	if replay.Status != knowl.StatusCommitted || replay.Changed || replay.Generation != "" || len(replay.Files) != 0 {
		t.Fatalf("hierarchy replay = %#v, want committed no-op", replay)
	}
	digestAfter, err := workspace.HierarchySnapshotDigest(ctx, fixture.config.Scope)
	if err != nil || digestAfter != digestBefore {
		t.Fatalf("hierarchy replay digest = %q, %v, want %q", digestAfter, err, digestBefore)
	}
	if observer.count() != 0 {
		t.Fatalf("hierarchy command started %d source synchronization attempts", observer.count())
	}
}

func TestHierarchyReconcileCommandPreservesOperationAndStopErrors(t *testing.T) {
	original := newLocalHierarchySession
	t.Cleanup(func() { newLocalHierarchySession = original })
	operationErr := app.ErrMaintainerUnavailable
	stopErr := errors.New("stop unavailable")
	host := &stubHierarchyHost{operationErr: operationErr, stopErr: stopErr}
	newLocalHierarchySession = func(context.Context) (localHierarchySession, error) {
		return localHierarchySession{Host: host, ShutdownTimeout: time.Second}, nil
	}
	command := newHierarchyReconcileCommand()
	err := command.Execute()
	if !errors.Is(err, operationErr) || !errors.Is(err, stopErr) {
		t.Fatalf("command error = %v, want operation and stop errors", err)
	}
	if host.calls != 1 || host.stopCalls != 1 {
		t.Fatalf("host calls = reconcile %d, stop %d", host.calls, host.stopCalls)
	}
}

type stubHierarchyHost struct {
	result       app.IngestResult
	operationErr error
	stopErr      error
	calls        int
	stopCalls    int
}

type hierarchySourceObserver struct {
	mu       sync.Mutex
	attempts int
}

func (observer *hierarchySourceObserver) ObserveSourceAttempt(knowlruntime.SourceAttempt) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.attempts++
}

func (observer *hierarchySourceObserver) count() int {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.attempts
}

func (host *stubHierarchyHost) ReconcileHierarchy(context.Context) (app.IngestResult, error) {
	host.calls++
	return host.result, host.operationErr
}

func (host *stubHierarchyHost) Stop(context.Context) error {
	host.stopCalls++
	return host.stopErr
}

func setHierarchyFixturePlan(t *testing.T, workspace *contentfs.Workspace, fixture *commandWorkflowFixture, firstPage, secondPage string) {
	t.Helper()
	digest, err := workspace.HierarchySnapshotDigest(context.Background(), fixture.config.Scope)
	if err != nil {
		t.Fatalf("read hierarchy fixture digest: %v", err)
	}
	fixture.maintainer.HierarchyResult = knowl.HierarchyModelPlan{
		SchemaDigest: fixture.schema.Digest, SnapshotDigest: digest,
		Catalogs: []knowl.HierarchyCatalogSpec{
			{Path: "wiki/index.md", Title: "Knowl", Children: []string{"wiki/catalogs/architecture/index.md", "wiki/catalogs/product/index.md"}},
			{Path: "wiki/catalogs/architecture/index.md", Title: "Architecture", Children: []string{firstPage}},
			{Path: "wiki/catalogs/product/index.md", Title: "Product", Children: []string{secondPage}},
		},
	}
}
