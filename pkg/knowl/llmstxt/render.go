package llmstxt

import (
	"bytes"
	"context"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const rootIndexPath = "wiki/index.md"

// Limits bounds renderer work and output.
type Limits struct {
	MaxPages       int
	MaxCatalogs    int
	MaxEdges       int
	MaxDepth       int
	MaxPathBytes   int
	MaxTextBytes   int
	MaxOutputBytes int
}

// DefaultLimits returns conservative limits for a local export.
func DefaultLimits() Limits {
	return Limits{10_000, 1_024, 100_000, 64, 4_096, 64 << 10, 4 << 20}
}

// Input is the inspected content needed to render llms.txt. Catalogs excludes
// the root catalog supplied in Index.
type Input struct {
	Index    knowl.PageSnapshot
	Catalogs []knowl.PageSnapshot
	Pages    []knowl.PageSnapshot
}

// Options configures document metadata and links.
type Options struct {
	Title   string
	Summary string
	BaseURL string
	Limits  Limits
}

type renderer struct {
	ctx       context.Context
	limits    Limits
	baseURL   *url.URL
	catalogs  map[string]knowl.PageSnapshot
	pages     map[string]knowl.PageSnapshot
	edges     map[string][]string
	edgeCount int
}

type section struct {
	title string
	pages []knowl.PageSnapshot
}

// Render returns a deterministic llms.txt document or no bytes on error.
func Render(ctx context.Context, input Input, options Options) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits := options.Limits
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	if limits.MaxPages <= 0 || limits.MaxCatalogs <= 0 || limits.MaxEdges <= 0 || limits.MaxDepth <= 0 || limits.MaxPathBytes <= 0 || limits.MaxTextBytes <= 0 || limits.MaxOutputBytes <= 0 {
		return nil, ErrInvalidInput
	}
	baseURL, err := parseBaseURL(options.BaseURL, limits.MaxPathBytes)
	if err != nil {
		return nil, err
	}
	if input.Index.Path != rootIndexPath || len(input.Pages) > limits.MaxPages || len(input.Catalogs)+1 > limits.MaxCatalogs {
		if len(input.Pages) > limits.MaxPages || len(input.Catalogs)+1 > limits.MaxCatalogs {
			return nil, ErrLimitExceeded
		}
		return nil, ErrInvalidInput
	}
	r := &renderer{ctx: ctx, limits: limits, baseURL: baseURL, catalogs: map[string]knowl.PageSnapshot{rootIndexPath: input.Index}, pages: make(map[string]knowl.PageSnapshot), edges: make(map[string][]string)}
	for _, catalog := range input.Catalogs {
		if catalog.Path == rootIndexPath || !validPath(catalog.Path, okf.DocumentIndex, limits.MaxPathBytes) || r.catalogs[catalog.Path].Path != "" {
			return nil, ErrInvalidInput
		}
		r.catalogs[catalog.Path] = catalog
	}
	for _, page := range input.Pages {
		if !validPath(page.Path, okf.DocumentConcept, limits.MaxPathBytes) || page.OKF == nil || r.pages[page.Path].Path != "" {
			return nil, ErrInvalidInput
		}
		r.pages[page.Path] = page
	}
	sections, err := r.buildSections()
	if err != nil {
		return nil, err
	}
	title := options.Title
	if title == "" {
		title = input.Index.Title
	}
	title, err = normalize(title, limits.MaxTextBytes)
	if err != nil {
		return nil, err
	}
	summary := ""
	if options.Summary != "" {
		summary, err = normalize(options.Summary, limits.MaxTextBytes)
		if err != nil {
			return nil, err
		}
	}
	var output bytes.Buffer
	appendText := func(text string) error {
		if output.Len()+len(text) > limits.MaxOutputBytes {
			return ErrLimitExceeded
		}
		output.WriteString(text)
		return nil
	}
	if err := appendText("# " + title + "\n"); err != nil {
		return nil, err
	}
	if summary != "" {
		if err := appendText("\n> " + summary + "\n"); err != nil {
			return nil, err
		}
	}
	for _, section := range sections {
		if err := appendText("\n## " + section.title + "\n"); err != nil {
			return nil, err
		}
		for _, page := range section.pages {
			title, err := normalize(page.Title, limits.MaxTextBytes)
			if err != nil {
				return nil, err
			}
			line := "- [" + escapeLabel(title) + "](" + r.pageURL(page.Path) + ")"
			if page.OKF.Description != "" {
				description, err := normalize(page.OKF.Description, limits.MaxTextBytes)
				if err != nil {
					return nil, err
				}
				line += ": " + description
			}
			if err := appendText(line + "\n"); err != nil {
				return nil, err
			}
		}
	}
	return output.Bytes(), nil
}

