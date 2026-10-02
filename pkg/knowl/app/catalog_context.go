package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
)

// DefaultCatalogLimits returns finite local catalog-context ceilings. These
// limits are independent of the number of selected factual pages.
func DefaultCatalogLimits() knowl.CatalogLimits {
	h := DefaultHierarchyLimits()
	return knowl.CatalogLimits{MaxCatalogs: h.MaxCatalogs, MaxEdges: h.MaxEdges, MaxDepth: h.MaxDepth,
		MaxPathBytes: h.MaxPathBytes, MaxCatalogBytes: h.MaxCatalogBytes, MaxSnapshotBytes: defaultReadBytes, MaxInputBytes: h.MaxInputBytes}
}

// NormalizeCatalogLimits supplies defaults only for an entirely zero value.
func NormalizeCatalogLimits(limits knowl.CatalogLimits) (knowl.CatalogLimits, error) {
	if limits == (knowl.CatalogLimits{}) {
		limits = DefaultCatalogLimits()
	}
	if limits.MaxCatalogs <= 0 || limits.MaxEdges <= 0 || limits.MaxDepth <= 0 || limits.MaxPathBytes <= 0 || limits.MaxPathBytes > maxEditPathBytes || limits.MaxCatalogBytes <= 0 || limits.MaxSnapshotBytes <= 0 || limits.MaxInputBytes <= 0 {
		return knowl.CatalogLimits{}, fmt.Errorf("invalid catalog limits: %w", ErrPlanInvalid)
	}
	return limits, nil
}

func catalogGraph(snapshots []knowl.PageSnapshot, limits knowl.CatalogLimits) ([]knowl.HierarchyCatalog, error) {
	var err error
	limits, err = NormalizeCatalogLimits(limits)
	if err != nil {
		return nil, err
	}
	if len(snapshots) > limits.MaxCatalogs {
		return nil, ErrPlanLimitExceeded
	}
	graph := make([]knowl.HierarchyCatalog, 0, len(snapshots))
	seen := make(map[string]bool, len(snapshots))
	bytes := 0
	edges := 0
	for _, snapshot := range snapshots {
		if len(snapshot.Path) > limits.MaxPathBytes || len(snapshot.Content) > limits.MaxCatalogBytes || len(snapshot.Content) > limits.MaxSnapshotBytes-bytes {
			return nil, ErrPlanLimitExceeded
		}
		canonical, kind, pathErr := validateHierarchyWikiPath(snapshot.Path, limits.MaxPathBytes)
		if pathErr != nil || kind != okf.DocumentIndex || seen[canonical] || !validSHA256Text(snapshot.Digest) || !utf8.ValidString(snapshot.Title) || !utf8.ValidString(snapshot.Content) {
			return nil, ErrPlanInvalid
		}
		seen[canonical] = true
		bytes += len(snapshot.Content)
		// Original bytes already bound the parser. Graph limits count unique internal
		// edges, so external links and duplicate Markdown destinations do not consume it.
		destinations, malformed := wiki.IndexDestinations(snapshot.Content, len(snapshot.Content)+1)
		if malformed {
			return nil, ErrPlanInvalid
		}
		children := make(map[string]bool)
		for _, destination := range destinations {
			target, external, valid := wiki.ResolveIndexDestination(strings.TrimPrefix(canonical, "wiki/"), destination)
			if !valid {
				return nil, ErrPlanInvalid
			}
			if external {
				continue
			}
			child := "wiki/" + target
			if len(child) > limits.MaxPathBytes {
				return nil, ErrPlanLimitExceeded
			}
			children[child] = true
		}
		edges += len(children)
		if edges > limits.MaxEdges {
			return nil, ErrPlanLimitExceeded
		}
		node := knowl.HierarchyCatalog{Path: canonical, Digest: snapshot.Digest, Title: snapshot.Title, Children: make([]string, 0, len(children))}
		for child := range children {
			node.Children = append(node.Children, child)
		}
		sort.Strings(node.Children)
		graph = append(graph, node)
	}
	for _, node := range graph {
		for _, child := range node.Children {
			kind, _ := okf.ClassifyPath(strings.TrimPrefix(child, "wiki/"))
			if kind == okf.DocumentIndex && !seen[child] {
				return nil, ErrPlanInvalid
			}
		}
	}
	if !seen[rootCatalogPath] {
		return nil, ErrPlanInvalid
	}
	sort.Slice(graph, func(i, j int) bool { return hierarchyCatalogPathLess(graph[i].Path, graph[j].Path) })
	if err := validateCatalogDepth(graph, limits.MaxDepth); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(graph)
	if err != nil {
		return nil, ErrPlanInvalid
	}
	if len(encoded) > limits.MaxInputBytes {
		return nil, ErrPlanLimitExceeded
	}
	return graph, nil
}

func validateCatalogDepth(graph []knowl.HierarchyCatalog, maxDepth int) error {
	nodes := make(map[string][]string, len(graph))
	for _, node := range graph {
		nodes[node.Path] = node.Children
	}
	visiting := make(map[string]bool, len(graph))
	depths := make(map[string]int, len(graph))
	var visit func(string, int) (int, error)
	visit = func(path string, ancestors int) (int, error) {
		if ancestors > maxDepth {
			return 0, ErrPlanLimitExceeded
		}
		if visiting[path] {
			return 0, ErrPlanInvalid
		}
		if depth := depths[path]; depth > 0 {
			return depth, nil
		}
		visiting[path] = true
		depth := 1
		for _, child := range nodes[path] {
			if _, catalog := nodes[child]; !catalog {
				continue
			}
			childDepth, err := visit(child, ancestors+1)
			if err != nil {
				return 0, err
			}
			depth = max(depth, childDepth+1)
		}
		visiting[path] = false
		if depth > maxDepth {
			return 0, ErrPlanLimitExceeded
		}
		depths[path] = depth
		return depth, nil
	}
	for _, node := range graph {
		if _, err := visit(node.Path, 1); err != nil {
			return err
		}
	}
	return nil
}
