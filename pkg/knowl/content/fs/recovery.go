package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	maxRecoveryJournalBytes = 8 << 20
	maxRecoveryEntries      = maxSourceStageEntries + 1
	maxRecoveryBackupBytes  = maxSourceStageFile
)

// Recover restores prepared journals and clears completed journals before readiness.
func (workspace *Workspace) Recover(ctx context.Context) ([]knowl.RecoveryResult, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	recoveryRoot := filepath.Join(workspace.root, knowlDir, "recovery")
	if err := rejectSymlinkPath(workspace.root, recoveryRoot); err != nil {
		return nil, err
	}
	stagingRoot := filepath.Join(workspace.root, knowlDir, "staging")
	if err := rejectSymlinkPath(workspace.root, stagingRoot); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(recoveryRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read recovery directory: %w", err)
	}
	results := make([]knowl.RecoveryResult, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink recovery journal %q: %w", entry.Name(), ErrPathRejected)
		}
		path := filepath.Join(recoveryRoot, entry.Name())
		journal, err := readJournal(path)
		if err != nil {
			return nil, fmt.Errorf("read recovery journal %q: %w", entry.Name(), err)
		}
		recoveryKey, err := validateRecoveryJournal(entry.Name(), journal)
		if err != nil {
			return nil, err
		}
		journalDir := filepath.Join(recoveryRoot, token(recoveryKey))
		switch journal.State {
		case recoveryPrepared:
			preimages, err := workspace.preflightPreparedRecovery(journal, journalDir)
			if err != nil {
				return nil, err
			}
			for index, recovery := range journal.Entries {
				target := filepath.Join(workspace.root, filepath.FromSlash(recovery.Target))
				if recovery.HadOld {
					mode := os.FileMode(recovery.Mode)
					if mode == 0 {
						mode = 0o600
					}
					if err := writeAtomic(target, preimages[index], mode); err != nil {
						return nil, fmt.Errorf("restore preimage %q: %w", recovery.Target, err)
					}
				} else if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("remove partial file %q: %w", recovery.Target, err)
				}
			}
			if manifestWriter(stageManifest{Writer: journal.Writer}) == stageWriterSource {
				if err := os.Remove(workspace.sourceCommitReceiptPath(journal.Scope, journal.OperationID)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("remove rolled-back source receipt: %w", err)
				}
			}
			results = append(results, knowl.RecoveryResult{OperationID: journal.OperationID, Action: recoveryRolledBack})
		case recoveryCommitted:
			for _, recovery := range journal.Entries {
				if err := rejectSymlinkPath(workspace.root, filepath.Join(workspace.root, filepath.FromSlash(recovery.Target))); err != nil {
					return nil, err
				}
			}
			if manifestWriter(stageManifest{Writer: journal.Writer}) == stageWriterSource {
				if !recoveryEntriesMatch(workspace.root, journal.Entries) {
					return nil, fmt.Errorf("committed source recovery diverged: %w", ErrWorkspaceInvalid)
				}
				if err := workspace.writeSourceCommitReceipt(commitReceipt{
					Writer: stageWriterSource, SourceID: journal.SourceID, Scope: journal.Scope, OperationID: journal.OperationID,
					Generation: journal.Generation, Files: append([]string(nil), journal.Files...),
				}); err != nil {
					return nil, err
				}
			}
			results = append(results, knowl.RecoveryResult{OperationID: journal.OperationID, Action: recoveryCompleted})
		default:
			return nil, fmt.Errorf("unknown recovery state %q: %w", journal.State, ErrWorkspaceInvalid)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove recovery journal %q: %w", entry.Name(), err)
		}
		if err := os.RemoveAll(filepath.Join(recoveryRoot, token(recoveryKey))); err != nil {
			return nil, fmt.Errorf("remove recovery backups %q: %w", entry.Name(), err)
		}
	}
	stagingEntries, err := os.ReadDir(stagingRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read staging directory: %w", err)
	}
	for _, entry := range stagingEntries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink in staging: %w", ErrPathRejected)
		}
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(stagingRoot, entry.Name(), "manifest.yaml")
		if _, statErr := os.Stat(manifestPath); statErr == nil {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect staging directory %q: %w", entry.Name(), statErr)
		}
		if err := os.RemoveAll(filepath.Join(stagingRoot, entry.Name())); err != nil {
			return nil, fmt.Errorf("discard incomplete staging %q: %w", entry.Name(), err)
		}
		results = append(results, knowl.RecoveryResult{Action: "discarded_staging"})
	}
	recoveryEntries, err := os.ReadDir(recoveryRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("re-read recovery directory: %w", err)
	}
	for _, entry := range recoveryEntries {
		if !entry.IsDir() {
			continue
		}
		journalPath := filepath.Join(recoveryRoot, entry.Name()+".yaml")
		if _, statErr := os.Stat(journalPath); statErr == nil {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect recovery directory %q: %w", entry.Name(), statErr)
		}
		if err := os.RemoveAll(filepath.Join(recoveryRoot, entry.Name())); err != nil {
			return nil, fmt.Errorf("discard orphan recovery %q: %w", entry.Name(), err)
		}
	}
	return results, nil
}

