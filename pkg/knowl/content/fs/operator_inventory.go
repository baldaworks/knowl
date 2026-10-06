package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const (
	operatorRootID   = "index"
	operatorPageKind = "page"
)

type operatorInventoryEntry struct {
	path     string
	identity string
	info     os.FileInfo
}

// PageSummaries inventories factual file metadata and materializes only the
// selected keyset window. Raw source bodies are never involved in polling.
func (workspace *Workspace) PageSummaries(ctx context.Context, scope knowl.ScopeRef, options app.OperatorReadOptions) (app.OperatorReadPage[knowl.OperatorPageSummary], error) {
	result := app.OperatorReadPage[knowl.OperatorPageSummary]{Items: []knowl.OperatorPageSummary{}}
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
		return result, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
	}
	defer wiki.close()
	entries, err := operatorPageInventory(ctx, wiki)
	if err != nil {
		return result, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
	}
	result.SnapshotVersion = operatorInventoryVersion(scope, "pages", "", entries)
	if err := operatorSnapshotValid(options, result.SnapshotVersion); err != nil {
		return result, err
	}
	selected, next := operatorWindow(entries, options)
	remaining := app.DefaultCatalogLimits().MaxSnapshotBytes
	for _, entry := range selected {
		if err := contextErr(ctx); err != nil {
			return result, err
		}
		limits, err := operatorRemainingLimits(options.ReadLimits, remaining)
		if err != nil {
			return result, err
		}
		page, info, err := workspace.operatorPage(wiki, entry.path, limits)
		if err != nil {
			return result, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
		}
		if !operatorFileUnchanged(entry.info, info) {
			return result, app.ErrOperatorSnapshotChanged
		}
		remaining -= len(page.Content)
		result.Items = append(result.Items, knowl.OperatorPageSummary{ID: page.ID, Title: page.Title, Description: page.OKF.Description, Digest: page.Digest, Version: page.Digest, UpdatedAt: page.UpdatedAt})
	}
	result.NextKey = next
	return result, nil
}

// CatalogChildren returns only the links explicitly present in one canonical
// index. Directories, including sources, never become implicit catalogs.
func (workspace *Workspace) CatalogChildren(ctx context.Context, scope knowl.ScopeRef, parent knowl.PageID, options app.OperatorReadOptions) (app.OperatorCatalogRead, error) {
	result := app.OperatorCatalogRead{Children: app.OperatorReadPage[knowl.OperatorCatalogChild]{Items: []knowl.OperatorCatalogChild{}}}
	if err := operatorOptionsValid(options); err != nil {
		return result, err
	}
	if parent == "" {
		parent = operatorRootID
	}
	relative, err := operatorPagePath(parent, okf.DocumentIndex)
	if err != nil {
		return result, err
	}
	unlock, err := workspace.operatorLock(ctx, scope)
	if err != nil {
		return result, err
	}
	defer unlock()
	wiki, err := openReadRoot(workspace.root, workspaceWikiDir)
	if err != nil {
		return result, operatorReadError(err, app.ErrOperatorCatalogNotFound)
	}
	defer wiki.close()
	bounds := app.DefaultCatalogLimits()
	parentEntry, err := operatorStat(wiki, relative)
	if err != nil {
		return result, operatorReadError(err, app.ErrOperatorCatalogNotFound)
	}
	content, parentInfo, err := wiki.read(relative, options.ReadLimits, min(workspace.maxSourceBytes, bounds.MaxCatalogBytes))
	if err != nil {
		return result, operatorReadError(err, app.ErrOperatorCatalogNotFound)
	}
	if !operatorFileUnchanged(parentEntry.info, parentInfo) {
		return result, app.ErrOperatorSnapshotChanged
	}
	index, err := okf.ValidateIndex(relative, content, okfLimits(bounds.MaxCatalogBytes))
	if err != nil {
		return result, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
	}
	result.Parent = knowl.OperatorCatalogSummary{ID: parent, Title: markdownTitle([]byte(index.Body))}
	entries, err := operatorCatalogInventory(ctx, wiki, relative, index.Body)
	if err != nil {
		return result, err
	}
	result.Children.SnapshotVersion = operatorInventoryVersion(scope, relative, digestBytes(content), append([]operatorInventoryEntry{parentEntry}, entries...))
	if err := operatorSnapshotValid(options, result.Children.SnapshotVersion); err != nil {
		return result, err
	}
	selected, next := operatorWindow(entries, options)
	remaining := bounds.MaxSnapshotBytes - len(content)
	for _, entry := range selected {
		if err := contextErr(ctx); err != nil {
			return result, err
		}
		limits, err := operatorRemainingLimits(options.ReadLimits, remaining)
		if err != nil {
			return result, err
		}
		child, size, err := workspace.operatorCatalogChild(wiki, entry, limits)
		if err != nil {
			return result, err
		}
		remaining -= size
		result.Children.Items = append(result.Children.Items, child)
	}
	result.Children.NextKey = next
	return result, nil
}

func operatorPageInventory(ctx context.Context, wiki *readRoot) ([]operatorInventoryEntry, error) {
	entries := make([]operatorInventoryEntry, 0)
	err := wiki.walk(func(relative string, entry os.DirEntry) error {
		if err := contextErr(ctx); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		kind, err := okf.ClassifyPath(relative)
		if err != nil {
			return err
		}
		if kind != okf.DocumentConcept {
			return nil
		}
		item, err := operatorStat(wiki, relative)
		if err != nil {
			return err
		}
		entries = append(entries, item)
		return nil
	})
	slices.SortFunc(entries, func(a, b operatorInventoryEntry) int {
		return strings.Compare(strings.TrimSuffix(a.path, markdownExt), strings.TrimSuffix(b.path, markdownExt))
	})
	return entries, err
}

