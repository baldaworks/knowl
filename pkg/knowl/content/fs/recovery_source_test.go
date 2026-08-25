package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var errInjectedCommitFault = errors.New("injected commit fault")

func TestWorkspaceRecoversSourceCommitAtEveryFaultPoint(t *testing.T) {
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
		{name: "after create", point: commitFaultApplied, index: 0, wantAction: recoveryRolledBack},
		{name: "after delete", point: commitFaultApplied, index: 1, wantAction: recoveryRolledBack},
		{name: "after replace", point: commitFaultApplied, index: 2, wantAction: recoveryRolledBack},
		{name: "durably committed", point: recoveryCommitted, index: -1, wantAfter: true, wantAction: recoveryCompleted},
		{name: "receipt", point: "receipt", index: -1, wantAfter: true, wantAction: recoveryCompleted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := newSourceStageWorkspace(t)
			createPath := "wiki/sources/engineering/a-create.bin"
			deletePath := "wiki/sources/engineering/b-delete.bin"
			replacePath := "wiki/sources/engineering/c-replace.bin"
			writeCanonicalFixture(t, workspace, deletePath, []byte("delete-before"))
			writeCanonicalFixture(t, workspace, replacePath, []byte("replace-before"))
			if err := os.Chmod(filepath.Join(workspace.Root(), filepath.FromSlash(deletePath)), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(workspace.Root(), filepath.FromSlash(replacePath)), 0o644); err != nil {
				t.Fatal(err)
			}
			plan := sourcePlan("sync-crash-"+knowl.SyncRunID(token(test.name)), testSourceID,
				knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: createPath, Content: []byte("create-after")},
				knowl.SourceMutation{Action: knowl.SourceMutationDelete, Path: deletePath, ExpectedDigest: digestBytes([]byte("delete-before"))},
				knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: replacePath, ExpectedDigest: digestBytes([]byte("replace-before")), Content: []byte("replace-after")},
			)
			staged, err := workspace.StageSourcePlan(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			workspace.commitFault = func(point string, index int) error {
				if point == test.point && index == test.index {
					return errInjectedCommitFault
				}
				return nil
			}
			if _, err := workspace.CommitSource(context.Background(), staged); !errors.Is(err, errInjectedCommitFault) {
				t.Fatalf("CommitSource() error = %v, want injected fault", err)
			}
			reopened, err := New(workspace.Root())
			if err != nil {
				t.Fatal(err)
			}
			results, err := reopened.Recover(context.Background())
			if err != nil {
				t.Fatalf("Recover() error = %v", err)
			}
			if test.wantAction != "" {
				if len(results) != 1 || results[0].Action != test.wantAction || results[0].OperationID != string(plan.RunID) {
					t.Fatalf("recovery results = %#v, want %q", results, test.wantAction)
				}
			}
			assertSourceCrashState(t, reopened, createPath, deletePath, replacePath, test.wantAfter)
			assertDirectoryEmpty(t, filepath.Join(workspace.Root(), knowlDir, "recovery"))
			if test.wantAfter {
				commit, err := reopened.CommitSource(context.Background(), staged)
				if err != nil || commit.Generation != staged.Generation {
					t.Fatalf("replay after committed recovery = %#v, %v", commit, err)
				}
			}
		})
	}
}