func (workspace *Workspace) preflightPreparedRecovery(journal recoveryJournal, journalDir string) ([][]byte, error) {
	preimages := make([][]byte, len(journal.Entries))
	total := 0
	for index, entry := range journal.Entries {
		target := filepath.Join(workspace.root, filepath.FromSlash(entry.Target))
		if err := rejectSymlinkPath(workspace.root, target); err != nil {
			return nil, err
		}
		if !entry.HadOld {
			continue
		}
		if err := validateRecoveryBackup(entry.Backup, journalDir); err != nil {
			return nil, err
		}
		content, err := readRecoveryBackup(entry.Backup)
		if err != nil {
			return nil, fmt.Errorf("read preimage %q: %w", entry.Target, err)
		}
		if total > maxSourceStageBytes-len(content) {
			return nil, fmt.Errorf("recovery preimages exceed limit: %w", ErrWorkspaceInvalid)
		}
		total += len(content)
		preimages[index] = content
	}
	if manifestWriter(stageManifest{Writer: journal.Writer}) == stageWriterSource {
		if err := rejectSymlinkPath(workspace.root, workspace.sourceCommitReceiptPath(journal.Scope, journal.OperationID)); err != nil {
			return nil, err
		}
	}
	return preimages, nil
}

func validateRecoveryJournal(fileName string, journal recoveryJournal) (string, error) {
	writer := manifestWriter(stageManifest{Writer: journal.Writer})
	if (writer != stageWriterMaintainer && writer != stageWriterSource) ||
		(journal.State != recoveryPrepared && journal.State != recoveryCommitted) || len(journal.Entries) == 0 || len(journal.Entries) > maxRecoveryEntries {
		return "", fmt.Errorf("invalid recovery journal: %w", ErrWorkspaceInvalid)
	}
	recoveryKey := journal.OperationID
	if writer == stageWriterSource {
		if app.ValidateSyncRunID(knowl.SyncRunID(journal.OperationID)) != nil || app.ValidateSourceID(knowl.SourceID(journal.SourceID)) != nil || strings.TrimSpace(journal.Scope) == "" || !validSHA256(journal.Generation) || len(journal.Files) == 0 {
			return "", fmt.Errorf("invalid source recovery journal: %w", ErrWorkspaceInvalid)
		}
		recoveryKey = sourceRecoveryKey(journal.Scope, journal.OperationID)
	} else if strings.TrimSpace(journal.OperationID) == "" || journal.SourceID != "" {
		return "", fmt.Errorf("invalid maintainer recovery identity: %w", ErrWorkspaceInvalid)
	}
	if fileName != token(recoveryKey)+".yaml" {
		return "", fmt.Errorf("recovery journal identity mismatch: %w", ErrWorkspaceInvalid)
	}
	seen := make(map[string]struct{}, len(journal.Entries))
	targets := make([]string, 0, len(journal.Entries))
	for _, entry := range journal.Entries {
		if validateJournalTarget(journal, entry.Target) != nil || entry.Mode&^uint32(0o777) != 0 || (entry.HadOld && entry.Backup == "") || (!entry.HadOld && entry.Backup != "") {
			return "", fmt.Errorf("invalid recovery entry: %w", ErrWorkspaceInvalid)
		}
		action := entry.Action
		if action == "" {
			action = knowl.SourceMutationWrite
		}
		if action != knowl.SourceMutationWrite && action != knowl.SourceMutationDelete {
			return "", fmt.Errorf("invalid recovery action: %w", ErrWorkspaceInvalid)
		}
		if writer == stageWriterSource {
			if action == knowl.SourceMutationWrite && !validSHA256(entry.Digest) {
				return "", fmt.Errorf("invalid recovery digest: %w", ErrWorkspaceInvalid)
			}
			if action == knowl.SourceMutationDelete && entry.Digest != "" {
				return "", fmt.Errorf("invalid delete recovery digest: %w", ErrWorkspaceInvalid)
			}
		}
		if _, exists := seen[entry.Target]; exists {
			return "", fmt.Errorf("duplicate recovery target: %w", ErrWorkspaceInvalid)
		}
		seen[entry.Target] = struct{}{}
		targets = append(targets, entry.Target)
	}
	slices.Sort(targets)
	if writer == stageWriterSource && !slices.Equal(targets, journal.Files) {
		return "", fmt.Errorf("source recovery files mismatch: %w", ErrWorkspaceInvalid)
	}
	return recoveryKey, nil
}

func readRecoveryBackup(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, maxRecoveryBackupBytes+1))
	if err != nil || len(content) > maxRecoveryBackupBytes {
		return nil, ErrWorkspaceInvalid
	}
	return content, nil
}

func recoveryEntriesMatch(root string, entries []recoveryEntry) bool {
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Target)))
		action := entry.Action
		if action == "" {
			action = knowl.SourceMutationWrite
		}
		if action == knowl.SourceMutationDelete {
			if !errors.Is(err, os.ErrNotExist) {
				return false
			}
			continue
		}
		if err != nil || digestBytes(content) != entry.Digest {
			return false
		}
	}
	return true
}
