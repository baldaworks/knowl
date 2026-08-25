package normalize

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	normalizationMetadataKey   = "kind"
	normalizationMarkdownMedia = "text/markdown"
)

func TestNormalizeSourcePortRendersFetchedAndRestoredRawIdentically(t *testing.T) {
	adapter := NewDefaultAdapter()
	ctx := context.Background()
	raw := "---\ntitle: Fetched\n---\n# Fetched\n\nBody\n"
	fetched := normalizationInput(t, engineeringSource, "docs/Page.md", raw, knowl.SourceFlavorMarkdown)
	restored := fetched
	restored.Document.Content = append([]byte(nil), fetched.Document.Content...)

	first, err := adapter.NormalizeSource(ctx, fetched)
	if err != nil {
		t.Fatalf("NormalizeSource() fetched error = %v", err)
	}
	second, err := adapter.NormalizeSource(ctx, restored)
	if err != nil {
		t.Fatalf("NormalizeSource() restored error = %v", err)
	}
	if first.FormatVersion != FormatVersion || len(first.MirrorDigest) != 64 || first.CatalogDigest == "" {
		t.Fatalf("result identities = %#v", first)
	}
	if !equalNormalizationResults(first, second) {
		t.Fatalf("fetched and restored diverged: %#v vs %#v", first, second)
	}
	if len(first.Mutations) != 1 || first.Mutations[0].Path != "wiki/sources/engineering/docs/Page.md" || len(first.Mutations[0].Content) == 0 {
		t.Fatalf("mutations = %#v", first.Mutations)
	}
	if first.Mutations[0].ExpectedDigest != "" || first.Mutations[0].Action != knowl.SourceMutationWrite {
		t.Fatalf("mutation shape = %#v", first.Mutations[0])
	}
}

func TestNormalizeSourceCatalogOnlyChangeAltersMirrorWithoutFetchIdentity(t *testing.T) {
	adapter := NewDefaultAdapter()
	ctx := context.Background()
	raw := "# Notes\n\nSee [[sibling]].\n"
	withSibling := normalizationInput(t, engineeringSource, "notes.md", raw, knowl.SourceFlavorObsidian)
	withSibling.Catalog = append(withSibling.Catalog, knowl.DocumentRef{
		ExternalID: "sibling.md", Revision: "rev-sibling", Path: "sibling.md",
	})
	withoutSibling := normalizationInput(t, engineeringSource, "notes.md", raw, knowl.SourceFlavorObsidian)

	resolved, err := adapter.NormalizeSource(ctx, withSibling)
	if err != nil {
		t.Fatalf("NormalizeSource(resolved) error = %v", err)
	}
	unresolved, err := adapter.NormalizeSource(ctx, withoutSibling)
	if err != nil {
		t.Fatalf("NormalizeSource(unresolved) error = %v", err)
	}
	if resolved.MirrorDigest == unresolved.MirrorDigest {
		t.Fatal("catalog-only change did not alter mirror digest")
	}
	if resolved.FormatVersion != unresolved.FormatVersion {
		t.Fatal("format version changed")
	}
	if got, want := string(resolved.Mutations[0].Content), string(unresolved.Mutations[0].Content); got == want {
		t.Fatal("rendered bodies identical despite catalog resolution difference")
	}
	for _, entry := range resolved.Mutations[0:1] {
		if !strings.Contains(string(entry.Content), "sources/engineering/sibling") {
			t.Fatalf("resolved body missing sibling link:\n%s", entry.Content)
		}
	}
}

func TestNormalizeSourceIsDeterministicAndDetached(t *testing.T) {
	adapter := NewDefaultAdapter()
	ctx := context.Background()
	input := normalizationInput(t, engineeringSource, "docs/Page.md", "# Page\n\nBody\n", knowl.SourceFlavorMarkdown)
	first, err := adapter.NormalizeSource(ctx, input)
	if err != nil {
		t.Fatalf("first NormalizeSource() error = %v", err)
	}
	second, err := adapter.NormalizeSource(ctx, input)
	if err != nil {
		t.Fatalf("second NormalizeSource() error = %v", err)
	}
	if !equalNormalizationResults(first, second) {
		t.Fatal("repeated normalization diverged")
	}
	mutated := first.Mutations[0].Content
	for index := range mutated {
		mutated[index] = 'x'
	}
	third, err := adapter.NormalizeSource(ctx, input)
	if err != nil {
		t.Fatalf("third NormalizeSource() error = %v", err)
	}
	if !equalNormalizationResults(first, third) && !equalNormalizationResults(second, third) {
		t.Fatal("returned mutation bytes alias normalizer state")
	}
}

