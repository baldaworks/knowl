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

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

func (workspace *Workspace) validateProspectivePlanLocked(scope knowl.ScopeRef, edits []prospectiveEdit, requiredSourceRef string, planSourceRefs []string) ([]knowl.MaintenanceDiagnostic, error) {
	accepted, diagnostics, err := workspace.filterProspectivePlanLocked(scope, edits, requiredSourceRef, planSourceRefs)
	if err != nil {
		return nil, err
	}
	if len(accepted) != len(edits) {
		return nil, ErrPlanConflict
	}
	return diagnostics, nil
}

func (workspace *Workspace) filterProspectivePlanLocked(scope knowl.ScopeRef, edits []prospectiveEdit, requiredSourceRef string, planSourceRefs []string) ([]prospectiveEdit, []knowl.MaintenanceDiagnostic, error) {
	existingDocuments, err := workspace.currentWikiDocumentsLocked()
	if err != nil {
		return nil, nil, err
	}
	rawSources, err := workspace.acceptedRawSourcesLocked(scope)
	if err != nil {
		return nil, nil, err
	}
	rawRefs := make(map[string]struct{}, len(rawSources))
	for sourceRef := range rawSources {
		rawRefs[sourceRef] = struct{}{}
	}
	planRefs := make(map[string]struct{}, len(planSourceRefs))
	for _, sourceRef := range planSourceRefs {
		planRefs[sourceRef] = struct{}{}
	}
	if requiredSourceRef == "" && len(planSourceRefs) == 1 {
		requiredSourceRef = planSourceRefs[0]
	}
	if requiredSourceRef != "" {
		if _, exists := rawSources[requiredSourceRef]; !exists {
			return nil, nil, contentInvalidError("<plan>", "citation.current_unknown")
		}
	}
	originalTargets := make(map[string]struct{})
	if requiredSourceRef != "" {
		sourceContent, readErr := workspace.ReadSource(context.Background(), rawSources[requiredSourceRef], knowl.ReadLimits{Bytes: workspace.maxSourceBytes})
		if readErr != nil {
			return nil, nil, readErr
		}
		targets, _ := knowlwiki.MarkdownTargets(string(sourceContent))
		for _, target := range targets {
			originalTargets[target] = struct{}{}
		}
	}
	accepted := append([]prospectiveEdit(nil), edits...)
	sort.Slice(accepted, func(left, right int) bool { return accepted[left].Target < accepted[right].Target })
	diagnostics := make([]knowl.MaintenanceDiagnostic, 0)
	for {
		pageTargets := prospectivePageTargets(existingDocuments, accepted)
		filtered := make([]prospectiveEdit, 0, len(accepted))
		roundDiagnostics := make([]knowl.MaintenanceDiagnostic, 0)
		changed := false
		for _, edit := range accepted {
			bundleRelative := strings.TrimPrefix(edit.Target, workspaceWikiDir+"/")
			kind, classifyErr := okf.ClassifyPath(bundleRelative)
			if classifyErr != nil {
				return nil, nil, contentInvalidError(edit.Target, string(okf.RulePathInvalid))
			}
			if kind == okf.DocumentIndex {
				index, validateErr := okf.ValidateIndex(bundleRelative, []byte(edit.Content), okfLimits(len(edit.Content)))
				if validateErr != nil {
					return nil, nil, okfContentInvalidError(edit.Target, validateErr)
				}
				if bundleRelative == okfIndexFilename && index.ObservedVersion != okf.Version {
					return nil, nil, contentInvalidError(edit.Target, string(okf.RuleIndexInvalid))
				}
				filtered = append(filtered, edit)
				continue
			}
			if kind == okf.DocumentLog {
				return nil, nil, contentInvalidError(edit.Target, string(okf.RuleLogInvalid))
			}
			pageID, ok := knowlwiki.PageIDFromPath(edit.Target)
			if !ok {
				filtered = append(filtered, edit)
				continue
			}
			pageDiagnostics, validateErr := validateOrdinaryPageEdit(edit.Target, pageID, edit.Content, rawRefs, pageTargets, originalTargets)
			if validateErr == nil {
				validateErr = validatePageProvenance(edit.Target, edit.Content, existingDocuments[edit.Target], requiredSourceRef, planRefs, rawSources)
			}
			if validateErr != nil {
				var invalid *contentValidationError
				if !errors.As(validateErr, &invalid) {
					return nil, nil, validateErr
				}
				diagnostics = append(diagnostics, knowl.MaintenanceDiagnostic{Code: invalid.rule, Path: edit.Target})
				changed = true
				continue
			}
			roundDiagnostics = append(roundDiagnostics, pageDiagnostics...)
			filtered = append(filtered, edit)
		}
		accepted = filtered
		if !changed {
			diagnostics = append(diagnostics, roundDiagnostics...)
			break
		}
	}

	rejectedNew := make(map[string]struct{})
	acceptedTargets := make(map[string]struct{}, len(accepted))
	for _, edit := range accepted {
		acceptedTargets[edit.Target] = struct{}{}
	}
	for _, edit := range edits {
		_, accepted := acceptedTargets[edit.Target]
		_, existed := existingDocuments[edit.Target]
		if accepted || existed {
			continue
		}
		rejectedNew[edit.Target] = struct{}{}
	}
	for {
		filtered := make([]prospectiveEdit, 0, len(accepted))
		changed := false
		for _, edit := range accepted {
			bundleRelative := strings.TrimPrefix(edit.Target, workspaceWikiDir+"/")
			kind, _ := okf.ClassifyPath(bundleRelative)
			if kind != okf.DocumentIndex {
				filtered = append(filtered, edit)
				continue
			}
			rejectedTarget := rejectedCatalogDependency(bundleRelative, edit.Content, rejectedNew)
			if rejectedTarget == "" {
				filtered = append(filtered, edit)
				continue
			}
			diagnostics = append(diagnostics, knowl.MaintenanceDiagnostic{Code: knowl.DiagnosticCatalogDependencyReject, Path: edit.Target, Target: rejectedTarget})
			if existingDocuments[edit.Target] == "" {
				rejectedNew[edit.Target] = struct{}{}
			}
			changed = true
		}
		accepted = filtered
		if !changed {
			break
		}
	}
	if len(accepted) == 0 && len(edits) > 0 {
		return nil, nil, contentInvalidError("<plan>", "plan.empty_safe_subset")
	}
	documents := make(map[string]string, len(existingDocuments)+len(accepted))
	for target, content := range existingDocuments {
		documents[target] = content
	}
	editedPages := make(map[string]struct{})
	for _, edit := range accepted {
		documents[edit.Target] = edit.Content
		if pageID, ok := knowlwiki.PageIDFromPath(edit.Target); ok && pageID != "" {
			editedPages[strings.TrimPrefix(edit.Target, workspaceWikiDir+"/")] = struct{}{}
		}
	}
	if err := validateCatalogGraph(documents, editedPages, workspace.maxSourceBytes); err != nil {
		return nil, nil, err
	}
	diagnostics, err = app.NormalizeMaintenanceDiagnostics(diagnostics)
	if err != nil {
		return nil, nil, contentInvalidError("<plan>", "diagnostic.limit")
	}
	return accepted, diagnostics, nil
}

