package fs

import (
	"context"
	"errors"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var _ app.WikiPageTreeReader = (*Workspace)(nil)

// WikiPageChildren reads one visible page branch under the publication lock.
// Directories without an index are transparent; an index page stops descent.
// Only selected page bodies are read, without loading source provenance.
func (workspace *Workspace) WikiPageChildren(ctx context.Context, scope knowl.ScopeRef, directory string, options app.OperatorReadOptions) (app.OperatorReadPage[knowl.OperatorWikiPage], error) {
	result := app.OperatorReadPage[knowl.OperatorWikiPage]{Items: []knowl.OperatorWikiPage{}}
	if !validWikiDirectory(directory) {
		return result, app.ErrOperatorInvalidRequest
	}
	if directory != "" && strings.Count(directory, "/")+1 > app.DefaultCatalogLimits().MaxDepth {
		return result, app.ErrOperatorReadLimitExceeded
	}
	if err := operatorOptionsValid(options); err != nil {
		return result, err
	}
	unlock, err := workspace.operatorLock(ctx, scope)
	if err != nil {
		return result, err
	}
	defer unlock()
	wiki, err := openReadRoot(workspace.root, workspaceWikiDir)
	if err != nil {
		return result, operatorReadError(err, app.ErrOperatorDirectoryNotFound)
	}
	defer wiki.close()
	branch := wiki.file
	if directory != "" {
		branch, err = wiki.open(directory, true)
		if err != nil {
			return result, operatorReadError(err, app.ErrOperatorDirectoryNotFound)
		}
		defer func() { _ = branch.Close() }()
	}
	parentInfo, err := branch.Stat()
	if err != nil {
		return result, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
	}
	parentIdentity, err := operatorFileIdentity(branch)
	if err != nil {
		return result, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
	}
	budget := &operatorDirectoryBudget{bytes: len(directory) + 128, count: 1}
	entries, versionEntries, err := operatorPageBranchInventory(ctx, wiki, branch, directory, budget)
	if err != nil {
		return result, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
	}
	versionEntries = append(versionEntries, operatorInventoryEntry{path: directory, identity: operatorFolderKind + ":" + parentIdentity, info: parentInfo})
	slices.SortFunc(entries, func(a, b operatorInventoryEntry) int { return strings.Compare(a.path, b.path) })
	slices.SortFunc(versionEntries, func(a, b operatorInventoryEntry) int { return strings.Compare(a.path, b.path) })
	result.SnapshotVersion = operatorInventoryVersion(scope, "wiki-pages", directory, versionEntries)
	if err := operatorSnapshotValid(options, result.SnapshotVersion); err != nil {
		return result, err
	}
	start := 0
	if key := options.Continuation.Key; key != "" {
		var found bool
		start, found = slices.BinarySearchFunc(entries, key, func(entry operatorInventoryEntry, key string) int { return strings.Compare(entry.path, key) })
		if !found {
			return result, app.ErrOperatorCursorInvalid
		}
		start++
	}
	end := min(start+options.Limit, len(entries))
	selected := entries[start:end]
	remaining := app.DefaultCatalogLimits().MaxSnapshotBytes - budget.bytes
	if options.ReadLimits.Bytes > 0 {
		remaining = min(remaining, options.ReadLimits.Bytes)
	}
	for _, entry := range selected {
		if err := contextErr(ctx); err != nil {
			return result, err
		}
		limits, err := operatorRemainingLimits(options.ReadLimits, remaining)
		if err != nil {
			return result, err
		}
		node, size, err := workspace.operatorWikiPage(wiki, entry, limits)
		if err != nil {
			return result, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
		}
		remaining -= size
		result.Items = append(result.Items, node)
	}
	if err := contextErr(ctx); err != nil {
		return result, err
	}
	if end < len(entries) {
		result.NextKey = entries[end-1].path
	}
	return result, nil
}

func operatorPageBranchInventory(ctx context.Context, wiki *readRoot, branch *os.File, directory string, budget *operatorDirectoryBudget) ([]operatorInventoryEntry, []operatorInventoryEntry, error) {
	entries, nodes, err := operatorDirectoryInventoryBounded(ctx, branch, directory, budget)
	if err != nil {
		return nil, nil, err
	}
	versions := slices.Clone(entries)
	pages := []operatorInventoryEntry{}
	for _, entry := range entries {
		if err := contextErr(ctx); err != nil {
			return nil, nil, err
		}
		node := nodes[entry.path]
		switch node.Kind {
		case operatorPageKind:
			if path.Base(entry.path) != okfIndexFilename {
				pages = append(pages, entry)
			}
		case operatorFolderKind:
			// Probe only the child's index metadata. Never inventory a page's
			// descendants until that page's branch is requested.
			index, err := operatorStat(wiki, path.Join(entry.path, okfIndexFilename))
			if err == nil {
				budget.count++
				budget.bytes += len(index.path) + 128
				if budget.count > app.DefaultCatalogLimits().MaxEdges || budget.bytes > app.DefaultCatalogLimits().MaxSnapshotBytes {
					return nil, nil, app.ErrOperatorReadLimitExceeded
				}
				versions = append(versions, index)
				pages = append(pages, index)
				continue
			}
			if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, ErrPathRejected) {
				return nil, nil, err
			}
			child, err := openReadChild(branch, path.Base(entry.path), true)
			if err != nil {
				return nil, nil, err
			}
			childInfo, err := child.Stat()
			if err != nil || !operatorFileUnchanged(entry.info, childInfo) {
				_ = child.Close()
				if err != nil {
					return nil, nil, err
				}
				return nil, nil, app.ErrOperatorSnapshotChanged
			}
			childPages, childVersions, err := operatorPageBranchInventory(ctx, wiki, child, entry.path, budget)
			_ = child.Close()
			if err != nil {
				return nil, nil, err
			}
			pages = append(pages, childPages...)
			versions = append(versions, childVersions...)
		}
	}
	return pages, versions, nil
}

