package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/types"
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
		path := filepath.Join(recoveryRoot, entry.Name())
		journal, err := readJournal(path)
		if err != nil {
			return nil, fmt.Errorf("read recovery journal %q: %w", entry.Name(), err)
		}
		switch journal.State {
		case recoveryPrepared:
			for _, recovery := range journal.Entries {
				if err := validateRecoveryTarget(recovery.Target); err != nil {
					return nil, err
				}
				target := filepath.Join(workspace.root, filepath.FromSlash(recovery.Target))
				if err := rejectSymlinkPath(workspace.root, target); err != nil {
					return nil, err
				}
				if recovery.HadOld {
					if err := validateRecoveryBackup(recovery.Backup, recoveryRoot); err != nil {
						return nil, err
					}
					old, readErr := os.ReadFile(recovery.Backup)
					if readErr != nil {
						return nil, fmt.Errorf("read preimage %q: %w", recovery.Target, readErr)
					}
					if err := writeAtomic(target, old, 0o600); err != nil {
						return nil, fmt.Errorf("restore preimage %q: %w", recovery.Target, err)
					}
				} else if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("remove partial file %q: %w", recovery.Target, err)
				}
			}
			results = append(results, knowl.RecoveryResult{OperationID: journal.OperationID, Action: "rolled_back"})
		case recoveryCommitted:
			for _, recovery := range journal.Entries {
				if err := validateRecoveryTarget(recovery.Target); err != nil {
					return nil, err
				}
				if err := rejectSymlinkPath(workspace.root, filepath.Join(workspace.root, filepath.FromSlash(recovery.Target))); err != nil {
					return nil, err
				}
			}
			results = append(results, knowl.RecoveryResult{OperationID: journal.OperationID, Action: "completed"})
		default:
			return nil, fmt.Errorf("unknown recovery state %q: %w", journal.State, ErrWorkspaceInvalid)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove recovery journal %q: %w", entry.Name(), err)
		}
		if err := os.RemoveAll(filepath.Join(recoveryRoot, token(journal.OperationID))); err != nil {
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
