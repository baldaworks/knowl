package normalize

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

const sourcePageType = "source"

// RenderInput contains one fetched or restored raw document and its complete
// source-local catalog.
type RenderInput struct {
	Source    knowl.Source
	Document  knowl.Document
	RawSource knowl.AcceptedSource
	Catalog   Catalog
}

// Result is one immutable deterministic normalization result.
type Result struct {
	formatVersion string
	catalogDigest string
	files         []RenderedFile
	mirrorDigest  string
}

// FormatVersion returns the rendering contract version.
func (result Result) FormatVersion() string {
	return result.formatVersion
}

// CatalogDigest returns the catalog identity used for rendering.
func (result Result) CatalogDigest() string {
	return result.catalogDigest
}

// Files returns detached immutable rendered file values.
func (result Result) Files() []RenderedFile {
	return append([]RenderedFile(nil), result.files...)
}

// MirrorDigest returns the independent mirror render identity.
func (result Result) MirrorDigest() string {
	return result.mirrorDigest
}

// Render normalizes one Markdown page or auxiliary asset without mutating a
// workspace or source.
func Render(input RenderInput, limits Limits) (Result, error) {
	if !validLimits(limits) || !input.Source.Enabled || app.ValidateSource(input.Source) != nil ||
		input.Source.Config.Filesystem == nil || app.ValidateDocument(input.Document, limits.MaxRenderedBytes) != nil ||
		input.Document.ExternalID != knowl.DocumentID(input.Document.Path) || !validSHA256(input.Catalog.Digest()) ||
		!input.Catalog.contains(input.Document.ExternalID, input.Document.Path) || !validAcceptedSource(input.RawSource) ||
		input.RawSource.Version.Version != input.Document.Revision || input.RawSource.Version.Digest != input.Document.Revision ||
		(input.Source.Config.Filesystem.Flavor != knowl.SourceFlavorMarkdown && input.Source.Config.Filesystem.Flavor != knowl.SourceFlavorObsidian) {
		return Result{}, ErrInvalid
	}
	var (
		file RenderedFile
		err  error
	)
	if strings.EqualFold(path.Ext(input.Document.Path), ".md") {
		file, err = renderMarkdown(input, limits)
	} else {
		file, err = renderAsset(input, limits)
	}
	if err != nil {
		return Result{}, err
	}
	if err := validateRenderedFile(input, file); err != nil {
		return Result{}, err
	}
	digest, err := MirrorDigest(MirrorIdentity{
		SourceID: input.Source.ID, DocumentID: input.Document.ExternalID, Revision: input.Document.Revision,
		RawSource: input.RawSource, CatalogDigest: input.Catalog.Digest(), Files: []RenderedFile{file},
	})
	if err != nil {
		return Result{}, err
	}
	return Result{
		formatVersion: FormatVersion,
		catalogDigest: input.Catalog.Digest(),
		files:         []RenderedFile{file},
		mirrorDigest:  digest,
	}, nil
}

type canonicalFrontmatter struct {
	ID             string               `yaml:"id"`
	Title          string               `yaml:"title"`
	Type           string               `yaml:"type"`
	SourceRefs     []string             `yaml:"source_refs"`
	SourceDocument knowl.SourceDocument `yaml:"source_document"`
}

