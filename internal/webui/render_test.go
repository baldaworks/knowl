package webui

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
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
				if a.Key == "href" {
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
