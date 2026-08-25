package normalize

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestRenderObsidianRewritesSupportedNotesAndAssets(t *testing.T) {
	body := "# Alpha\n\n[[nested/Beta.md#Heading|Second]]\n![[Beta]]\n[[assets/diagram.png|Diagram]]\n![[diagram.png]]\n![[assets/my diagram(1).png]]\n[[Missing]]\n![[missing.bin]]\n[[../escape]]\n"
	input := renderInput(t, engineeringSource, "notes/Alpha.md", body, knowl.SourceFlavorObsidian)
	input.Catalog = buildCatalog(t,
		catalogRef("notes/Alpha.md", "markdown"),
		catalogRef("nested/Beta.md", "markdown"),
		catalogRef("assets/diagram.png", "asset"),
		catalogRef("assets/my diagram(1).png", "asset"),
	)
	result, err := Render(input, DefaultLimits())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	content := string(result.Files()[0].Content())
	for _, wanted := range []string{
		"[[sources/engineering/nested/Beta#Heading|Second]]",
		"![[sources/engineering/nested/Beta]]",
		"[Diagram](../assets/diagram.png)",
		"![](../assets/diagram.png)",
		"![](../assets/my%20diagram%281%29.png)",
		"[[Missing]]",
		"![unsupported obsidian embed](missing.bin)",
		"[[../escape]]",
	} {
		if !strings.Contains(content, wanted) {
			t.Fatalf("content missing %q:\n%s", wanted, content)
		}
	}
}

func TestRenderObsidianAmbiguityIsOrderIndependentAndExactWins(t *testing.T) {
	body := "# Alpha\n\n[[Beta]]\n[[docs/Beta|Exact]]\n"
	refs := []knowl.DocumentRef{
		catalogRef("Alpha.md", "markdown"),
		catalogRef("docs/Beta.md", "markdown"),
		catalogRef("other/Beta.md", "markdown"),
	}
	firstCatalog := buildCatalog(t, refs...)
	slices.Reverse(refs)
	secondCatalog := buildCatalog(t, refs...)
	if firstCatalog.Digest() != secondCatalog.Digest() {
		t.Fatal("catalog digest changed with descriptor order")
	}

	input := renderInput(t, engineeringSource, "Alpha.md", body, knowl.SourceFlavorObsidian)
	input.Catalog = firstCatalog
	result, err := Render(input, DefaultLimits())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	content := string(result.Files()[0].Content())
	if !strings.Contains(content, "[[Beta]]") || !strings.Contains(content, "[[sources/engineering/docs/Beta|Exact]]") {
		t.Fatalf("ambiguity/exact output:\n%s", content)
	}
}

func TestRenderMarkdownFlavorBypassesObsidianNormalization(t *testing.T) {
	body := "# Alpha\n\n[[Beta|Alias]]\n![[diagram.png]]\n"
	input := renderInput(t, engineeringSource, "Alpha.md", body, knowl.SourceFlavorMarkdown)
	input.Catalog = buildCatalog(t,
		catalogRef("Alpha.md", "markdown"),
		catalogRef("Beta.md", "markdown"),
		catalogRef("diagram.png", "asset"),
	)
	result, err := Render(input, DefaultLimits())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	content := string(result.Files()[0].Content())
	if !strings.Contains(content, "[[Beta|Alias]]") || !strings.Contains(content, "![[diagram.png]]") || strings.Contains(content, "sources/engineering/Beta") {
		t.Fatalf("Markdown flavor rewrote references:\n%s", content)
	}
}

func TestRelevantCatalogChangeRerendersStoredRawWithoutRevisionChange(t *testing.T) {
	body := "# Alpha\n\n[[Beta]]\n"
	input := renderInput(t, engineeringSource, "Alpha.md", body, knowl.SourceFlavorObsidian)
	input.Catalog = buildCatalog(t,
		catalogRef("Alpha.md", "markdown"),
		catalogRef("docs/Beta.md", "markdown"),
	)
	first, err := Render(input, DefaultLimits())
	if err != nil {
		t.Fatalf("Render(first) error = %v", err)
	}
	externalRevision := input.Document.Revision
	rawVersion := input.RawSource.Version

	input.Catalog = buildCatalog(t,
		catalogRef("Alpha.md", "markdown"),
		catalogRef("docs/Beta.md", "markdown"),
		catalogRef("other/Beta.md", "markdown"),
	)
	second, err := Render(input, DefaultLimits())
	if err != nil {
		t.Fatalf("Render(second) error = %v", err)
	}
	if bytes.Equal(first.Files()[0].Content(), second.Files()[0].Content()) || first.MirrorDigest() == second.MirrorDigest() {
		t.Fatal("relevant catalog change did not rerender mirror")
	}
	if input.Document.Revision != externalRevision || input.RawSource.Version != rawVersion {
		t.Fatal("catalog-only rerender changed external/raw revision identity")
	}
	if !strings.Contains(string(first.Files()[0].Content()), "[[sources/engineering/docs/Beta]]") ||
		!strings.Contains(string(second.Files()[0].Content()), "[[Beta]]") {
		t.Fatalf("catalog-only outputs:\nfirst=%s\nsecond=%s", first.Files()[0].Content(), second.Files()[0].Content())
	}
}

func TestObsidianMalformedReferenceIsPreserved(t *testing.T) {
	body := "# Alpha\n\nBefore ![[unclosed after"
	input := renderInput(t, engineeringSource, "Alpha.md", body, knowl.SourceFlavorObsidian)
	result, err := Render(input, DefaultLimits())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(string(result.Files()[0].Content()), "Before ![[unclosed after") {
		t.Fatalf("malformed reference changed:\n%s", result.Files()[0].Content())
	}
}

func buildCatalog(t *testing.T, refs ...knowl.DocumentRef) Catalog {
	t.Helper()
	catalog, err := BuildCatalog(refs, DefaultLimits())
	if err != nil {
		t.Fatalf("BuildCatalog() error = %v", err)
	}
	return catalog
}
