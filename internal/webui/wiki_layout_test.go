package webui

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func TestKnowledgeNativeContextAndSingleTitle(t *testing.T) {
	for _, authored := range []string{"Team", "Distinct authored heading"} {
		t.Run(authored, func(t *testing.T) {
			f := canonicalKnowledgeFixture()
			p := f.pages[renderTestCatalogID]
			p.Markdown = "# " + authored + "\n\n* [Distant](../../sources/distant/leaf.md)\n  Complete introduction.\n\n# Further reading\n\n* [Other](../other/index.md)\n  Last authored paragraph.\n"
			f.pages[renderTestCatalogID] = p
			w, doc := fragmentDocument(t, screenHandler(t, f), knowledgeFragment+"?parent_id="+url.QueryEscape(renderTestCatalogID))
			if w.Code != 200 {
				t.Fatalf("status=%d", w.Code)
			}
			var titles, headings []string
			tabs := map[string]string{}
			var context, outline bool
			walk(doc, func(n *html.Node) {
				if n.DataAtom == atom.H1 {
					titles = append(titles, nodeText(n))
				}
				if n.DataAtom == atom.H2 {
					headings = append(headings, nodeText(n))
				}
				if hasAttribute(n, "role", "tab") {
					tabs[attribute(n, "id")] = attribute(n, "data-bs-target")
				}
				if hasAttribute(n, "id", "page-context") {
					context = hasAttribute(n, "class", "offcanvas-xl offcanvas-end")
				}
				if n.DataAtom == atom.A && nodeText(n) == "Further reading" && strings.HasPrefix(attribute(n, "href"), "#knowl-heading-") {
					outline = true
				}
			})
			if !reflect.DeepEqual(titles, []string{renderTestCatalogTitle}) || !context || !outline || !reflect.DeepEqual(tabs, map[string]string{"contents-tab": "#contents", "sources-tab": "#page-sources", "details-tab": "#details"}) {
				t.Fatalf("titles=%v context=%v outline=%v tabs=%v", titles, context, outline, tabs)
			}
			if authored != renderTestCatalogTitle && !containsString(headings, authored) {
				t.Errorf("distinct authored heading lost: %v", headings)
			}
		})
	}
}

func TestMarkdownTablesAndSafeHeadingAnchors(t *testing.T) {
	rendered, err := RenderMarkdown("## Repeated\n\n### Repeated\n\n| Name | Link |\n| --- | --- |\n| Accepted | [safe](https://user:secret@example.com/doc?private=1) |\n\n<h2 id='knowl-heading-1' hx-get='/unsafe' onclick='attack()'>Hostile</h2>\n")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(strings.NewReader(string(rendered)))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	var cells []string
	var tables, wrappers int
	walk(doc, func(n *html.Node) {
		if n.DataAtom == atom.H2 || n.DataAtom == atom.H3 {
			id := attribute(n, "id")
			if !strings.HasPrefix(id, "knowl-heading-") || ids[id] {
				t.Errorf("unsafe/duplicate generated headingid=%q", id)
			}
			ids[id] = true
		}
		if n.DataAtom == atom.Table {
			tables++
		}
		if hasAttribute(n, "class", "table-responsive") {
			wrappers++
		}
		if n.DataAtom == atom.Th || n.DataAtom == atom.Td {
			cells = append(cells, nodeText(n))
		}
		for _, a := range n.Attr {
			if strings.HasPrefix(a.Key, "on") || strings.HasPrefix(a.Key, "hx-") || strings.HasPrefix(a.Key, "data-") {
				t.Errorf("activeattribute=%s", a.Key)
			}
		}
	})
	if len(ids) != 2 || tables != 1 || wrappers != 1 || !reflect.DeepEqual(cells, []string{"Name", "Link", "Accepted", "safe"}) {
		t.Fatalf("ids=%v tables=%d wrappers=%d cells=%v", ids, tables, wrappers, cells)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