func (workspace *Workspace) operatorWikiPage(wiki *readRoot, entry operatorInventoryEntry, limits knowl.ReadLimits) (knowl.OperatorWikiPage, int, error) {
	node := knowl.OperatorWikiPage{Path: entry.path, PageID: knowl.PageID(strings.TrimSuffix(entry.path, markdownExt))}
	if path.Base(entry.path) == okfIndexFilename {
		ceiling := app.DefaultCatalogLimits().MaxCatalogBytes
		content, info, err := wiki.read(entry.path, limits, min(workspace.maxSourceBytes, ceiling))
		if err != nil {
			return node, 0, err
		}
		if !operatorFileUnchanged(entry.info, info) {
			return node, 0, app.ErrOperatorSnapshotChanged
		}
		index, err := okf.ParseIndex(entry.path, content, okfLimits(ceiling))
		if err != nil {
			return node, 0, err
		}
		node.Title = operatorIndexTitle(entry.path, index.Body)
		node.ChildrenDirectory = path.Dir(entry.path)
		return node, len(content), nil
	}
	page, info, err := workspace.operatorPage(wiki, entry.path, limits)
	if err != nil {
		return node, 0, err
	}
	if !operatorFileUnchanged(entry.info, info) {
		return node, 0, app.ErrOperatorSnapshotChanged
	}
	node.Title = page.Title
	if node.Title == "" {
		node.Title = strings.TrimSuffix(path.Base(entry.path), markdownExt)
	}
	return node, len(page.Content), nil
}

func operatorIndexTitle(relative, body string) string {
	if title := markdownTitle([]byte(body)); title != "" {
		return title
	}
	if directory := path.Dir(relative); directory != "." {
		return path.Base(directory)
	}
	return strings.TrimSuffix(path.Base(relative), markdownExt)
}
