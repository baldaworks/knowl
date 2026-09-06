package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
)

const (
	defaultSchemaContent = `# Knowl workspace policy

schema_version: 1

## Purpose and enforcement boundary

This operator-owned Markdown policy guides the maintainer as untrusted input. Maintainer plans may read it but may not modify it. Knowl code independently enforces OKF structure, safe paths, provenance, links, limits, and protected control files.

## Page taxonomy

- entities/ describes stable named things.
- concepts/ describes reusable topics, policies, and procedures.
- syntheses/ combines evidence across multiple subjects or sources.

## Metadata and provenance

Use concise titles and types. Preserve stable source references for every factual claim, and merge overlapping evidence into durable semantic pages instead of mirroring source files.

## Links and organization

Link only to existing or same-plan pages. Keep every page reachable from the root catalog and organize navigation by subject rather than source location.

## Change handling

Preserve compatible facts when updating a page. Record material contradictions explicitly, and mark superseded guidance instead of silently deleting historical context.
`
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
		schemaFile: defaultSchemaContent,
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
