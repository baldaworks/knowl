package fs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
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
		bundleRelative := strings.TrimPrefix(edit.Target, workspaceWikiDir+"/")
		kind, classifyErr := okf.ClassifyPath(bundleRelative)
		if classifyErr != nil {
			return contentInvalidError(edit.Target, string(okf.RulePathInvalid))
		}
		if kind == okf.DocumentIndex {
			index, validateErr := okf.ValidateIndex(bundleRelative, []byte(edit.Content), okfLimits(len(edit.Content)))
			if validateErr != nil {
				return okfContentInvalidError(edit.Target, validateErr)
			}
			if edit.Target == indexPath && index.ObservedVersion != okf.Version {
				return contentInvalidError(edit.Target, string(okf.RuleIndexInvalid))
			}
			if edit.Target != indexPath {
				continue
			}
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
		if kind == okf.DocumentLog {
			return contentInvalidError(edit.Target, string(okf.RuleLogInvalid))
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
	bundleRelative := strings.TrimPrefix(target, workspaceWikiDir+"/")
	if _, err := okf.ParseConcept(bundleRelative, []byte(content), okfLimits(len(content))); err != nil {
		return okfContentInvalidError(target, err)
	}
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

func okfContentInvalidError(target string, err error) error {
	var invalid *okf.Violation
	if errors.As(err, &invalid) {
		return contentInvalidError(target, string(invalid.Rule))
	}
	return contentInvalidError(target, "okf.invalid")
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
