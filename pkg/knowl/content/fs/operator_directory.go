package fs

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var _ app.WikiDirectoryReader = (*Workspace)(nil)

const (
	operatorFolderKind      = "folder"
	operatorUnsupportedKind = "unsupported"
)

// WikiDirectoryChildren inventories one direct branch under the locked,
// descriptor-pinned published wiki. It never follows authored index links.
func (workspace *Workspace) WikiDirectoryChildren(ctx context.Context, scope knowl.ScopeRef, directory string, options app.OperatorReadOptions) (app.OperatorReadPage[knowl.OperatorWikiEntry], error) {
	result := app.OperatorReadPage[knowl.OperatorWikiEntry]{Items: []knowl.OperatorWikiEntry{}}
	if directory != "" && strings.Count(directory, "/")+1 > app.DefaultCatalogLimits().MaxDepth {
		return result, app.ErrOperatorReadLimitExceeded
	}
	if !validWikiDirectory(directory) {
		return result, app.ErrOperatorInvalidRequest
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
	entries, nodes, err := operatorDirectoryInventory(ctx, branch, directory)
	if err != nil {
		return result, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
	}
	versionEntries := append([]operatorInventoryEntry{{path: directory, identity: operatorFolderKind + ":" + parentIdentity, info: parentInfo}}, entries...)
	result.SnapshotVersion = operatorInventoryVersion(scope, "wiki-directory", directory, versionEntries)
	if err := operatorSnapshotValid(options, result.SnapshotVersion); err != nil {
		return result, err
	}
	start, found := slices.BinarySearchFunc(entries, options.Continuation.Key, func(entry operatorInventoryEntry, key string) int {
		return strings.Compare(entry.path, key)
	})
	if found {
		start++
	} else if options.Continuation.Key != "" {
		return result, app.ErrOperatorCursorInvalid
	}
	end := min(start+options.Limit, len(entries))
	for _, entry := range entries[start:end] {
		result.Items = append(result.Items, nodes[entry.path])
	}
	if end < len(entries) {
		result.NextKey = entries[end-1].path
	}
	return result, nil
}

func validWikiDirectory(directory string) bool {
	if directory == "" {
		return true
	}
	if !validReadPath(directory) || strings.HasSuffix(directory, ".") {
		return false
	}
	return true
}

func operatorDirectoryInventory(ctx context.Context, branch *os.File, directory string) ([]operatorInventoryEntry, map[string]knowl.OperatorWikiEntry, error) {
	entries := []operatorInventoryEntry{}
	nodes := make(map[string]knowl.OperatorWikiEntry)
	limits := app.DefaultCatalogLimits()
	bytes := 0
	count := 0
	for {
		if err := contextErr(ctx); err != nil {
			return nil, nil, err
		}
		batch, err := branch.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, nil, err
		}
		for _, entry := range batch {
			count++
			bytes += len(entry.Name()) + 128 // fixed metadata allowance bounds inventory allocation
			if count > limits.MaxEdges || bytes > limits.MaxSnapshotBytes {
				return nil, nil, app.ErrOperatorReadLimitExceeded
			}
			// Unrelated assets (including linked assets) are not wiki nodes.
			// A linked directory is likewise omitted; requesting it directly
			// still fails through the no-follow open above.
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			relative := path.Join(directory, entry.Name())
			kind := ""
			isDirectory := entry.IsDir()
			if isDirectory {
				kind = operatorFolderKind
			} else {
				if path.Base(relative) == okfLogFilename || path.Ext(relative) != markdownExt {
					continue
				}
				classification, classifyErr := okf.ClassifyPath(relative)
				if classifyErr != nil {
					return nil, nil, classifyErr
				}
				if classification == okf.DocumentConcept || classification == okf.DocumentIndex {
					kind = operatorPageKind
				}
			}
			if kind == "" {
				continue
			}
			if !validReadPath(relative) || (kind == operatorPageKind && !app.ValidOperatorReadPath(strings.TrimSuffix(relative, markdownExt))) {
				kind = operatorUnsupportedKind
			}
			if isDirectory && strings.Count(relative, "/")+1 > limits.MaxDepth {
				return nil, nil, app.ErrOperatorReadLimitExceeded
			}
			if kind == operatorUnsupportedKind {
				// Do not pass an excluded component to an OS open. This also
				// avoids interpreting an ADS-shaped name on Windows.
				entries = append(entries, operatorInventoryEntry{path: relative, identity: operatorUnsupportedKind + ":" + entry.Type().String()})
				nodes[relative] = knowl.OperatorWikiEntry{Path: relative, Name: entry.Name(), Kind: kind}
				continue
			}
			file, openErr := openReadChild(branch, entry.Name(), isDirectory)
			if openErr != nil {
				return nil, nil, openErr
			}
			info, statErr := file.Stat()
			identity, identityErr := operatorFileIdentity(file)
			_ = file.Close()
			if statErr != nil {
				return nil, nil, statErr
			}
			if identityErr != nil {
				return nil, nil, identityErr
			}
			if (isDirectory && !info.IsDir()) || (!isDirectory && !info.Mode().IsRegular()) {
				return nil, nil, ErrPathRejected
			}
			entries = append(entries, operatorInventoryEntry{path: relative, identity: kind + ":" + identity, info: info})
			node := knowl.OperatorWikiEntry{Path: relative, Name: entry.Name(), Kind: kind}
			if kind == operatorPageKind {
				node.PageID = knowl.PageID(strings.TrimSuffix(relative, markdownExt))
			}
			nodes[relative] = node
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	slices.SortFunc(entries, func(a, b operatorInventoryEntry) int { return strings.Compare(a.path, b.path) })
	return entries, nodes, nil
}
