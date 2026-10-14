package wiki

import (
	"net/url"
	"path"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
)

// Stable generated-navigation rules, independent of OKF structural violations.
const (
	RuleLinkLimit       = "link.limit"
	RuleLinkNonportable = "link.nonportable"
	RuleLinkUnsafe      = "link.unsafe"
)

// Navigation is bounded generated-page navigation, separate from OKF syntax
// validity. Targets are bundle-relative concept IDs; existence is a caller concern.
type Navigation struct {
	Targets []string
	Rule    string
}

// InspectNavigation checks the Markdown body of a generated concept. Literal
// code and link destinations are never inspected as prose. It does not read or
// fetch destinations. A nonempty Rule rejects generated navigation, not OKF.
func InspectNavigation(relative, body string, limit int) Navigation {
	result := Navigation{Targets: make([]string, 0)}
	if limit <= 0 {
		result.Rule = RuleLinkLimit
		return result
	}
	source := []byte(body)
	document := parser.New().Parse(source)
	count := 0
	var prose strings.Builder
	candidate := func() bool {
		count++
		if count > limit {
			result.Rule = RuleLinkLimit
			return false
		}
		return true
	}
	flush := func() {
		value := prose.String()
		prose.Reset()
		if result.Rule != "" {
			return
		}
		for offset := 0; offset < len(value); {
			i := strings.Index(value[offset:], "[[")
			if i < 0 {
				return
			}
			i += offset
			escaped := 0
			for j := i - 1; j >= 0 && value[j] == '\\'; j-- {
				escaped++
			}
			offset = i + 2
			if escaped%2 != 0 {
				continue
			}
			if !candidate() {
				return
			}
			result.Rule = RuleLinkNonportable
			return
		}
	}
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if result.Rule != "" {
			return ast.WalkStop, nil
		}
		if _, block := node.(ast.BlockNode); block {
			flush()
		}
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.Link:
			flush()
			if !candidate() {
				return ast.WalkStop, nil
			}
			raw := n.Destination.Value(source)
			target, rule := generatedDestination(relative, raw)
			if rule != "" {
				result.Rule = rule
				return ast.WalkStop, nil
			}
			if target != "" {
				result.Targets = append(result.Targets, target)
			}
			return ast.WalkSkipChildren, nil
		case *ast.CodeSpan, *ast.CodeBlock, *ast.Image, *ast.AutoLink, *ast.RawHTML, *ast.HTMLBlock:
			flush()
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			prose.Write(n.Value.Bytes(source))
			if n.SoftLineBreak() || n.HardLineBreak() {
				prose.WriteByte('\n')
			}
		}
		return ast.WalkContinue, nil
	})
	flush()
	return result
}

func generatedDestination(relative, raw string) (string, string) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", RuleLinkUnsafe
	}
	if parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(raw, "//") {
		return "", ""
	}
	decoded := parsed.Path // url.Parse has already URL-decoded exactly once.
	if decoded == "" {
		return "", ""
	}
	if strings.ContainsAny(decoded, "\\\x00\r\n") {
		return "", RuleLinkUnsafe
	}
	if strings.HasPrefix(decoded, "/") {
		return "", RuleLinkNonportable
	}
	target := path.Clean(path.Join(path.Dir(relative), decoded))
	if target == ".." || strings.HasPrefix(target, "../") || path.IsAbs(target) {
		return "", RuleLinkUnsafe
	}
	extension := path.Ext(decoded)
	if extension != "" && extension != ".md" {
		return "", ""
	}
	if extension != ".md" {
		return "", RuleLinkNonportable
	}
	kind, err := okf.ClassifyPath(target)
	if err != nil {
		return "", RuleLinkUnsafe
	}
	if kind != okf.DocumentConcept {
		return "", ""
	}
	return strings.TrimSuffix(target, ".md"), ""
}

// conceptDestinations uses the same Markdown syntax as the browser so literal
// code, images and unused reference definitions cannot fabricate graph edges.
func conceptDestinations(body string, limit int) []string {
	source := []byte(body)
	document := parser.New().Parse(source)
	destinations := make([]string, 0)
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if _, image := node.(*ast.Image); image {
			return ast.WalkSkipChildren, nil
		}
		if link, ok := node.(*ast.Link); ok {
			if len(destinations) >= limit {
				return ast.WalkStop, nil
			}
			destinations = append(destinations, link.Destination.Value(source))
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return destinations
}

// legacyNavigationProse preserves legacy reader syntax while excluding literal
// code and image/link destinations from the graph just as the browser does.
func legacyNavigationProse(body string) string {
	source := []byte(body)
	document := parser.New().Parse(source)
	var prose strings.Builder
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if _, block := node.(ast.BlockNode); block {
			prose.WriteByte('\n')
		}
		switch n := node.(type) {
		case *ast.CodeSpan, *ast.CodeBlock, *ast.Image, *ast.Link, *ast.AutoLink, *ast.RawHTML, *ast.HTMLBlock:
			prose.WriteByte('\n')
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			prose.Write(n.Value.Bytes(source))
			if n.SoftLineBreak() || n.HardLineBreak() {
				prose.WriteByte('\n')
			}
		}
		return ast.WalkContinue, nil
	})
	return prose.String()
}
