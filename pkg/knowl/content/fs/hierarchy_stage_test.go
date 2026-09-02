package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testHierarchyOperation = "hierarchy-operation"
	testHierarchyOldPath   = "wiki/catalogs/old/index.md"
	testHierarchyArchPath  = "wiki/catalogs/architecture/index.md"
	testHierarchyProdPath  = "wiki/catalogs/product/index.md"
	testHierarchyPageTwo   = "wiki/entities/two.md"
)

func TestWorkspaceHierarchySnapshotDigestBindsCanonicalAndRawFiles(t *testing.T) {
	workspace := newHierarchyWorkspace(t)
	first, err := workspace.HierarchySnapshotDigest(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspace.HierarchySnapshotDigest(context.Background(), testScope)
	if err != nil || second != first {
		t.Fatalf("stable snapshot = %q, %v; want %q", second, err, first)
	}
	writeCanonicalFixture(t, workspace, testPageOnePath, validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, "changed"))
	changed, err := workspace.HierarchySnapshotDigest(context.Background(), testScope)
	if err != nil || changed == first {
		t.Fatalf("changed snapshot = %q, %v; want digest change", changed, err)
	}
}

func TestWorkspaceStagesLoadsCommitsAndReplaysHierarchyPlan(t *testing.T) {
	workspace := newHierarchyWorkspace(t)
	pageBefore := mustReadHierarchyFile(t, workspace, testPageOnePath)
	rawBefore := hierarchyRawState(t, workspace)
	plan := hierarchyStagePlan(t, workspace)

	staged, err := workspace.StageHierarchyPlan(context.Background(), testHierarchyOperation, plan)
	if err != nil {
		t.Fatalf("StageHierarchyPlan() error: %v", err)
	}
	reopened, err := New(workspace.Root())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.LoadHierarchyStage(context.Background(), testScope, testHierarchyOperation)
	if err != nil {
		t.Fatalf("LoadHierarchyStage() error: %v", err)
	}
	if loaded.Digest != staged.Digest || !slices.Equal(loaded.Files, staged.Files) {
		t.Fatalf("loaded hierarchy stage = %#v, want %#v", loaded, staged)
	}
	commit, err := reopened.CommitHierarchy(context.Background(), loaded)
	if err != nil {
		t.Fatalf("CommitHierarchy() error: %v", err)
	}
	wantFiles := []string{testHierarchyArchPath, testHierarchyOldPath, testHierarchyProdPath, testIndexPath, canonicalLogPath}
	if commit.Generation != staged.Digest || !slices.Equal(commit.Files, wantFiles) {
		t.Fatalf("hierarchy commit = %#v, want files %#v", commit, wantFiles)
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(testHierarchyOldPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete managed catalog stat = %v, want absent", err)
	}
	if got := mustReadHierarchyFile(t, workspace, testPageOnePath); !slices.Equal(got, pageBefore) {
		t.Fatal("hierarchy commit changed an ordinary page")
	}
	if got := hierarchyRawState(t, workspace); got != rawBefore {
		t.Fatalf("hierarchy commit changed raw state: %q != %q", got, rawBefore)
	}
	if err := reopened.Validate(); err != nil {
		t.Fatalf("committed workspace is invalid: %v", err)
	}
	replayed, err := reopened.CommitHierarchy(context.Background(), loaded)
	if err != nil || replayed.Generation != commit.Generation || !slices.Equal(replayed.Files, commit.Files) {
		t.Fatalf("CommitHierarchy() replay = %#v, %v", replayed, err)
	}
}

func TestWorkspaceHierarchyStageRejectsUnsafeOrInvalidPlans(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Workspace, *knowl.ValidatedHierarchyPlan)
		want   error
	}{
		{name: "stale snapshot", mutate: func(_ *testing.T, _ *Workspace, plan *knowl.ValidatedHierarchyPlan) {
			plan.SnapshotDigest = strings.Repeat("f", 64)
		}, want: ErrPrecondition},
		{name: "stale target digest", mutate: func(_ *testing.T, _ *Workspace, plan *knowl.ValidatedHierarchyPlan) {
			plan.Mutations[0].ExpectedDigest = strings.Repeat("f", 64)
		}, want: ErrPrecondition},
		{name: "ordinary page target", mutate: func(_ *testing.T, _ *Workspace, plan *knowl.ValidatedHierarchyPlan) {
			plan.Mutations[0].Path = testPageOnePath
		}, want: ErrPathRejected},
		{name: "source mirror target", mutate: func(_ *testing.T, _ *Workspace, plan *knowl.ValidatedHierarchyPlan) {
			plan.Mutations[0].Path = "wiki/sources/source-a/index.md"
		}, want: ErrPathRejected},
		{name: "root delete", mutate: func(_ *testing.T, workspace *Workspace, plan *knowl.ValidatedHierarchyPlan) {
			root := mustReadHierarchyFile(t, workspace, testIndexPath)
			plan.Mutations = []knowl.HierarchyMutation{{Action: knowl.SourceMutationDelete, Path: testIndexPath, ExpectedDigest: digestBytes(root)}}
		}, want: ErrPlanConflict},
		{name: "broken graph", mutate: func(_ *testing.T, _ *Workspace, plan *knowl.ValidatedHierarchyPlan) {
			plan.Mutations[0].Content = []byte("---\nokf_version: \"0.2\"\n---\n# Broken\n* [Missing](catalogs/missing/index.md)\n")
		}, want: ErrContentInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := newHierarchyWorkspace(t)
			plan := hierarchyStagePlan(t, workspace)
			test.mutate(t, workspace, &plan)
			_, err := workspace.StageHierarchyPlan(context.Background(), testHierarchyOperation, plan)
			if !errors.Is(err, test.want) {
				t.Fatalf("StageHierarchyPlan() error = %v, want %v", err, test.want)
			}
		})
	}

	t.Run("symlink target", func(t *testing.T) {
		workspace := newHierarchyWorkspace(t)
		plan := hierarchyStagePlan(t, workspace)
		catalogs := filepath.Join(workspace.Root(), "wiki", "catalogs")
		if err := os.RemoveAll(catalogs); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), catalogs); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.StageHierarchyPlan(context.Background(), testHierarchyOperation, plan); !errors.Is(err, ErrPathRejected) && !errors.Is(err, ErrPrecondition) {
			t.Fatalf("StageHierarchyPlan() symlink error = %v", err)
		}
	})
}

