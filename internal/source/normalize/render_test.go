package normalize

import (
	"bytes"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

func TestRenderMarkdownPreservesExtrasAndReplacesReservedFrontmatter(t *testing.T) {
	raw := "---\nid: old\ntitle: Explicit title\ntype: old\nsource_refs: [old]\nsource_document:\n  source_id: old\n  document_id: old\n  revision: old\n  uri: https://old.example.test\ntags: [one, two]\nweight: 7\n---\n# Ignored heading\n\nBody\n"
	input := renderInput(t, engineeringSource, "nested/Page.MD", raw, knowl.SourceFlavorMarkdown)
	result, err := Render(input, DefaultLimits())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if result.FormatVersion() != FormatVersion || result.CatalogDigest() != input.Catalog.Digest() || len(result.MirrorDigest()) != 64 {
		t.Fatalf("result identity = %#v", result)
	}
	files := result.Files()
	if len(files) != 1 || files[0].Path() != "wiki/sources/engineering/nested/Page.md" {
		t.Fatalf("files = %#v", files)
	}
	content := string(files[0].Content())
	metadata, err := knowlwiki.ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("ParseFrontmatter() error = %v\n%s", err, content)
	}
	if metadata.ID != "sources/engineering/nested/Page" || metadata.Title != "Explicit title" || metadata.Type != sourcePageType {
		t.Fatalf("metadata = %#v", metadata)
	}
	if len(metadata.SourceRefs) != 1 || metadata.SourceRefs[0] != app.SourceRefKey(input.RawSource) {
		t.Fatalf("source refs = %#v", metadata.SourceRefs)
	}
	if metadata.SourceDocument == nil || metadata.SourceDocument.SourceID != engineeringSource || metadata.SourceDocument.DocumentID != "nested/Page.MD" ||
		metadata.SourceDocument.Revision != input.Document.Revision || metadata.SourceDocument.URI != input.Document.URI {
		t.Fatalf("source document = %#v", metadata.SourceDocument)
	}
	for _, wanted := range []string{"tags:", "- one", "- two", "weight: 7", "# Ignored heading", bodyText} {
		if !strings.Contains(content, wanted) {
			t.Fatalf("content missing %q:\n%s", wanted, content)
		}
	}
	if strings.Contains(content, "id: old") || strings.Contains(content, "https://old.example.test") {
		t.Fatalf("reserved source frontmatter survived:\n%s", content)
	}

	repeated, err := Render(input, DefaultLimits())
	if err != nil {
		t.Fatalf("Render(repeated) error = %v", err)
	}
	if !bytes.Equal(repeated.Files()[0].Content(), files[0].Content()) || repeated.MirrorDigest() != result.MirrorDigest() {
		t.Fatal("rendering is not deterministic")
	}
}

func TestRenderMarkdownTitleFallbacksAndMalformedFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content string
		title   string
		body    string
	}{
		{name: "heading", path: "heading.md", content: "# Heading title\n" + bodyText, title: "Heading title", body: bodyText},
		{name: "filename", path: "nested/Filename.md", content: bodyText, title: "Filename", body: bodyText},
		{name: "malformed", path: "malformed.md", content: "---\ntitle: [invalid\n---\nBody", title: "malformed", body: "title: [invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := renderInput(t, engineeringSource, test.path, test.content, knowl.SourceFlavorMarkdown)
			result, err := Render(input, DefaultLimits())
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			content := string(result.Files()[0].Content())
			metadata, err := knowlwiki.ParseFrontmatter(content)
			if err != nil {
				t.Fatalf("ParseFrontmatter() error = %v", err)
			}
			if metadata.Title != test.title || !strings.Contains(content, test.body) {
				t.Fatalf("metadata/content = %#v\n%s", metadata, content)
			}
		})
	}
}

func TestRenderIsolatesEqualPathsAcrossSources(t *testing.T) {
	engineering := renderInput(t, engineeringSource, "same/page.md", "# Page\n", knowl.SourceFlavorMarkdown)
	operations := renderInput(t, "operations", "same/page.md", "# Page\n", knowl.SourceFlavorMarkdown)
	first, err := Render(engineering, DefaultLimits())
	if err != nil {
		t.Fatalf("Render(engineering) error = %v", err)
	}
	second, err := Render(operations, DefaultLimits())
	if err != nil {
		t.Fatalf("Render(operations) error = %v", err)
	}
	if first.Files()[0].Path() == second.Files()[0].Path() || first.MirrorDigest() == second.MirrorDigest() {
		t.Fatalf("source outputs collided: %#v %#v", first, second)
	}
}

