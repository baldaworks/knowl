package wiki

import (
	"fmt"
	"net/url"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	rootDir              = "wiki/"
	markdownExt          = ".md"
	relationWiki         = "wiki"
	relationOKF          = "okf"
	frontmatterDelimiter = "---"
	ownedID              = "id"
	ownedSourceRefs      = "source_refs"
	ownedSourceDocument  = "source_document"
)

// Frontmatter is the bounded YAML metadata recognized on ordinary pages.
type Frontmatter struct {
	ID             string                `yaml:"id"`
	Title          string                `yaml:"title"`
	Type           string                `yaml:"type"`
	SourceRefs     []string              `yaml:"source_refs"`
	SourceDocument *knowl.SourceDocument `yaml:"source_document,omitempty"`
	Legacy         bool                  `yaml:"-"`
}

// ParseFrontmatter reads canonical namespaced Knowl metadata and retains
// deterministic compatibility with the legacy flat envelope.
func ParseFrontmatter(content string) (Frontmatter, error) {
	limits := okf.DefaultLimits()
	if len(content) > limits.MaxBytes && len(content) <= 64<<20 {
		limits.MaxBytes = len(content)
	}
	document, err := okf.ParseConcept("page.md", []byte(content), limits)
	if err != nil {
		return Frontmatter{}, fmt.Errorf("parse OKF frontmatter: %w", err)
	}
	return FrontmatterFromMetadata(document.Metadata)
}

// FrontmatterFromMetadata extracts the Knowl envelope from already parsed OKF
// metadata without decoding the concept again.
func FrontmatterFromMetadata(concept okf.Metadata) (Frontmatter, error) {
	metadata := Frontmatter{Title: strings.TrimSpace(concept.Title), Type: strings.TrimSpace(concept.Type)}
	legacy, err := legacyFrontmatter(concept.Extensions)
	if err != nil {
		return Frontmatter{}, err
	}
	namespaced, owned, err := namespacedFrontmatter(concept.Extensions)
	if err != nil {
		return Frontmatter{}, err
	}
	if owned && hasLegacyOwned(concept.Extensions) {
		return Frontmatter{}, fmt.Errorf("ambiguous Knowl frontmatter envelope")
	}
	if owned {
		metadata.ID = namespaced.ID
		metadata.SourceRefs = namespaced.SourceRefs
		metadata.SourceDocument = namespaced.SourceDocument
	} else {
		metadata.ID = legacy.ID
		metadata.SourceRefs = legacy.SourceRefs
		metadata.SourceDocument = legacy.SourceDocument
		metadata.Legacy = hasLegacyOwned(concept.Extensions)
	}
	metadata.ID = strings.TrimSpace(metadata.ID)
	for index := range metadata.SourceRefs {
		metadata.SourceRefs[index] = strings.TrimSpace(metadata.SourceRefs[index])
	}
	if metadata.SourceDocument != nil {
		metadata.SourceDocument.SourceID = knowl.SourceID(strings.TrimSpace(string(metadata.SourceDocument.SourceID)))
		metadata.SourceDocument.DocumentID = knowl.DocumentID(strings.TrimSpace(string(metadata.SourceDocument.DocumentID)))
		metadata.SourceDocument.Revision = strings.TrimSpace(metadata.SourceDocument.Revision)
		metadata.SourceDocument.URI = strings.TrimSpace(metadata.SourceDocument.URI)
	}
	return metadata, nil
}

// MigrateLegacyEnvelope moves the legacy flat Knowl-owned fields into the
// namespaced extension without changing standard OKF fields or user content.
func MigrateLegacyEnvelope(metadata okf.Metadata) (okf.Metadata, bool, error) {
	legacy := hasLegacyOwned(metadata.Extensions)
	if !legacy {
		if _, _, err := namespacedFrontmatter(metadata.Extensions); err != nil {
			return okf.Metadata{}, false, err
		}
		return metadata, false, nil
	}

	result := metadata
	result.Extensions = make(map[string]any, len(metadata.Extensions))
	for key, value := range metadata.Extensions {
		result.Extensions[key] = value
	}
	knowlExtension := make(map[string]any)
	if raw, exists := result.Extensions["knowl"]; exists {
		fields, ok := raw.(map[string]any)
		if !ok {
			return okf.Metadata{}, false, fmt.Errorf("knowl extension must be a mapping")
		}
		for key, value := range fields {
			knowlExtension[key] = value
		}
		if hasLegacyOwned(knowlExtension) {
			return okf.Metadata{}, false, fmt.Errorf("ambiguous Knowl frontmatter envelope")
		}
	}
	for _, key := range []string{ownedID, ownedSourceRefs, ownedSourceDocument} {
		if value, exists := result.Extensions[key]; exists {
			knowlExtension[key] = value
			delete(result.Extensions, key)
		}
	}
	result.Extensions["knowl"] = knowlExtension
	return result, true, nil
}

