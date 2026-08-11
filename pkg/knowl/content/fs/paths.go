package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateWikiPath(raw string) error {
	clean := filepath.ToSlash(filepath.Clean(raw))
	if clean == "." || !strings.HasPrefix(clean, workspaceWikiDir+"/") || clean == filepath.Join(workspaceWikiDir, "log.md") || filepath.Ext(clean) != markdownExt || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return fmt.Errorf("wiki path %q: %w", raw, ErrPathRejected)
	}
	return nil
}

func validateCommitTarget(target string) error {
	if target == filepath.ToSlash(filepath.Join(workspaceWikiDir, "log.md")) {
		return nil
	}
	return validateWikiPath(target)
}

func validateRecoveryTarget(target string) error {
	if err := validateCommitTarget(target); err != nil {
		return fmt.Errorf("recovery target %q: %w", target, err)
	}
	return nil
}

func validateRecoveryBackup(backup, recoveryRoot string) error {
	if strings.TrimSpace(backup) == "" {
		return fmt.Errorf("recovery preimage is missing: %w", ErrWorkspaceInvalid)
	}
	absolute, err := filepath.Abs(backup)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(recoveryRoot)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("recovery preimage escapes recovery directory: %w", ErrPathRejected)
	}
	return rejectSymlinkPath(root, absolute)
}

func pageRelativePath(raw string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean(raw))
	if !strings.HasPrefix(clean, workspaceWikiDir+"/") {
		clean = filepath.ToSlash(filepath.Join(workspaceWikiDir, clean))
	}
	if filepath.Ext(clean) == "" {
		clean += markdownExt
	}
	if err := validateWikiPath(clean); err != nil {
		return "", err
	}
	return clean, nil
}

func rejectSymlinkPath(root, target string) error {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	cleanTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes workspace: %w", ErrPathRejected)
	}
	current := cleanRoot
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %q: %w", current, ErrPathRejected)
		}
	}
	return nil
}
