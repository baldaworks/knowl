package wiki

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl"
	"gopkg.in/yaml.v3"
)

const (
	rootDir      = "wiki/"
	indexPath    = "wiki/index.md"
	logPath      = "wiki/log.md"
	markdownExt  = ".md"
	relationWiki = "wiki"
)

// Frontmatter is the bounded YAML metadata recognized on ordinary pages.
type Frontmatter struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	Type       string   `yaml:"type"`
	SourceRefs []string `yaml:"source_refs"`
}

// ParseFrontmatter reads and trims the leading YAML frontmatter block.
func ParseFrontmatter(content string) (Frontmatter, error) {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return Frontmatter{}, fmt.Errorf("frontmatter opening delimiter is missing")
	}
	end := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			end = index
			break
		}
	}
	if end < 0 {
		return Frontmatter{}, fmt.Errorf("frontmatter closing delimiter is missing")
	}
	var metadata Frontmatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &metadata); err != nil {
		return Frontmatter{}, err
	}
	metadata.ID = strings.TrimSpace(metadata.ID)
	metadata.Title = strings.TrimSpace(metadata.Title)
	metadata.Type = strings.TrimSpace(metadata.Type)
	for index := range metadata.SourceRefs {
		metadata.SourceRefs[index] = strings.TrimSpace(metadata.SourceRefs[index])
	}
	return metadata, nil
}

// SourceRefs returns a deterministic, deduplicated source-ref list.
func SourceRefs(content string) []string {
	metadata, err := ParseFrontmatter(content)
	if err != nil {
		return nil
	}
	refs := append([]string(nil), metadata.SourceRefs...)
	sort.Strings(refs)
	return uniqueStrings(refs)
}

// MarkdownTargets extracts normalized wiki-link targets from Markdown.
func MarkdownTargets(content string) ([]string, bool) {
	targets := make([]string, 0)
	malformed := false
	for offset := 0; offset < len(content); {
		start := strings.Index(content[offset:], "[[")
		if start < 0 {
			break
		}
		start += offset + 2
		end := strings.Index(content[start:], "]]")
		if end < 0 {
			malformed = true
			break
		}
		target := strings.TrimSpace(content[start : start+end])
		if separator := strings.IndexAny(target, "|#"); separator >= 0 {
			target = target[:separator]
		}
		target = NormalizePageTarget(target)
		if target != "" {
			targets = append(targets, target)
		}
		offset = start + end + 2
	}
	return targets, malformed
}

// IndexTargets extracts both wiki links and bare list-item targets from index content.
func IndexTargets(content string) ([]string, bool) {
	targets, malformed := MarkdownTargets(content)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		if target := NormalizePageTarget(strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))); target != "" {
			targets = append(targets, target)
		}
	}
	return targets, malformed
}

// NormalizePageTarget converts a wiki reference into a canonical page ID.
func NormalizePageTarget(target string) string {
	target = strings.TrimSpace(strings.TrimPrefix(target, "wiki/"))
	target = strings.TrimSuffix(target, ".md")
	if target == "" || target == "." || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "../") || strings.Contains(target, "/../") {
		return ""
	}
	return target
}

// PageIDFromPath derives the canonical page ID from a wiki Markdown path.
func PageIDFromPath(path string) (knowl.PageID, bool) {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	if normalized == indexPath || normalized == logPath {
		return "", false
	}
	if !strings.HasPrefix(normalized, rootDir) || !strings.HasSuffix(normalized, markdownExt) {
		return "", false
	}
	pageID := strings.TrimSuffix(strings.TrimPrefix(normalized, rootDir), markdownExt)
	if NormalizePageTarget(pageID) != pageID {
		return "", false
	}
	return knowl.PageID(pageID), pageID != ""
}

// Links converts normalized page targets into unique wiki link references.
func Links(from knowl.PageID, content string) []knowl.LinkReference {
	targets, _ := MarkdownTargets(content)
	links := make([]knowl.LinkReference, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		key := target + "\x00" + relationWiki
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		links = append(links, knowl.LinkReference{From: from, To: knowl.PageID(target), Relation: relationWiki})
	}
	return links
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}
