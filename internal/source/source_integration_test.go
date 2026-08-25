package source_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/internal/source/filesystem"
	"github.com/baldaworks/knowl/internal/source/normalize"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

func TestFilesystemNormalizationVerticalSlice(t *testing.T) {
	t.Parallel()

	const (
		pagePath  = "same/Page.md"
		assetPath = "assets/logo.bin"
		pageBody  = "# Shared page\n\n[[guides/Guide]]\n[[Topic]]\n[[one/Topic]]\n![[assets/logo.bin]]\n"
	)
	assetBody := []byte{'L', 'O', 'G', 'O', 0, 0xff}

	limits := filesystem.DefaultLimits()
	limits.PageSize = 2
	adapter, err := filesystem.New(limits)
	if err != nil {
		t.Fatalf("filesystem.New() error = %v", err)
	}

	sources := []knowl.Source{
		integrationSource(t, "alpha", knowl.SourceFlavorObsidian, pageBody, assetBody),
		integrationSource(t, "beta", knowl.SourceFlavorMarkdown, pageBody, assetBody),
	}
	outputs := make(map[knowl.SourceID]map[string]normalize.RenderedFile, len(sources))
	selectedFetches := 0

	for _, source := range sources {
		refs, pages := listAll(t, adapter, source)
		if pages < 2 || len(refs) != 5 {
			t.Fatalf("List(%s) returned %d descriptors in %d pages", source.ID, len(refs), pages)
		}
		catalog, err := normalize.BuildCatalog(refs, normalize.DefaultLimits())
		if err != nil {
			t.Fatalf("BuildCatalog(%s) error = %v", source.ID, err)
		}

		fetch := func(documentPath string) knowl.Document {
			t.Helper()
			selectedFetches++
			document, fetchErr := adapter.Fetch(context.Background(), source, refByPath(t, refs, documentPath))
			if fetchErr != nil {
				t.Fatalf("Fetch(%s, %s) error = %v", source.ID, documentPath, fetchErr)
			}
			return document
		}
		page := fetch(pagePath)
		asset := fetch(assetPath)
		if page.Revision != refByPath(t, refs, pagePath).Revision || asset.Revision != refByPath(t, refs, assetPath).Revision {
			t.Fatalf("Fetch(%s) did not preserve listed revisions", source.ID)
		}

		rendered := make(map[string]normalize.RenderedFile, 2)
		for _, document := range []knowl.Document{page, asset} {
			result, renderErr := normalize.Render(normalize.RenderInput{
				Source: source, Document: document, RawSource: accepted(document, source.ID), Catalog: catalog,
			}, normalize.DefaultLimits())
			if renderErr != nil {
				t.Fatalf("Render(%s, %s) error = %v", source.ID, document.Path, renderErr)
			}
			for _, file := range result.Files() {
				rendered[document.Path] = file
			}
		}
		outputs[source.ID] = rendered

		pageFile := rendered[pagePath]
		metadata, parseErr := knowlwiki.ParseFrontmatter(string(pageFile.Content()))
		if parseErr != nil {
			t.Fatalf("ParseFrontmatter(%s) error = %v", source.ID, parseErr)
		}
		wantURI := "https://fixtures.example.test/" + string(source.ID) + "/" + pagePath
		if metadata.SourceDocument == nil || metadata.SourceDocument.SourceID != source.ID ||
			metadata.SourceDocument.DocumentID != page.ExternalID || metadata.SourceDocument.Revision != page.Revision ||
			metadata.SourceDocument.URI != wantURI || len(metadata.SourceRefs) != 1 {
			t.Fatalf("provenance(%s) = %#v, refs = %#v", source.ID, metadata.SourceDocument, metadata.SourceRefs)
		}
		if !bytes.Equal(rendered[assetPath].Content(), assetBody) {
			t.Fatalf("asset(%s) was not byte-exact", source.ID)
		}

		stale := refByPath(t, refs, pagePath)
		stale.Revision = strings.Repeat("0", 64)
		_, staleErr := adapter.Fetch(context.Background(), source, stale)
		if !errors.Is(staleErr, filesystem.ErrRevisionChanged) || strings.Contains(staleErr.Error(), source.Config.Filesystem.Root) || strings.Contains(staleErr.Error(), pageBody) {
			t.Fatalf("Fetch(%s, stale) error is not stable and redacted: %v", source.ID, staleErr)
		}

		if source.ID == "alpha" {
			content := string(pageFile.Content())
			for _, wanted := range []string{
				"[[sources/alpha/guides/Guide]]", "[[Topic]]", "[[sources/alpha/one/Topic]]", "![](../assets/logo.bin)",
			} {
				if !strings.Contains(content, wanted) {
					t.Fatalf("Obsidian output missing %q:\n%s", wanted, content)
				}
			}

			withoutGuide := make([]knowl.DocumentRef, 0, len(refs)-1)
			for _, ref := range refs {
				if ref.Path != "guides/Guide.md" {
					withoutGuide = append(withoutGuide, ref)
				}
			}
			reduced, catalogErr := normalize.BuildCatalog(withoutGuide, normalize.DefaultLimits())
			if catalogErr != nil {
				t.Fatalf("BuildCatalog(reduced) error = %v", catalogErr)
			}
			rerendered, renderErr := normalize.Render(normalize.RenderInput{
				Source: source, Document: page, RawSource: accepted(page, source.ID), Catalog: reduced,
			}, normalize.DefaultLimits())
			if renderErr != nil {
				t.Fatalf("Render(reduced catalog) error = %v", renderErr)
			}
			if !strings.Contains(string(rerendered.Files()[0].Content()), "[[guides/Guide]]") || rerendered.MirrorDigest() == renderedMirrorDigest(t, source, page, catalog) {
				t.Fatal("catalog-only change did not produce a distinct unresolved mirror")
			}
		} else if !strings.Contains(string(pageFile.Content()), "[[guides/Guide]]") || strings.Contains(string(pageFile.Content()), "sources/beta/guides/Guide") {
			t.Fatalf("Markdown source references were unexpectedly rewritten:\n%s", pageFile.Content())
		}
	}

	if selectedFetches != 4 {
		t.Fatalf("selective fetch count = %d, want 4 for 10 listed descriptors", selectedFetches)
	}
	if outputs["alpha"][pagePath].Path() == outputs["beta"][pagePath].Path() || outputs["alpha"][assetPath].Path() == outputs["beta"][assetPath].Path() {
		t.Fatal("equal source-relative paths collided across source namespaces")
	}
}

