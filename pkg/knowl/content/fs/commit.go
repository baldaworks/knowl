package fs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// Commit applies a staged plan under the workspace writer lock.
func (workspace *Workspace) Commit(ctx context.Context, staged knowl.StagedChange) (knowl.ContentCommit, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.ContentCommit{}, err
	}
	if strings.TrimSpace(staged.OperationID) == "" {
		return knowl.ContentCommit{}, ErrPlanConflict
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	stageDir := filepath.Join(workspace.root, knowlDir, "staging", token(staged.OperationID))
	manifest, err := readStageManifest(filepath.Join(stageDir, "manifest.yaml"))
	if err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("read staged plan: %w", err)
	}
	if manifest.OperationID != staged.OperationID {
		return knowl.ContentCommit{}, fmt.Errorf("staged operation mismatch: %w", ErrPlanConflict)
	}
	for _, entry := range manifest.Entries {
		if err := validateCommitTarget(entry.Target); err != nil {
			return knowl.ContentCommit{}, err
		}
		if err := rejectSymlinkPath(workspace.root, filepath.Join(workspace.root, filepath.FromSlash(entry.Target))); err != nil {
			return knowl.ContentCommit{}, err
		}
	}
	if err := validateStagedPaths(workspace.root, stageDir, manifest.Entries); err != nil {
		return knowl.ContentCommit{}, err
	}
	if !stagedFilesMatch(stageDir, manifest.Entries) {
		return knowl.ContentCommit{}, fmt.Errorf("staged file content changed: %w", ErrPlanConflict)
	}
	generation := stageGeneration(manifest)
	schema, err := os.ReadFile(filepath.Join(workspace.root, schemaFile))
	if err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("read schema before commit: %w", err)
	}
	if manifest.SchemaDigest != "" && digestBytes(schema) != manifest.SchemaDigest {
		return knowl.ContentCommit{}, fmt.Errorf("schema changed after staging: %w", ErrPrecondition)
	}
	stagedEdits, err := readStagedPlanEdits(stageDir, manifest.Entries)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := workspace.validateProspectivePlanLocked(manifestScope(manifest), stagedEdits); err != nil {
		return knowl.ContentCommit{}, err
	}
	journalDir := filepath.Join(workspace.root, knowlDir, "recovery", token(staged.OperationID))
	if err := rejectSymlinkPath(workspace.root, journalDir); err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("create recovery directory: %w", err)
	}
	logPath := filepath.Join(workspace.root, workspaceWikiDir, "log.md")
	if err := rejectSymlinkPath(workspace.root, logPath); err != nil {
		return knowl.ContentCommit{}, err
	}
	logBefore, err := os.ReadFile(logPath)
	if err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("read canonical log: %w", err)
	}
	logAfter, err := appendLogEntry(logBefore, manifest, generation)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	if digestBytes(logBefore) != manifest.LogExpectedDigest {
		if digestBytes(logBefore) == manifest.LogDigest && canonicalEntriesMatch(workspace.root, manifest.Entries) {
			return knowl.ContentCommit{OperationID: staged.OperationID, Generation: generation, Files: commitTargets(manifest.Entries), CommittedAt: time.Now().UTC()}, nil
		}
		return knowl.ContentCommit{}, fmt.Errorf("canonical log changed after staging: %w", ErrPrecondition)
	}
	if digestBytes(logAfter) != manifest.LogDigest {
		return knowl.ContentCommit{}, fmt.Errorf("staged log digest changed: %w", ErrPlanConflict)
	}
	commitEntries := append(append([]stageEntry(nil), manifest.Entries...), stageEntry{Target: filepath.ToSlash(filepath.Join(workspaceWikiDir, "log.md")), ExpectedDigest: manifest.LogExpectedDigest, Digest: manifest.LogDigest})
	journal := recoveryJournal{OperationID: staged.OperationID, State: recoveryPrepared, Entries: make([]recoveryEntry, 0, len(commitEntries))}
	for _, entry := range commitEntries {
		target := filepath.Join(workspace.root, filepath.FromSlash(entry.Target))
		old, readErr := os.ReadFile(target)
		hadOld := readErr == nil
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return knowl.ContentCommit{}, fmt.Errorf("read commit target %q: %w", entry.Target, readErr)
		}
		if entry.ExpectedDigest != "" && (!hadOld || digestBytes(old) != entry.ExpectedDigest) {
			return knowl.ContentCommit{}, fmt.Errorf("target %q changed after staging: %w", entry.Target, ErrPrecondition)
		}
		if entry.ExpectedDigest == "" && hadOld {
			return knowl.ContentCommit{}, fmt.Errorf("new target %q already exists: %w", entry.Target, ErrPrecondition)
		}
		backup := ""
		if hadOld {
			backup = filepath.Join(journalDir, token(entry.Target)+".old")
			if err := writeAtomic(backup, old, 0o600); err != nil {
				return knowl.ContentCommit{}, fmt.Errorf("write preimage %q: %w", entry.Target, err)
			}
		}
		journal.Entries = append(journal.Entries, recoveryEntry{Target: entry.Target, Backup: backup, HadOld: hadOld})
	}
	journalPath := filepath.Join(workspace.root, knowlDir, "recovery", token(staged.OperationID)+".yaml")
	if err := writeJournal(journalPath, journal); err != nil {
		return knowl.ContentCommit{}, err
	}
	for _, entry := range commitEntries {
		var content []byte
		if entry.Target == filepath.ToSlash(filepath.Join(workspaceWikiDir, "log.md")) {
			content = logAfter
		} else {
			content, err = os.ReadFile(filepath.Join(stageDir, filepath.FromSlash(entry.Target)))
			if err != nil {
				return knowl.ContentCommit{}, fmt.Errorf("read staged file %q: %w", entry.Target, err)
			}
		}
		if err := writeAtomic(filepath.Join(workspace.root, filepath.FromSlash(entry.Target)), content, 0o600); err != nil {
			return knowl.ContentCommit{}, fmt.Errorf("commit file %q: %w", entry.Target, err)
		}
	}
	journal.State = recoveryCommitted
	if err := writeJournal(journalPath, journal); err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := os.Remove(journalPath); err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("remove recovery journal: %w", err)
	}
	return knowl.ContentCommit{OperationID: staged.OperationID, Generation: generation, Files: commitTargets(manifest.Entries), CommittedAt: time.Now().UTC()}, nil
}

func appendLogEntry(existing []byte, manifest stageManifest, generation string) ([]byte, error) {
	entry, err := json.Marshal(logEntry{
		OperationID:  manifest.OperationID,
		Generation:   generation,
		SchemaDigest: manifest.SchemaDigest,
		SourceRefs:   append([]string(nil), manifest.SourceRefs...),
		Files:        entryTargets(manifest.Entries),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal commit log entry: %w", err)
	}
	content := append([]byte(nil), existing...)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	content = append(content, []byte("- ")...)
	content = append(content, entry...)
	content = append(content, '\n')
	return content, nil
}

func canonicalEntriesMatch(root string, entries []stageEntry) bool {
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Target)))
		if err != nil || digestBytes(content) != entry.Digest {
			return false
		}
	}
	return true
}

func commitTargets(entries []stageEntry) []string {
	targets := append([]stageEntry(nil), entries...)
	targets = append(targets, stageEntry{Target: filepath.ToSlash(filepath.Join(workspaceWikiDir, "log.md"))})
	return entryTargets(targets)
}
