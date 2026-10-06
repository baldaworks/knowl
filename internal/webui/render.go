package webui

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark/v2/parser"
	markdownhtml "github.com/yuin/goldmark/v2/renderer/html"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const linkHrefAttribute = "href"

// RenderMarkdown is the sole trusted-HTML conversion. Raw HTML is disabled in
// goldmark; the independent allowlist excludes images and all active attributes.
// Relative links remain inert until resolved against canonical identities.
func RenderMarkdown(markdown string) (template.HTML, error) {
	source := []byte(markdown)
	document := parser.New().Parse(source)
	var rendered bytes.Buffer
	if err := markdownhtml.New().Render(&rendered, source, document); err != nil {
		return "", fmt.Errorf("render Markdown: %w", err)
	}
	policy := bluemonday.NewPolicy()
	policy.AllowElements("p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6", "strong", "em", "s", "blockquote", "ul", "ol", "li", "pre", "code", "a", "table", "thead", "tbody", "tr", "th", "td")
	policy.AllowAttrs(linkHrefAttribute).OnElements("a")
	policy.AllowURLSchemes("https", "http")
	policy.RequireNoReferrerOnLinks(true)
	nodes, err := html.ParseFragment(strings.NewReader(policy.Sanitize(rendered.String())), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return "", fmt.Errorf("parse sanitized Markdown: %w", err)
	}
	rendered.Reset()
	for _, node := range nodes {
		sanitizeLinks(node)
		if err = html.Render(&rendered, node); err != nil {
			return "", fmt.Errorf("serialize sanitized Markdown: %w", err)
		}
	}
	return template.HTML(rendered.String()), nil //nolint:gosec // Only allowlisted Markdown and parsed, redacted links reach this conversion.
}
func sanitizeLinks(node *html.Node) {
	if node.Type == html.ElementNode && node.Data == "a" {
		attrs := make([]html.Attribute, 0, 2)
		for _, attr := range node.Attr {
			if attr.Key == linkHrefAttribute {
				if safe := SafeURI(attr.Val); safe != "" {
					attrs = append(attrs, html.Attribute{Key: linkHrefAttribute, Val: safe}, html.Attribute{Key: "rel", Val: "noreferrer noopener"})
				}
			}
		}
		node.Attr = attrs
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		sanitizeLinks(child)
	}
}
