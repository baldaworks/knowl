package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Init creates the canonical empty workspace without replacing existing files.
func (workspace *Workspace) Init() error {
	for _, relative := range []string{
		workspaceRawDir,
		filepath.Join(workspaceWikiDir, "entities"),
		filepath.Join(workspaceWikiDir, "concepts"),
		filepath.Join(workspaceWikiDir, "syntheses"),
		filepath.Join(knowlDir, "staging"),
		filepath.Join(knowlDir, "recovery"),
	} {
		if err := os.MkdirAll(filepath.Join(workspace.root, relative), 0o700); err != nil {
			return fmt.Errorf("create workspace directory %q: %w", relative, err)
		}
	}
	files := map[string]string{
		schemaFile: "# Knowl schema\n\nMaintainer plans may read this document but may not modify it.\n",
		filepath.Join(workspaceWikiDir, "index.md"): "# Knowl index\n\nNo pages have been committed yet.\n",
		filepath.Join(workspaceWikiDir, "log.md"):   "# Knowl log\n\n",
	}
	for relative, contents := range files {
		path := filepath.Join(workspace.root, relative)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat workspace file %q: %w", relative, err)
		}
		if err := writeAtomic(path, []byte(contents), 0o600); err != nil {
			return fmt.Errorf("write workspace file %q: %w", relative, err)
		}
	}
	return nil
}

// Validate checks the required workspace shape and rejects symlinked roots.
func (workspace *Workspace) Validate() error {
	if err := rejectSymlinkPath(workspace.root, workspace.root); err != nil {
		return err
	}
	for _, relative := range []string{schemaFile, filepath.Join(workspaceWikiDir, "index.md"), filepath.Join(workspaceWikiDir, "log.md")} {
		path := filepath.Join(workspace.root, relative)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("required workspace file %q: %w", relative, err)
		}
		if info.IsDir() {
			return fmt.Errorf("required workspace path %q is a directory: %w", relative, ErrWorkspaceInvalid)
		}
		if err := rejectSymlinkPath(workspace.root, path); err != nil {
			return err
		}
	}
	for _, relative := range []string{workspaceRawDir, workspaceWikiDir, knowlDir} {
		path := filepath.Join(workspace.root, relative)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("required workspace directory %q: %w", relative, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("required workspace path %q is not a directory: %w", relative, ErrWorkspaceInvalid)
		}
		if err := rejectSymlinkPath(workspace.root, path); err != nil {
			return err
		}
	}
	return nil
}