func legacyFrontmatter(extensions map[string]any) (Frontmatter, error) {
	return frontmatterFromMap(extensions)
}

func namespacedFrontmatter(extensions map[string]any) (Frontmatter, bool, error) {
	raw, present := extensions["knowl"]
	if !present {
		return Frontmatter{}, false, nil
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return Frontmatter{}, false, fmt.Errorf("knowl extension must be a mapping")
	}
	owned := hasLegacyOwned(fields)
	metadata, err := frontmatterFromMap(fields)
	return metadata, owned, err
}

func frontmatterFromMap(fields map[string]any) (Frontmatter, error) {
	metadata := Frontmatter{}
	var err error
	if metadata.ID, err = optionalMapString(fields, ownedID); err != nil {
		return Frontmatter{}, err
	}
	if metadata.SourceRefs, err = optionalMapStrings(fields, ownedSourceRefs); err != nil {
		return Frontmatter{}, err
	}
	if metadata.SourceDocument, err = optionalSourceDocument(fields); err != nil {
		return Frontmatter{}, err
	}
	return metadata, nil
}

func optionalMapString(fields map[string]any, key string) (string, error) {
	raw, present := fields[key]
	if !present {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("knowl %s must be a string", key)
	}
	return value, nil
}

func optionalMapStrings(fields map[string]any, key string) ([]string, error) {
	raw, present := fields[key]
	if !present {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("knowl %s must be a string list", key)
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("knowl %s must be a string list", key)
		}
		values = append(values, value)
	}
	return values, nil
}

func optionalSourceDocument(fields map[string]any) (*knowl.SourceDocument, error) {
	raw, present := fields[ownedSourceDocument]
	if !present {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("knowl source_document must be a mapping")
	}
	sourceID, err := optionalMapString(values, "source_id")
	if err != nil {
		return nil, err
	}
	documentID, err := optionalMapString(values, "document_id")
	if err != nil {
		return nil, err
	}
	revision, err := optionalMapString(values, "revision")
	if err != nil {
		return nil, err
	}
	uri, err := optionalMapString(values, "uri")
	if err != nil {
		return nil, err
	}
	return &knowl.SourceDocument{SourceID: knowl.SourceID(sourceID), DocumentID: knowl.DocumentID(documentID), Revision: revision, URI: uri}, nil
}

func hasLegacyOwned(fields map[string]any) bool {
	for _, key := range []string{ownedID, ownedSourceRefs, ownedSourceDocument} {
		if _, present := fields[key]; present {
			return true
		}
	}
	return false
}

// Body returns the user-authored Markdown after a valid leading frontmatter
// block. Content without a complete leading block is returned unchanged.
func Body(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != frontmatterDelimiter {
		return content
	}
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == frontmatterDelimiter {
			return strings.TrimLeft(strings.Join(lines[index+1:], "\n"), "\n")
		}
	}
	return content
}

// SourceDocument returns a copy of optional structured source provenance.
func SourceDocument(content string) *knowl.SourceDocument {
	metadata, err := ParseFrontmatter(content)
	if err != nil || metadata.SourceDocument == nil {
		return nil
	}
	document := *metadata.SourceDocument
	return &document
}

// SourceRefs returns a deterministic, deduplicated source-ref list.
func SourceRefs(content string) []string {
	metadata, err := ParseFrontmatter(content)
	if err != nil {
		return nil
	}
	refs := append([]string(nil), metadata.SourceRefs...)
	sort.Strings(refs)
	return uniqueStrings(refs)
}

