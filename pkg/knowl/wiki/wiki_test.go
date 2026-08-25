package wiki

import (
	"reflect"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	pageOneID         = "entities/one"
	pageTwoID         = "entities/two"
	conceptLinkFromID = "sources/engineering/docs/one"
	testReferenceType = "Reference"
)

func TestParseFrontmatterTrimsFields(t *testing.T) {
	metadata, err := ParseFrontmatter("---\nid:  " + pageOneID + " \ntitle:  One \ntype:  entity \nsource_refs:\n  - alpha:one@v1\n  -  beta:two@v2 \n---\n# One\n")
	if err != nil {
		t.Fatalf("ParseFrontmatter() error = %v", err)
	}
	if metadata.ID != pageOneID || metadata.Title != "One" || metadata.Type != "entity" {
		t.Fatalf("ParseFrontmatter() metadata = %#v", metadata)
	}
	if want := []string{"alpha:one@v1", "beta:two@v2"}; !reflect.DeepEqual(metadata.SourceRefs, want) {
		t.Fatalf("ParseFrontmatter() source refs = %#v, want %#v", metadata.SourceRefs, want)
	}
}

func TestParseFrontmatterCarriesOptionalSourceDocument(t *testing.T) {
	content := "---\nid: sources/engineering/auth\ntitle: Auth\ntype: source\nsource_refs:\n  - raw:auth@1\nsource_document:\n  source_id: ' engineering '\n  document_id: ' architecture/auth.md '\n  revision: ' revision-1 '\n  uri: ' https://wiki.example.test/auth '\n---\n# Auth\n"
	metadata, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatal(err)
	}
	want := &knowl.SourceDocument{SourceID: "engineering", DocumentID: "architecture/auth.md", Revision: "revision-1", URI: "https://wiki.example.test/auth"}
	if !reflect.DeepEqual(metadata.SourceDocument, want) || !reflect.DeepEqual(SourceDocument(content), want) {
		t.Fatalf("source document = %#v", metadata.SourceDocument)
	}
	copied := SourceDocument(content)
	copied.Revision = "changed"
	if metadata.SourceDocument.Revision != "revision-1" {
		t.Fatal("SourceDocument() returned aliased metadata")
	}
	curated, err := ParseFrontmatter("---\nid: entities/one\ntitle: One\ntype: entity\nsource_refs: [raw:one@1]\n---\n# One\n")
	if err != nil || curated.SourceDocument != nil || SourceDocument("# no frontmatter") != nil {
		t.Fatalf("curated source document = %#v, %v", curated.SourceDocument, err)
	}
}

func TestParseFrontmatterReadsNamespacedKnowlMetadata(t *testing.T) {
	content := "---\ntype: Reference\ntitle: Auth\nknowl:\n  vendor: retained\n  id: sources/engineering/auth\n  source_refs: [raw:auth@1]\n  source_document:\n    source_id: engineering\n    document_id: architecture/auth.md\n    revision: revision-1\n    uri: https://wiki.example.test/auth\n---\n# Auth\n"
	metadata, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("ParseFrontmatter() error = %v", err)
	}
	if metadata.Legacy || metadata.ID != "sources/engineering/auth" || metadata.Type != testReferenceType || len(metadata.SourceRefs) != 1 || metadata.SourceDocument == nil {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestParseFrontmatterRejectsEnvelopeCollision(t *testing.T) {
	content := "---\ntype: Reference\ntitle: Auth\nid: legacy\nknowl:\n  id: namespaced\n  source_refs: [raw:auth@1]\n---\n# Auth\n"
	if _, err := ParseFrontmatter(content); err == nil {
		t.Fatal("ParseFrontmatter() accepted ambiguous legacy and namespaced IDs")
	}
}

func TestParseFrontmatterRejectsMalformedBlocks(t *testing.T) {
	t.Run("missing opening", func(t *testing.T) {
		if _, err := ParseFrontmatter("# no frontmatter\n"); err == nil {
			t.Fatal("ParseFrontmatter() error = nil, want error")
		}
	})
	t.Run("missing closing", func(t *testing.T) {
		if _, err := ParseFrontmatter("---\nid: " + pageOneID + "\n"); err == nil {
			t.Fatal("ParseFrontmatter() error = nil, want error")
		}
	})
	t.Run("invalid yaml", func(t *testing.T) {
		if _, err := ParseFrontmatter("---\nsource_refs: [\n---\n"); err == nil {
			t.Fatal("ParseFrontmatter() error = nil, want error")
		}
	})
}

func TestBodyStripsOnlyCompleteLeadingFrontmatter(t *testing.T) {
	content := "---\ntitle: Глоссарий-проекта\nsource_refs:\n  - raw:glossary@1\n---\n\nПолезный пользовательский текст.\n"
	if got, want := Body(content), "Полезный пользовательский текст.\n"; got != want {
		t.Fatalf("Body() = %q, want %q", got, want)
	}
	for _, unchanged := range []string{"# Heading\nBody\n", "---\ntitle: incomplete\n"} {
		if got := Body(unchanged); got != unchanged {
			t.Fatalf("Body(%q) = %q, want unchanged", unchanged, got)
		}
	}
}

func TestMarkdownTargetsAndLinksNormalizeAndDedupe(t *testing.T) {
	content := "[[wiki/" + pageOneID + ".md|One]] [[" + pageTwoID + "#anchor]] [[" + pageOneID + "]] [[../bad]] [[ ]] [[broken"
	targets, malformed := MarkdownTargets(content)
	if !malformed {
		t.Fatal("MarkdownTargets() malformed = false, want true")
	}
	if want := []string{pageOneID, pageTwoID, pageOneID}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("MarkdownTargets() = %#v, want %#v", targets, want)
	}
	links := Links("entities/source", content)
	if want := []knowl.LinkReference{
		{From: "entities/source", To: pageOneID, Relation: relationWiki},
		{From: "entities/source", To: pageTwoID, Relation: relationWiki},
	}; !reflect.DeepEqual(links, want) {
		t.Fatalf("Links() = %#v, want %#v", links, want)
	}
}

