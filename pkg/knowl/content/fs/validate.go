package fs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

func (workspace *Workspace) validateProspectivePlanLocked(scope knowl.ScopeRef, edits []prospectiveEdit) error {
	pageTargets, err := workspace.currentPageTargetsLocked()
	if err != nil {
		return err
	}
	for _, edit := range edits {
		pageID, ok := knowlwiki.PageIDFromPath(edit.Target)
		if ok {
			pageTargets[pageID] = struct{}{}
		}
	}
	rawRefs, err := workspace.acceptedRawSourceKeysLocked(scope)
	if err != nil {
		return err
	}
	indexPath := filepath.ToSlash(filepath.Join(workspaceWikiDir, "index.md"))
	for _, edit := range edits {
		if edit.Target == indexPath {
			targets, malformed := knowlwiki.IndexTargets(edit.Content)
			if malformed {
				return contentInvalidError(edit.Target, "index.malformed")
			}
			for _, target := range targets {
				if _, exists := pageTargets[knowl.PageID(target)]; !exists {
					return contentInvalidError(edit.Target, "index.broken_page")
				}
			}
			continue
		}
		pageID, ok := knowlwiki.PageIDFromPath(edit.Target)
		if !ok {
			continue
		}
		if err := validateOrdinaryPageEdit(edit.Target, pageID, edit.Content, rawRefs, pageTargets); err != nil {
			return err
		}
	}
	return nil
}

func validateOrdinaryPageEdit(target string, pageID knowl.PageID, content string, rawRefs map[string]struct{}, pageTargets map[knowl.PageID]struct{}) error {
	metadata, err := knowlwiki.ParseFrontmatter(content)
	if err != nil {
		return contentInvalidError(target, "frontmatter.malformed")
	}
	if metadata.ID == "" {
		return contentInvalidError(target, "frontmatter.id_missing")
	}
	if metadata.ID != string(pageID) {
		return contentInvalidError(target, "frontmatter.id_mismatch")
	}
	if metadata.Title == "" {
		return contentInvalidError(target, "frontmatter.title_missing")
	}
	if metadata.Type == "" {
		return contentInvalidError(target, "frontmatter.type_missing")
	}
	nonEmptySourceRefs := 0
	for _, sourceRef := range metadata.SourceRefs {
		if sourceRef == "" {
			continue
		}
		nonEmptySourceRefs++
		if _, exists := rawRefs[sourceRef]; !exists {
			return contentInvalidError(target, "citation.unknown_source")
		}
	}
	if nonEmptySourceRefs == 0 {
		return contentInvalidError(target, "citation.missing")
	}
	targets, malformed := knowlwiki.MarkdownTargets(content)
	if malformed {
		return contentInvalidError(target, "link.malformed")
	}
	for _, linkedTarget := range targets {
		if _, exists := pageTargets[knowl.PageID(linkedTarget)]; !exists {
			return contentInvalidError(target, "link.broken")
		}
	}
	return nil
}

func (workspace *Workspace) acceptedRawSourceKeysLocked(scope knowl.ScopeRef) (map[string]struct{}, error) {
	records, err := workspace.inspectRawSourcesLocked(scope)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record.Valid {
			keys[sourceRefKey(record.Source)] = struct{}{}
		}
	}
	return keys, nil
}

func (workspace *Workspace) currentPageTargetsLocked() (map[knowl.PageID]struct{}, error) {
	wikiRoot := filepath.Join(workspace.root, workspaceWikiDir)
	if err := rejectSymlinkPath(workspace.root, wikiRoot); err != nil {
		return nil, err
	}
	targets := make(map[knowl.PageID]struct{})
	err := filepath.WalkDir(wikiRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in wiki: %w", ErrPathRejected)
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != markdownExt {
			return nil
		}
		relative, relErr := filepath.Rel(workspace.root, path)
		if relErr != nil {
			return relErr
		}
		pageID, ok := knowlwiki.PageIDFromPath(filepath.ToSlash(relative))
		if ok {
			targets[pageID] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enumerate canonical pages: %w", err)
	}
	return targets, nil
}