func prospectivePageTargets(existing map[string]string, edits []prospectiveEdit) map[knowl.PageID]struct{} {
	targets := make(map[knowl.PageID]struct{}, len(existing)+len(edits))
	for target := range existing {
		if pageID, ok := knowlwiki.PageIDFromPath(target); ok {
			targets[pageID] = struct{}{}
		}
	}
	for _, edit := range edits {
		if pageID, ok := knowlwiki.PageIDFromPath(edit.Target); ok {
			targets[pageID] = struct{}{}
		}
	}
	return targets
}

func rejectedCatalogDependency(catalogPath, content string, rejectedNew map[string]struct{}) string {
	destinations, malformed := knowlwiki.IndexDestinations(content, maxCatalogLinks)
	if malformed {
		return ""
	}
	for _, destination := range destinations {
		target, external, valid := knowlwiki.ResolveIndexDestination(catalogPath, destination)
		if !valid || external {
			continue
		}
		if _, rejected := rejectedNew[workspaceWikiDir+"/"+target]; rejected {
			return target
		}
	}
	return ""
}

func validatePageProvenance(target, content, existing, requiredSourceRef string, planRefs map[string]struct{}, rawSources map[string]knowl.AcceptedSource) error {
	metadata, err := knowlwiki.ParseFrontmatter(content)
	if err != nil {
		return contentInvalidError(target, "frontmatter.malformed")
	}
	currentRefs := make(map[string]struct{}, len(metadata.SourceRefs))
	for _, sourceRef := range metadata.SourceRefs {
		if sourceRef == "" {
			continue
		}
		currentRefs[sourceRef] = struct{}{}
		if _, covered := planRefs[sourceRef]; !covered {
			return contentInvalidError(target, "citation.plan_missing")
		}
	}
	if requiredSourceRef != "" {
		if _, cited := currentRefs[requiredSourceRef]; !cited {
			return contentInvalidError(target, "citation.current_missing")
		}
	}
	if existing == "" {
		return nil
	}
	previous, err := knowlwiki.ParseFrontmatter(existing)
	if err != nil {
		return contentInvalidError(target, "frontmatter.existing_malformed")
	}
	currentDocument := rawSources[requiredSourceRef].SourceDocument
	for _, sourceRef := range previous.SourceRefs {
		if _, retained := currentRefs[sourceRef]; retained {
			continue
		}
		previousDocument := rawSources[sourceRef].SourceDocument
		if currentDocument == (knowl.SourceDocument{}) || previousDocument == (knowl.SourceDocument{}) ||
			currentDocument.SourceID != previousDocument.SourceID || currentDocument.DocumentID != previousDocument.DocumentID ||
			currentDocument.Revision == previousDocument.Revision {
			return contentInvalidError(target, "citation.lineage_removed")
		}
	}
	return nil
}