// MarkdownTargets extracts normalized wiki-link targets from Markdown.
func MarkdownTargets(content string) ([]string, bool) {
	targets := make([]string, 0)
	malformed := false
	for offset := 0; offset < len(content); {
		start := strings.Index(content[offset:], "[[")
		if start < 0 {
			break
		}
		start += offset + 2
		end := strings.Index(content[start:], "]]")
		if end < 0 {
			malformed = true
			break
		}
		target := strings.TrimSpace(content[start : start+end])
		if separator := strings.IndexAny(target, "|#"); separator >= 0 {
			target = target[:separator]
		}
		if isNavigationDirective(target) {
			offset = start + end + 2
			continue
		}
		target = NormalizePageTarget(target)
		if target != "" {
			targets = append(targets, target)
		}
		offset = start + end + 2
	}
	return targets, malformed
}

func isNavigationDirective(target string) bool {
	switch strings.TrimSpace(target) {
	case "_TOC_", "_TOSP_":
		return true
	default:
		return false
	}
}

// IndexTargets extracts both wiki links and bare list-item targets from index content.
func IndexTargets(content string) ([]string, bool) {
	targets, malformed := MarkdownTargets(content)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") && !strings.HasPrefix(trimmed, "* ") {
			continue
		}
		item := strings.TrimSpace(trimmed[2:])
		if strings.HasPrefix(item, "[") {
			separator := strings.Index(item, "](")
			if separator < 0 {
				malformed = true
				continue
			}
			end := strings.Index(item[separator+2:], ")")
			if end < 0 {
				malformed = true
				continue
			}
			item = item[separator+2 : separator+2+end]
			item = strings.TrimPrefix(strings.TrimPrefix(item, "/"), "./")
		}
		if target := NormalizePageTarget(item); target != "" {
			targets = append(targets, target)
		}
	}
	return targets, malformed
}

// IndexDestinations returns bounded raw internal-link candidates from an OKF
// catalog. Resolution remains the caller's responsibility because nested
// catalogs resolve relative paths from their own directory.
func IndexDestinations(content string, limit int) ([]string, bool) {
	if limit <= 0 {
		return nil, true
	}
	destinations := markdownDestinations(content, limit+1)
	malformed := len(destinations) > limit
	if len(destinations) > limit {
		destinations = destinations[:limit]
	}
	for offset := 0; offset < len(content) && len(destinations) < limit; {
		start := strings.Index(content[offset:], "[[")
		if start < 0 {
			break
		}
		start += offset + 2
		end := strings.Index(content[start:], "]]")
		if end < 0 {
			malformed = true
			break
		}
		target := strings.TrimSpace(content[start : start+end])
		if separator := strings.IndexAny(target, "|#"); separator >= 0 {
			target = target[:separator]
		}
		if target != "" && !isNavigationDirective(target) {
			destinations = append(destinations, target)
		}
		offset = start + end + 2
	}
	for _, line := range strings.Split(content, "\n") {
		if len(destinations) >= limit {
			malformed = true
			break
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") && !strings.HasPrefix(trimmed, "* ") {
			continue
		}
		item := strings.TrimSpace(trimmed[2:])
		if item == "" || strings.HasPrefix(item, "[") {
			continue
		}
		destinations = append(destinations, item)
	}
	sort.Strings(destinations)
	return uniqueStrings(destinations), malformed
}

// ResolveIndexDestination resolves one catalog destination to a bundle-relative
// Markdown path. External and anchor destinations are valid but do not
// participate in the catalog graph.
func ResolveIndexDestination(catalogPath, raw string) (target string, external bool, valid bool) {
	destination := strings.TrimSpace(strings.Trim(raw, "<>"))
	if destination == "" || strings.HasPrefix(destination, "#") {
		return "", true, true
	}
	parsed, err := url.Parse(destination)
	if err != nil {
		return "", false, false
	}
	if parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(destination, "//") {
		return "", true, true
	}
	decoded, err := url.PathUnescape(parsed.Path)
	if err != nil || strings.ContainsAny(decoded, "\\\x00\r\n") {
		return "", false, false
	}
	rootRelative := strings.HasPrefix(decoded, "/") || strings.HasPrefix(decoded, "wiki/")
	decoded = strings.TrimPrefix(decoded, "/")
	decoded = strings.TrimPrefix(decoded, "wiki/")
	if strings.HasSuffix(decoded, "/") {
		decoded += "index.md"
	}
	if pathpkg.Ext(decoded) == "" {
		decoded += markdownExt
	}
	if pathpkg.Ext(decoded) != markdownExt {
		return "", true, true
	}
	target = decoded
	if !rootRelative {
		target = pathpkg.Join(pathpkg.Dir(catalogPath), decoded)
	}
	target = pathpkg.Clean(target)
	if target == "." || target == ".." || strings.HasPrefix(target, "../") || pathpkg.IsAbs(target) {
		return "", false, false
	}
	kind, err := okf.ClassifyPath(target)
	if err != nil || (kind != okf.DocumentIndex && kind != okf.DocumentConcept) {
		return "", false, false
	}
	return target, false, true
}

