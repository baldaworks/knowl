package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

const testScope = "local"
const (
	testFixtureAdapter     = "fixture"
	testWorkspaceSourceRef = "fixture:source-1@1"
	testPageOnePath        = "wiki/entities/one.md"
	testIndexPath          = "wiki/index.md"
)

func TestWorkspaceInitAcceptsImmutableSourceAndReplaysIt(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	content := []byte("source content")
	digest := sha256.Sum256(content)
	envelope := knowl.SourceEnvelope{
		Scope:     testScope,
		Source:    knowl.SourceRef{Adapter: testFixtureAdapter, ID: "source-1"},
		Version:   knowl.SourceVersion{Version: "1", Digest: hex.EncodeToString(digest[:])},
		MediaType: "text/plain",
		Content:   content,
	}
	first, err := workspace.AcceptSource(context.Background(), envelope)
	if err != nil {
		t.Fatalf("accept source: %v", err)
	}
	second, err := workspace.AcceptSource(context.Background(), envelope)
	if err != nil {
		t.Fatalf("replay source: %v", err)
	}
	if first != second {
		t.Fatalf("replay result changed: %#v != %#v", first, second)
	}
	conflict := envelope
	conflict.Content = []byte("different")
	conflictDigest := sha256.Sum256(conflict.Content)
	conflict.Version.Digest = hex.EncodeToString(conflictDigest[:])
	if _, err := workspace.AcceptSource(context.Background(), conflict); !errors.Is(err, ErrSourceConflict) {
		t.Fatalf("conflicting content error = %v, want source conflict", err)
	}
}

func TestWorkspaceReadsAcceptedSourceWithBoundedDigest(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	content := []byte("bounded source")
	digestValue := sha256.Sum256(content)
	accepted, err := workspace.AcceptSource(context.Background(), knowl.SourceEnvelope{
		Scope:   testScope,
		Source:  knowl.SourceRef{Adapter: "fixture", ID: "read-source"},
		Version: knowl.SourceVersion{Version: "1", Digest: hex.EncodeToString(digestValue[:])},
		Content: content,
	})
	if err != nil {
		t.Fatalf("accept source: %v", err)
	}
	read, err := workspace.ReadSource(context.Background(), accepted, knowl.ReadLimits{Bytes: len(content)})
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if string(read) != string(content) {
		t.Fatalf("read source = %q, want %q", read, content)
	}
	if _, err := workspace.ReadSource(context.Background(), accepted, knowl.ReadLimits{Bytes: len(content) - 1}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("bounded source error = %v, want invalid source", err)
	}
}

func TestWorkspaceRejectsUnsafePlanPath(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	_, err = workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID: "op-1",
		Edits:       []knowl.FileEdit{{Path: "wiki/../schema.md", Content: []byte("no")}},
	})
	if !errors.Is(err, ErrPathRejected) {
		t.Fatalf("unsafe plan error = %v, want path rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(workspace.Root(), "schema.md")); statErr != nil {
		t.Fatalf("schema disappeared after rejected plan: %v", statErr)
	}
}

func TestWorkspaceSnapshotIncludesMarkdownDigests(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	pagePath := filepath.Join(workspace.Root(), "wiki", "entities", "one.md")
	if err := os.WriteFile(pagePath, []byte("# One\n"), 0o600); err != nil {
		t.Fatalf("write page: %v", err)
	}
	snapshot, err := workspace.Snapshot(context.Background(), testScope)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.PageDigests[testPageOnePath] == "" {
		t.Fatalf("snapshot missing page digest: %#v", snapshot.PageDigests)
	}
	if len(snapshot.Pages) != 1 || snapshot.Pages[0].ID != "entities/one" {
		t.Fatalf("snapshot pages = %#v", snapshot.Pages)
	}
}

