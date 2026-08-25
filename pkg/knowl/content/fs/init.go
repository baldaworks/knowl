package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
)

const (
	rootIndexContent = "---\nokf_version: \"0.2\"\n---\n# Knowl Index\n"
	rootLogContent   = "# Knowl Update Log\n"
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
		filepath.Join(knowlDir, "commits"),
	} {
		if err := os.MkdirAll(filepath.Join(workspace.root, relative), 0o700); err != nil {
			return fmt.Errorf("create workspace directory %q: %w", relative, err)
		}
	}
	files := map[string]string{
		schemaFile: "# Knowl schema\n\nMaintainer plans may read this document but may not modify it.\n",
		filepath.Join(workspaceWikiDir, "index.md"): rootIndexContent,
		filepath.Join(workspaceWikiDir, "log.md"):   rootLogContent,
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
	indexRelative := filepath.Join(workspaceWikiDir, "index.md")
	logRelative := filepath.Join(workspaceWikiDir, "log.md")
	for _, relative := range []string{schemaFile, indexRelative, logRelative} {
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
		if relative == schemaFile {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read workspace control file %q: %w", relative, err)
		}
		limits := okfLimits(workspace.maxSourceBytes)
		if relative == indexRelative {
			index, parseErr := okf.ParseRootIndex(content, limits)
			if parseErr != nil {
				return fmt.Errorf("validate workspace root index: %w", errors.Join(ErrWorkspaceInvalid, parseErr))
			}
			if index.ObservedVersion != okf.Version {
				return fmt.Errorf("workspace root index is not OKF v%s: %w", okf.Version, ErrWorkspaceInvalid)
			}
			continue
		}
		if _, parseErr := okf.ValidateLog("log.md", content, limits); parseErr != nil {
			return fmt.Errorf("validate workspace root log: %w", errors.Join(ErrWorkspaceInvalid, parseErr))
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
