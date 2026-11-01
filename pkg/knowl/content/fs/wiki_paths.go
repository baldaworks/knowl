package fs

import (
	"context"
	"sort"
	"strings"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
	"golang.org/x/text/unicode/norm"
)

// captureWikiPaths enriches the immutable page inputs using catalog bytes read
// under the same workspace lock. Directory names never imply membership.
func captureWikiPaths(ctx context.Context, pages, catalogs []knowl.PageSnapshot, maxBytes int) error {
	titles := make(map[string]string, len(catalogs))
	targets := make(map[string]bool, len(catalogs)+len(pages))
	for _, page := range pages {
		targets[strings.TrimPrefix(page.Path, workspaceWikiDir+"/")] = true
	}
	for _, catalog := range catalogs {
		path := strings.TrimPrefix(catalog.Path, workspaceWikiDir+"/")
		titles[path] = norm.NFC.String(strings.Join(strings.Fields(catalog.Title), " "))
		targets[path] = true
	}
	parents := make(map[string][]string)
	for _, catalog := range catalogs {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := strings.TrimPrefix(catalog.Path, workspaceWikiDir+"/")
		destinations, malformed := knowlwiki.IndexDestinations(catalog.Content, maxCatalogLinks)
		if malformed {
			return contentInvalidError(catalog.Path, "catalog.links_invalid")
		}
		for _, destination := range destinations {
			target, external, valid := knowlwiki.ResolveIndexDestination(path, destination)
			if !valid {
				return contentInvalidError(catalog.Path, "catalog.target_escape")
			}
			if external {
				continue
			}
			if !targets[target] {
				// Snapshot also serves diagnostics before publication repair. A
				// pending/broken link cannot contribute context to any captured
				// page; keep its validation in the existing lint/staging flow.
				continue
			}
			parents[target] = append(parents[target], path)
		}
	}
	// Memoize each catalog's semantic paths, and cap materialized context rather
	// than walking every graph placement anew for every content page.
	memo := make(map[string][]string, len(catalogs))
	state := make(map[string]uint8, len(catalogs))
	var pathsTo func(string) ([]string, error)
	pathsTo = func(path string) ([]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if state[path] == 1 {
			return nil, contentInvalidError(workspaceWikiDir+"/"+path, "catalog.cycle")
		}
		if state[path] == 2 {
			return memo[path], nil
		}
		state[path] = 1
		values := make(map[string]struct{})
		total := 0
		add := func(value string) error {
			if _, exists := values[value]; !exists {
				total += len(value)
				if total > maxBytes {
					return contentInvalidError(workspaceWikiDir+"/"+path, "catalog.context_limit")
				}
				values[value] = struct{}{}
			}
			return nil
		}
		if path == okfIndexFilename {
			if err := add(titles[path]); err != nil {
				return nil, err
			}
		}
		for _, parent := range parents[path] {
			prefixes, err := pathsTo(parent)
			if err != nil {
				return nil, err
			}
			for _, prefix := range prefixes {
				value := prefix
				if titles[path] != "" {
					if value != "" {
						value += " / "
					}
					value += titles[path]
				}
				if err := add(value); err != nil {
					return nil, err
				}
			}
		}
		result := make([]string, 0, len(values))
		for value := range values {
			if value != "" {
				result = append(result, value)
			}
		}
		sort.Strings(result)
		memo[path] = result
		state[path] = 2
		return result, nil
	}
	for path := range titles {
		if _, err := pathsTo(path); err != nil {
			return err
		}
	}
	for i := range pages {
		if err := ctx.Err(); err != nil {
			return err
		}
		values := make(map[string]struct{})
		total := 0
		for _, parent := range parents[strings.TrimPrefix(pages[i].Path, workspaceWikiDir+"/")] {
			for _, value := range memo[parent] {
				if _, exists := values[value]; !exists {
					total += len(value)
					if total > maxBytes {
						return contentInvalidError(pages[i].Path, "catalog.context_limit")
					}
					values[value] = struct{}{}
				}
			}
		}
		paths := make([]string, 0, len(values))
		for value := range values {
			paths = append(paths, value)
		}
		sort.Strings(paths)
		pages[i].WikiPaths = paths
	}
	return nil
}