func TestWorkspaceReadsAndSnapshotsSourceDocumentProvenance(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	relative := "wiki/sources/engineering/auth.md"
	content := []byte("---\nid: sources/engineering/auth\ntitle: Auth\ntype: source\nsource_refs:\n  - raw:auth@1\nsource_document:\n  source_id: engineering\n  document_id: architecture/auth.md\n  revision: revision-1\n  uri: https://wiki.example.test/auth\n---\n# Auth\n")
	target := filepath.Join(workspace.Root(), filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	pages, err := workspace.ReadPages(context.Background(), testScope, []knowl.PageID{"sources/engineering/auth"}, knowl.ReadLimits{Pages: 1, Bytes: len(content)})
	if err != nil || len(pages) != 1 || pages[0].SourceDocument == nil || pages[0].SourceDocument.DocumentID != "architecture/auth.md" {
		t.Fatalf("ReadPages() = %#v, %v", pages, err)
	}
	snapshot, err := workspace.Snapshot(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pages) != 1 || snapshot.Pages[0].SourceDocument == nil || snapshot.Pages[0].SourceDocument.SourceID != "engineering" {
		t.Fatalf("Snapshot() pages = %#v", snapshot.Pages)
	}
}

func TestWorkspaceRecoveryRollsBackPreparedGenerationAndCommitReplays(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	acceptWorkspaceSource(t, workspace)
	plan := knowl.ValidatedEditPlan{
		OperationID:  "recovery-operation",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits:        []knowl.FileEdit{{Path: "wiki/entities/recovered.md", Content: validWorkspacePage("entities/recovered", "Recovered", testWorkspaceSourceRef, "")}},
	}
	staged, err := workspace.StagePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("stage plan: %v", err)
	}
	logPath := filepath.Join(workspace.Root(), "wiki", "log.md")
	originalLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read original log: %v", err)
	}
	recoveryDir := filepath.Join(workspace.Root(), knowlDir, "recovery", token(plan.OperationID))
	if err := os.MkdirAll(recoveryDir, 0o700); err != nil {
		t.Fatalf("create recovery fixture: %v", err)
	}
	logBackup := filepath.Join(recoveryDir, "log.old")
	if err := writeAtomic(logBackup, originalLog, 0o600); err != nil {
		t.Fatalf("write log preimage: %v", err)
	}
	pagePath := filepath.Join(workspace.Root(), "wiki", "entities", "recovered.md")
	if err := os.WriteFile(pagePath, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write partial page: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("partial log"), 0o600); err != nil {
		t.Fatalf("write partial log: %v", err)
	}
	journaling := recoveryJournal{
		OperationID: plan.OperationID,
		State:       recoveryPrepared,
		Entries: []recoveryEntry{
			{Target: "wiki/entities/recovered.md", HadOld: false},
			{Target: canonicalLogPath, Backup: logBackup, HadOld: true},
		},
	}
	journalPath := filepath.Join(workspace.Root(), knowlDir, "recovery", token(plan.OperationID)+".yaml")
	if err := writeJournal(journalPath, journaling); err != nil {
		t.Fatalf("write recovery journal: %v", err)
	}
	results, err := workspace.Recover(context.Background())
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(results) != 1 || results[0].Action != recoveryRolledBack {
		t.Fatalf("recovery results = %#v", results)
	}
	if _, err := os.Stat(pagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered page stat = %v, want absent", err)
	}
	restoredLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read restored log: %v", err)
	}
	if string(restoredLog) != string(originalLog) {
		t.Fatalf("restored log = %q, want %q", restoredLog, originalLog)
	}
	firstCommit, err := workspace.Commit(context.Background(), staged)
	if err != nil {
		t.Fatalf("commit after recovery: %v", err)
	}
	secondCommit, err := workspace.Commit(context.Background(), staged)
	if err != nil {
		t.Fatalf("replay commit: %v", err)
	}
	if firstCommit.Generation != secondCommit.Generation || len(secondCommit.Files) != 2 {
		t.Fatalf("replayed commits differ: %#v != %#v", firstCommit, secondCommit)
	}
}

