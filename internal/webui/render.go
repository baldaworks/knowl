package webui

import (
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
	markdownhtml "github.com/yuin/goldmark/v2/renderer/html"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	linkHrefAttribute       = "href"
	articleContainerElement = "div"
)

// RenderMarkdown is the sole trusted-HTML conversion. Raw HTML is disabled in
// goldmark; the independent allowlist excludes images and all active attributes.
// Relative links remain inert until resolved against canonical identities.
func RenderMarkdown(markdown string) (template.HTML, error) { return renderPageMarkdown(markdown, "") }
func renderPageMarkdown(markdown, pageID string) (template.HTML, error) {
	return renderTitledPageMarkdown(markdown, pageID, "")
}
func renderTitledPageMarkdown(markdown, pageID, displayedTitle string) (template.HTML, error) {
	title := ""
	if pageID != "" {
		relative := pageID + ".md"
		kind, err := okf.ClassifyPath(relative)
		if err != nil {
			return "", fmt.Errorf("read canonical body: %w", err)
		}
		limits := okf.DefaultLimits()
		switch kind {
		case okf.DocumentIndex:
			index, err := okf.ParseIndex(relative, []byte(markdown), limits)
			if err != nil {
				return "", fmt.Errorf("read canonical body: %w", err)
			}
			markdown = index.Body
		case okf.DocumentConcept:
			if strings.HasPrefix(markdown, "---\n") || strings.HasPrefix(markdown, "---\r\n") {
				limits.MaxBytes = max(limits.MaxBytes, len(markdown))
				parsed, err := okf.ParseConcept(relative, []byte(markdown), limits)
				if err != nil {
					return "", fmt.Errorf("read canonical body: %w", err)
				}
				markdown = parsed.Body
				title = parsed.Metadata.Title
			}
		default:
			return "", fmt.Errorf("read canonical body: %w", &okf.Violation{Path: relative, Rule: okf.RulePathInvalid})
		}
	}
	if displayedTitle != "" {
		title = displayedTitle
	}
	source := []byte(markdown)
	document := parser.New(parser.WithExtensions(extension.TableParser)).Parse(source)
	var rendered bytes.Buffer
	if err := markdownhtml.New(markdownhtml.WithExtensions(extension.TableHTMLRenderer)).Render(&rendered, source, document); err != nil {
		return "", fmt.Errorf("render Markdown: %w", err)
	}
	policy := bluemonday.NewPolicy()
	policy.AllowElements("p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6", "strong", "em", "s", "blockquote", "ul", "ol", "li", "pre", "code", "a", "table", "thead", "tbody", "tr", "th", "td")
	policy.AllowAttrs(linkHrefAttribute).OnElements("a")
	policy.AllowURLSchemes("https", "http")
	policy.AllowRelativeURLs(true)
	policy.RequireNoReferrerOnLinks(true)
	nodes, err := html.ParseFragment(strings.NewReader(policy.Sanitize(rendered.String())), &html.Node{Type: html.ElementNode, Data: articleContainerElement, DataAtom: atom.Div})
	if err != nil {
		return "", fmt.Errorf("parse sanitized Markdown: %w", err)
	}
	rendered.Reset()
	heading := 0
	for i, node := range nodes {
		if i == 0 && title != "" && node.Data == "h1" && renderedText(node) == title {
			continue
		}
		sanitizeLinks(node, pageID)
		decorateArticle(node, displayedTitle != "", &heading)
		if node.DataAtom == atom.Table {
			wrapper := tableWrapper()
			wrapper.AppendChild(node)
			node = wrapper
		}
		if err = html.Render(&rendered, node); err != nil {
			return "", fmt.Errorf("serialize sanitized Markdown: %w", err)
		}
	}
	return template.HTML(rendered.String()), nil //nolint:gosec // Only allowlisted Markdown and parsed, redacted links reach this conversion.
}

// Decoration runs only on sanitized nodes. Author attributes never supply IDs
// or active behavior; numbering reflects the headings actually displayed.
func decorateArticle(node *html.Node, hasTitle bool, heading *int) {
	if hasTitle && node.DataAtom == atom.H1 {
		node.Data, node.DataAtom = "h2", atom.H2
	}
	switch node.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		*heading++
		node.Attr = append(node.Attr, html.Attribute{Key: "id", Val: "knowl-heading-" + strconv.Itoa(*heading)}, html.Attribute{Key: "tabindex", Val: "-1"})
	}
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		decorateArticle(child, hasTitle, heading)
		child = next
	}
	if node.DataAtom == atom.Table && node.Parent != nil {
		wrapper := tableWrapper()
		parent := node.Parent
		parent.InsertBefore(wrapper, node)
		parent.RemoveChild(node)
		wrapper.AppendChild(node)
	}
}

func tableWrapper() *html.Node {
	return &html.Node{Type: html.ElementNode, Data: articleContainerElement, DataAtom: atom.Div, Attr: []html.Attribute{{Key: "class", Val: "table-responsive"}}}
}

