package fs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

func (workspace *Workspace) validateProspectivePlanLocked(scope knowl.ScopeRef, edits []prospectiveEdit, requiredSourceRef string, planSourceRefs []string) error {
	existingDocuments, err := workspace.currentWikiDocumentsLocked()
	if err != nil {
		return err
	}
	documents := make(map[string]string, len(existingDocuments)+len(edits))
	for target, content := range existingDocuments {
		documents[target] = content
	}
	for _, edit := range edits {
		documents[edit.Target] = edit.Content
	}
	pageTargets := make(map[knowl.PageID]struct{})
	for target := range documents {
		if pageID, ok := knowlwiki.PageIDFromPath(target); ok {
			pageTargets[pageID] = struct{}{}
		}
	}
	rawSources, err := workspace.acceptedRawSourcesLocked(scope)
	if err != nil {
		return err
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
			return contentInvalidError("<plan>", "citation.current_unknown")
		}
	}
	editedPages := make(map[string]struct{})
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
			if bundleRelative == okfIndexFilename && index.ObservedVersion != okf.Version {
				return contentInvalidError(edit.Target, string(okf.RuleIndexInvalid))
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
		editedPages[bundleRelative] = struct{}{}
		if err := validateOrdinaryPageEdit(edit.Target, pageID, edit.Content, rawRefs, pageTargets); err != nil {
			return err
		}
		if err := validatePageProvenance(
			edit.Target, edit.Content, existingDocuments[edit.Target], requiredSourceRef, planRefs, rawSources,
		); err != nil {
			return err
		}
	}
	return validateCatalogGraph(documents, editedPages, workspace.maxSourceBytes)
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
