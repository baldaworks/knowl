package normalize

import (
	"path"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const sourcePageType = "Reference"

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
	diagnostics   []knowl.SourceDiagnostic
}

// Diagnostics returns detached, bounded non-fatal compatibility observations.
func (result Result) Diagnostics() []knowl.SourceDiagnostic {
	return append([]knowl.SourceDiagnostic(nil), result.diagnostics...)
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
		(input.Source.Config.Filesystem.Flavor != knowl.SourceFlavorMarkdown && input.Source.Config.Filesystem.Flavor != knowl.SourceFlavorObsidian && input.Source.Config.Filesystem.Flavor != knowl.SourceFlavorOKF) {
		return Result{}, ErrInvalid
	}
	var (
		file        RenderedFile
		diagnostics []knowl.SourceDiagnostic
		err         error
	)
	if strings.EqualFold(path.Ext(input.Document.Path), ".md") {
		file, diagnostics, err = renderMarkdown(input, limits)
	} else {
		file, err = renderAsset(input, limits)
	}
	if err != nil {
		return Result{}, err
	}
	if err := validateRenderedFile(input, file); err != nil {
		return Result{}, err
	}
	version := formatVersion(input.Source.Config.Filesystem.Flavor)
	digest, err := MirrorDigest(MirrorIdentity{
		FormatVersion: version,
		SourceID:      input.Source.ID, DocumentID: input.Document.ExternalID, Revision: input.Document.Revision,
		RawSource: input.RawSource, CatalogDigest: input.Catalog.Digest(), Files: []RenderedFile{file},
	})
	if err != nil {
		return Result{}, err
	}
	return Result{
		formatVersion: version,
		catalogDigest: input.Catalog.Digest(),
		files:         []RenderedFile{file},
		mirrorDigest:  digest,
		diagnostics:   diagnostics,
	}, nil
}

func renderMarkdown(input RenderInput, limits Limits) (RenderedFile, []knowl.SourceDiagnostic, error) {
	target := sourceTarget(input.Source.ID, trimMarkdownExtension(input.Document.Path)+".md")
	bundleRelative := strings.TrimPrefix(target, "wiki/")
	formatLimits := okf.DefaultLimits()
	formatLimits.MaxBytes = limits.MaxRenderedBytes
	if input.Source.Config.Filesystem.Flavor == knowl.SourceFlavorOKF {
		kind, err := okf.ClassifyPath(input.Document.Path)
		if err != nil {
			return RenderedFile{}, nil, err
		}
		switch kind {
		case okf.DocumentIndex:
			index, err := okf.ValidateIndex(input.Document.Path, input.Document.Content, formatLimits)
			if err != nil {
				return RenderedFile{}, nil, err
			}
			var diagnostics []knowl.SourceDiagnostic
			if index.ObservedVersion != "" && index.ObservedVersion != okf.Version {
				diagnostics = []knowl.SourceDiagnostic{{Code: "okf.version.best_effort", Path: input.Document.Path, ObservedVersion: boundedObservedVersion(index.ObservedVersion)}}
			}
			index.ObservedVersion = ""
			content, err := okf.RenderIndex(strings.TrimPrefix(target, "wiki/sources/"+string(input.Source.ID)+"/"), index, formatLimits)
			if err != nil {
				return RenderedFile{}, nil, err
			}
			file, err := NewRenderedFile(target, content, limits)
			return file, diagnostics, err
		case okf.DocumentLog:
			if _, err := okf.ValidateLog(input.Document.Path, input.Document.Content, formatLimits); err != nil {
				return RenderedFile{}, nil, err
			}
			file, err := NewRenderedFile(target, input.Document.Content, limits)
			return file, nil, err
		}
	}
	document, parseErr := okf.ParseConceptWithDefaultType(bundleRelative, input.Document.Content, sourcePageType, formatLimits)
	if parseErr != nil {
		if input.Source.Config.Filesystem.Flavor == knowl.SourceFlavorOKF {
			return RenderedFile{}, nil, parseErr
		}
		document = okf.Document{Metadata: okf.Metadata{Type: sourcePageType}, Body: string(input.Document.Content)}
	}
	title := resolveTitle(document.Metadata.Title, document.Body, input.Document.Path)
	if !validText(title, 1024) {
		return RenderedFile{}, nil, ErrInvalid
	}
	if input.Source.Config.Filesystem.Flavor == knowl.SourceFlavorObsidian {
		document.Body = rewriteObsidianReferences(input.Source.ID, input.Document.Path, input.Catalog, document.Body)
	}
	pageID := strings.TrimSuffix(strings.TrimPrefix(target, "wiki/"), ".md")
	provenance := knowl.SourceDocument{
		SourceID: input.Source.ID, DocumentID: input.Document.ExternalID,
		Revision: input.Document.Revision, URI: input.Document.URI,
	}
	if app.ValidateOwnedSourceDocument(input.Source.ID, provenance) != nil {
		return RenderedFile{}, nil, ErrInvalid
	}
	document.Metadata.Title = title
	if document.Metadata.Extensions == nil {
		document.Metadata.Extensions = make(map[string]any)
	}
	if input.Source.Config.Filesystem.Flavor == knowl.SourceFlavorOKF {
		for _, owned := range []string{"id", "source_refs", "source_document"} {
			if _, exists := document.Metadata.Extensions[owned]; exists {
				return RenderedFile{}, nil, ErrInvalid
			}
		}
	}
	delete(document.Metadata.Extensions, "id")
	delete(document.Metadata.Extensions, "source_refs")
	delete(document.Metadata.Extensions, "source_document")
	if err := mergeKnowlExtension(document.Metadata.Extensions, pageID, app.SourceRefKey(input.RawSource), provenance); err != nil {
		return RenderedFile{}, nil, err
	}
	document.Body = strings.TrimLeft(document.Body, "\n")
	if document.Body == "" || document.Body[len(document.Body)-1] != '\n' {
		document.Body += "\n"
	}
	content, err := okf.RenderConcept(bundleRelative, document, formatLimits)
	if err != nil {
		return RenderedFile{}, nil, ErrInvalid
	}
	file, err := NewRenderedFile(target, content, limits)
	return file, nil, err
}

func boundedObservedVersion(version string) string {
	const maxVersionBytes = 128
	version = strings.TrimSpace(version)
	if len(version) > maxVersionBytes {
		return "<oversized>"
	}
	return version
}

func formatVersion(flavor string) string {
	if flavor == knowl.SourceFlavorOKF {
		return OKFFormatVersion
	}
	return FormatVersion
}

func mergeKnowlExtension(metadata map[string]any, pageID, sourceRef string, provenance knowl.SourceDocument) error {
	extension := make(map[string]any)
	if raw, present := metadata["knowl"]; present {
		values, ok := raw.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		for key, value := range values {
			switch key {
			case "id", "source_refs", "source_document":
				return ErrInvalid
			default:
				extension[key] = value
			}
		}
	}
	extension["id"] = pageID
	extension["source_refs"] = []string{sourceRef}
	extension["source_document"] = map[string]any{
		"source_id":   string(provenance.SourceID),
		"document_id": string(provenance.DocumentID),
		"revision":    provenance.Revision,
		"uri":         provenance.URI,
	}
	metadata["knowl"] = extension
	return nil
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

func resolveTitle(explicit, body, relative string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
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