type articleHeading struct{ ID, Title string }

func articleOutline(markdown template.HTML) ([]articleHeading, error) {
	nodes, err := html.ParseFragment(strings.NewReader(string(markdown)), &html.Node{Type: html.ElementNode, Data: articleContainerElement, DataAtom: atom.Div})
	if err != nil {
		return nil, fmt.Errorf("parse article outline: %w", err)
	}
	headings := []articleHeading{}
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.DataAtom == atom.H2 || node.DataAtom == atom.H3 {
			for _, attr := range node.Attr {
				if attr.Key == "id" {
					headings = append(headings, articleHeading{ID: attr.Val, Title: renderedText(node)})
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	for _, node := range nodes {
		visit(node)
	}
	return headings, nil
}
func sanitizeLinks(node *html.Node, pageID string) {
	if node.Type == html.ElementNode && node.Data == "a" {
		attrs := make([]html.Attribute, 0, 2)
		for _, attr := range node.Attr {
			if attr.Key == linkHrefAttribute {
				if safe := documentLink(attr.Val, pageID); safe != "" {
					attrs = append(attrs, html.Attribute{Key: linkHrefAttribute, Val: safe}, html.Attribute{Key: "rel", Val: "noreferrer noopener"})
				}
			}
		}
		node.Attr = attrs
	}
	if node.Type == html.ElementNode && (node.Data == "code" || node.Data == "pre" || node.Data == "a") {
		return
	}
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		if child.Type == html.TextNode && pageID != "" {
			linkWikiText(node, child, pageID)
		} else {
			sanitizeLinks(child, pageID)
		}
		child = next
	}
}

// Canonical identities are application names, never filesystem paths or fetch URLs.
func validPageIdentity(id string) bool {
	if id == "" || len(id) > 2048 || !utf8.ValidString(id) || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\\?#:|") || path.IsAbs(id) || path.Clean(id) != id || id == "." || id == ".." || strings.HasPrefix(id, "../") {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func documentLink(raw, pageID string) string {
	if safe := SafeURI(raw); safe != "" {
		return safe
	}
	if pageID == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.RawQuery != "" || strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "\\") || u.Path == "" {
		return ""
	}
	if strings.HasSuffix(u.Path, "/") || u.Path == "." || u.Path == ".." {
		return ""
	}
	if ext := path.Ext(u.Path); ext != "" && ext != ".md" {
		return ""
	}
	id := strings.TrimSuffix(path.Join(path.Dir(pageID), u.Path), ".md")
	if !validPageIdentity(id) {
		return ""
	}
	return contextualDocumentURL(id, pageID)
}
func contextualDocumentURL(id, pageID string) string {
	currentKind, _ := okf.ClassifyPath(pageID + ".md")
	targetKind, _ := okf.ClassifyPath(id + ".md")
	if currentKind == okf.DocumentIndex && targetKind == okf.DocumentConcept && canonicalURL(id) != "" {
		return contextualPageURL(domain.PageID(id), domain.PageID(pageID))
	}
	return canonicalURL(id)
}
func linkWikiText(parent, node *html.Node, pageID string) {
	remaining := node.Data
	for {
		start := strings.Index(remaining, "[[")
		if start < 0 {
			break
		}
		end := strings.Index(remaining[start+2:], "]]")
		if end < 0 {
			break
		}
		end += start + 2
		target := remaining[start+2 : end]
		id, label, hasLabel := strings.Cut(target, "|")
		id, _, _ = strings.Cut(id, "#")
		id = wiki.NormalizePageTarget(id)
		if !hasLabel {
			label = id
		}
		if canonicalURL(id) == "" {
			node.Data = remaining
			return
		}
		parent.InsertBefore(&html.Node{Type: html.TextNode, Data: remaining[:start]}, node)
		link := &html.Node{Type: html.ElementNode, Data: "a", DataAtom: atom.A, Attr: []html.Attribute{{Key: linkHrefAttribute, Val: contextualDocumentURL(id, pageID)}}}
		link.AppendChild(&html.Node{Type: html.TextNode, Data: label})
		parent.InsertBefore(link, node)
		remaining = remaining[end+2:]
	}
	node.Data = remaining
}

func renderedText(node *html.Node) string {
	if node.Type == html.TextNode {
		return node.Data
	}
	var b strings.Builder
	for c := node.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(renderedText(c))
	}
	return b.String()
}
func canonicalURL(id string) string {
	if !validPageIdentity(id) {
		return ""
	}
	kind, err := okf.ClassifyPath(id + ".md")
	if err != nil {
		return ""
	}
	switch kind {
	case okf.DocumentConcept:
		return pageURL(id)
	case okf.DocumentIndex:
		return "/ui/knowledge?" + url.Values{parentIDParameter: {id}}.Encode()
	default:
		return ""
	}
}