func TestWorkspaceLoadHierarchyStageRejectsOversizedManifest(t *testing.T) {
	workspace := newHierarchyWorkspace(t)
	plan := hierarchyStagePlan(t, workspace)
	if _, err := workspace.StageHierarchyPlan(context.Background(), testHierarchyOperation, plan); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(workspace.Root(), knowlDir, "staging", token(testHierarchyOperation), "manifest.yaml")
	if err := os.WriteFile(manifestPath, make([]byte, maxStageManifestBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.LoadHierarchyStage(context.Background(), testScope, testHierarchyOperation); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("LoadHierarchyStage() error = %v, want plan conflict", err)
	}
}

func TestWorkspaceHierarchyCommitRejectsCanonicalPreservationDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Workspace)
	}{
		{name: "ordinary page", mutate: func(t *testing.T, workspace *Workspace) {
			writeCanonicalFixture(t, workspace, testPageOnePath, validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, "drift"))
		}},
		{name: "raw source", mutate: func(t *testing.T, workspace *Workspace) {
			records, err := workspace.inspectRawSources(context.Background(), testScope)
			if err != nil || len(records) == 0 {
				t.Fatalf("inspect raw sources = %#v, %v", records, err)
			}
			sourcePath := filepath.Join(workspace.Root(), filepath.Dir(filepath.FromSlash(records[0].Path)), "source")
			if err := os.WriteFile(sourcePath, []byte("raw drift"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := newHierarchyWorkspace(t)
			plan := hierarchyStagePlan(t, workspace)
			staged, err := workspace.StageHierarchyPlan(context.Background(), testHierarchyOperation, plan)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, workspace)
			if _, err := workspace.CommitHierarchy(context.Background(), staged); !errors.Is(err, ErrPrecondition) {
				t.Fatalf("CommitHierarchy() error = %v, want precondition", err)
			}
			if got := mustReadHierarchyFile(t, workspace, testHierarchyOldPath); len(got) == 0 {
				t.Fatal("failed hierarchy commit deleted the existing catalog")
			}
		})
	}
}

