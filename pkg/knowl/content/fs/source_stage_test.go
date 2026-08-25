package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const testSourceID knowl.SourceID = "engineering"
const testSourceAssetPath = "wiki/sources/engineering/asset.bin"
const testSourcePagePath = "wiki/sources/engineering/docs/page.md"

func TestWorkspaceStagesAndReloadsDeterministicSourcePlan(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	oldPath := "wiki/sources/engineering/old.md"
	oldContent := sourcePage("sources/engineering/old", "old.md", "revision-0")
	writeCanonicalFixture(t, workspace, oldPath, oldContent)
	pagePath := testSourcePagePath
	assetPath := "wiki/sources/engineering/assets/logo.bin"
	plan := knowl.SourceMutationPlan{
		RunID: "sync-1", Scope: testScope, SourceID: testSourceID,
		Mutations: []knowl.SourceMutation{
			{Action: knowl.SourceMutationDelete, Path: oldPath, ExpectedDigest: digestBytes(oldContent)},
			{Action: knowl.SourceMutationWrite, Path: assetPath, Content: []byte("asset")},
			{Action: knowl.SourceMutationWrite, Path: pagePath, Content: sourcePage("sources/engineering/docs/page", "docs/page.md", "revision-1")},
		},
	}
	first, err := workspace.StageSourcePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("StageSourcePlan() error = %v", err)
	}
	wantFiles := []string{assetPath, pagePath, oldPath}
	slices.Sort(wantFiles)
	if !slices.Equal(first.Files, wantFiles) || first.Generation == "" {
		t.Fatalf("staged source = %#v, want files %v and generation", first, wantFiles)
	}
	for _, relative := range []string{pagePath, assetPath} {
		if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canonical %q stat = %v, want absent", relative, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(oldPath))); err != nil {
		t.Fatalf("old canonical page changed during staging: %v", err)
	}
	stageDir := workspace.sourceStageDir(testScope, testSourceID, "sync-1")
	if _, err := os.Stat(filepath.Join(stageDir, filepath.FromSlash(oldPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete payload stat = %v, want absent", err)
	}
	second, err := workspace.StageSourcePlan(context.Background(), plan)
	if err != nil || second.Generation != first.Generation || !slices.Equal(second.Files, first.Files) {
		t.Fatalf("replay = %#v, %v; want generation %q", second, err, first.Generation)
	}
	reopened, err := New(workspace.Root())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.LoadSourceStage(context.Background(), testScope, testSourceID, "sync-1")
	if err != nil || loaded.Generation != first.Generation || !slices.Equal(loaded.Files, first.Files) {
		t.Fatalf("LoadSourceStage() = %#v, %v", loaded, err)
	}
}

func TestWorkspaceSourceStageRejectsConflictsAndPreconditionFailures(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *Workspace) knowl.SourceMutationPlan
		want    error
	}{
		{
			name: "existing create",
			prepare: func(t *testing.T, workspace *Workspace) knowl.SourceMutationPlan {
				path := "wiki/sources/engineering/existing.bin"
				writeCanonicalFixture(t, workspace, path, []byte("old"))
				return sourcePlan("run", testSourceID, knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: path, Content: []byte("new")})
			},
			want: ErrPrecondition,
		},
		{
			name: "missing delete",
			prepare: func(_ *testing.T, _ *Workspace) knowl.SourceMutationPlan {
				return sourcePlan("run", testSourceID, knowl.SourceMutation{Action: knowl.SourceMutationDelete, Path: "wiki/sources/engineering/missing.bin", ExpectedDigest: digestBytes([]byte("old"))})
			},
			want: ErrPrecondition,
		},
		{
			name: "wrong digest",
			prepare: func(t *testing.T, workspace *Workspace) knowl.SourceMutationPlan {
				path := "wiki/sources/engineering/existing.bin"
				writeCanonicalFixture(t, workspace, path, []byte("old"))
				return sourcePlan("run", testSourceID, knowl.SourceMutation{Action: knowl.SourceMutationDelete, Path: path, ExpectedDigest: digestBytes([]byte("other"))})
			},
			want: ErrPrecondition,
		},
		{
			name: "foreign namespace",
			prepare: func(_ *testing.T, _ *Workspace) knowl.SourceMutationPlan {
				return sourcePlan("run", testSourceID, knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: "wiki/sources/operations/page.bin", Content: []byte("new")})
			},
			want: app.ErrSourceMutationInvalid,
		},
		{
			name: "missing page provenance",
			prepare: func(_ *testing.T, _ *Workspace) knowl.SourceMutationPlan {
				return sourcePlan("run", testSourceID, knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: "wiki/sources/engineering/page.md", Content: validWorkspacePage("sources/engineering/page", "Page", testWorkspaceSourceRef, "")})
			},
			want: ErrContentInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := newSourceStageWorkspace(t)
			_, err := workspace.StageSourcePlan(context.Background(), test.prepare(t, workspace))
			if !errors.Is(err, test.want) {
				t.Fatalf("StageSourcePlan() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestWorkspaceSourceStageRejectsIdentityReuseAndTampering(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	path := testSourceAssetPath
	plan := sourcePlan("sync-conflict", testSourceID, knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: path, Content: []byte("one")})
	if _, err := workspace.StageSourcePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	changed := plan
	changed.Mutations = []knowl.SourceMutation{{Action: knowl.SourceMutationWrite, Path: path, Content: []byte("two")}}
	if _, err := workspace.StageSourcePlan(context.Background(), changed); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("changed replay error = %v, want plan conflict", err)
	}
	foreign := sourcePlan("sync-conflict", "operations", knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: "wiki/sources/operations/asset.bin", Content: []byte("one")})
	if _, err := workspace.StageSourcePlan(context.Background(), foreign); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("owner reuse error = %v, want plan conflict", err)
	}
	stagePath := filepath.Join(workspace.sourceStageDir(testScope, testSourceID, "sync-conflict"), filepath.FromSlash(path))
	if err := os.WriteFile(stagePath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.LoadSourceStage(context.Background(), testScope, testSourceID, "sync-conflict"); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("tampered load error = %v, want plan conflict", err)
	}
}