func TestConceptLinksResolveRelativeCommonMarkAndIgnoreNonConcepts(t *testing.T) {
	content := "[Two](../two.md) [Bundle root](/root.md) [Unicode](%D0%9E%D0%B1%D0%B7%D0%BE%D1%80.md#part) [External](https://example.test/page.md) [Asset](logo.png) [Anchor](#local) [Broken](../missing.md) [Reference][deep]\n\n[deep]: nested/deep.md \"title\"\n`[Code](../code.md)`\n```md\n[Fenced](../fenced.md)\n```\n"
	links := ConceptLinks(conceptLinkFromID, content)
	want := []knowl.LinkReference{
		{From: conceptLinkFromID, To: "sources/engineering/two", Relation: relationOKF},
		{From: conceptLinkFromID, To: "sources/engineering/root", Relation: relationOKF},
		{From: conceptLinkFromID, To: "sources/engineering/docs/Обзор", Relation: relationOKF},
		{From: conceptLinkFromID, To: "sources/engineering/missing", Relation: relationOKF},
		{From: conceptLinkFromID, To: "sources/engineering/docs/nested/deep", Relation: relationOKF},
	}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("ConceptLinks() = %#v, want %#v", links, want)
	}
}

func TestConceptLinksResolveCuratedBundleRoot(t *testing.T) {
	links := ConceptLinks("concepts/nested/one", "[Entity](/entities/two.md)")
	want := []knowl.LinkReference{{From: "concepts/nested/one", To: "entities/two", Relation: relationOKF}}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("ConceptLinks() = %#v, want %#v", links, want)
	}
}

func TestIndexTargetsIncludeBareListItems(t *testing.T) {
	content := "# Index\n\n- " + pageOneID + "\n- wiki/" + pageTwoID + ".md\n[[entities/three]]\n"
	targets, malformed := IndexTargets(content)
	if malformed {
		t.Fatal("IndexTargets() malformed = true, want false")
	}
	if want := []string{"entities/three", pageOneID, pageTwoID}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("IndexTargets() = %#v, want %#v", targets, want)
	}
}

func TestMarkdownTargetsIgnoreAzureWikiNavigationDirectives(t *testing.T) {
	targets, malformed := MarkdownTargets("[[_TOC_]]\n[[_TOSP_]]\n[[Архитектура/Обзор]]\n")
	if malformed {
		t.Fatal("MarkdownTargets() malformed = true, want false")
	}
	want := []string{"Архитектура/Обзор"}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("MarkdownTargets() = %#v, want %#v", targets, want)
	}
}

func TestPageIDFromPath(t *testing.T) {
	tests := []struct {
		path string
		want knowl.PageID
		ok   bool
	}{
		{path: "wiki/" + pageOneID + ".md", want: pageOneID, ok: true},
		{path: "wiki/index.md", ok: false},
		{path: "wiki/log.md", ok: false},
		{path: "wiki/nested/index.md", ok: false},
		{path: "wiki/nested/log.md", ok: false},
		{path: "schema.md", ok: false},
		{path: "wiki/../one.md", ok: false},
	}
	for _, tt := range tests {
		got, ok := PageIDFromPath(tt.path)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("PageIDFromPath(%q) = (%q, %t), want (%q, %t)", tt.path, got, ok, tt.want, tt.ok)
		}
	}
}

func TestMigrateLegacyEnvelopePreservesUnknownExtensions(t *testing.T) {
	metadata := okf.Metadata{Type: testReferenceType, Extensions: map[string]any{
		ownedID: "concepts/one", ownedSourceRefs: []any{"raw:one@1"},
		"custom": map[string]any{"retained": true}, "knowl": map[string]any{"future": "value"},
	}}
	migrated, changed, err := MigrateLegacyEnvelope(metadata)
	if err != nil || !changed {
		t.Fatalf("MigrateLegacyEnvelope() = %#v, %v, %v", migrated, changed, err)
	}
	if _, exists := migrated.Extensions["id"]; exists {
		t.Fatal("flat ID survived migration")
	}
	knowlExtension, ok := migrated.Extensions["knowl"].(map[string]any)
	if !ok || knowlExtension["id"] != "concepts/one" || knowlExtension["future"] != "value" || migrated.Extensions["custom"] == nil {
		t.Fatalf("migrated extensions = %#v", migrated.Extensions)
	}
	if _, exists := metadata.Extensions["knowl"].(map[string]any)["id"]; exists {
		t.Fatal("migration mutated input metadata")
	}
}

func TestMigrateLegacyEnvelopeRejectsOwnedCollision(t *testing.T) {
	metadata := okf.Metadata{Type: testReferenceType, Extensions: map[string]any{
		ownedID: "flat", "knowl": map[string]any{ownedID: "namespaced"},
	}}
	if _, _, err := MigrateLegacyEnvelope(metadata); err == nil {
		t.Fatal("MigrateLegacyEnvelope() error = nil")
	}
}