const maxCatalogLinks = 4096

func validateCatalogGraph(documents map[string]string, editedPages map[string]struct{}, maxBytes int) error {
	bundleDocuments := make(map[string]string, len(documents))
	kinds := make(map[string]okf.DocumentKind, len(documents))
	documentPaths := make([]string, 0, len(documents))
	for target := range documents {
		documentPaths = append(documentPaths, target)
	}
	sort.Strings(documentPaths)
	for _, target := range documentPaths {
		content := documents[target]
		bundleRelative := strings.TrimPrefix(target, workspaceWikiDir+"/")
		kind, err := okf.ClassifyPath(bundleRelative)
		if err != nil {
			return contentInvalidError(target, string(okf.RulePathInvalid))
		}
		bundleDocuments[bundleRelative] = content
		kinds[bundleRelative] = kind
	}
	if kinds[okfIndexFilename] != okf.DocumentIndex {
		return contentInvalidError(canonicalIndexPath, "catalog.root_missing")
	}

	edges := make(map[string][]string)
	catalogPaths := make([]string, 0)
	for catalogPath, kind := range kinds {
		if kind != okf.DocumentIndex {
			continue
		}
		catalogPaths = append(catalogPaths, catalogPath)
	}
	sort.Strings(catalogPaths)
	for _, catalogPath := range catalogPaths {
		content := bundleDocuments[catalogPath]
		if _, err := okf.ValidateIndex(catalogPath, []byte(content), okfLimits(maxBytes)); err != nil {
			return okfContentInvalidError(workspaceWikiDir+"/"+catalogPath, err)
		}
		destinations, malformed := knowlwiki.IndexDestinations(content, maxCatalogLinks)
		if malformed {
			return contentInvalidError(workspaceWikiDir+"/"+catalogPath, "catalog.links_invalid")
		}
		for _, destination := range destinations {
			target, external, valid := knowlwiki.ResolveIndexDestination(catalogPath, destination)
			if !valid {
				return contentInvalidError(workspaceWikiDir+"/"+catalogPath, "catalog.target_escape")
			}
			if external {
				continue
			}
			targetKind, exists := kinds[target]
			if !exists || (targetKind != okf.DocumentIndex && targetKind != okf.DocumentConcept) {
				return contentInvalidError(workspaceWikiDir+"/"+catalogPath, "catalog.broken_target")
			}
			edges[catalogPath] = append(edges[catalogPath], target)
		}
		sort.Strings(edges[catalogPath])
	}
	state := make(map[string]uint8, len(catalogPaths))
	var visit func(string) error
	visit = func(catalog string) error {
		state[catalog] = 1
		for _, target := range edges[catalog] {
			if kinds[target] != okf.DocumentIndex {
				continue
			}
			switch state[target] {
			case 1:
				return contentInvalidError(workspaceWikiDir+"/"+catalog, "catalog.cycle")
			case 0:
				if err := visit(target); err != nil {
					return err
				}
			}
		}
		state[catalog] = 2
		return nil
	}
	for _, catalog := range catalogPaths {
		if state[catalog] == 0 {
			if err := visit(catalog); err != nil {
				return err
			}
		}
	}

	reachable := make(map[string]struct{})
	queue := []string{okfIndexFilename}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, seen := reachable[current]; seen {
			continue
		}
		reachable[current] = struct{}{}
		queue = append(queue, edges[current]...)
	}
	ordinaryPaths := make([]string, 0, len(kinds))
	for documentPath, kind := range kinds {
		if kind == okf.DocumentConcept {
			ordinaryPaths = append(ordinaryPaths, documentPath)
		}
	}
	sort.Strings(ordinaryPaths)
	for _, page := range ordinaryPaths {
		if _, exists := reachable[page]; !exists {
			rule := "catalog.reconciliation_required"
			if _, edited := editedPages[page]; edited {
				rule = "catalog.unreachable"
			}
			return contentInvalidError(workspaceWikiDir+"/"+page, rule)
		}
	}
	return nil
}