func renderMarkdown(input RenderInput, limits Limits) (RenderedFile, error) {
	body, metadata := splitMarkdownFrontmatter(string(input.Document.Content))
	title := resolveTitle(metadata, body, input.Document.Path)
	if !validText(title, 1024) {
		return RenderedFile{}, ErrInvalid
	}
	if input.Source.Config.Filesystem.Flavor == knowl.SourceFlavorObsidian {
		body = rewriteObsidianReferences(input.Source.ID, input.Document.Path, input.Catalog, body)
	}
	target := sourceTarget(input.Source.ID, trimMarkdownExtension(input.Document.Path)+".md")
	pageID := strings.TrimSuffix(strings.TrimPrefix(target, "wiki/"), ".md")
	provenance := knowl.SourceDocument{
		SourceID: input.Source.ID, DocumentID: input.Document.ExternalID,
		Revision: input.Document.Revision, URI: input.Document.URI,
	}
	if app.ValidateOwnedSourceDocument(input.Source.ID, provenance) != nil {
		return RenderedFile{}, ErrInvalid
	}
	base, err := yaml.Marshal(canonicalFrontmatter{
		ID: pageID, Title: title, Type: sourcePageType,
		SourceRefs: []string{app.SourceRefKey(input.RawSource)}, SourceDocument: provenance,
	})
	if err != nil {
		return RenderedFile{}, ErrInvalid
	}
	extras, err := marshalExtras(metadata)
	if err != nil {
		return RenderedFile{}, err
	}
	content := make([]byte, 0, len(base)+len(extras)+len(body)+16)
	content = append(content, "---\n"...)
	content = append(content, base...)
	content = append(content, extras...)
	content = append(content, "---\n"...)
	content = append(content, strings.TrimLeft(body, "\n")...)
	if len(content) == 0 || content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	return NewRenderedFile(target, content, limits)
}

func renderAsset(input RenderInput, limits Limits) (RenderedFile, error) {
	return NewRenderedFile(sourceTarget(input.Source.ID, input.Document.Path), input.Document.Content, limits)
}

func validateRenderedFile(input RenderInput, file RenderedFile) error {
	plan, err := app.NormalizeSourceMutationPlan(knowl.SourceMutationPlan{
		RunID: "normalizer-validation", Scope: input.RawSource.Scope, SourceID: input.Source.ID,
		Mutations: []knowl.SourceMutation{{Action: knowl.SourceMutationWrite, Path: file.Path(), Content: file.Content()}},
	})
	if err != nil || len(plan.Mutations) != 1 || plan.Mutations[0].Path != file.Path() {
		return ErrInvalid
	}
	return nil
}

func sourceTarget(sourceID knowl.SourceID, relative string) string {
	return "wiki/sources/" + string(sourceID) + "/" + relative
}

func splitMarkdownFrontmatter(content string) (string, map[string]any) {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return content, nil
	}
	end := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			end = index
			break
		}
	}
	if end < 0 {
		return content, nil
	}
	metadata := make(map[string]any)
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &metadata); err != nil {
		return content, nil
	}
	return strings.Join(lines[end+1:], "\n"), metadata
}

func resolveTitle(metadata map[string]any, body, relative string) string {
	if title, ok := metadata["title"].(string); ok && strings.TrimSpace(title) != "" {
		return strings.TrimSpace(title)
	}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") && strings.TrimSpace(strings.TrimPrefix(trimmed, "# ")) != "" {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
	}
	title := path.Base(trimMarkdownExtension(relative))
	if strings.TrimSpace(title) == "" {
		return "Source document"
	}
	return title
}

func marshalExtras(metadata map[string]any) ([]byte, error) {
	if len(metadata) == 0 {
		return nil, nil
	}
	reserved := map[string]struct{}{
		"id": {}, "title": {}, "type": {}, "source_refs": {}, "source_document": {},
	}
	keys := make([]string, 0, len(metadata))
	extras := make(map[string]any, len(metadata))
	for key, value := range metadata {
		normalized := strings.TrimSpace(key)
		if !validText(normalized, 256) {
			return nil, ErrInvalid
		}
		if _, exists := reserved[normalized]; exists {
			continue
		}
		if _, exists := extras[normalized]; exists {
			return nil, ErrInvalid
		}
		extras[normalized] = value
		keys = append(keys, normalized)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	sort.Strings(keys)
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, key := range keys {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
		valueNode := &yaml.Node{}
		if err := valueNode.Encode(extras[key]); err != nil {
			return nil, fmt.Errorf("encode source frontmatter extra: %w", ErrInvalid)
		}
		node.Content = append(node.Content, keyNode, valueNode)
	}
	return yaml.Marshal(node)
}
