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
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
	"gopkg.in/yaml.v3"
)

// Snapshot captures canonical Markdown digests for projection rebuilds.
func (workspace *Workspace) Snapshot(ctx context.Context, scope knowl.ScopeRef) (knowl.WorkspaceSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.WorkspaceSnapshot{}, err
	}
	if err := workspace.Validate(); err != nil {
		return knowl.WorkspaceSnapshot{}, err
	}
	schema, err := workspace.Schema(ctx, scope)
	if err != nil {
		return knowl.WorkspaceSnapshot{}, err
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	digests := make(map[string]string)
	pages := make([]knowl.PageSnapshot, 0)
	links := make([]knowl.LinkReference, 0)
	wikiRoot := filepath.Join(workspace.root, workspaceWikiDir)
	err = filepath.WalkDir(wikiRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in wiki: %w", ErrPathRejected)
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != markdownExt {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relative, relErr := filepath.Rel(workspace.root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		digest := digestBytes(content)
		digests[relative] = digest
		if relative == filepath.ToSlash(filepath.Join(workspaceWikiDir, "index.md")) || relative == filepath.ToSlash(filepath.Join(workspaceWikiDir, "log.md")) {
			return nil
		}
		pageID, _ := knowlwiki.PageIDFromPath(relative)
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		pages = append(pages, knowl.PageSnapshot{
			ID:             pageID,
			Path:           relative,
			Digest:         digest,
			Title:          markdownTitle(content),
			Content:        string(content),
			SourceRefs:     markdownSourceRefs(content),
			SourceDocument: markdownSourceDocument(content),
			UpdatedAt:      info.ModTime().UTC(),
		})
		links = append(links, markdownLinks(pageID, content)...)
		return nil
	})
	if err != nil {
		return knowl.WorkspaceSnapshot{}, fmt.Errorf("snapshot wiki: %w", err)
	}
	sort.Slice(pages, func(left, right int) bool { return pages[left].Path < pages[right].Path })
	sort.Slice(links, func(left, right int) bool {
		if links[left].From == links[right].From {
			if links[left].To == links[right].To {
				return links[left].Relation < links[right].Relation
			}
			return links[left].To < links[right].To
		}
		return links[left].From < links[right].From
	})
	links = uniqueLinks(links)
	return knowl.WorkspaceSnapshot{Scope: scope, SchemaDigest: schema.Digest, PageDigests: digests, Pages: pages, Links: links, CapturedAt: time.Now().UTC()}, nil
}

// Inspect captures the bounded metadata required by deterministic workspace lint.
func (workspace *Workspace) Inspect(ctx context.Context, scope knowl.ScopeRef) (knowl.WorkspaceInspection, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.WorkspaceInspection{}, err
	}
	snapshot, err := workspace.Snapshot(ctx, scope)
	if err != nil {
		return knowl.WorkspaceInspection{}, err
	}
	controlPages := make([]knowl.PageSnapshot, 0, 2)
	for _, id := range []knowl.PageID{"index", "log"} {
		controlPage, controlErr := workspace.readControlPage(ctx, id, knowl.ReadLimits{Bytes: workspace.maxSourceBytes})
		if controlErr != nil {
			return knowl.WorkspaceInspection{}, fmt.Errorf("read control page %q: %w", id, controlErr)
		}
		controlPages = append(controlPages, controlPage)
	}
	rawSources, err := workspace.inspectRawSources(ctx, scope)
	if err != nil {
		return knowl.WorkspaceInspection{}, err
	}
	return knowl.WorkspaceInspection{Scope: scope, Snapshot: snapshot, Index: controlPages[0], Log: controlPages[1], RawSources: rawSources}, nil
}

type rawDirectoryState struct {
	relative    string
	hasManifest bool
	hasSource   bool
	record      *knowl.RawSourceRecord
}

func (workspace *Workspace) inspectRawSources(ctx context.Context, scope knowl.ScopeRef) ([]knowl.RawSourceRecord, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	return workspace.inspectRawSourcesLocked(scope)
}

func (workspace *Workspace) inspectRawSourcesLocked(scope knowl.ScopeRef) ([]knowl.RawSourceRecord, error) {
	rawRoot := filepath.Join(workspace.root, workspaceRawDir)
	if err := rejectSymlinkPath(workspace.root, rawRoot); err != nil {
		return nil, err
	}
	states := make(map[string]*rawDirectoryState)
	err := filepath.WalkDir(rawRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in raw source tree: %w", ErrPathRejected)
		}
		if entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(workspace.root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		directory := filepath.ToSlash(filepath.Dir(relative))
		state := states[directory]
		if state == nil {
			state = &rawDirectoryState{relative: directory}
			states[directory] = state
		}
		switch filepath.Base(path) {
		case "source":
			state.hasSource = true
		case "manifest.yaml":
			state.hasManifest = true
			record := &knowl.RawSourceRecord{Path: relative}
			state.record = record
			manifestBytes, readErr := os.ReadFile(path)
			if readErr != nil {
				record.ErrorClass = "manifest_unreadable"
				return nil
			}
			var manifest sourceManifest
			if unmarshalErr := yaml.Unmarshal(manifestBytes, &manifest); unmarshalErr != nil {
				record.ErrorClass = "manifest_invalid"
				return nil
			}
			record.Source = manifest.accepted()
			if strings.TrimSpace(manifest.Scope) != "" && knowl.ScopeRef(manifest.Scope) != scope {
				state.record = nil
				return nil
			}
			record.Valid = validSourceManifest(manifest)
			if !record.Valid {
				record.ErrorClass = "manifest_invalid"
				return nil
			}
			sourcePath := filepath.Join(filepath.Dir(path), "source")
			info, statErr := os.Stat(sourcePath)
			if errors.Is(statErr, os.ErrNotExist) {
				record.Valid = false
				record.ErrorClass = "source_missing"
				return nil
			}
			if statErr != nil {
				record.Valid = false
				record.ErrorClass = "source_unreadable"
				return nil
			}
			if info.Size() > int64(workspace.maxSourceBytes) {
				record.Valid = false
				record.ErrorClass = "source_too_large"
				return nil
			}
			content, contentErr := os.ReadFile(sourcePath)
			if contentErr != nil {
				record.Valid = false
				record.ErrorClass = "source_unreadable"
				return nil
			}
			record.ContentDigest = digestBytes(content)
			if record.ContentDigest != manifest.Digest {
				record.Valid = false
				record.ErrorClass = "source_digest_mismatch"
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspect raw sources: %w", err)
	}
	records := make([]knowl.RawSourceRecord, 0, len(states))
	for _, state := range states {
		if state.record != nil {
			records = append(records, *state.record)
			continue
		}
		if state.hasSource && state.hasManifest {
			continue
		}
		if state.hasSource {
			records = append(records, knowl.RawSourceRecord{Path: filepath.ToSlash(filepath.Join(state.relative, "source")), ErrorClass: "manifest_missing"})
		}
	}
	sort.Slice(records, func(left, right int) bool { return records[left].Path < records[right].Path })
	return records, nil
}