func validateOrdinaryPageEdit(target string, pageID knowl.PageID, content string, rawRefs map[string]struct{}, pageTargets map[knowl.PageID]struct{}, originalTargets map[string]struct{}) ([]knowl.MaintenanceDiagnostic, error) {
	bundleRelative := strings.TrimPrefix(target, workspaceWikiDir+"/")
	if _, err := okf.ParseConcept(bundleRelative, []byte(content), okfLimits(len(content))); err != nil {
		return nil, okfContentInvalidError(target, err)
	}
	metadata, err := knowlwiki.ParseFrontmatter(content)
	if err != nil {
		return nil, contentInvalidError(target, "frontmatter.malformed")
	}
	if metadata.ID == "" {
		return nil, contentInvalidError(target, "frontmatter.id_missing")
	}
	if metadata.ID != string(pageID) {
		return nil, contentInvalidError(target, "frontmatter.id_mismatch")
	}
	if metadata.Title == "" {
		return nil, contentInvalidError(target, "frontmatter.title_missing")
	}
	if metadata.Type == "" {
		return nil, contentInvalidError(target, "frontmatter.type_missing")
	}
	nonEmptySourceRefs := 0
	for _, sourceRef := range metadata.SourceRefs {
		if sourceRef == "" {
			continue
		}
		nonEmptySourceRefs++
		if _, exists := rawRefs[sourceRef]; !exists {
			return nil, contentInvalidError(target, "citation.unknown_source")
		}
	}
	if nonEmptySourceRefs == 0 {
		return nil, contentInvalidError(target, "citation.missing")
	}
	targets, malformed := knowlwiki.MarkdownTargets(content)
	if malformed {
		return nil, contentInvalidError(target, "link.malformed")
	}
	diagnostics := make([]knowl.MaintenanceDiagnostic, 0)
	for _, linkedTarget := range targets {
		if _, exists := pageTargets[knowl.PageID(linkedTarget)]; !exists {
			if _, original := originalTargets[linkedTarget]; !original {
				return nil, contentInvalidError(target, "link.broken")
			}
			diagnostics = append(diagnostics, knowl.MaintenanceDiagnostic{Code: knowl.DiagnosticOriginalLinkUnresolved, Path: target, Target: linkedTarget})
		}
	}
	return diagnostics, nil
}

func okfContentInvalidError(target string, err error) error {
	var invalid *okf.Violation
	if errors.As(err, &invalid) {
		return contentInvalidError(target, string(invalid.Rule))
	}
	return contentInvalidError(target, "okf.invalid")
}

func (workspace *Workspace) acceptedRawSourceKeysLocked(scope knowl.ScopeRef) (map[string]struct{}, error) {
	sources, err := workspace.acceptedRawSourcesLocked(scope)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]struct{}, len(sources))
	for sourceRef := range sources {
		keys[sourceRef] = struct{}{}
	}
	return keys, nil
}

func (workspace *Workspace) acceptedRawSourcesLocked(scope knowl.ScopeRef) (map[string]knowl.AcceptedSource, error) {
	records, err := workspace.inspectRawSourcesLocked(scope)
	if err != nil {
		return nil, err
	}
	sources := make(map[string]knowl.AcceptedSource, len(records))
	for _, record := range records {
		if record.Valid {
			sources[sourceRefKey(record.Source)] = record.Source
		}
	}
	return sources, nil
}

func (workspace *Workspace) currentWikiDocumentsLocked() (map[string]string, error) {
	wikiRoot := filepath.Join(workspace.root, workspaceWikiDir)
	if err := rejectSymlinkPath(workspace.root, wikiRoot); err != nil {
		return nil, err
	}
	documents := make(map[string]string)
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
		relative = filepath.ToSlash(relative)
		if relative == canonicalLogPath {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		documents[relative] = string(content)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enumerate canonical pages: %w", err)
	}
	return documents, nil
}

func (workspace *Workspace) currentPageTargetsLocked() (map[knowl.PageID]struct{}, error) {
	documents, err := workspace.currentWikiDocumentsLocked()
	if err != nil {
		return nil, err
	}
	targets := make(map[knowl.PageID]struct{}, len(documents))
	for target := range documents {
		if pageID, ok := knowlwiki.PageIDFromPath(target); ok {
			targets[pageID] = struct{}{}
		}
	}
	return targets, nil
}