func (r *renderer) buildSections() ([]section, error) {
	rootChildren, err := r.children(rootIndexPath)
	if err != nil {
		return nil, err
	}
	emitted := make(map[string]bool)
	seenCatalogs := map[string]bool{rootIndexPath: true}
	sectionTitles := make(map[string]bool)
	sections := make([]section, 0)
	direct := make([]knowl.PageSnapshot, 0)
	for _, child := range rootChildren {
		if page, found := r.pages[child]; found {
			if !emitted[child] {
				emitted[child] = true
				direct = append(direct, page)
			}
			continue
		}
		catalog := r.catalogs[child]
		title, err := normalize(catalog.Title, r.limits.MaxTextBytes)
		key := strings.ToLower(title)
		if err != nil || key == "docs" || sectionTitles[key] {
			return nil, ErrInvalidGraph
		}
		sectionTitles[key] = true
		pages, hasPage, err := r.collect(child, 2, map[string]bool{}, seenCatalogs, emitted)
		if err != nil || !hasPage {
			return nil, errorsOr(err, ErrInvalidGraph)
		}
		if len(pages) > 0 {
			sections = append(sections, section{title, pages})
		}
	}
	if len(direct) > 0 {
		sections = append(sections, section{"Docs", direct})
	}
	if len(emitted) != len(r.pages) || len(seenCatalogs) != len(r.catalogs) {
		return nil, ErrInvalidGraph
	}
	return sections, nil
}

func (r *renderer) collect(catalog string, depth int, visiting, seenCatalogs, emitted map[string]bool) ([]knowl.PageSnapshot, bool, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, false, err
	}
	if depth > r.limits.MaxDepth {
		return nil, false, ErrLimitExceeded
	}
	if visiting[catalog] {
		return nil, false, ErrInvalidGraph
	}
	visiting[catalog], seenCatalogs[catalog] = true, true
	defer delete(visiting, catalog)
	children, err := r.children(catalog)
	if err != nil {
		return nil, false, err
	}
	result := make([]knowl.PageSnapshot, 0)
	hasPage := false
	for _, child := range children {
		if page, found := r.pages[child]; found {
			hasPage = true
			if !emitted[child] {
				emitted[child] = true
				result = append(result, page)
			}
			continue
		}
		pages, childHasPage, err := r.collect(child, depth+1, visiting, seenCatalogs, emitted)
		if err != nil {
			return nil, false, err
		}
		hasPage = hasPage || childHasPage
		result = append(result, pages...)
	}
	return result, hasPage, nil
}

func (r *renderer) children(catalogPath string) ([]string, error) {
	if children, found := r.edges[catalogPath]; found {
		return children, nil
	}
	catalog := r.catalogs[catalogPath]
	if len(catalog.Content) > r.limits.MaxTextBytes {
		return nil, ErrLimitExceeded
	}
	if !utf8.ValidString(catalog.Content) {
		return nil, ErrInvalidInput
	}
	destinations, malformed := wiki.IndexDestinations(catalog.Content, r.limits.MaxEdges+1)
	if r.edgeCount+len(destinations) > r.limits.MaxEdges {
		return nil, ErrLimitExceeded
	}
	if malformed {
		return nil, ErrInvalidGraph
	}
	children := make([]string, 0, len(destinations))
	for _, destination := range destinations {
		target, external, valid := wiki.ResolveIndexDestination(strings.TrimPrefix(catalogPath, "wiki/"), destination)
		if external && strings.HasPrefix(strings.TrimSpace(destination), "#") {
			continue
		}
		target = "wiki/" + target
		if !valid || external || (r.catalogs[target].Path == "" && r.pages[target].Path == "") {
			return nil, ErrInvalidGraph
		}
		children = append(children, target)
	}
	r.edgeCount += len(destinations)
	r.edges[catalogPath] = children
	return children, nil
}

func (r *renderer) pageURL(pagePath string) string {
	parts := strings.Split(strings.TrimPrefix(pagePath, "wiki/"), "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	escaped := strings.Join(parts, "/")
	if r.baseURL == nil {
		return escaped
	}
	joined := strings.TrimSuffix(r.baseURL.EscapedPath(), "/") + "/" + escaped
	result := *r.baseURL
	result.Path, _ = url.PathUnescape(joined)
	result.RawPath = joined
	return result.String()
}

func parseBaseURL(raw string, maxBytes int) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxBytes {
		return nil, ErrLimitExceeded
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, ErrInvalidInput
	}
	return parsed, nil
}

func validPath(candidate string, kind okf.DocumentKind, maxBytes int) bool {
	if len(candidate) > maxBytes || !utf8.ValidString(candidate) || !strings.HasPrefix(candidate, "wiki/") || strings.Contains(candidate, "\\") {
		return false
	}
	relative := strings.TrimPrefix(candidate, "wiki/")
	actual, err := okf.ClassifyPath(relative)
	return err == nil && actual == kind && path.Clean(relative) == relative
}

func normalize(value string, maxBytes int) (string, error) {
	if len(value) > maxBytes {
		return "", ErrLimitExceeded
	}
	if !utf8.ValidString(value) {
		return "", ErrInvalidInput
	}
	for _, character := range value {
		if unicode.IsControl(character) && !unicode.IsSpace(character) {
			return "", ErrInvalidInput
		}
	}
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "", ErrInvalidInput
	}
	return value, nil
}

func escapeLabel(value string) string {
	return strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(value)
}

func errorsOr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