func TestNormalizeSourceRejectsInvalidAndBoundedInputs(t *testing.T) {
	adapter := NewDefaultAdapter()
	ctx := context.Background()
	valid := normalizationInput(t, engineeringSource, "docs/Page.md", "# Page\n", knowl.SourceFlavorMarkdown)
	if _, err := adapter.NormalizeSource(ctx, valid); err != nil {
		t.Fatalf("valid fixture error = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := adapter.NormalizeSource(canceled, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v, want context canceled", err)
	}
	mismatched := valid
	mismatched.RawSource.Version.Digest = textDigest("other")
	if _, err := adapter.NormalizeSource(ctx, mismatched); err == nil {
		t.Fatal("identity mismatch accepted")
	}
	disabled := valid
	disabled.Source.Enabled = false
	if _, err := adapter.NormalizeSource(ctx, disabled); err == nil {
		t.Fatal("disabled source accepted")
	}
	missingFromCatalog := valid
	missingFromCatalog.Catalog = nil
	if _, err := adapter.NormalizeSource(ctx, missingFromCatalog); err == nil {
		t.Fatal("document missing from catalog accepted")
	}
	bounded := NewAdapter(Limits{MaxDocuments: 1, MaxPathBytes: 2048, MaxMetadataBytes: 4096, MaxRenderedBytes: 1 << 20})
	overflowing := normalizationInput(t, engineeringSource, "docs/Page.md", strings.Repeat("x", 2<<20), knowl.SourceFlavorMarkdown)
	if _, err := bounded.NormalizeSource(ctx, overflowing); err == nil {
		t.Fatal("oversized document accepted")
	}
	tooManyDescriptors := valid
	tooManyDescriptors.Catalog = []knowl.DocumentRef{
		{ExternalID: "a.md", Revision: "r", Path: "a.md"},
		{ExternalID: "b.md", Revision: "r", Path: "b.md"},
		validDescriptor("docs/Page.md"),
	}
	if _, err := bounded.NormalizeSource(ctx, tooManyDescriptors); err == nil {
		t.Fatal("over-limit catalog accepted")
	}
}

func equalNormalizationResults(left, right app.SourceNormalizationResult) bool {
	if left.FormatVersion != right.FormatVersion || left.CatalogDigest != right.CatalogDigest || left.MirrorDigest != right.MirrorDigest || len(left.Mutations) != len(right.Mutations) {
		return false
	}
	for index := range left.Mutations {
		if left.Mutations[index].Path != right.Mutations[index].Path || left.Mutations[index].Action != right.Mutations[index].Action ||
			left.Mutations[index].ExpectedDigest != right.Mutations[index].ExpectedDigest ||
			!bytes.Equal(left.Mutations[index].Content, right.Mutations[index].Content) {
			return false
		}
	}
	return true
}

func validDescriptor(documentPath string) knowl.DocumentRef {
	content := "# filler\n"
	return knowl.DocumentRef{ExternalID: knowl.DocumentID(documentPath), Revision: textDigest(content), Path: documentPath}
}

func normalizationInput(t *testing.T, sourceID knowl.SourceID, documentPath, content, flavor string) app.SourceNormalizationInput {
	t.Helper()
	revision := textDigest(content)
	ref := knowl.DocumentRef{
		ExternalID: knowl.DocumentID(documentPath), Path: documentPath, Revision: revision,
		Metadata: map[string]string{normalizationMetadataKey: "markdown"},
	}
	raw := acceptedSource()
	raw.Source.ID = string(sourceID) + "/" + documentPath
	raw.Version = knowl.SourceVersion{Version: revision, Digest: revision}
	mediaType := normalizationMarkdownMedia
	if !strings.EqualFold(pathExtension(documentPath), ".md") {
		mediaType = "application/octet-stream"
	}
	return app.SourceNormalizationInput{
		Source: knowl.Source{
			ID: sourceID, Type: knowl.SourceTypeFilesystem, Enabled: true,
			Config: knowl.SourceConfig{Filesystem: &knowl.FilesystemSourceConfig{Root: "/source", Include: []string{"**/*"}, Flavor: flavor}},
		},
		Document: knowl.Document{
			DocumentRef: ref, Title: "Fetched title", URI: "https://wiki.example.test/" + documentPath,
			MediaType: mediaType, Content: []byte(content),
		},
		RawSource: raw,
		Catalog:   []knowl.DocumentRef{ref},
	}
}