func TestWorkspaceStagePlanRejectsInvalidProspectiveContentWithoutCanonicalMutation(t *testing.T) {
	tests := []struct {
		name  string
		edits []knowl.FileEdit
	}{
		{name: "missing frontmatter", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("# One\n")}}},
		{name: "malformed frontmatter", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("---\nid: [\n---\n# One\n")}}},
		{name: "missing id", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("---\ntitle: One\ntype: entity\nsource_refs:\n  - " + testWorkspaceSourceRef + "\n---\n# One\n")}}},
		{name: "missing title", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("---\nid: entities/one\ntype: entity\nsource_refs:\n  - " + testWorkspaceSourceRef + "\n---\n# One\n")}}},
		{name: "missing type", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("---\nid: entities/one\ntitle: One\nsource_refs:\n  - " + testWorkspaceSourceRef + "\n---\n# One\n")}}},
		{name: "missing source refs", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("---\nid: entities/one\ntitle: One\ntype: entity\n---\n# One\n")}}},
		{name: "id mismatch", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage("entities/two", "One", testWorkspaceSourceRef, "")}}},
		{name: "unknown source ref", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage("entities/one", "One", "fixture:missing@1", "")}}},
		{name: "malformed link", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, "[[broken")}}},
		{name: "missing link target", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, "[[entities/missing]]")}}},
		{name: "broken index target", edits: []knowl.FileEdit{{Path: testIndexPath, Content: []byte("# Knowl index\n\n- entities/missing\n")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace, err := New(t.TempDir())
			if err != nil {
				t.Fatalf("new workspace: %v", err)
			}
			if err := workspace.Init(); err != nil {
				t.Fatalf("init workspace: %v", err)
			}
			acceptWorkspaceSource(t, workspace)
			before := captureCanonicalState(t, workspace, testPageOnePath)
			schema, err := workspace.Schema(context.Background(), testScope)
			if err != nil {
				t.Fatalf("read schema: %v", err)
			}
			edits := append([]knowl.FileEdit(nil), tt.edits...)
			for index := range edits {
				if edits[index].Path != testIndexPath || edits[index].ExpectedDigest != "" {
					continue
				}
				indexContent, readErr := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "index.md"))
				if readErr != nil {
					t.Fatalf("read index: %v", readErr)
				}
				edits[index].ExpectedDigest = digestBytes(indexContent)
			}
			_, err = workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
				OperationID:  "invalid-" + token(tt.name),
				Scope:        testScope,
				SchemaDigest: schema.Digest,
				SourceRefs:   []string{testWorkspaceSourceRef},
				Edits:        edits,
			})
			if !errors.Is(err, ErrContentInvalid) {
				t.Fatalf("StagePlan() error = %v, want content invalid", err)
			}
			assertCanonicalState(t, workspace, before)
		})
	}
}

func TestWorkspaceStagePlanAllowsExistingAndSamePlanTargets(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	acceptWorkspaceSource(t, workspace)
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "entities", "two.md"), validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, ""), 0o600); err != nil {
		t.Fatalf("write existing target: %v", err)
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "existing-target",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits:        []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, "[[entities/two]]")}},
	}); err != nil {
		t.Fatalf("stage existing target: %v", err)
	}
	if _, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "same-plan-target",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits: []knowl.FileEdit{
			{Path: testPageOnePath, Content: validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, "[[entities/two]]")},
			{Path: "wiki/entities/two.md", ExpectedDigest: digestBytes(validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, "")), Content: validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, "")},
		},
	}); err != nil {
		t.Fatalf("stage same-plan target: %v", err)
	}
}

