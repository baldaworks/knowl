package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	hierarchyRootPath       = "wiki/index.md"
	hierarchyCatalogPrefix  = "wiki/catalogs/"
	hierarchyCatalogName    = "index.md"
	maxHierarchyInputBytes  = 64 << 20
	maxHierarchyPlanBytes   = 16 << 20
	maxHierarchyCatalogs    = 4096
	maxHierarchyPages       = 100_000
	maxHierarchyEdges       = 1_000_000
	maxHierarchyGraphDepth  = 256
	maxHierarchyExcerptRune = 64 << 10
	maxHierarchyManifest    = 1 << 20
)

var (
	// ErrHierarchyPlanInvalid reports malformed or incomplete semantic output.
	ErrHierarchyPlanInvalid = errors.New("invalid hierarchy plan")
	// ErrHierarchyDigestMismatch reports a plan made for other canonical inputs.
	ErrHierarchyDigestMismatch = errors.New("hierarchy plan digest mismatch")
	// ErrHierarchyLimitExceeded reports a configured hierarchy bound violation.
	ErrHierarchyLimitExceeded = errors.New("hierarchy plan exceeds a limit")
	// ErrHierarchyForbiddenPath reports a path outside the managed catalog boundary.
	ErrHierarchyForbiddenPath = errors.New("hierarchy plan targets a forbidden path")
)

// HierarchyValidationOptions contains application-only policy that must not be
// exposed as semantic provider context.
type HierarchyValidationOptions struct {
	// ForbiddenCatalogTerms are configured source IDs or source-native namespace
	// names. A nested catalog path segment or complete title may not equal one.
	ForbiddenCatalogTerms []string
}

// DefaultHierarchyLimits returns bounded defaults large enough for local
// multi-source workspaces while remaining below repository hard ceilings.
func DefaultHierarchyLimits() knowl.HierarchyLimits {
	return knowl.HierarchyLimits{
		MaxPages:             1024,
		MaxCatalogs:          1024,
		MaxEdges:             16_384,
		MaxDepth:             16,
		MaxInputBytes:        4 << 20,
		MaxExcerptCharacters: 4 << 10,
		MaxPlanBytes:         1 << 20,
		MaxEdits:             1024,
		MaxPathBytes:         maxEditPathBytes,
		MaxCatalogBytes:      256 << 10,
		MaxManifestBytes:     1 << 20,
	}
}

// ValidateHierarchyPlan validates a complete semantic graph and renders the
// desired managed OKF catalogs. It never accepts provider-supplied file bytes.
func ValidateHierarchyPlan(ctx context.Context, input knowl.HierarchyInput, model knowl.HierarchyModelPlan, options HierarchyValidationOptions) (knowl.ValidatedHierarchyPlan, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.ValidatedHierarchyPlan{}, err
	}
	normalizedInput, pages, currentCatalogs, err := prepareHierarchyInput(input)
	if err != nil {
		return knowl.ValidatedHierarchyPlan{}, err
	}
	input = normalizedInput
	if model.SchemaDigest != input.SchemaDigest || model.SnapshotDigest != input.SnapshotDigest {
		return knowl.ValidatedHierarchyPlan{}, ErrHierarchyDigestMismatch
	}
	if encoded, marshalErr := json.Marshal(model); marshalErr != nil {
		return knowl.ValidatedHierarchyPlan{}, fmt.Errorf("marshal hierarchy model plan: %w", ErrHierarchyPlanInvalid)
	} else if len(encoded) > input.Limits.MaxPlanBytes {
		return knowl.ValidatedHierarchyPlan{}, fmt.Errorf("hierarchy plan bytes %d exceed %d: %w", len(encoded), input.Limits.MaxPlanBytes, ErrHierarchyLimitExceeded)
	}

	catalogs, err := normalizeDesiredCatalogs(model.Catalogs, input.Limits, options)
	if err != nil {
		return knowl.ValidatedHierarchyPlan{}, err
	}
	if err := validateHierarchyGraph(catalogs, pages, input.MinRootCatalogs, input.Limits); err != nil {
		return knowl.ValidatedHierarchyPlan{}, err
	}
	mutations, err := renderHierarchyMutations(catalogs, pages, currentCatalogs, input.Limits)
	if err != nil {
		return knowl.ValidatedHierarchyPlan{}, err
	}
	return knowl.ValidatedHierarchyPlan{
		Scope:          input.Scope,
		SchemaDigest:   input.SchemaDigest,
		SnapshotDigest: input.SnapshotDigest,
		Catalogs:       catalogs,
		Mutations:      mutations,
	}, nil
}