func TestRenderAssetPreservesExactBytesAndNamespace(t *testing.T) {
	input := renderInput(t, engineeringSource, "assets/logo.png", "\x00PNG\xff", knowl.SourceFlavorObsidian)
	result, err := Render(input, DefaultLimits())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	files := result.Files()
	if len(files) != 1 || files[0].Path() != "wiki/sources/engineering/assets/logo.png" || !bytes.Equal(files[0].Content(), input.Document.Content) {
		t.Fatalf("asset files = %#v", files)
	}
	copyContent := files[0].Content()
	copyContent[0] = 'X'
	if bytes.Equal(copyContent, result.Files()[0].Content()) {
		t.Fatal("result exposed mutable asset content")
	}
}

func TestRenderRejectsIncompleteProvenanceAndBounds(t *testing.T) {
	input := renderInput(t, engineeringSource, "page.md", "# Page\n", knowl.SourceFlavorMarkdown)
	input.Document.URI = "relative"
	if _, err := Render(input, DefaultLimits()); err == nil {
		t.Fatal("Render() accepted relative provenance URI")
	}

	input = renderInput(t, engineeringSource, "page.md", "content", knowl.SourceFlavorMarkdown)
	limits := DefaultLimits()
	limits.MaxRenderedBytes = 4
	if _, err := Render(input, limits); err == nil {
		t.Fatal("Render() accepted oversized output")
	}

	input = renderInput(t, engineeringSource, "page.md", "content", knowl.SourceFlavorMarkdown)
	input.RawSource.Version.Version = textDigest("other")
	if _, err := Render(input, DefaultLimits()); err == nil {
		t.Fatal("Render() accepted unrelated raw source revision")
	}

	input = renderInput(t, engineeringSource, "page.md", "content", knowl.SourceFlavorMarkdown)
	otherCatalog, err := BuildCatalog([]knowl.DocumentRef{catalogRef("other.md", "markdown")}, DefaultLimits())
	if err != nil {
		t.Fatalf("BuildCatalog(other) error = %v", err)
	}
	input.Catalog = otherCatalog
	if _, err := Render(input, DefaultLimits()); err == nil {
		t.Fatal("Render() accepted a catalog without its document")
	}
}

func renderInput(t *testing.T, sourceID knowl.SourceID, documentPath, content, flavor string) RenderInput {
	t.Helper()
	revision := textDigest(content)
	ref := knowl.DocumentRef{
		ExternalID: knowl.DocumentID(documentPath), Path: documentPath, Revision: revision,
		Metadata: map[string]string{"kind": "markdown"},
	}
	catalog, err := BuildCatalog([]knowl.DocumentRef{ref}, DefaultLimits())
	if err != nil {
		t.Fatalf("BuildCatalog() error = %v", err)
	}
	raw := acceptedSource()
	raw.Source.ID = string(sourceID) + "/" + documentPath
	raw.Version = knowl.SourceVersion{Version: revision, Digest: revision}
	mediaType := "text/markdown"
	if !strings.EqualFold(pathExtension(documentPath), ".md") {
		mediaType = "application/octet-stream"
	}
	return RenderInput{
		Source: knowl.Source{
			ID: sourceID, Type: knowl.SourceTypeFilesystem, Enabled: true,
			Config: knowl.SourceConfig{Filesystem: &knowl.FilesystemSourceConfig{Root: "/source", Include: []string{"**/*"}, Flavor: flavor}},
		},
		Document: knowl.Document{
			DocumentRef: ref, Title: "Fetched title", URI: "https://wiki.example.test/" + documentPath,
			MediaType: mediaType, Content: []byte(content),
		},
		RawSource: raw,
		Catalog:   catalog,
	}
}

func pathExtension(value string) string {
	index := strings.LastIndex(value, ".")
	if index < 0 {
		return ""
	}
	return value[index:]
}