func operatorCatalogInventory(ctx context.Context, wiki *readRoot, parent, body string) ([]operatorInventoryEntry, error) {
	destinations, malformed := knowlwiki.IndexDestinations(body, maxCatalogLinks)
	if malformed {
		return nil, app.ErrOperatorReadLimitExceeded
	}
	entries := make([]operatorInventoryEntry, 0, len(destinations))
	seen := make(map[string]bool)
	for _, destination := range destinations {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		relative, external, valid := knowlwiki.ResolveIndexDestination(parent, destination)
		if !valid {
			return nil, app.ErrOperatorWorkspaceUnavailable
		}
		if external || seen[relative] {
			continue
		}
		// Domain resolution handles legitimate relative links, but encoded paths
		// are not canonical identities and must not become routes after decoding.
		if strings.Contains(destination, "%") {
			return nil, app.ErrOperatorWorkspaceUnavailable
		}
		if !validReadPath(relative) {
			return nil, app.ErrOperatorWorkspaceUnavailable
		}
		item, err := operatorStat(wiki, relative)
		if err != nil {
			return nil, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
		}
		seen[relative] = true
		entries = append(entries, item)
	}
	slices.SortFunc(entries, func(a, b operatorInventoryEntry) int {
		return strings.Compare(strings.TrimSuffix(a.path, markdownExt), strings.TrimSuffix(b.path, markdownExt))
	})
	return entries, nil
}

func (workspace *Workspace) operatorCatalogChild(wiki *readRoot, entry operatorInventoryEntry, limits knowl.ReadLimits) (knowl.OperatorCatalogChild, int, error) {
	child := knowl.OperatorCatalogChild{ID: knowl.PageID(strings.TrimSuffix(entry.path, markdownExt)), Kind: operatorPageKind}
	kind, err := okf.ClassifyPath(entry.path)
	if err != nil {
		return child, 0, app.ErrOperatorWorkspaceUnavailable
	}
	if kind == okf.DocumentConcept {
		page, info, err := workspace.operatorPage(wiki, entry.path, limits)
		if err != nil {
			return child, 0, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
		}
		if !operatorFileUnchanged(entry.info, info) {
			return child, 0, app.ErrOperatorSnapshotChanged
		}
		child.Title, child.Description = page.Title, page.OKF.Description
		return child, len(page.Content), nil
	}
	content, info, err := wiki.read(entry.path, limits, min(workspace.maxSourceBytes, app.DefaultCatalogLimits().MaxCatalogBytes))
	if err != nil {
		return child, 0, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
	}
	if !operatorFileUnchanged(entry.info, info) {
		return child, 0, app.ErrOperatorSnapshotChanged
	}
	index, err := okf.ValidateIndex(entry.path, content, okfLimits(app.DefaultCatalogLimits().MaxCatalogBytes))
	if err != nil {
		return child, 0, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
	}
	child.Kind, child.Title = "catalog", markdownTitle([]byte(index.Body))
	return child, len(content), nil
}

func operatorStat(root *readRoot, relative string) (operatorInventoryEntry, error) {
	file, err := root.open(relative, false)
	if err != nil {
		return operatorInventoryEntry{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return operatorInventoryEntry{}, err
	}
	if !info.Mode().IsRegular() {
		return operatorInventoryEntry{}, ErrPathRejected
	}
	identity, err := operatorFileIdentity(file)
	return operatorInventoryEntry{path: relative, identity: identity, info: info}, err
}

func operatorFileUnchanged(before, after os.FileInfo) bool {
	return os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func operatorInventoryVersion(scope knowl.ScopeRef, endpoint, parentDigest string, entries []operatorInventoryEntry) string {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	_ = encoder.Encode([]string{"operator-inventory-v1", string(scope), endpoint, parentDigest})
	for _, entry := range entries {
		// Explicit stable facts: Sys contains atime on some platforms and cannot be
		// serialized wholesale without invalidating cursors on a read.
		_ = encoder.Encode([]any{entry.path, entry.identity, entry.info.Size(), entry.info.ModTime().UTC().UnixNano()})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func operatorWindow(entries []operatorInventoryEntry, options app.OperatorReadOptions) ([]operatorInventoryEntry, string) {
	start, _ := slices.BinarySearchFunc(entries, options.Continuation.Key, func(entry operatorInventoryEntry, key string) int {
		return strings.Compare(strings.TrimSuffix(entry.path, markdownExt), key)
	})
	if start < len(entries) && strings.TrimSuffix(entries[start].path, markdownExt) == options.Continuation.Key {
		start++
	}
	end := min(start+options.Limit, len(entries))
	next := ""
	if end < len(entries) {
		next = strings.TrimSuffix(entries[end-1].path, markdownExt)
	}
	return entries[start:end], next
}

func operatorOptionsValid(options app.OperatorReadOptions) error {
	if options.Limit < 1 || options.Limit > 100 {
		return app.ErrOperatorLimitInvalid
	}
	if options.Continuation.Key != "" && options.Continuation.SnapshotVersion == "" {
		return app.ErrOperatorCursorInvalid
	}
	return nil
}

func operatorSnapshotValid(options app.OperatorReadOptions, version string) error {
	if options.Continuation.SnapshotVersion != "" && options.Continuation.SnapshotVersion != version {
		return app.ErrOperatorSnapshotChanged
	}
	return nil
}

func operatorRemainingLimits(limits knowl.ReadLimits, remaining int) (knowl.ReadLimits, error) {
	if remaining <= 0 {
		return limits, app.ErrOperatorReadLimitExceeded
	}
	if limits.Bytes <= 0 || remaining < limits.Bytes {
		limits.Bytes = remaining
	}
	return limits, nil
}