func TestWorkspaceRecoversHierarchyCommitAtEveryFaultPoint(t *testing.T) {
	tests := []struct {
		name       string
		point      string
		index      int
		wantAfter  bool
		wantAction string
	}{
		{name: "before journal", point: "before_journal", index: -1},
		{name: "after preimages", point: "after_preimages", index: -1},
		{name: recoveryPrepared, point: recoveryPrepared, index: -1, wantAction: recoveryRolledBack},
		{name: "after root", point: commitFaultApplied, index: 0, wantAction: recoveryRolledBack},
		{name: "after create", point: commitFaultApplied, index: 1, wantAction: recoveryRolledBack},
		{name: "after delete", point: commitFaultApplied, index: 2, wantAction: recoveryRolledBack},
		{name: "after product", point: commitFaultApplied, index: 3, wantAction: recoveryRolledBack},
		{name: "after log", point: commitFaultApplied, index: 4, wantAction: recoveryRolledBack},
		{name: testDurablyCommitted, point: recoveryCommitted, index: -1, wantAfter: true, wantAction: recoveryCompleted},
		{name: commitFaultReceipt, point: commitFaultReceipt, index: -1, wantAfter: true, wantAction: recoveryCompleted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := newHierarchyWorkspace(t)
			beforeRoot := mustReadHierarchyFile(t, workspace, testIndexPath)
			plan := hierarchyStagePlan(t, workspace)
			staged, err := workspace.StageHierarchyPlan(context.Background(), testHierarchyOperation, plan)
			if err != nil {
				t.Fatal(err)
			}
			workspace.commitFault = func(point string, index int) error {
				if point == test.point && index == test.index {
					return errInjectedCommitFault
				}
				return nil
			}
			if _, err := workspace.CommitHierarchy(context.Background(), staged); !errors.Is(err, errInjectedCommitFault) {
				t.Fatalf("CommitHierarchy() error = %v, want injected fault", err)
			}
			reopened, err := New(workspace.Root())
			if err != nil {
				t.Fatal(err)
			}
			results, err := reopened.Recover(context.Background())
			if err != nil {
				t.Fatalf("Recover() error = %v", err)
			}
			if test.wantAction != "" && (len(results) != 1 || results[0].Action != test.wantAction || results[0].OperationID != testHierarchyOperation) {
				t.Fatalf("recovery results = %#v, want %q", results, test.wantAction)
			}
			root := mustReadHierarchyFile(t, reopened, testIndexPath)
			if test.wantAfter == slices.Equal(root, beforeRoot) {
				t.Fatalf("root after recovery matches prior=%v, want after=%v", slices.Equal(root, beforeRoot), test.wantAfter)
			}
			_, oldErr := os.Stat(filepath.Join(reopened.Root(), filepath.FromSlash(testHierarchyOldPath)))
			if test.wantAfter && !errors.Is(oldErr, os.ErrNotExist) {
				t.Fatalf("old catalog after completed recovery stat = %v", oldErr)
			}
			if !test.wantAfter && oldErr != nil {
				t.Fatalf("old catalog after rollback stat = %v", oldErr)
			}
			if test.wantAfter {
				if _, err := reopened.CommitHierarchy(context.Background(), staged); err != nil {
					t.Fatalf("replay after committed recovery: %v", err)
				}
			}
		})
	}
}

func newHierarchyWorkspace(t *testing.T) *Workspace {
	t.Helper()
	workspace := newSourceStageWorkspace(t)
	writeCanonicalFixture(t, workspace, testPageOnePath, validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, ""))
	writeCanonicalFixture(t, workspace, testHierarchyPageTwo, validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, ""))
	writeRootCatalogTargets(t, workspace, "entities/one.md", "entities/two.md")
	writeCanonicalFixture(t, workspace, testHierarchyOldPath, []byte("# Old\n\n* [One](../../entities/one.md)\n"))
	return workspace
}

func hierarchyStagePlan(t *testing.T, workspace *Workspace) knowl.ValidatedHierarchyPlan {
	t.Helper()
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	snapshotDigest, err := workspace.HierarchySnapshotDigest(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	rootBefore := mustReadHierarchyFile(t, workspace, testIndexPath)
	oldBefore := mustReadHierarchyFile(t, workspace, testHierarchyOldPath)
	return knowl.ValidatedHierarchyPlan{
		Scope: testScope, SchemaDigest: schema.Digest, SnapshotDigest: snapshotDigest,
		Mutations: []knowl.HierarchyMutation{
			{Action: knowl.SourceMutationWrite, Path: testIndexPath, ExpectedDigest: digestBytes(rootBefore), Content: []byte("---\nokf_version: \"0.2\"\n---\n# Knowl\n\n* [Architecture](catalogs/architecture/index.md)\n* [Product](catalogs/product/index.md)\n")},
			{Action: knowl.SourceMutationWrite, Path: testHierarchyArchPath, Content: []byte("# Architecture\n\n* [One](../../entities/one.md)\n")},
			{Action: knowl.SourceMutationDelete, Path: testHierarchyOldPath, ExpectedDigest: digestBytes(oldBefore)},
			{Action: knowl.SourceMutationWrite, Path: testHierarchyProdPath, Content: []byte("# Product\n\n* [Two](../../entities/two.md)\n")},
		},
	}
}

func mustReadHierarchyFile(t *testing.T, workspace *Workspace, relative string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read %q: %v", relative, err)
	}
	return content
}

func hierarchyRawState(t *testing.T, workspace *Workspace) string {
	t.Helper()
	root := filepath.Join(workspace.Root(), workspaceRawDir)
	var state strings.Builder
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(workspace.Root(), path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		state.WriteString(filepath.ToSlash(relative))
		state.WriteByte(0)
		state.WriteString(digestBytes(content))
		state.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state.String()
}