func TestWorkspaceStagePlanAllowsIndexTargetsWithoutFrontmatter(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	acceptWorkspaceSource(t, workspace)
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "entities", "two.md"), validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, ""), 0o600); err != nil {
		t.Fatalf("write existing target: %v", err)
	}
	indexPath := filepath.Join(workspace.Root(), "wiki", "index.md")
	indexContent, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "index-existing-target",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits: []knowl.FileEdit{{
			Path:           testIndexPath,
			ExpectedDigest: digestBytes(indexContent),
			Content:        []byte("# Knowl index\n\n- entities/two\n"),
		}},
	}); err != nil {
		t.Fatalf("stage index existing target: %v", err)
	}
	if _, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "index-same-plan-target",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits: []knowl.FileEdit{
			{
				Path:           testIndexPath,
				ExpectedDigest: digestBytes(indexContent),
				Content:        []byte("# Knowl index\n\n- entities/three\n"),
			},
			{
				Path:    "wiki/entities/three.md",
				Content: validWorkspacePage("entities/three", "Three", testWorkspaceSourceRef, ""),
			},
		},
	}); err != nil {
		t.Fatalf("stage index same-plan target: %v", err)
	}
}

func TestWorkspaceStagePlanRejectsInvalidExistingStageReplay(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	acceptWorkspaceSource(t, workspace)
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	plan := knowl.ValidatedEditPlan{
		OperationID:  "replay-invalid",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits:        []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("# One\n")}},
	}
	stageDir := filepath.Join(workspace.Root(), knowlDir, "staging", token(plan.OperationID))
	if err := os.MkdirAll(filepath.Dir(filepath.Join(stageDir, filepath.FromSlash(plan.Edits[0].Path))), 0o700); err != nil {
		t.Fatalf("create staged parent: %v", err)
	}
	if err := writeAtomic(filepath.Join(stageDir, filepath.FromSlash(plan.Edits[0].Path)), plan.Edits[0].Content, 0o600); err != nil {
		t.Fatalf("write staged page: %v", err)
	}
	manifest := stageManifest{
		OperationID:  plan.OperationID,
		Scope:        string(plan.Scope),
		SchemaDigest: plan.SchemaDigest,
		SourceRefs:   append([]string(nil), plan.SourceRefs...),
		Entries: []stageEntry{{
			Target: plan.Edits[0].Path,
			Digest: digestBytes(plan.Edits[0].Content),
		}},
	}
	core, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal core manifest: %v", err)
	}
	logBefore, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logAfter, err := appendLogEntry(logBefore, manifest, digestBytes(core))
	if err != nil {
		t.Fatalf("append log entry: %v", err)
	}
	manifest.LogExpectedDigest = digestBytes(logBefore)
	manifest.LogDigest = digestBytes(logAfter)
	metadata, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := writeAtomic(filepath.Join(stageDir, "manifest.yaml"), metadata, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	_, err = workspace.StagePlan(context.Background(), plan)
	if !errors.Is(err, ErrContentInvalid) {
		t.Fatalf("StagePlan() replay error = %v, want content invalid", err)
	}
}

