package fs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const exportFaultCopied = "copied"

// ExportLimits bounds an OKF directory export.
type ExportLimits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxPathBytes  int
}

// DefaultExportLimits returns conservative local export bounds.
func DefaultExportLimits() ExportLimits {
	return ExportLimits{MaxFiles: 10_000, MaxFileBytes: 64 << 20, MaxTotalBytes: 1 << 30, MaxPathBytes: 4_096}
}

type exportEntry struct {
	path   string
	mode   fs.FileMode
	digest string
	data   []byte
	dir    bool
}

// ExportOKF copies wiki/ into a new directory whose root is the OKF bundle.
func (workspace *Workspace) ExportOKF(ctx context.Context, destination string, limits ExportLimits) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if limits == (ExportLimits{}) {
		limits = DefaultExportLimits()
	}
	if limits.MaxFiles <= 0 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes <= 0 || limits.MaxPathBytes <= 0 {
		return fmt.Errorf("export limits: %w", ErrWorkspaceInvalid)
	}
	destination, err := workspace.safeExportDestination(destination)
	if err != nil {
		return err
	}
	if err := workspace.Validate(); err != nil {
		return fmt.Errorf("validate export source: %w", err)
	}

	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	before, err := workspace.exportEntries(ctx, limits, true)
	if err != nil {
		return fmt.Errorf("read export source: %w", err)
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".knowl-export-")
	if err != nil {
		return fmt.Errorf("create export temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	if err := copyExportEntries(ctx, temporary, before); err != nil {
		return fmt.Errorf("copy export: %w", err)
	}
	if workspace.exportFault != nil {
		if err := workspace.exportFault(exportFaultCopied); err != nil {
			return err
		}
	}
	after, err := workspace.exportEntries(ctx, limits, false)
	if err != nil || !sameExportEntries(before, after) {
		return errors.Join(ErrExportSourceChanged, err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return ErrExportDestinationExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return fmt.Errorf("publish export: %w", err)
	}
	return nil
}

func (workspace *Workspace) safeExportDestination(destination string) (string, error) {
	if strings.TrimSpace(destination) == "" {
		return "", ErrPathRejected
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(destination); err == nil {
		return "", ErrExportDestinationExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return "", err
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	source, err := filepath.EvalSymlinks(filepath.Join(workspace.root, workspaceWikiDir))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(source, destination)
	if err != nil {
		return "", err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return "", fmt.Errorf("destination is inside wiki: %w", ErrPathRejected)
	}
	return destination, nil
}

func (workspace *Workspace) exportEntries(ctx context.Context, limits ExportLimits, withData bool) ([]exportEntry, error) {
	root := filepath.Join(workspace.root, workspaceWikiDir)
	entries := make([]exportEntry, 0)
	var total int64
	err := filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return ErrPathRejected
		}
		relative = filepath.ToSlash(relative)
		if len(relative) > limits.MaxPathBytes || len(entries) >= limits.MaxFiles {
			return ErrExportLimitExceeded
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrPathRejected
		}
		entry := exportEntry{path: relative, mode: info.Mode().Perm(), dir: item.IsDir()}
		if item.IsDir() {
			entries = append(entries, entry)
			return nil
		}
		if !info.Mode().IsRegular() {
			return ErrPathRejected
		}
		if info.Size() > limits.MaxFileBytes || info.Size() > limits.MaxTotalBytes-total {
			return ErrExportLimitExceeded
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if int64(len(data)) > limits.MaxFileBytes || int64(len(data)) > limits.MaxTotalBytes-total {
			return ErrExportLimitExceeded
		}
		total += int64(len(data))
		entry.digest = digestBytes(data)
		if withData {
			entry.data = data
		}
		entries = append(entries, entry)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries, err
}

func copyExportEntries(ctx context.Context, root string, entries []exportEntry) error {
	for _, entry := range entries {
		if err := contextErr(ctx); err != nil {
			return err
		}
		path := filepath.Join(root, filepath.FromSlash(entry.path))
		if entry.dir {
			if err := os.Mkdir(path, entry.mode); err != nil {
				return err
			}
			continue
		}
		if err := writeAtomic(path, entry.data, entry.mode); err != nil {
			return err
		}
	}
	return nil
}

func sameExportEntries(left, right []exportEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].path != right[index].path || left[index].mode != right[index].mode || left[index].digest != right[index].digest || left[index].dir != right[index].dir {
			return false
		}
	}
	return true
}
