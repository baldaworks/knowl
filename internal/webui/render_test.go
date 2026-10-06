package webui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func TestMarkdownDeniesActiveContentAndPrivateURIs(t *testing.T) {
	rendered, err := RenderMarkdown("# Safe\n\n**Bold** [external](https://user:secret@example.com/doc?token=secret#private)\n\n![remote](https://evil.invalid/pixel)\n\n<script>alert(1)</script><svg onload=alert(1)></svg><a hx-get='/v1/retrieve' data-hx-get='/v1/retrieve' onclick='alert(1)'>bad</a>\n\n[unsafe](javascript:alert%281%29) [local](file:///private/path)")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(strings.NewReader(string(rendered)))
	if err != nil {
		t.Fatal(err)
	}
	var headings, links int
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.Data {
		case "img", "script", "svg", "iframe", "form", "input":
			t.Errorf("active element %s", n.Data)
		case "h1":
			headings++
		case "a":
			for _, a := range n.Attr {
				if a.Key == linkHrefAttribute {
					links++
					if a.Val != "https://example.com/doc" {
						t.Errorf("unsafe link %q", a.Val)
					}
				}
			}
		}
		for _, a := range n.Attr {
			if strings.HasPrefix(a.Key, "hx-") || strings.HasPrefix(a.Key, "data-hx-") || strings.HasPrefix(a.Key, "on") {
				t.Errorf("active attribute %s", a.Key)
			}
		}
	})
	if headings != 1 || links != 1 {
		t.Fatalf("headings=%d safe links=%d", headings, links)
	}
}

const (
	renderTestRootID       = "index"
	renderTestCatalogID    = "catalogs/team/index"
	renderTestRootTitle    = "Root"
	renderTestCatalogTitle = "Team"
)

func TestRenderIndexBodyAndLinks(t *testing.T) {
	for _, tc := range []struct {
		name, id, markdown, title string
		links                     map[string]string
	}{
		{
			name: "root version", id: renderTestRootID, title: renderTestRootTitle,
			markdown: "---\nokf_version: \"0.2\"\n---\n# Root\n\n* [Team](catalogs/team/index.md)\n* [Article](concepts/article.md)\n* [Log](log.md)\n* [External](https://user:secret@example.com/doc?token=secret#private)\n* [Unsafe](javascript:alert%281%29)\n* [Active](concepts/active.md) <a hx-get='/v1/retrieve' onclick='alert(1)'>bad</a>\n",
			links:    map[string]string{renderTestCatalogTitle: "/ui/knowledge?parent_id=catalogs%2Fteam%2Findex", "Article": "/ui/knowledge?page_id=concepts%2Farticle", "External": "https://example.com/doc", "Active": "/ui/knowledge?page_id=concepts%2Factive"},
		},
		{name: "root heading only", id: renderTestRootID, markdown: "# Root\n", title: renderTestRootTitle, links: map[string]string{}},
		{name: "nested heading only", id: renderTestCatalogID, markdown: "# Team\n", title: renderTestCatalogTitle, links: map[string]string{}},
		{
			name: "nested links", id: renderTestCatalogID, title: renderTestCatalogTitle,
			markdown: "# Team\n\n* [Root](../../index.md)\n* [Other](../other/index.md)\n* [Article](../../concepts/article.md)\n* [Escape](../../../private.md)\n",
			links:    map[string]string{renderTestRootTitle: "/ui/knowledge?parent_id=index", "Other": "/ui/knowledge?parent_id=catalogs%2Fother%2Findex", "Article": "/ui/knowledge?page_id=concepts%2Farticle"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := renderPageMarkdown(tc.markdown, tc.id)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := html.Parse(strings.NewReader(string(rendered)))
			if err != nil {
				t.Fatal(err)
			}
			var headings []string
			links := map[string]string{}
			walk(doc, func(n *html.Node) {
				if n.Type != html.ElementNode {
					return
				}
				switch n.DataAtom {
				case atom.H1:
					headings = append(headings, nodeText(n))
				case atom.Hr, atom.Script, atom.Img, atom.Svg, atom.Iframe, atom.Form, atom.Input:
					t.Errorf("unexpected index element %q", n.Data)
				case atom.A:
					for _, attr := range n.Attr {
						if attr.Key == linkHrefAttribute {
							links[nodeText(n)] = attr.Val
						}
					}
				}
				for _, attr := range n.Attr {
					if strings.HasPrefix(attr.Key, "hx-") || strings.HasPrefix(attr.Key, "data-hx-") || strings.HasPrefix(attr.Key, "on") {
						t.Errorf("active index attribute %q", attr.Key)
					}
				}
			})
			if !reflect.DeepEqual(headings, []string{tc.title}) || !reflect.DeepEqual(links, tc.links) {
				t.Fatalf("headings = %v, links = %v; want [%s], %v", headings, links, tc.title, tc.links)
			}
		})
	}
}

func TestRenderInvalidIndex(t *testing.T) {
	for _, tc := range []struct {
		name, id, markdown string
		rule               okf.Rule
	}{
		{"empty", renderTestRootID, "", okf.RuleIndexInvalid},
		{"invalid heading", renderTestRootID, "## Root\n", okf.RuleIndexInvalid},
		{"malformed root", renderTestRootID, "---\ntitle: Root\n---\n# Root\n", okf.RuleIndexInvalid},
		{"nested frontmatter", renderTestCatalogID, "---\nokf_version: \"0.2\"\n---\n# Team\n", okf.RuleIndexInvalid},
		{"invalid UTF8", renderTestRootID, "# Root\n\xff", okf.RuleUTF8Invalid},
		{"byte limit", renderTestRootID, "# " + strings.Repeat("x", okf.DefaultLimits().MaxBytes), okf.RuleSizeExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := renderPageMarkdown(tc.markdown, tc.id)
			var violation *okf.Violation
			if !errors.As(err, &violation) || violation.Rule != tc.rule || rendered != "" {
				t.Fatalf("invalid index rendered %d bytes, error %v; want rule %s", len(rendered), err, tc.rule)
			}
		})
	}
}

func TestCanonicalMarkdownBodyAndLinks(t *testing.T) {
	input := "---\ntype: topic\ntitle: Article\nknowl:\n  id: concepts/article\n---\n# Article\n\n[related](other.md) [escape](../../private.md) [asset](secret.png) [external](https://user:pass@example.com/a?secret=1)\n\n[[wiki/concepts/second.md|Second]] `[[concepts/code]]`"
	rendered, err := renderPageMarkdown(input, "concepts/article")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(strings.NewReader(string(rendered)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	walk(doc, func(n *html.Node) {
		if n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == linkHrefAttribute {
					got[nodeText(n)] = a.Val
				}
			}
		}
		if n.Data == "hr" {
			t.Error("front matter rendered as article")
		}
	})
	want := map[string]string{"related": "/ui/knowledge?page_id=concepts%2Fother", "external": "https://example.com/a", "Second": "/ui/knowledge?page_id=concepts%2Fsecond"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("links=%v want %v", got, want)
	}
}
