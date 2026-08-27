package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	hierarchySnapshotVersion    = "knowl-hierarchy-snapshot-v1"
	maxHierarchySnapshotEntries = 100_000
)

// HierarchySnapshotDigest binds hierarchy planning to every canonical schema,
// wiki, and immutable raw file without exposing those bytes to the provider.
func (workspace *Workspace) HierarchySnapshotDigest(ctx context.Context, scope knowl.ScopeRef) (string, error) {
	if err := contextErr(ctx); err != nil {
		return "", err
	}
	if strings.TrimSpace(string(scope)) == "" {
		return "", fmt.Errorf("hierarchy scope is required: %w", ErrWorkspaceInvalid)
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	return workspace.hierarchySnapshotDigestLocked(scope)
}

func (workspace *Workspace) hierarchySnapshotDigestLocked(scope knowl.ScopeRef) (string, error) {
	hash := sha256.New()
	_, _ = hash.Write([]byte(hierarchySnapshotVersion))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(scope))
	count := 0
	for _, relativeRoot := range []string{schemaFile, workspaceRawDir, workspaceWikiDir} {
		absoluteRoot := filepath.Join(workspace.root, relativeRoot)
		if err := rejectSymlinkPath(workspace.root, absoluteRoot); err != nil {
			return "", err
		}
		err := filepath.WalkDir(absoluteRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink in hierarchy snapshot at %q: %w", path, ErrPathRejected)
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("non-regular hierarchy snapshot file %q: %w", path, ErrWorkspaceInvalid)
			}
			count++
			if count > maxHierarchySnapshotEntries {
				return fmt.Errorf("hierarchy snapshot entries exceed %d: %w", maxHierarchySnapshotEntries, ErrWorkspaceInvalid)
			}
			relative, relErr := filepath.Rel(workspace.root, path)
			if relErr != nil {
				return relErr
			}
			relative = filepath.ToSlash(relative)
			if len(relative) > maxCanonicalPathBytes {
				return fmt.Errorf("hierarchy snapshot path %q exceeds limit: %w", relative, ErrPathRejected)
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			if info.Size() > int64(workspace.maxSourceBytes) {
				return fmt.Errorf("hierarchy snapshot file %q has %d bytes, exceeds %d: %w", relative, info.Size(), workspace.maxSourceBytes, ErrWorkspaceInvalid)
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			fileDigest := sha256.Sum256(content)
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write([]byte(relative))
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write(fileDigest[:])
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("capture hierarchy snapshot %q: %w", relativeRoot, err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