func TestWorkspaceStagePlanRejectsProtectedSourceNamespace(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	for index, target := range []string{
		"wiki/sources/engineering/page.md",
		"wiki/entities/../sources/engineering/page.md",
		`wiki\sources\engineering\page.md`,
	} {
		plan := knowl.ValidatedEditPlan{
			OperationID: fmt.Sprintf("protected-source-%d", index), Scope: testScope, SchemaDigest: schema.Digest,
			SourceRefs: []string{"fixture:source@1"}, Edits: []knowl.FileEdit{{Path: target, Content: []byte("protected")}},
		}
		if _, err := workspace.StagePlan(context.Background(), plan); !errors.Is(err, ErrPathRejected) {
			t.Fatalf("StagePlan(%q) error = %v, want ErrPathRejected", target, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki", "sources", "engineering", "page.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("protected canonical target stat = %v", err)
	}
}

func TestWorkspaceLoadStageSurvivesReopen(t *testing.T) {
	workspace, staged, _ := stageLoadFixture(t)
	reopened, err := New(workspace.Root())
	if err != nil {
		t.Fatalf("reopen workspace: %v", err)
	}
	loaded, err := reopened.LoadStage(context.Background(), testScope, knowl.OperationID(staged.OperationID))
	if err != nil {
		t.Fatalf("load staged artifact: %v", err)
	}
	if loaded.OperationID != staged.OperationID || loaded.Digest != staged.Digest || !slices.Equal(loaded.Files, staged.Files) {
		t.Fatalf("loaded stage = %#v, want %#v", loaded, staged)
	}
}

func TestWorkspaceLoadStageFailsClosed(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		workspace, err := New(t.TempDir())
		if err != nil {
			t.Fatalf("new workspace: %v", err)
		}
		if err := workspace.Init(); err != nil {
			t.Fatalf("init workspace: %v", err)
		}
		_, err = workspace.LoadStage(context.Background(), testScope, "absent")
		if !errors.Is(err, app.ErrStageNotFound) {
			t.Fatalf("absent stage error = %v", err)
		}
	})

	tests := []struct {
		name   string
		load   func(knowl.StagedChange) (knowl.ScopeRef, knowl.OperationID)
		mutate func(t *testing.T, workspace *Workspace, staged knowl.StagedChange, stageDir string)
		want   error
	}{
		{
			name: "missing manifest",
			mutate: func(t *testing.T, _ *Workspace, _ knowl.StagedChange, stageDir string) {
				t.Helper()
				if err := os.Remove(filepath.Join(stageDir, "manifest.yaml")); err != nil {
					t.Fatalf("remove manifest: %v", err)
				}
			},
			want: ErrPlanConflict,
		},
		{
			name: "cross scope",
			load: func(staged knowl.StagedChange) (knowl.ScopeRef, knowl.OperationID) {
				return "other", knowl.OperationID(staged.OperationID)
			},
			want: ErrPlanConflict,
		},
		{
			name: "content digest mismatch",
			mutate: func(t *testing.T, _ *Workspace, _ knowl.StagedChange, stageDir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(stageDir, filepath.FromSlash(testPageOnePath)), []byte("corrupt"), 0o600); err != nil {
					t.Fatalf("corrupt staged file: %v", err)
				}
			},
			want: ErrPlanConflict,
		},
		{
			name: "oversized staged content",
			mutate: func(t *testing.T, _ *Workspace, _ knowl.StagedChange, stageDir string) {
				t.Helper()
				oversized := make([]byte, app.DefaultPlanLimits().MaxFileBytes+1)
				if err := os.WriteFile(filepath.Join(stageDir, filepath.FromSlash(testPageOnePath)), oversized, 0o600); err != nil {
					t.Fatalf("write oversized staged file: %v", err)
				}
			},
			want: ErrPlanConflict,
		},
		{
			name: "stale schema",
			mutate: func(t *testing.T, workspace *Workspace, _ knowl.StagedChange, _ string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(workspace.Root(), schemaFile), []byte("# Changed schema\n"), 0o600); err != nil {
					t.Fatalf("change schema: %v", err)
				}
			},
			want: ErrPrecondition,
		},
		{
			name: "symlinked content",
			mutate: func(t *testing.T, _ *Workspace, _ knowl.StagedChange, stageDir string) {
				t.Helper()
				path := filepath.Join(stageDir, filepath.FromSlash(testPageOnePath))
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove staged file: %v", err)
				}
				if err := os.Symlink(filepath.Join(stageDir, "manifest.yaml"), path); err != nil {
					t.Skipf("create symlink: %v", err)
				}
			},
			want: ErrPathRejected,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, staged, stageDir := stageLoadFixture(t)
			if test.mutate != nil {
				test.mutate(t, workspace, staged, stageDir)
			}
			scope, id := knowl.ScopeRef(testScope), knowl.OperationID(staged.OperationID)
			if test.load != nil {
				scope, id = test.load(staged)
			}
			_, err := workspace.LoadStage(context.Background(), scope, id)
			if !errors.Is(err, test.want) {
				t.Fatalf("LoadStage() error = %v, want %v", err, test.want)
			}
		})
	}
}

