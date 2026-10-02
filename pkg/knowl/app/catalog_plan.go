package app

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// ValidateMaintenancePlan renders bounded navigation intents from authoritative
// originals before validating the combined concrete file edits. StagePlan owns
// prospective canonical validation and digest preconditions.
func ValidateMaintenancePlan(ctx context.Context, input knowl.MaintenanceInput, model knowl.ModelEditPlan, inspection knowl.WorkspaceInspection, catalogLimits knowl.CatalogLimits, planLimits PlanLimits) (knowl.ValidatedEditPlan, error) {
	limits, err := NormalizeCatalogLimits(catalogLimits)
	if err != nil {
		return knowl.ValidatedEditPlan{}, err
	}
	for _, edit := range model.Edits {
		if path.Base(edit.Path) == hierarchyCatalogName {
			return knowl.ValidatedEditPlan{}, ErrForbiddenEdit
		}
	}
	ordinary, err := ValidatePlan(ctx, input, model, planLimits)
	if err != nil {
		return knowl.ValidatedEditPlan{}, err
	}
	graph, err := catalogGraph(inspection.Catalogs, limits)
	if err != nil {
		return knowl.ValidatedEditPlan{}, err
	}
	if len(model.CatalogAdditions) > limits.MaxCatalogs {
		return knowl.ValidatedEditPlan{}, ErrPlanLimitExceeded
	}
	originals := make(map[string]knowl.PageSnapshot, len(inspection.Catalogs))
	known := make(map[string]bool)
	for _, snapshot := range inspection.Catalogs {
		originals[snapshot.Path] = snapshot
		known[snapshot.Path] = true
	}
	for _, page := range inspection.Snapshot.Pages {
		known[page.Path] = true
	}
	for _, edit := range ordinary.Edits {
		known[edit.Path] = true
	}
	additions := make(map[string]knowl.CatalogAddition, len(model.CatalogAdditions))
	for _, addition := range model.CatalogAdditions {
		if err := contextErr(ctx); err != nil {
			return knowl.ValidatedEditPlan{}, err
		}
		if len(addition.Path) > limits.MaxPathBytes {
			return knowl.ValidatedEditPlan{}, ErrPlanLimitExceeded
		}
		canonical, err := validateEditPath(addition.Path)
		if err != nil {
			return knowl.ValidatedEditPlan{}, err
		}
		_, kind, err := validateHierarchyWikiPath(canonical, limits.MaxPathBytes)
		if err != nil || kind != okf.DocumentIndex {
			return knowl.ValidatedEditPlan{}, ErrForbiddenEdit
		}
		if _, duplicate := additions[canonical]; duplicate {
			return knowl.ValidatedEditPlan{}, ErrPlanInvalid
		}
		original, existing := originals[canonical]
		if existing {
			if addition.ExpectedDigest != original.Digest || addition.Title != "" {
				return knowl.ValidatedEditPlan{}, ErrPlanInvalid
			}
		} else if addition.ExpectedDigest != "" || !validHierarchyText(addition.Title, true) || len(addition.Children) == 0 {
			return knowl.ValidatedEditPlan{}, ErrPlanInvalid
		}
		if len(addition.Title) > limits.MaxCatalogBytes {
			return knowl.ValidatedEditPlan{}, ErrPlanLimitExceeded
		}
		children := make(map[string]bool)
		for _, child := range addition.Children {
			if len(child) > limits.MaxPathBytes {
				return knowl.ValidatedEditPlan{}, ErrPlanLimitExceeded
			}
			canonicalChild, err := validateEditPath(child)
			if err != nil {
				return knowl.ValidatedEditPlan{}, err
			}
			_, kind, err := validateHierarchyWikiPath(canonicalChild, limits.MaxPathBytes)
			if err != nil || (kind != okf.DocumentIndex && kind != okf.DocumentConcept) {
				return knowl.ValidatedEditPlan{}, ErrForbiddenEdit
			}
			children[canonicalChild] = true
			if len(children) > limits.MaxEdges {
				return knowl.ValidatedEditPlan{}, ErrPlanLimitExceeded
			}
		}
		addition.Children = make([]string, 0, len(children))
		for child := range children {
			addition.Children = append(addition.Children, child)
		}
		sort.Strings(addition.Children)
		additions[canonical] = addition
		known[canonical] = true
	}
	paths := make([]string, 0, len(additions))
	for catalog := range additions {
		paths = append(paths, catalog)
	}
	sort.Strings(paths)
	memberships := make(map[string]map[string]bool, len(graph))
	for _, node := range graph {
		memberships[node.Path] = make(map[string]bool, len(node.Children))
		for _, child := range node.Children {
			memberships[node.Path][child] = true
		}
	}
	combined := model
	combined.Edits = append([]knowl.FileEdit(nil), ordinary.Edits...)
	combined.CatalogAdditions = nil
	for _, catalog := range paths {
		addition := additions[catalog]
		original, existing := originals[catalog]
		content := original.Content
		if !existing {
			content = "# " + escapeMarkdownText(addition.Title) + "\n"
		}
		changed := !existing
		for _, child := range addition.Children {
			if !known[child] {
				return knowl.ValidatedEditPlan{}, ErrPlanInvalid
			}
			if memberships[catalog][child] {
				continue
			}
			rel, err := filepath.Rel(filepath.Dir(catalog), child)
			if err != nil {
				return knowl.ValidatedEditPlan{}, ErrPlanInvalid
			}
			label := strings.TrimSuffix(path.Base(child), ".md")
			if node, ok := originals[child]; ok && node.Title != "" {
				label = node.Title
			} else if node, ok := additions[child]; ok {
				label = node.Title
			}
			line := "\n* [" + escapeMarkdownLabel(label) + "](" + escapeHierarchyDestination(filepath.ToSlash(rel)) + ")\n"
			if len(line) > limits.MaxCatalogBytes-len(content) {
				return knowl.ValidatedEditPlan{}, ErrPlanLimitExceeded
			}
			content += line
			changed = true
		}
		if changed {
			combined.Edits = append(combined.Edits, knowl.FileEdit{Path: catalog, ExpectedDigest: original.Digest, Content: []byte(content)})
			original.Path = catalog
			original.Content = content
			original.Digest = digestHierarchyBytes([]byte(content))
			if !existing {
				original.Title = addition.Title
			}
			originals[catalog] = original
		}
	}
	final := make([]knowl.PageSnapshot, 0, len(originals))
	for _, original := range originals {
		final = append(final, original)
	}
	finalGraph, err := catalogGraph(final, limits)
	if err != nil {
		return knowl.ValidatedEditPlan{}, err
	}
	// An appended link must actually be navigable, even if the original ends in
	// an unclosed code fence or another construct that hides appended Markdown.
	for _, node := range finalGraph {
		if addition, changed := additions[node.Path]; changed {
			children := make(map[string]bool, len(node.Children))
			for _, child := range node.Children {
				children[child] = true
			}
			for _, child := range addition.Children {
				if !children[child] {
					return knowl.ValidatedEditPlan{}, fmt.Errorf("catalog addition is not visible: %w", ErrPlanInvalid)
				}
			}
		}
	}
	nodes := make(map[string][]string, len(finalGraph))
	for _, node := range finalGraph {
		nodes[node.Path] = node.Children
	}
	reachable := make(map[string]bool)
	queue := []string{rootCatalogPath}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if reachable[current] {
			continue
		}
		reachable[current] = true
		queue = append(queue, nodes[current]...)
	}
	for _, addition := range model.CatalogAdditions {
		if _, existing := memberships[addition.Path]; !existing && !reachable[addition.Path] {
			return knowl.ValidatedEditPlan{}, ErrPlanInvalid
		}
	}
	for _, edit := range ordinary.Edits {
		if !reachable[edit.Path] {
			return knowl.ValidatedEditPlan{}, ErrPlanInvalid
		}
	}
	return ValidatePlan(ctx, input, combined, planLimits)
}