// NormalizePageTarget converts a wiki reference into a canonical page ID.
func NormalizePageTarget(target string) string {
	target = strings.TrimSpace(strings.TrimPrefix(target, "wiki/"))
	target = strings.TrimSuffix(target, ".md")
	if target == "" || target == "." || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "../") || strings.Contains(target, "/../") {
		return ""
	}
	return target
}

// PageIDFromPath derives the canonical page ID from a wiki Markdown path.
func PageIDFromPath(path string) (knowl.PageID, bool) {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	if !strings.HasPrefix(normalized, rootDir) || !strings.HasSuffix(normalized, markdownExt) {
		return "", false
	}
	relative := strings.TrimPrefix(normalized, rootDir)
	kind, err := okf.ClassifyPath(relative)
	if err != nil || kind != okf.DocumentConcept {
		return "", false
	}
	pageID := strings.TrimSuffix(relative, markdownExt)
	if NormalizePageTarget(pageID) != pageID {
		return "", false
	}
	return knowl.PageID(pageID), pageID != ""
}

// Links converts normalized page targets into unique wiki link references.
func Links(from knowl.PageID, content string) []knowl.LinkReference {
	targets, _ := MarkdownTargets(content)
	links := make([]knowl.LinkReference, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		key := target + "\x00" + relationWiki
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		links = append(links, knowl.LinkReference{From: from, To: knowl.PageID(target), Relation: relationWiki})
	}
	return links
}

// ConceptLinks extracts bounded standard Markdown links to OKF concepts and
// resolves them relative to the canonical page ID. External, asset, anchor,
// unsafe, and malformed destinations are ignored.
func ConceptLinks(from knowl.PageID, content string) []knowl.LinkReference {
	const maxConceptLinks = 4096
	destinations := markdownDestinations(content, maxConceptLinks)
	links := make([]knowl.LinkReference, 0, len(destinations))
	seen := make(map[string]struct{}, len(destinations))
	for _, destination := range destinations {
		target := resolveConceptDestination(from, destination)
		if target == "" {
			continue
		}
		key := target + "\x00" + relationOKF
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		links = append(links, knowl.LinkReference{From: from, To: knowl.PageID(target), Relation: relationOKF})
	}
	return links
}

func markdownDestinations(content string, limit int) []string {
	result := make([]string, 0)
	definitions := make(map[string]string)
	references := make([]string, 0)
	fenced := false
	var fence byte
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[0]
			if !fenced {
				fenced, fence = true, marker
			} else if marker == fence {
				fenced = false
			}
			continue
		}
		if fenced {
			continue
		}
		line = stripInlineCode(line)
		if label, destination, ok := markdownReferenceDefinition(line); ok {
			definitions[label] = destination
			continue
		}
		references = append(references, markdownReferenceLabels(line, max(0, limit-len(references)))...)
		for offset := 0; offset < len(line) && len(result) < limit; {
			closeLabel := strings.Index(line[offset:], "](")
			if closeLabel < 0 {
				break
			}
			start := offset + closeLabel + 2
			if start >= len(line) {
				break
			}
			destination, consumed := markdownDestination(line[start:])
			if destination != "" {
				result = append(result, destination)
			}
			if consumed <= 0 {
				consumed = 1
			}
			offset = start + consumed
		}
	}
	for _, label := range references {
		if len(result) >= limit {
			break
		}
		if destination := definitions[label]; destination != "" {
			result = append(result, destination)
		}
	}
	return result
}

func markdownReferenceDefinition(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return "", "", false
	}
	separator := strings.Index(trimmed, "]:")
	if separator <= 1 {
		return "", "", false
	}
	label := normalizeReferenceLabel(trimmed[1:separator])
	remainder := strings.TrimSpace(trimmed[separator+2:])
	if label == "" || remainder == "" {
		return "", "", false
	}
	if remainder[0] == '<' {
		end := strings.IndexByte(remainder[1:], '>')
		if end < 0 {
			return "", "", false
		}
		return label, remainder[1 : end+1], true
	}
	end := strings.IndexAny(remainder, " \t")
	if end < 0 {
		end = len(remainder)
	}
	return label, remainder[:end], true
}

