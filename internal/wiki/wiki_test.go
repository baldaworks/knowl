package wiki

import (
	"reflect"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl"
)

func TestParseFrontmatterTrimsFields(t *testing.T) {
	metadata, err := ParseFrontmatter("---\nid:  entities/one \ntitle:  One \ntype:  entity \nsource_refs:\n  - alpha:one@v1\n  -  beta:two@v2 \n---\n# One\n")
	if err != nil {
		t.Fatalf("ParseFrontmatter() error = %v", err)
	}
	if metadata.ID != "entities/one" || metadata.Title != "One" || metadata.Type != "entity" {
		t.Fatalf("ParseFrontmatter() metadata = %#v", metadata)
	}
	if want := []string{"alpha:one@v1", "beta:two@v2"}; !reflect.DeepEqual(metadata.SourceRefs, want) {
		t.Fatalf("ParseFrontmatter() source refs = %#v, want %#v", metadata.SourceRefs, want)
	}
}

func TestParseFrontmatterRejectsMalformedBlocks(t *testing.T) {
	t.Run("missing opening", func(t *testing.T) {
		if _, err := ParseFrontmatter("# no frontmatter\n"); err == nil {
			t.Fatal("ParseFrontmatter() error = nil, want error")
		}
	})
	t.Run("missing closing", func(t *testing.T) {
		if _, err := ParseFrontmatter("---\nid: entities/one\n"); err == nil {
			t.Fatal("ParseFrontmatter() error = nil, want error")
		}
	})
	t.Run("invalid yaml", func(t *testing.T) {
		if _, err := ParseFrontmatter("---\nsource_refs: [\n---\n"); err == nil {
			t.Fatal("ParseFrontmatter() error = nil, want error")
		}
	})
}

func TestMarkdownTargetsAndLinksNormalizeAndDedupe(t *testing.T) {
	content := "[[wiki/entities/one.md|One]] [[entities/two#anchor]] [[entities/one]] [[../bad]] [[ ]] [[broken"
	targets, malformed := MarkdownTargets(content)
	if !malformed {
		t.Fatal("MarkdownTargets() malformed = false, want true")
	}
	if want := []string{"entities/one", "entities/two", "entities/one"}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("MarkdownTargets() = %#v, want %#v", targets, want)
	}
	links := Links("entities/source", content)
	if want := []knowl.LinkReference{
		{From: "entities/source", To: "entities/one", Relation: "wiki"},
		{From: "entities/source", To: "entities/two", Relation: "wiki"},
	}; !reflect.DeepEqual(links, want) {
		t.Fatalf("Links() = %#v, want %#v", links, want)
	}
}

func TestIndexTargetsIncludeBareListItems(t *testing.T) {
	content := "# Index\n\n- entities/one\n- wiki/entities/two.md\n[[entities/three]]\n"
	targets, malformed := IndexTargets(content)
	if malformed {
		t.Fatal("IndexTargets() malformed = true, want false")
	}
	if want := []string{"entities/three", "entities/one", "entities/two"}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("IndexTargets() = %#v, want %#v", targets, want)
	}
}

func TestPageIDFromPath(t *testing.T) {
	tests := []struct {
		path string
		want knowl.PageID
		ok   bool
	}{
		{path: "wiki/entities/one.md", want: "entities/one", ok: true},
		{path: "wiki/index.md", ok: false},
		{path: "wiki/log.md", ok: false},
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
