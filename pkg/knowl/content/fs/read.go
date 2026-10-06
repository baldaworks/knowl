package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// Schema reads and fingerprints the operator-owned schema document.
func (workspace *Workspace) Schema(ctx context.Context, scope knowl.ScopeRef) (knowl.SchemaDocument, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.SchemaDocument{}, err
	}
	unlock, err := workspace.lock(ctx)
	if err != nil {
		return knowl.SchemaDocument{}, err
	}
	defer unlock()
	content, err := os.ReadFile(filepath.Join(workspace.root, schemaFile))
	if err != nil {
		return knowl.SchemaDocument{}, fmt.Errorf("read schema: %w", err)
	}
	return knowl.SchemaDocument{Scope: scope, Digest: digestBytes(content), Version: schemaVersion(string(content)), Content: content}, nil
}

// ReadPages reads the requested Markdown pages by safe page-relative ID.
func (workspace *Workspace) ReadPages(ctx context.Context, scope knowl.ScopeRef, ids []knowl.PageID, limits knowl.ReadLimits) ([]knowl.PageSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	unlock, err := workspace.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := workspace.checkPublishedLocked(); err != nil {
		return nil, err
	}
	wiki, err := openReadRoot(workspace.root, workspaceWikiDir)
	if err != nil {
		return nil, err
	}
	defer wiki.close()
	rawSources, err := workspace.acceptedRawSourcesLocked(scope)
	if err != nil {
		return nil, err
	}
	pages := make([]knowl.PageSnapshot, 0, len(ids))
	now := workspace.now().UTC()
	for _, id := range ids {
		if limits.Pages > 0 && len(pages) >= limits.Pages {
			break
		}
		relative, err := pageRelativePath(string(id))
		if err != nil {
			return nil, err
		}
		content, info, err := wiki.read(strings.TrimPrefix(relative, workspaceWikiDir+"/"), limits, workspace.maxSourceBytes)
		if err != nil {
			return nil, fmt.Errorf("read page %q: %w", id, err)
		}
		bundleRelative := strings.TrimPrefix(relative, workspaceWikiDir+"/")
		kind, classifyErr := okf.ClassifyPath(bundleRelative)
		if classifyErr != nil {
			return nil, classifyErr
		}
		if kind == okf.DocumentConcept {
			document, parseErr := okf.ParseConcept(bundleRelative, content, okfLimits(len(content)))
			if parseErr != nil {
				return nil, parseErr
			}
			page, snapshotErr := parsedPageSnapshot(id, relative, content, digestBytes(content), info.ModTime(), document, now)
			if snapshotErr != nil {
				return nil, snapshotErr
			}
			resolvePageProvenance(&page, rawSources)
			pages = append(pages, page)
			continue
		}
		pages = append(pages, knowl.PageSnapshot{
			ID: id, Path: relative, Digest: digestBytes(content), Title: markdownTitle(content),
			Content: string(content), Body: string(content), UpdatedAt: info.ModTime().UTC(),
		})
	}
	return pages, nil
}

func (workspace *Workspace) readControlPage(ctx context.Context, id knowl.PageID, limits knowl.ReadLimits) (knowl.PageSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.PageSnapshot{}, err
	}
	if id != "index" && id != "log" {
		return knowl.PageSnapshot{}, fmt.Errorf("unsupported control page %q: %w", id, ErrPathRejected)
	}
	unlock, err := workspace.lock(ctx)
	if err != nil {
		return knowl.PageSnapshot{}, err
	}
	defer unlock()
	if err := workspace.checkPublishedLocked(); err != nil {
		return knowl.PageSnapshot{}, err
	}
	wiki, err := openReadRoot(workspace.root, workspaceWikiDir)
	if err != nil {
		return knowl.PageSnapshot{}, err
	}
	defer wiki.close()
	relative := filepath.ToSlash(filepath.Join(workspaceWikiDir, string(id)+markdownExt))
	content, info, err := wiki.read(string(id)+markdownExt, limits, workspace.maxSourceBytes)
	if err != nil {
		return knowl.PageSnapshot{}, fmt.Errorf("read control page %q: %w", id, err)
	}
	return knowl.PageSnapshot{ID: id, Path: relative, Digest: digestBytes(content), Title: markdownTitle(content), Content: string(content), Body: string(content), UpdatedAt: info.ModTime().UTC()}, nil
}