// NormalizeHierarchyInput validates and deterministically orders the complete
// bounded semantic input before it crosses the provider trust boundary.
func NormalizeHierarchyInput(input knowl.HierarchyInput) (knowl.HierarchyInput, error) {
	normalized, _, _, err := prepareHierarchyInput(input)
	return normalized, err
}

func prepareHierarchyInput(input knowl.HierarchyInput) (knowl.HierarchyInput, map[string]knowl.HierarchyPage, map[string]knowl.HierarchyCatalog, error) {
	if err := validateHierarchyLimits(input.Limits); err != nil {
		return knowl.HierarchyInput{}, nil, nil, err
	}
	if strings.TrimSpace(string(input.Scope)) == "" || !validSHA256Text(input.SchemaDigest) || !validSHA256Text(input.SnapshotDigest) {
		return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("hierarchy identity is incomplete: %w", ErrHierarchyPlanInvalid)
	}
	if input.MinRootCatalogs < 0 || input.MinRootCatalogs > input.Limits.MaxCatalogs {
		return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("minimum root catalog count %d: %w", input.MinRootCatalogs, ErrHierarchyLimitExceeded)
	}
	normalized, pages, catalogs, err := normalizeHierarchyInput(input)
	if err != nil {
		return knowl.HierarchyInput{}, nil, nil, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("marshal hierarchy input: %w", ErrHierarchyPlanInvalid)
	}
	if len(encoded) > input.Limits.MaxInputBytes {
		return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("hierarchy input bytes %d exceed %d: %w", len(encoded), input.Limits.MaxInputBytes, ErrHierarchyLimitExceeded)
	}
	return normalized, pages, catalogs, nil
}