func markdownReferenceLabels(line string, limit int) []string {
	var labels []string
	for offset := 0; offset < len(line) && len(labels) < limit; {
		open := strings.IndexByte(line[offset:], '[')
		if open < 0 {
			break
		}
		open += offset
		closeBracket := strings.IndexByte(line[open+1:], ']')
		if closeBracket < 0 {
			break
		}
		closeBracket += open + 1
		if closeBracket+1 < len(line) && line[closeBracket+1] == '(' {
			offset = closeBracket + 2
			continue
		}
		label := line[open+1 : closeBracket]
		if closeBracket+1 < len(line) && line[closeBracket+1] == '[' {
			end := strings.IndexByte(line[closeBracket+2:], ']')
			if end < 0 {
				break
			}
			end += closeBracket + 2
			if explicit := line[closeBracket+2 : end]; explicit != "" {
				label = explicit
			}
			offset = end + 1
		} else {
			offset = closeBracket + 1
		}
		if normalized := normalizeReferenceLabel(label); normalized != "" {
			labels = append(labels, normalized)
		}
	}
	return labels
}

func normalizeReferenceLabel(label string) string {
	return strings.ToLower(strings.Join(strings.Fields(label), " "))
}

func stripInlineCode(line string) string {
	var builder strings.Builder
	inCode := false
	for index := 0; index < len(line); index++ {
		if line[index] == '`' && (index == 0 || line[index-1] != '\\') {
			inCode = !inCode
			continue
		}
		if !inCode {
			builder.WriteByte(line[index])
		}
	}
	return builder.String()
}

func markdownDestination(value string) (string, int) {
	if value == "" {
		return "", 0
	}
	if value[0] == '<' {
		end := strings.IndexByte(value[1:], '>')
		if end < 0 {
			return "", len(value)
		}
		return value[1 : end+1], end + 2
	}
	depth := 0
	escaped := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		switch character {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return strings.TrimSpace(value[:index]), index + 1
			}
			depth--
		case ' ', '\t':
			if depth == 0 {
				end := strings.IndexByte(value[index:], ')')
				if end >= 0 {
					return strings.TrimSpace(value[:index]), index + end + 1
				}
				return "", len(value)
			}
		}
	}
	return "", len(value)
}

func resolveConceptDestination(from knowl.PageID, raw string) string {
	destination := strings.TrimSpace(strings.ReplaceAll(raw, `\(`, `(`))
	destination = strings.ReplaceAll(destination, `\)`, `)`)
	if destination == "" || strings.HasPrefix(destination, "#") || strings.HasPrefix(destination, "//") {
		return ""
	}
	parsed, err := url.Parse(destination)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Path == "" {
		return ""
	}
	decoded, err := url.PathUnescape(parsed.Path)
	if err != nil || strings.Contains(decoded, "\\") {
		return ""
	}
	extension := pathpkg.Ext(decoded)
	if extension != "" && !strings.EqualFold(extension, markdownExt) {
		return ""
	}
	decoded = strings.TrimSuffix(decoded, extension)
	fromParts := strings.Split(string(from), "/")
	target := ""
	if strings.HasPrefix(decoded, "/") {
		decoded = strings.TrimPrefix(decoded, "/")
		if len(fromParts) >= 3 && fromParts[0] == "sources" {
			target = pathpkg.Clean(pathpkg.Join(strings.Join(fromParts[:2], "/"), decoded))
		} else {
			target = pathpkg.Clean(decoded)
		}
	} else {
		target = pathpkg.Clean(pathpkg.Join(pathpkg.Dir(string(from)), decoded))
	}
	if target == "." || target == "" || target == ".." || strings.HasPrefix(target, "../") {
		return ""
	}
	if len(fromParts) >= 3 && fromParts[0] == "sources" {
		prefix := strings.Join(fromParts[:2], "/") + "/"
		if !strings.HasPrefix(target, prefix) {
			return ""
		}
	}
	kind, err := okf.ClassifyPath(target + markdownExt)
	if err != nil || kind != okf.DocumentConcept {
		return ""
	}
	return target
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}