func TestWorkspaceRecoveryRejectsUnsafeOrUnboundedJournals(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		workspace := newSourceStageWorkspace(t)
		journalPath := filepath.Join(workspace.Root(), knowlDir, "recovery", token("malformed")+".yaml")
		if err := os.WriteFile(journalPath, []byte("entries: ["), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.Recover(context.Background()); err == nil {
			t.Fatal("Recover() error = nil, want malformed journal rejection")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		workspace := newSourceStageWorkspace(t)
		journalPath := filepath.Join(workspace.Root(), knowlDir, "recovery", token("oversized")+".yaml")
		if err := os.WriteFile(journalPath, make([]byte, maxRecoveryJournalBytes+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.Recover(context.Background()); !errors.Is(err, ErrWorkspaceInvalid) {
			t.Fatalf("Recover() error = %v, want workspace invalid", err)
		}
	})

	t.Run("escaped backup", func(t *testing.T) {
		workspace := newSourceStageWorkspace(t)
		operationID := "escaped-backup"
		journal := recoveryJournal{
			OperationID: operationID, State: recoveryPrepared,
			Entries: []recoveryEntry{{Target: "wiki/entities/page.md", Backup: filepath.Join(t.TempDir(), "outside.old"), HadOld: true, Mode: 0o600}},
		}
		if err := os.WriteFile(journal.Entries[0].Backup, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeJournal(filepath.Join(workspace.Root(), knowlDir, "recovery", token(operationID)+".yaml"), journal); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.Recover(context.Background()); !errors.Is(err, ErrPathRejected) {
			t.Fatalf("Recover() error = %v, want path rejected", err)
		}
	})

	t.Run("late escaped backup cannot cause partial rollback", func(t *testing.T) {
		workspace := newSourceStageWorkspace(t)
		operationID := "late-escaped-backup"
		firstPath := "wiki/sources/engineering/a-new.bin"
		secondPath := "wiki/sources/engineering/b-old.bin"
		writeCanonicalFixture(t, workspace, firstPath, []byte("partial-new"))
		writeCanonicalFixture(t, workspace, secondPath, []byte("partial-old"))
		outside := filepath.Join(t.TempDir(), "outside.old")
		if err := os.WriteFile(outside, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		journal := recoveryJournal{
			OperationID: operationID, Writer: stageWriterSource, SourceID: string(testSourceID), Scope: testScope,
			State: recoveryPrepared, Generation: digestBytes([]byte("generation")), Files: []string{firstPath, secondPath},
			Entries: []recoveryEntry{
				{Action: knowl.SourceMutationWrite, Target: firstPath, Digest: digestBytes([]byte("new"))},
				{Action: knowl.SourceMutationDelete, Target: secondPath, Backup: outside, HadOld: true, Mode: 0o600},
			},
		}
		key := sourceRecoveryKey(journal.Scope, journal.OperationID)
		if err := writeJournal(filepath.Join(workspace.Root(), knowlDir, "recovery", token(key)+".yaml"), journal); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.Recover(context.Background()); !errors.Is(err, ErrPathRejected) {
			t.Fatalf("Recover() error = %v, want path rejected", err)
		}
		content, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(firstPath)))
		if err != nil || string(content) != "partial-new" {
			t.Fatalf("first target mutated before late preflight failure: %q, %v", content, err)
		}
	})

	t.Run("source journal identity mismatch", func(t *testing.T) {
		workspace := newSourceStageWorkspace(t)
		journal := validSourceRecoveryFixture("recovery-source")
		if err := writeJournal(filepath.Join(workspace.Root(), knowlDir, "recovery", token("wrong-key")+".yaml"), journal); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.Recover(context.Background()); !errors.Is(err, ErrWorkspaceInvalid) {
			t.Fatalf("Recover() error = %v, want workspace invalid", err)
		}
	})

	t.Run("symlinked canonical target", func(t *testing.T) {
		workspace := newSourceStageWorkspace(t)
		journal := validSourceRecoveryFixture("recovery-symlink")
		target := filepath.Join(workspace.Root(), filepath.FromSlash(journal.Entries[0].Target))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(workspace.Root(), "schema.md"), target); err != nil {
			t.Fatal(err)
		}
		key := sourceRecoveryKey(journal.Scope, journal.OperationID)
		if err := writeJournal(filepath.Join(workspace.Root(), knowlDir, "recovery", token(key)+".yaml"), journal); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.Recover(context.Background()); !errors.Is(err, ErrPathRejected) {
			t.Fatalf("Recover() error = %v, want path rejected", err)
		}
	})

	t.Run("symlinked journal", func(t *testing.T) {
		workspace := newSourceStageWorkspace(t)
		journalPath := filepath.Join(workspace.Root(), knowlDir, "recovery", token("symlink-journal")+".yaml")
		if err := os.Symlink(filepath.Join(workspace.Root(), "schema.md"), journalPath); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.Recover(context.Background()); !errors.Is(err, ErrPathRejected) {
			t.Fatalf("Recover() error = %v, want path rejected", err)
		}
	})
}

func TestWorkspaceRecoveryPreservesLegacyUnboundedMaintainerOperationID(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	operationID := strings.Repeat("legacy-operation-", 20)
	staged, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID: operationID, Scope: testScope, SchemaDigest: schema.Digest, SourceRefs: []string{testWorkspaceSourceRef},
		Edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, "")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace.commitFault = func(point string, _ int) error {
		if point == recoveryPrepared {
			return errInjectedCommitFault
		}
		return nil
	}
	if _, err := workspace.Commit(context.Background(), staged); !errors.Is(err, errInjectedCommitFault) {
		t.Fatalf("Commit() error = %v, want injected fault", err)
	}
	reopened, err := New(workspace.Root())
	if err != nil {
		t.Fatal(err)
	}
	results, err := reopened.Recover(context.Background())
	if err != nil || len(results) != 1 || results[0].OperationID != operationID || results[0].Action != recoveryRolledBack {
		t.Fatalf("Recover() = %#v, %v", results, err)
	}
}

func assertSourceCrashState(t *testing.T, workspace *Workspace, createPath, deletePath, replacePath string, after bool) {
	t.Helper()
	create, createErr := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(createPath)))
	deleted, deleteErr := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(deletePath)))
	replaced, replaceErr := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(replacePath)))
	if after {
		if createErr != nil || string(create) != "create-after" || !errors.Is(deleteErr, os.ErrNotExist) || replaceErr != nil || string(replaced) != "replace-after" {
			t.Fatalf("after state: create=%q/%v delete=%q/%v replace=%q/%v", create, createErr, deleted, deleteErr, replaced, replaceErr)
		}
		return
	}
	if !errors.Is(createErr, os.ErrNotExist) || deleteErr != nil || string(deleted) != "delete-before" || replaceErr != nil || string(replaced) != "replace-before" {
		t.Fatalf("before state: create=%q/%v delete=%q/%v replace=%q/%v", create, createErr, deleted, deleteErr, replaced, replaceErr)
	}
	deleteInfo, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(deletePath)))
	if err != nil || deleteInfo.Mode().Perm() != 0o640 {
		t.Fatalf("delete mode = %v, %v; want 0640", deleteInfo, err)
	}
	replaceInfo, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(replacePath)))
	if err != nil || replaceInfo.Mode().Perm() != 0o644 {
		t.Fatalf("replace mode = %v, %v; want 0644", replaceInfo, err)
	}
}

func assertDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory %q contains %#v", path, entries)
	}
}

func validSourceRecoveryFixture(operationID string) recoveryJournal {
	target := testSourceAssetPath
	return recoveryJournal{
		OperationID: operationID, Writer: stageWriterSource, SourceID: string(testSourceID), Scope: testScope,
		State: recoveryCommitted, Generation: digestBytes([]byte("generation")), Files: []string{target},
		Entries: []recoveryEntry{{Action: knowl.SourceMutationWrite, Target: target, Digest: digestBytes([]byte("content"))}},
	}
}
