package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

const maxCanonicalPathBytes = 2048

func validateWikiPath(raw string) error {
	if err := validateCanonicalWikiTarget(raw); err != nil {
		return err
	}
	if filepath.Base(raw) == okfLogFilename || filepath.Ext(raw) != markdownExt {
		return fmt.Errorf("wiki path %q: %w", raw, ErrPathRejected)
	}
	return nil
}

func validateCanonicalWikiTarget(raw string) error {
	clean := filepath.ToSlash(filepath.Clean(raw))
	if raw == "" || len(raw) > maxCanonicalPathBytes || strings.TrimSpace(raw) != raw || !utf8.ValidString(raw) || strings.Contains(raw, "\\") || clean != raw || !strings.HasPrefix(raw, workspaceWikiDir+"/") {
		return fmt.Errorf("wiki path %q: %w", raw, ErrPathRejected)
	}
	for _, character := range raw {
		if character < ' ' || character == 0x7f {
			return fmt.Errorf("wiki path %q: %w", raw, ErrPathRejected)
		}
	}
	return nil
}

func validateSourceTarget(sourceID, raw string) error {
	if err := validateCanonicalWikiTarget(raw); err != nil {
		return err
	}
	prefix := "wiki/sources/" + sourceID + "/"
	if !strings.HasPrefix(raw, prefix) || len(raw) <= len(prefix) {
		return fmt.Errorf("source path %q: %w", raw, ErrPathRejected)
	}
	return nil
}

func validateMaintainerWikiPath(raw string) error {
	if err := validateWikiPath(raw); err != nil {
		return err
	}
	if raw == "wiki/sources" || strings.HasPrefix(raw, "wiki/sources/") {
		return fmt.Errorf("maintainer path %q: %w", raw, ErrPathRejected)
	}
	return nil
}

func validateCommitTarget(target string) error {
	if target == filepath.ToSlash(filepath.Join(workspaceWikiDir, "log.md")) {
		return nil
	}
	return validateMaintainerWikiPath(target)
}

func validateRecoveryTarget(target string) error {
	if err := validateCommitTarget(target); err != nil {
		return fmt.Errorf("recovery target %q: %w", target, err)
	}
	return nil
}

func validateJournalTarget(journal recoveryJournal, target string) error {
	switch manifestWriter(stageManifest{Writer: journal.Writer}) {
	case stageWriterMaintainer:
		return validateRecoveryTarget(target)
	case stageWriterSource:
		if err := validateSourceTarget(journal.SourceID, target); err != nil {
			return fmt.Errorf("source recovery target %q: %w", target, err)
		}
		return nil
	case stageWriterMigration:
		if target == migrationLegacyLogPath {
			return nil
		}
		if err := validateCanonicalWikiTarget(target); err != nil || filepath.Ext(target) != markdownExt {
			return fmt.Errorf("migration recovery target %q: %w", target, ErrPathRejected)
		}
		return nil
	case stageWriterHierarchy:
		if target == canonicalLogPath || app.IsManagedHierarchyCatalog(target) {
			return nil
		}
		return fmt.Errorf("hierarchy recovery target %q: %w", target, ErrPathRejected)
	default:
		return fmt.Errorf("unknown recovery writer %q: %w", journal.Writer, ErrWorkspaceInvalid)
	}
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
	if base := filepath.Base(clean); base == okfLogFilename || (base == okfIndexFilename && clean != filepath.ToSlash(filepath.Join(workspaceWikiDir, okfIndexFilename))) {
		return "", fmt.Errorf("reserved OKF document %q: %w", clean, ErrPathRejected)
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
			return fmt.Errorf("symlinked workspace path: %w", ErrPathRejected)
		}
	}
	return nil
}