func TestWorkspaceSourceStageRejectsSymlinkTarget(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	prefix := filepath.Join(workspace.Root(), "wiki", "sources", "engineering")
	if err := os.MkdirAll(prefix, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(prefix, "link")); err != nil {
		t.Fatal(err)
	}
	plan := sourcePlan("sync-link", testSourceID, knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: "wiki/sources/engineering/link/asset.bin", Content: []byte("asset")})
	if _, err := workspace.StageSourcePlan(context.Background(), plan); !errors.Is(err, ErrPathRejected) {
		t.Fatalf("symlink target error = %v, want path rejection", err)
	} else if strings.Contains(err.Error(), workspace.Root()) {
		t.Fatalf("symlink target error discloses workspace root: %v", err)
	}
}

func TestWorkspaceCommitsMixedSourceMutationsWithoutMaintainerLog(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	deletePath := "wiki/sources/engineering/delete.bin"
	updatePath := "wiki/sources/engineering/update.bin"
	pagePath := testSourcePagePath
	writeCanonicalFixture(t, workspace, deletePath, []byte("delete me"))
	writeCanonicalFixture(t, workspace, updatePath, []byte("old asset"))
	logBefore, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatal(err)
	}
	plan := sourcePlan("sync-commit", testSourceID,
		knowl.SourceMutation{Action: knowl.SourceMutationDelete, Path: deletePath, ExpectedDigest: digestBytes([]byte("delete me"))},
		knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: updatePath, ExpectedDigest: digestBytes([]byte("old asset")), Content: []byte("new asset")},
		knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: pagePath, Content: sourcePage("sources/engineering/docs/page", "docs/page.md", "revision-1")},
	)
	staged, err := workspace.StageSourcePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := workspace.CommitSource(context.Background(), staged)
	if err != nil {
		t.Fatalf("CommitSource() error = %v", err)
	}
	if commit.OperationID != string(plan.RunID) || commit.Generation != staged.Generation || !slices.Equal(commit.Files, staged.Files) {
		t.Fatalf("commit = %#v, staged = %#v", commit, staged)
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(deletePath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted target stat = %v, want absent", err)
	}
	updated, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(updatePath)))
	if err != nil || string(updated) != "new asset" {
		t.Fatalf("updated asset = %q, %v", updated, err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(pagePath))); err != nil {
		t.Fatalf("new page stat: %v", err)
	}
	logAfter, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil || string(logAfter) != string(logBefore) {
		t.Fatalf("source commit changed maintainer log: %v, %q != %q", err, logAfter, logBefore)
	}
	reopened, err := New(workspace.Root())
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := reopened.CommitSource(context.Background(), staged)
	if err != nil || replayed.Generation != commit.Generation || !slices.Equal(replayed.Files, commit.Files) {
		t.Fatalf("replayed CommitSource() = %#v, %v", replayed, err)
	}
}

