package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl"
)

const testScope = "local"

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
		Source:    knowl.SourceRef{Adapter: "fixture", ID: "source-1"},
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
	if snapshot.PageDigests["wiki/entities/one.md"] == "" {
		t.Fatalf("snapshot missing page digest: %#v", snapshot.PageDigests)
	}
	if len(snapshot.Pages) != 1 || snapshot.Pages[0].ID != "entities/one" {
		t.Fatalf("snapshot pages = %#v", snapshot.Pages)
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
	plan := knowl.ValidatedEditPlan{
		OperationID:  "recovery-operation",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{"fixture:recovery@1"},
		Edits:        []knowl.FileEdit{{Path: "wiki/entities/recovered.md", Content: []byte("# Recovered\n")}},
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
			{Target: "wiki/log.md", Backup: logBackup, HadOld: true},
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
	if len(results) != 1 || results[0].Action != "rolled_back" {
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
