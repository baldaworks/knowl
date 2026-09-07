package reconcile

import (
	"context"
	"testing"

	sourcegit "github.com/baldaworks/knowl/internal/source/git"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testGitSnapshot1 = "1111111111111111111111111111111111111111"
	testGitSnapshot2 = "2222222222222222222222222222222222222222"
	testGitDoc1Path  = "docs/intro.md"
	testGitDoc2Path  = "docs/architecture.md"
	metaSnapshot     = "snapshot"
	metaBlobSHA      = "blob_sha"
)

func TestGitSourceReconciliation_CheckpointAndIncremental(t *testing.T) {
	harness := newStageHarness(t, nil)
	ctx := context.Background()
	harness.service.adapters[knowl.SourceTypeGit] = harness.adapter

	source := knowl.Source{
		ID:      "git-team-docs",
		Type:    knowl.SourceTypeGit,
		Enabled: true,
		Config: knowl.SourceConfig{
			Git: &knowl.GitSourceConfig{
				Remote:  "https://github.com/org/team-docs.git",
				Ref:     "main",
				RefKind: knowl.GitRefKindBranch,
				Include: []string{"**/*.md"},
			},
		},
	}

	harness.adapter.script(knowl.DocumentID(testGitDoc1Path), "# Intro\n\nWelcome.")
	harness.adapter.script(knowl.DocumentID(testGitDoc2Path), "# Architecture\n\nDesign.")

	blob1Rev := "b111111111111111111111111111111111111111"
	blob2Rev := "b222222222222222222222222222222222222222"

	ref1 := knowl.DocumentRef{
		ExternalID: knowl.DocumentID(testGitDoc1Path),
		Path:       testGitDoc1Path,
		Revision:   blob1Rev,
		Metadata: map[string]string{
			metaSnapshot: testGitSnapshot1,
			metaBlobSHA:  blob1Rev,
		},
	}
	ref2 := knowl.DocumentRef{
		ExternalID: knowl.DocumentID(testGitDoc2Path),
		Path:       testGitDoc2Path,
		Revision:   blob2Rev,
		Metadata: map[string]string{
			metaSnapshot: testGitSnapshot1,
			metaBlobSHA:  blob2Rev,
		},
	}

	// 1. Initial Sync
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref1, ref2}, ""))
	result1, err := harness.service.SyncSource(ctx, stageScope, source)
	if err != nil {
		t.Fatalf("Initial sync error: %v", err)
	}
	if result1.Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("run status = %s, want succeeded", result1.Run.Status)
	}
	if result1.Run.Checkpoint != testGitSnapshot1 {
		t.Fatalf("checkpoint = %s, want snapshot %s", result1.Run.Checkpoint, testGitSnapshot1)
	}
	if result1.Run.Counts.Added != 2 {
		t.Errorf("counts.Added = %d, want 2", result1.Run.Counts.Added)
	}

	// 2. Idempotent Sync (unchanged snapshot)
	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref1, ref2}, ""))
	result2, err := harness.service.SyncSource(ctx, stageScope, source)
	if err != nil {
		t.Fatalf("Second sync error: %v", err)
	}
	if result2.Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("second run status = %s, want succeeded", result2.Run.Status)
	}
	if result2.Run.Checkpoint != testGitSnapshot1 {
		t.Fatalf("second checkpoint = %s, want %s", result2.Run.Checkpoint, testGitSnapshot1)
	}
	if result2.Run.Counts.Unchanged != 2 || result2.Run.Counts.Added != 0 {
		t.Errorf("unexpected counts: %+v", result2.Run.Counts)
	}

	// 3. Incremental Advance to Snapshot S2 (Doc 1 updated)
	blob1UpdatedRev := "b33333333333333333333333333333333333333"
	harness.adapter.script(knowl.DocumentID(testGitDoc1Path), "# Intro Updated\n\nWelcome back.")
	ref1Updated := knowl.DocumentRef{
		ExternalID: knowl.DocumentID(testGitDoc1Path),
		Path:       testGitDoc1Path,
		Revision:   blob1UpdatedRev,
		Metadata: map[string]string{
			metaSnapshot: testGitSnapshot2,
			metaBlobSHA:  blob1UpdatedRev,
		},
	}
	ref2InS2 := ref2
	ref2InS2.Metadata = map[string]string{
		metaSnapshot: testGitSnapshot2,
		metaBlobSHA:  blob2Rev,
	}

	harness.adapter.enqueue(harness.adapter.page([]knowl.DocumentRef{ref1Updated, ref2InS2}, ""))
	result3, err := harness.service.SyncSource(ctx, stageScope, source)
	if err != nil {
		t.Fatalf("Third sync error: %v", err)
	}
	if result3.Run.Status != knowl.SyncStatusSucceeded {
		t.Fatalf("third run status = %s, want succeeded", result3.Run.Status)
	}
	if result3.Run.Checkpoint != testGitSnapshot2 {
		t.Fatalf("third checkpoint = %s, want %s", result3.Run.Checkpoint, testGitSnapshot2)
	}
	if result3.Run.Counts.Updated != 1 || result3.Run.Counts.Unchanged != 1 {
		t.Errorf("third counts = %+v, want 1 updated, 1 unchanged", result3.Run.Counts)
	}
}

type failListAdapter struct {
	err error
}

func (a *failListAdapter) List(context.Context, knowl.Source, string) (knowl.DocumentPage, error) {
	return knowl.DocumentPage{}, a.err
}

func (a *failListAdapter) Fetch(context.Context, knowl.Source, knowl.DocumentRef) (knowl.Document, error) {
	return knowl.Document{}, a.err
}

func TestGitSourceReconciliation_FailureClass(t *testing.T) {
	harness := newStageHarness(t, nil)
	ctx := context.Background()

	source := knowl.Source{
		ID:      "git-failing-source",
		Type:    knowl.SourceTypeGit,
		Enabled: true,
		Config: knowl.SourceConfig{
			Git: &knowl.GitSourceConfig{
				Remote:  "https://github.com/org/fail.git",
				Ref:     "main",
				RefKind: knowl.GitRefKindBranch,
			},
		},
	}

	classifiedErr := sourcegit.WrapClassified(sourcegit.ClassRejectedHistoryRewrite, sourcegit.ErrRejectedHistoryRewrite, "rewrite detected")
	failingAdapter := &failListAdapter{err: classifiedErr}

	harness.service.adapters[knowl.SourceTypeGit] = failingAdapter

	result, err := harness.service.SyncSource(ctx, stageScope, source)
	if err == nil {
		t.Fatal("expected error from failing adapter")
	}

	if result.Run.Status != knowl.SyncStatusFailed {
		t.Errorf("status = %s, want failed", result.Run.Status)
	}
	if result.Run.FailureClass != sourcegit.ClassRejectedHistoryRewrite {
		t.Errorf("failureClass = %q, want %q", result.Run.FailureClass, sourcegit.ClassRejectedHistoryRewrite)
	}
}