func TestWorkspaceSourceCommitPreflightsAllTargetsAndRejectsFalseReplay(t *testing.T) {
	t.Run("stale delete preserves every canonical target", func(t *testing.T) {
		workspace := newSourceStageWorkspace(t)
		deletePath := "wiki/sources/engineering/delete.bin"
		createPath := "wiki/sources/engineering/create.bin"
		writeCanonicalFixture(t, workspace, deletePath, []byte("old"))
		plan := sourcePlan("sync-stale", testSourceID,
			knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: createPath, Content: []byte("created")},
			knowl.SourceMutation{Action: knowl.SourceMutationDelete, Path: deletePath, ExpectedDigest: digestBytes([]byte("old"))},
		)
		staged, err := workspace.StageSourcePlan(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		writeCanonicalFixture(t, workspace, deletePath, []byte("replacement"))
		if _, err := workspace.CommitSource(context.Background(), staged); !errors.Is(err, ErrPrecondition) {
			t.Fatalf("CommitSource() error = %v, want precondition", err)
		}
		if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(createPath))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("create target stat = %v, want absent", err)
		}
		content, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(deletePath)))
		if err != nil || string(content) != "replacement" {
			t.Fatalf("replacement = %q, %v", content, err)
		}
	})

	t.Run("matching bytes without receipt are not replay", func(t *testing.T) {
		workspace := newSourceStageWorkspace(t)
		path := testSourceAssetPath
		plan := sourcePlan("sync-false-replay", testSourceID, knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: path, Content: []byte("desired")})
		staged, err := workspace.StageSourcePlan(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		writeCanonicalFixture(t, workspace, path, []byte("desired"))
		if _, err := workspace.CommitSource(context.Background(), staged); !errors.Is(err, ErrPrecondition) {
			t.Fatalf("CommitSource() error = %v, want precondition", err)
		}
	})
}

func TestWorkspaceSourceCommitRejectsDivergenceAfterCommittedReplay(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	path := testSourceAssetPath
	staged, err := workspace.StageSourcePlan(context.Background(), sourcePlan("sync-diverged", testSourceID, knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: path, Content: []byte("committed")}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.CommitSource(context.Background(), staged); err != nil {
		t.Fatal(err)
	}
	writeCanonicalFixture(t, workspace, path, []byte("replacement"))
	if _, err := workspace.CommitSource(context.Background(), staged); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("diverged replay error = %v, want plan conflict", err)
	}
}

func newSourceStageWorkspace(t *testing.T) *Workspace {
	t.Helper()
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	acceptWorkspaceSource(t, workspace)
	return workspace
}

func sourcePlan(runID knowl.SyncRunID, sourceID knowl.SourceID, mutations ...knowl.SourceMutation) knowl.SourceMutationPlan {
	return knowl.SourceMutationPlan{RunID: runID, Scope: testScope, SourceID: sourceID, Mutations: mutations}
}

func sourcePage(id, documentID, revision string) []byte {
	return []byte("---\nid: " + id + "\ntitle: Source page\ntype: source\nsource_refs:\n  - " + testWorkspaceSourceRef + "\nsource_document:\n  source_id: engineering\n  document_id: " + documentID + "\n  revision: " + revision + "\n  uri: https://wiki.example.test/" + documentID + "\n---\n# Source page\n")
}

func writeCanonicalFixture(t *testing.T, workspace *Workspace, relative string, content []byte) {
	t.Helper()
	target := filepath.Join(workspace.Root(), filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