func integrationSource(t *testing.T, id knowl.SourceID, flavor, pageBody string, assetBody []byte) knowl.Source {
	t.Helper()
	root := t.TempDir()
	fixtures := map[string][]byte{
		"same/Page.md":    []byte(pageBody),
		"guides/Guide.md": []byte("# Guide\n"),
		"one/Topic.md":    []byte("# Topic one\n"),
		"two/Topic.md":    []byte("# Topic two\n"),
		"assets/logo.bin": assetBody,
	}
	for relative, content := range fixtures {
		target := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", relative, err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", relative, err)
		}
	}
	return knowl.Source{
		ID: id, Type: knowl.SourceTypeFilesystem, Enabled: true,
		Config: knowl.SourceConfig{Filesystem: &knowl.FilesystemSourceConfig{
			Root: root, Include: []string{"**/*"}, Flavor: flavor, URIBase: "https://fixtures.example.test/" + string(id),
		}},
	}
}

func listAll(t *testing.T, adapter *filesystem.Adapter, source knowl.Source) ([]knowl.DocumentRef, int) {
	t.Helper()
	var refs []knowl.DocumentRef
	pages := 0
	for token := ""; ; {
		page, err := adapter.List(context.Background(), source, token)
		if err != nil {
			t.Fatalf("List(%s) error = %v", source.ID, err)
		}
		pages++
		refs = append(refs, page.Documents...)
		if page.NextPageToken == "" {
			return refs, pages
		}
		token = page.NextPageToken
	}
}

func refByPath(t *testing.T, refs []knowl.DocumentRef, documentPath string) knowl.DocumentRef {
	t.Helper()
	for _, ref := range refs {
		if ref.Path == documentPath {
			return ref
		}
	}
	t.Fatalf("descriptor %q not found", documentPath)
	return knowl.DocumentRef{}
}

func accepted(document knowl.Document, sourceID knowl.SourceID) knowl.AcceptedSource {
	return knowl.AcceptedSource{
		Scope: "integration", Source: knowl.SourceRef{Adapter: "wiki-filesystem", ID: string(sourceID) + "/" + document.Path},
		Version: knowl.SourceVersion{Version: document.Revision, Digest: document.Revision}, MediaType: document.MediaType,
		ManifestRef: "raw/" + string(sourceID) + "/manifest.yaml",
	}
}

func renderedMirrorDigest(t *testing.T, source knowl.Source, document knowl.Document, catalog normalize.Catalog) string {
	t.Helper()
	result, err := normalize.Render(normalize.RenderInput{
		Source: source, Document: document, RawSource: accepted(document, source.ID), Catalog: catalog,
	}, normalize.DefaultLimits())
	if err != nil {
		t.Fatalf("Render(full catalog digest) error = %v", err)
	}
	return result.MirrorDigest()
}