func stageLoadFixture(t *testing.T) (*Workspace, knowl.StagedChange, string) {
	t.Helper()
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	acceptWorkspaceSource(t, workspace)
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	staged, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "load-stage",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits: []knowl.FileEdit{{
			Path:    testPageOnePath,
			Content: validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, ""),
		}},
	})
	if err != nil {
		t.Fatalf("stage fixture: %v", err)
	}
	return workspace, staged, filepath.Join(workspace.Root(), knowlDir, "staging", token(staged.OperationID))
}

func TestWorkspaceCommitRejectsBrokenProspectiveStateBeforeJournal(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	acceptWorkspaceSource(t, workspace)
	targetPath := filepath.Join(workspace.Root(), "wiki", "entities", "two.md")
	if err := os.WriteFile(targetPath, validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, ""), 0o600); err != nil {
		t.Fatalf("write target page: %v", err)
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	staged, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "commit-invalid",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits:        []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, "[[entities/two]]")}},
	})
	if err != nil {
		t.Fatalf("stage plan: %v", err)
	}
	logBefore, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove target page: %v", err)
	}
	_, err = workspace.Commit(context.Background(), staged)
	if !errors.Is(err, ErrContentInvalid) {
		t.Fatalf("Commit() error = %v, want content invalid", err)
	}
	if _, statErr := os.Stat(filepath.Join(workspace.Root(), "wiki", "entities", "one.md")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("committed page stat = %v, want absent", statErr)
	}
	logAfter, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read log after failed commit: %v", err)
	}
	if string(logAfter) != string(logBefore) {
		t.Fatalf("log changed after failed commit: %q != %q", logAfter, logBefore)
	}
	if _, statErr := os.Stat(filepath.Join(workspace.Root(), knowlDir, "recovery", token(staged.OperationID)+".yaml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("recovery journal stat = %v, want absent", statErr)
	}
}

func acceptWorkspaceSource(t *testing.T, workspace *Workspace) {
	t.Helper()
	content := []byte("source content")
	digest := sha256.Sum256(content)
	if _, err := workspace.AcceptSource(context.Background(), knowl.SourceEnvelope{
		Scope:     testScope,
		Source:    knowl.SourceRef{Adapter: testFixtureAdapter, ID: "source-1"},
		Version:   knowl.SourceVersion{Version: "1", Digest: hex.EncodeToString(digest[:])},
		MediaType: "text/plain",
		Content:   content,
	}); err != nil {
		t.Fatalf("accept source: %v", err)
	}
}

func validWorkspacePage(id, title, sourceRef, body string) []byte {
	content := "---\nid: " + id + "\ntitle: " + title + "\ntype: entity\nsource_refs:\n  - " + sourceRef + "\n---\n# " + title + "\n"
	if body != "" {
		content += "\n" + body + "\n"
	}
	return []byte(content)
}

type canonicalState struct {
	content map[string][]byte
	missing map[string]struct{}
}

func captureCanonicalState(t *testing.T, workspace *Workspace, extraPaths ...string) canonicalState {
	t.Helper()
	state := canonicalState{
		content: make(map[string][]byte),
		missing: make(map[string]struct{}),
	}
	for _, relative := range append([]string{testIndexPath, canonicalLogPath}, extraPaths...) {
		path := filepath.Join(workspace.Root(), filepath.FromSlash(relative))
		content, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			state.missing[relative] = struct{}{}
			continue
		}
		if err != nil {
			t.Fatalf("read canonical state %q: %v", relative, err)
		}
		state.content[relative] = append([]byte(nil), content...)
	}
	return state
}

func assertCanonicalState(t *testing.T, workspace *Workspace, state canonicalState) {
	t.Helper()
	for relative, before := range state.content {
		after, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read canonical state %q: %v", relative, err)
		}
		if string(after) != string(before) {
			t.Fatalf("canonical state %q changed: %q != %q", relative, after, before)
		}
	}
	for relative := range state.missing {
		if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canonical state %q stat = %v, want absent", relative, err)
		}
	}
}