// HierarchyPlanDigest returns a deterministic SHA-256 digest of a normalized
// validated hierarchy plan.
func HierarchyPlanDigest(plan knowl.ValidatedHierarchyPlan) (string, error) {
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("marshal validated hierarchy plan: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// IsManagedHierarchyCatalog reports whether a canonical workspace path is
// owned by hierarchy reconciliation.
func IsManagedHierarchyCatalog(candidate string) bool {
	if candidate == hierarchyRootPath {
		return true
	}
	if !strings.HasPrefix(candidate, hierarchyCatalogPrefix) || path.Base(candidate) != hierarchyCatalogName {
		return false
	}
	relative := strings.TrimPrefix(candidate, "wiki/")
	kind, err := okf.ClassifyPath(relative)
	return err == nil && kind == okf.DocumentIndex
}

func validateHierarchyLimits(limits knowl.HierarchyLimits) error {
	if limits.MaxPages <= 0 || limits.MaxPages > maxHierarchyPages ||
		limits.MaxCatalogs <= 0 || limits.MaxCatalogs > maxHierarchyCatalogs ||
		limits.MaxEdges <= 0 || limits.MaxEdges > maxHierarchyEdges ||
		limits.MaxDepth <= 0 || limits.MaxDepth > maxHierarchyGraphDepth ||
		limits.MaxInputBytes <= 0 || limits.MaxInputBytes > maxHierarchyInputBytes ||
		limits.MaxExcerptCharacters <= 0 || limits.MaxExcerptCharacters > maxHierarchyExcerptRune ||
		limits.MaxPlanBytes <= 0 || limits.MaxPlanBytes > maxHierarchyPlanBytes ||
		limits.MaxEdits <= 0 || limits.MaxEdits > maxHierarchyCatalogs+1 ||
		limits.MaxPathBytes <= 0 || limits.MaxPathBytes > maxEditPathBytes ||
		limits.MaxCatalogBytes <= 0 || limits.MaxCatalogBytes > 64<<20 ||
		limits.MaxManifestBytes <= 0 || limits.MaxManifestBytes > maxHierarchyManifest {
		return fmt.Errorf("invalid hierarchy limits: %w", ErrHierarchyPlanInvalid)
	}
	return nil
}

func normalizeHierarchyInput(input knowl.HierarchyInput) (knowl.HierarchyInput, map[string]knowl.HierarchyPage, map[string]knowl.HierarchyCatalog, error) {
	if len(input.Pages) > input.Limits.MaxPages {
		return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("hierarchy page count %d exceeds %d: %w", len(input.Pages), input.Limits.MaxPages, ErrHierarchyLimitExceeded)
	}
	if len(input.Catalogs) > input.Limits.MaxCatalogs {
		return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("current catalog count %d exceeds %d: %w", len(input.Catalogs), input.Limits.MaxCatalogs, ErrHierarchyLimitExceeded)
	}
	normalized := input
	normalized.Pages = make([]knowl.HierarchyPage, 0, len(input.Pages))
	pages := make(map[string]knowl.HierarchyPage, len(input.Pages))
	pageIDs := make(map[knowl.PageID]string, len(input.Pages))
	for _, page := range input.Pages {
		canonical, err := validateOrdinaryHierarchyPath(page.Path, input.Limits.MaxPathBytes)
		if err != nil {
			return knowl.HierarchyInput{}, nil, nil, err
		}
		if page.ID == "" || !validSHA256Text(page.Digest) || !validHierarchyText(page.Title, true) || !validHierarchyText(page.Type, true) ||
			!validHierarchyText(page.Description, false) || !validHierarchyText(page.Excerpt, false) {
			return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("ordinary page %q has invalid semantic metadata: %w", canonical, ErrHierarchyPlanInvalid)
		}
		if excerptRunes := utf8.RuneCountInString(page.Excerpt); excerptRunes > input.Limits.MaxExcerptCharacters {
			return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("ordinary page %q excerpt characters %d exceed %d: %w", canonical, excerptRunes, input.Limits.MaxExcerptCharacters, ErrHierarchyLimitExceeded)
		}
		if _, exists := pages[canonical]; exists {
			return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("duplicate ordinary page path %q: %w", canonical, ErrHierarchyPlanInvalid)
		}
		if previous, exists := pageIDs[page.ID]; exists {
			return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("duplicate ordinary page id %q at %q and %q: %w", page.ID, previous, canonical, ErrHierarchyPlanInvalid)
		}
		page.Path = canonical
		page.Tags, err = normalizedTextSet(page.Tags)
		if err != nil {
			return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("ordinary page %q tags: %w", canonical, err)
		}
		page.Catalogs, err = normalizedCatalogPathSet(page.Catalogs, input.Limits.MaxPathBytes)
		if err != nil {
			return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("ordinary page %q catalogs: %w", canonical, err)
		}
		pages[canonical] = page
		pageIDs[page.ID] = canonical
		normalized.Pages = append(normalized.Pages, page)
	}
	sort.Slice(normalized.Pages, func(left, right int) bool {
		if normalized.Pages[left].Path == normalized.Pages[right].Path {
			return normalized.Pages[left].ID < normalized.Pages[right].ID
		}
		return normalized.Pages[left].Path < normalized.Pages[right].Path
	})

	normalized.Catalogs = make([]knowl.HierarchyCatalog, 0, len(input.Catalogs))
	current := make(map[string]knowl.HierarchyCatalog, len(input.Catalogs))
	for _, catalog := range input.Catalogs {
		canonical, err := validateExistingCatalogPath(catalog.Path, input.Limits.MaxPathBytes)
		if err != nil {
			return knowl.HierarchyInput{}, nil, nil, err
		}
		if !validSHA256Text(catalog.Digest) || !validHierarchyText(catalog.Title, true) {
			return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("current catalog %q has invalid metadata: %w", canonical, ErrHierarchyPlanInvalid)
		}
		if _, exists := current[canonical]; exists {
			return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("duplicate current catalog %q: %w", canonical, ErrHierarchyPlanInvalid)
		}
		catalog.Path = canonical
		catalog.Children, err = normalizedPathSet(catalog.Children, input.Limits.MaxPathBytes, true)
		if err != nil {
			return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("current catalog %q children: %w", canonical, err)
		}
		current[canonical] = catalog
		normalized.Catalogs = append(normalized.Catalogs, catalog)
	}
	if _, exists := current[hierarchyRootPath]; !exists {
		return knowl.HierarchyInput{}, nil, nil, fmt.Errorf("current root catalog %q is missing: %w", hierarchyRootPath, ErrHierarchyPlanInvalid)
	}
	sort.Slice(normalized.Catalogs, func(left, right int) bool {
		return hierarchyCatalogPathLess(normalized.Catalogs[left].Path, normalized.Catalogs[right].Path)
	})
	return normalized, pages, current, nil
}

func normalizeDesiredCatalogs(input []knowl.HierarchyCatalogSpec, limits knowl.HierarchyLimits, options HierarchyValidationOptions) ([]knowl.HierarchyCatalogSpec, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("desired root catalog is missing: %w", ErrHierarchyPlanInvalid)
	}
	if len(input) > limits.MaxCatalogs {
		return nil, fmt.Errorf("desired catalog count %d: %w", len(input), ErrHierarchyLimitExceeded)
	}
	forbidden, err := normalizedForbiddenTerms(options.ForbiddenCatalogTerms)
	if err != nil {
		return nil, err
	}
	result := make([]knowl.HierarchyCatalogSpec, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, catalog := range input {
		canonical, pathErr := validateManagedCatalogPath(catalog.Path, limits.MaxPathBytes)
		if pathErr != nil {
			return nil, pathErr
		}
		if !validHierarchyText(catalog.Title, true) {
			return nil, fmt.Errorf("catalog %q has invalid title: %w", canonical, ErrHierarchyPlanInvalid)
		}
		if _, exists := seen[canonical]; exists {
			return nil, fmt.Errorf("duplicate catalog %q: %w", canonical, ErrHierarchyPlanInvalid)
		}
		if catalogUsesForbiddenTerm(canonical, catalog.Title, forbidden) {
			return nil, fmt.Errorf("catalog %q uses a configured source namespace: %w", canonical, ErrHierarchyForbiddenPath)
		}
		children, childErr := normalizedPathSet(catalog.Children, limits.MaxPathBytes, true)
		if childErr != nil {
			return nil, fmt.Errorf("catalog %q children: %w", canonical, childErr)
		}
		catalog.Path = canonical
		catalog.Children = children
		seen[canonical] = struct{}{}
		result = append(result, catalog)
	}
	if _, exists := seen[hierarchyRootPath]; !exists {
		return nil, fmt.Errorf("desired root catalog %q is missing: %w", hierarchyRootPath, ErrHierarchyPlanInvalid)
	}
	sort.Slice(result, func(left, right int) bool { return hierarchyCatalogPathLess(result[left].Path, result[right].Path) })
	return result, nil
}

func validateHierarchyGraph(catalogs []knowl.HierarchyCatalogSpec, pages map[string]knowl.HierarchyPage, minRootCatalogs int, limits knowl.HierarchyLimits) error {
	byPath := make(map[string]knowl.HierarchyCatalogSpec, len(catalogs))
	for _, catalog := range catalogs {
		byPath[catalog.Path] = catalog
	}
	edges := 0
	rootCatalogs := 0
	rootPages := 0
	for _, catalog := range catalogs {
		if catalog.Path != hierarchyRootPath && len(catalog.Children) == 0 {
			return fmt.Errorf("catalog %q has no children: %w", catalog.Path, ErrHierarchyPlanInvalid)
		}
		for _, child := range catalog.Children {
			edges++
			if edges > limits.MaxEdges {
				return fmt.Errorf("catalog edge count %d exceeds %d at %q: %w", edges, limits.MaxEdges, catalog.Path, ErrHierarchyLimitExceeded)
			}
			_, isCatalog := byPath[child]
			_, isPage := pages[child]
			if !isCatalog && !isPage {
				return fmt.Errorf("catalog %q has missing child %q: %w", catalog.Path, child, ErrHierarchyPlanInvalid)
			}
			if catalog.Path == hierarchyRootPath {
				if isCatalog {
					rootCatalogs++
				} else {
					rootPages++
				}
			}
		}
	}
	if rootCatalogs < minRootCatalogs {
		return fmt.Errorf("root has %d semantic child catalogs, requires %d: %w", rootCatalogs, minRootCatalogs, ErrHierarchyPlanInvalid)
	}
	if len(pages) > 0 && rootPages == len(pages) && minRootCatalogs > 0 {
		return fmt.Errorf("root directly enumerates all %d ordinary pages: %w", len(pages), ErrHierarchyPlanInvalid)
	}

	state := make(map[string]uint8, len(catalogs))
	var visit func(string, int) error
	visit = func(catalogPath string, depth int) error {
		if depth > limits.MaxDepth {
			return fmt.Errorf("catalog %q exceeds graph depth %d: %w", catalogPath, limits.MaxDepth, ErrHierarchyLimitExceeded)
		}
		if state[catalogPath] == 1 {
			return fmt.Errorf("catalog cycle at %q: %w", catalogPath, ErrHierarchyPlanInvalid)
		}
		if state[catalogPath] == 2 {
			return nil
		}
		state[catalogPath] = 1
		for _, child := range byPath[catalogPath].Children {
			if _, isCatalog := byPath[child]; isCatalog {
				if err := visit(child, depth+1); err != nil {
					return err
				}
			}
		}
		state[catalogPath] = 2
		return nil
	}
	if err := visit(hierarchyRootPath, 1); err != nil {
		return err
	}
	for _, catalog := range catalogs {
		if state[catalog.Path] != 2 {
			return fmt.Errorf("catalog %q is unreachable from root: %w", catalog.Path, ErrHierarchyPlanInvalid)
		}
	}

	reachable := make(map[string]struct{}, len(pages)+len(catalogs))
	queue := []string{hierarchyRootPath}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, seen := reachable[current]; seen {
			continue
		}
		reachable[current] = struct{}{}
		if catalog, exists := byPath[current]; exists {
			queue = append(queue, catalog.Children...)
		}
	}
	pagePaths := make([]string, 0, len(pages))
	for pagePath := range pages {
		pagePaths = append(pagePaths, pagePath)
	}
	sort.Strings(pagePaths)
	for _, pagePath := range pagePaths {
		if _, exists := reachable[pagePath]; !exists {
			return fmt.Errorf("ordinary page %q is unreachable from root: %w", pagePath, ErrHierarchyPlanInvalid)
		}
	}
	return nil
}

func renderHierarchyMutations(catalogs []knowl.HierarchyCatalogSpec, pages map[string]knowl.HierarchyPage, current map[string]knowl.HierarchyCatalog, limits knowl.HierarchyLimits) ([]knowl.HierarchyMutation, error) {
	titles := make(map[string]string, len(catalogs)+len(pages))
	for path, page := range pages {
		titles[path] = page.Title
	}
	for _, catalog := range catalogs {
		titles[catalog.Path] = catalog.Title
	}
	desired := make(map[string][]byte, len(catalogs))
	mutations := make([]knowl.HierarchyMutation, 0, len(catalogs))
	for _, catalog := range catalogs {
		content, err := renderHierarchyCatalog(catalog, titles, limits.MaxCatalogBytes)
		if err != nil {
			return nil, err
		}
		desired[catalog.Path] = content
		contentDigest := digestHierarchyBytes(content)
		before, exists := current[catalog.Path]
		if exists && strings.EqualFold(before.Digest, contentDigest) {
			continue
		}
		mutation := knowl.HierarchyMutation{Action: knowl.SourceMutationWrite, Path: catalog.Path, Content: content}
		if exists {
			mutation.ExpectedDigest = before.Digest
		}
		mutations = append(mutations, mutation)
	}
	currentPaths := make([]string, 0, len(current))
	for currentPath := range current {
		currentPaths = append(currentPaths, currentPath)
	}
	sort.Strings(currentPaths)
	for _, currentPath := range currentPaths {
		if currentPath == hierarchyRootPath || !IsManagedHierarchyCatalog(currentPath) {
			continue
		}
		if _, retained := desired[currentPath]; retained {
			continue
		}
		mutations = append(mutations, knowl.HierarchyMutation{
			Action:         knowl.SourceMutationDelete,
			Path:           currentPath,
			ExpectedDigest: current[currentPath].Digest,
		})
	}
	sort.Slice(mutations, func(left, right int) bool {
		return hierarchyCatalogPathLess(mutations[left].Path, mutations[right].Path)
	})
	if len(mutations) > limits.MaxEdits {
		return nil, fmt.Errorf("hierarchy edit count %d exceeds %d: %w", len(mutations), limits.MaxEdits, ErrHierarchyLimitExceeded)
	}
	return mutations, nil
}

func renderHierarchyCatalog(catalog knowl.HierarchyCatalogSpec, titles map[string]string, maxBytes int) ([]byte, error) {
	var body strings.Builder
	body.WriteString("# ")
	body.WriteString(escapeMarkdownText(catalog.Title))
	body.WriteByte('\n')
	for _, child := range catalog.Children {
		title := titles[child]
		if title == "" {
			return nil, fmt.Errorf("catalog %q child %q has no title: %w", catalog.Path, child, ErrHierarchyPlanInvalid)
		}
		body.WriteString("* [")
		body.WriteString(escapeMarkdownLabel(title))
		body.WriteString("](/")
		body.WriteString(escapeHierarchyDestination(strings.TrimPrefix(child, "wiki/")))
		body.WriteString(")\n")
	}
	if body.Len() > maxBytes {
		return nil, fmt.Errorf("catalog %q bytes %d exceed %d: %w", catalog.Path, body.Len(), maxBytes, ErrHierarchyLimitExceeded)
	}
	index := okf.Index{Body: body.String()}
	if catalog.Path == hierarchyRootPath {
		index.ObservedVersion = okf.Version
	}
	relative := strings.TrimPrefix(catalog.Path, "wiki/")
	rendered, err := okf.RenderIndex(relative, index, okf.Limits{MaxBytes: maxBytes, MaxNodes: okf.DefaultLimits().MaxNodes, MaxAliases: okf.DefaultLimits().MaxAliases, MaxDepth: okf.DefaultLimits().MaxDepth})
	if err != nil {
		return nil, fmt.Errorf("render catalog %q: %w", catalog.Path, err)
	}
	return rendered, nil
}

func validateOrdinaryHierarchyPath(raw string, maxBytes int) (string, error) {
	canonical, kind, err := validateHierarchyWikiPath(raw, maxBytes)
	if err != nil || kind != okf.DocumentConcept || canonical == "wiki/sources.md" || strings.HasPrefix(canonical, "wiki/sources/") {
		return "", fmt.Errorf("ordinary page path %q: %w", raw, ErrHierarchyForbiddenPath)
	}
	return canonical, nil
}

func validateExistingCatalogPath(raw string, maxBytes int) (string, error) {
	canonical, kind, err := validateHierarchyWikiPath(raw, maxBytes)
	if err != nil || kind != okf.DocumentIndex {
		return "", fmt.Errorf("current catalog path %q: %w", raw, ErrHierarchyForbiddenPath)
	}
	return canonical, nil
}

func validateManagedCatalogPath(raw string, maxBytes int) (string, error) {
	canonical, err := validateExistingCatalogPath(raw, maxBytes)
	if err != nil || !IsManagedHierarchyCatalog(canonical) {
		return "", fmt.Errorf("managed catalog path %q: %w", raw, ErrHierarchyForbiddenPath)
	}
	return canonical, nil
}

func validateHierarchyWikiPath(raw string, maxBytes int) (string, okf.DocumentKind, error) {
	if raw == "" || len(raw) > maxBytes || len(raw) > maxEditPathBytes || strings.TrimSpace(raw) != raw || !utf8.ValidString(raw) ||
		strings.Contains(raw, "\\") || path.IsAbs(raw) || path.Clean(raw) != raw || !strings.HasPrefix(raw, "wiki/") {
		return "", "", ErrHierarchyForbiddenPath
	}
	relative := strings.TrimPrefix(raw, "wiki/")
	kind, err := okf.ClassifyPath(relative)
	if err != nil {
		return "", "", ErrHierarchyForbiddenPath
	}
	return raw, kind, nil
}

func normalizedPathSet(values []string, maxBytes int, allowCatalog bool) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		canonical, kind, err := validateHierarchyWikiPath(value, maxBytes)
		if err != nil || (kind != okf.DocumentConcept && (!allowCatalog || kind != okf.DocumentIndex)) || kind == okf.DocumentLog ||
			canonical == "wiki/sources.md" || strings.HasPrefix(canonical, "wiki/sources/") {
			return nil, fmt.Errorf("child path %q: %w", value, ErrHierarchyForbiddenPath)
		}
		if _, exists := seen[canonical]; exists {
			return nil, fmt.Errorf("duplicate child path %q: %w", canonical, ErrHierarchyPlanInvalid)
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	sort.Strings(result)
	return result, nil
}

func normalizedCatalogPathSet(values []string, maxBytes int) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		canonical, kind, err := validateHierarchyWikiPath(value, maxBytes)
		if err != nil || kind != okf.DocumentIndex || canonical == "wiki/sources/index.md" || strings.HasPrefix(canonical, "wiki/sources/") {
			return nil, fmt.Errorf("catalog membership path %q: %w", value, ErrHierarchyForbiddenPath)
		}
		if _, exists := seen[canonical]; exists {
			return nil, fmt.Errorf("duplicate catalog membership path %q: %w", canonical, ErrHierarchyPlanInvalid)
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	sort.Slice(result, func(left, right int) bool { return hierarchyCatalogPathLess(result[left], result[right]) })
	return result, nil
}

func normalizedTextSet(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validHierarchyText(value, true) {
			return nil, ErrHierarchyPlanInvalid
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func normalizedForbiddenTerms(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !validHierarchyText(value, true) || strings.Contains(value, "/") || strings.Contains(value, "\\") {
			return nil, fmt.Errorf("invalid forbidden catalog term: %w", ErrHierarchyPlanInvalid)
		}
		folded := strings.ToLower(value)
		if _, exists := seen[folded]; exists {
			continue
		}
		seen[folded] = struct{}{}
		result = append(result, folded)
	}
	sort.Strings(result)
	return result, nil
}

func catalogUsesForbiddenTerm(catalogPath, title string, forbidden []string) bool {
	if len(forbidden) == 0 {
		return false
	}
	title = strings.ToLower(strings.TrimSpace(title))
	for _, term := range forbidden {
		if title == term {
			return true
		}
	}
	if catalogPath == hierarchyRootPath {
		return false
	}
	segments := strings.Split(strings.TrimSuffix(strings.TrimPrefix(catalogPath, hierarchyCatalogPrefix), "/"+hierarchyCatalogName), "/")
	for _, term := range forbidden {
		for _, segment := range segments {
			if strings.EqualFold(segment, term) {
				return true
			}
		}
	}
	return false
}

func validHierarchyText(value string, required bool) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if required && value == "" {
		return false
	}
	for _, character := range value {
		if character < ' ' || character == 0x7f {
			return false
		}
	}
	return true
}

func validSHA256Text(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func digestHierarchyBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func escapeMarkdownLabel(value string) string {
	return escapeMarkdownText(value)
}

func escapeMarkdownText(value string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"[", "&#91;",
		"]", "&#93;",
	).Replace(value)
}

func escapeHierarchyDestination(value string) string {
	segments := strings.Split(value, "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func hierarchyCatalogPathLess(left, right string) bool {
	if left == hierarchyRootPath {
		return right != hierarchyRootPath
	}
	if right == hierarchyRootPath {
		return false
	}
	return left < right
}
