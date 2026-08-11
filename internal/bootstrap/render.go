package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

type frontmatter struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	Type       string   `yaml:"type"`
	SourceRefs []string `yaml:"source_refs"`
}

func newDocument(sourcePath, relative string, content []byte) (document, error) {
	body, metadata := splitFrontmatter(string(content))
	title := resolveTitle(metadata, body, relative)
	normalizedRelative := trimMarkdownExtension(relative) + ".md"
	pageID := filepath.ToSlash(filepath.Join("notes", trimMarkdownExtension(relative)))
	targetPath := filepath.ToSlash(filepath.Join(notesDir, normalizedRelative))
	return document{
		SourcePath:     sourcePath,
		SourceRelative: relative,
		TargetPath:     targetPath,
		PageID:         pageID,
		Title:          title,
		RawContent:     append([]byte(nil), content...),
		Body:           body,
		Extras:         frontmatterExtras(metadata),
	}, nil
}

func splitFrontmatter(content string) (string, map[string]any) {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return content, nil
	}
	end := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			end = index
			break
		}
	}
	if end < 0 {
		return content, nil
	}
	var metadata map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &metadata); err != nil {
		return content, nil
	}
	return strings.Join(lines[end+1:], "\n"), metadata
}

func frontmatterExtras(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	extras := make(map[string]any, len(metadata))
	for key, value := range metadata {
		normalized := strings.TrimSpace(key)
		switch normalized {
		case "id", "title", "type", "source_refs":
			continue
		default:
			extras[normalized] = value
		}
	}
	if len(extras) == 0 {
		return nil
	}
	return extras
}

func resolveTitle(metadata map[string]any, body, relative string) string {
	if title, ok := metadata["title"].(string); ok && strings.TrimSpace(title) != "" {
		return strings.TrimSpace(title)
	}
	if title := markdownTitle(body); strings.TrimSpace(title) != "" {
		return title
	}
	base := strings.TrimSuffix(filepath.Base(relative), filepath.Ext(relative))
	if strings.TrimSpace(base) == "" {
		return "Imported note"
	}
	return base
}

func markdownTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func acceptSource(ctx context.Context, workspace *contentfs.Workspace, scope domain.ScopeRef, adapter string, document *document) error {
	digestBytes := sha256.Sum256(document.RawContent)
	digest := hex.EncodeToString(digestBytes[:])
	source := domain.SourceEnvelope{
		Scope:     scope,
		Source:    domain.SourceRef{Adapter: adapter, ID: document.SourceRelative},
		Version:   domain.SourceVersion{Version: digest, Digest: digest},
		MediaType: "text/markdown",
		Content:   append([]byte(nil), document.RawContent...),
		Provenance: map[string]any{
			"bootstrap_source_path": document.SourcePath,
			"bootstrap_relative":    document.SourceRelative,
		},
	}
	accepted, err := workspace.AcceptSource(ctx, source)
	if err != nil {
		return fmt.Errorf("accept bootstrap source %q: %w", document.SourceRelative, err)
	}
	document.SourceRef = accepted.Source.Adapter + ":" + accepted.Source.ID + "@" + accepted.Version.Version
	return nil
}

func renderPage(document document) ([]byte, error) {
	content := strings.TrimLeft(document.Body, "\n")
	base, err := yaml.Marshal(frontmatter{
		ID:         document.PageID,
		Title:      document.Title,
		Type:       pageType,
		SourceRefs: []string{document.SourceRef},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal bootstrap frontmatter for %q: %w", document.SourceRelative, err)
	}
	combined := make([]byte, 0, len(base)+len(content)+32)
	combined = append(combined, []byte("---\n")...)
	combined = append(combined, base...)
	if len(document.Extras) != 0 {
		extras, extrasErr := yaml.Marshal(document.Extras)
		if extrasErr != nil {
			return nil, fmt.Errorf("marshal bootstrap frontmatter extras for %q: %w", document.SourceRelative, extrasErr)
		}
		combined = append(combined, extras...)
	}
	combined = append(combined, []byte("---\n")...)
	combined = append(combined, []byte(content)...)
	if len(combined) == 0 || combined[len(combined)-1] != '\n' {
		combined = append(combined, '\n')
	}
	return combined, nil
}

func renderIndex(documents []document, sourceRoot, flavor string) []byte {
	var builder strings.Builder
	builder.WriteString("# Knowl index\n\n")
	builder.WriteString("Bootstrapped from ")
	builder.WriteString(flavor)
	builder.WriteString(" source tree `")
	builder.WriteString(sourceRoot)
	builder.WriteString("`.\n\n")
	for _, document := range documents {
		builder.WriteString("- [[")
		builder.WriteString(document.PageID)
		builder.WriteString("|")
		builder.WriteString(document.Title)
		builder.WriteString("]]\n")
	}
	return []byte(builder.String())
}

func writeFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create bootstrap parent directory for %q: %w", path, err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("write bootstrap file %q: %w", path, err)
	}
	return nil
}
