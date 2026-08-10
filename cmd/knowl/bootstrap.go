package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	bootstrapNotesDir        = "wiki/notes"
	bootstrapPageType        = "note"
	bootstrapWikiAdapter     = "bootstrap_wiki"
	bootstrapObsidianAdapter = "bootstrap_obsidian"
)

type bootstrapFlavor struct {
	Name               string
	Adapter            string
	RewriteObsidianRef bool
}

type bootstrapDocument struct {
	SourcePath     string
	SourceRelative string
	TargetPath     string
	PageID         string
	Title          string
	SourceRef      string
	RawContent     []byte
	Body           string
	Extras         map[string]any
}

type bootstrapAsset struct {
	SourcePath     string
	SourceRelative string
	TargetPath     string
	Content        []byte
}

type bootstrapCatalog struct {
	NoteExact     map[string]string
	NoteBasename  map[string]string
	AssetExact    map[string]string
	AssetBasename map[string]string
}

type bootstrapFrontmatter struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	Type       string   `yaml:"type"`
	SourceRefs []string `yaml:"source_refs"`
}

func newBootstrapCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   bootstrapCommandName,
		Short: "Bootstrap a Knowl workspace from an existing Markdown wiki or Obsidian vault",
		Long: `Bootstrap reads an existing Markdown tree or Obsidian vault and creates a
normalized Knowl-owned workspace in the configured workspace path.

Bootstrap is a one-time adoption flow, not ongoing sync. It initializes the
workspace and local config if needed, then imports Markdown notes into
wiki/notes/**, stores immutable raw source copies under raw/**, and preserves
local assets alongside the imported notes when possible.`,
	}
	command.AddCommand(
		newBootstrapSourceCommand(bootstrapFlavor{
			Name:    bootstrapWikiName,
			Adapter: bootstrapWikiAdapter,
		}),
		newBootstrapSourceCommand(bootstrapFlavor{
			Name:               bootstrapObsidianName,
			Adapter:            bootstrapObsidianAdapter,
			RewriteObsidianRef: true,
		}),
	)
	return command
}

func newBootstrapSourceCommand(flavor bootstrapFlavor) *cobra.Command {
	return &cobra.Command{
		Use:   flavor.Name + " <path>",
		Short: "Bootstrap Knowl from an existing " + flavor.Name + " source tree",
		Long: "Read one existing " + flavor.Name + " source tree from PATH and create a fresh Knowl workspace in the configured workspace directory.\n\n" +
			"The configured workspace must be fresh and separate from the source path. Markdown files become canonical Knowl pages under wiki/notes/**, and immutable raw source copies are stored under raw/**.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrap(cmd, flavor, args[0])
		},
	}
}

func runBootstrap(cmd *cobra.Command, flavor bootstrapFlavor, sourceArg string) error {
	config, err := configFromContext(cmd.Context())
	if err != nil {
		return err
	}
	workspaceRoot, err := workspacePath(cmd.Context())
	if err != nil {
		return err
	}
	sourceRoot, err := filepath.Abs(strings.TrimSpace(sourceArg))
	if err != nil {
		return fmt.Errorf("resolve bootstrap source path: %w", err)
	}
	sourceRoot = filepath.Clean(sourceRoot)
	info, err := os.Stat(sourceRoot)
	if err != nil {
		return fmt.Errorf("stat bootstrap source path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("bootstrap source path %q is not a directory", sourceRoot)
	}
	if err := ensureBootstrapPathsAreSeparate(workspaceRoot, sourceRoot); err != nil {
		return err
	}
	if err := initWorkspace(workspaceRoot); err != nil {
		return err
	}
	configPath := configOutputPath(config)
	if err := writeConfig(configPath, workspaceRoot); err != nil {
		return err
	}
	if err := ensureBootstrapTargetIsFresh(workspaceRoot); err != nil {
		return err
	}

	documents, assets, catalog, err := collectBootstrapInputs(sourceRoot, flavor)
	if err != nil {
		return err
	}
	if len(documents) == 0 {
		return fmt.Errorf("bootstrap source path %q contains no Markdown files", sourceRoot)
	}
	if flavor.RewriteObsidianRef {
		for index := range documents {
			documents[index].Body = rewriteObsidianReferences(documents[index], catalog, documents[index].Body)
		}
	}

	workspace, err := contentfs.New(workspaceRoot)
	if err != nil {
		return fmt.Errorf("open bootstrap workspace: %w", err)
	}
	scope := config.Document.Knowl.Scope
	if strings.TrimSpace(string(scope)) == "" {
		scope = domain.ScopeRef("local")
	}
	for index := range documents {
		if err := acceptBootstrapSource(cmd.Context(), workspace, scope, flavor.Adapter, &documents[index]); err != nil {
			return err
		}
		pageContent, renderErr := renderBootstrapPage(documents[index])
		if renderErr != nil {
			return renderErr
		}
		if err := writeBootstrapFile(filepath.Join(workspaceRoot, filepath.FromSlash(documents[index].TargetPath)), pageContent); err != nil {
			return err
		}
	}
	for _, asset := range assets {
		if err := writeBootstrapFile(filepath.Join(workspaceRoot, filepath.FromSlash(asset.TargetPath)), asset.Content); err != nil {
			return err
		}
	}
	if err := writeBootstrapFile(filepath.Join(workspaceRoot, filepath.FromSlash(indexFile)), renderBootstrapIndex(documents, sourceRoot, flavor.Name)); err != nil {
		return err
	}
	if err := validateWorkspace(workspaceRoot); err != nil {
		return err
	}
	commandLogger(cmd).Info().
		Str("flavor", flavor.Name).
		Str("source", sourceRoot).
		Str("workspace", workspaceRoot).
		Str("config_path", configPath).
		Int("markdown_files", len(documents)).
		Int("auxiliary_files", len(assets)).
		Msg("bootstrapped Knowl workspace")
	return nil
}

func ensureBootstrapPathsAreSeparate(workspaceRoot, sourceRoot string) error {
	overlap, err := pathsOverlap(workspaceRoot, sourceRoot)
	if err != nil {
		return err
	}
	if overlap {
		return fmt.Errorf("bootstrap source path %q must be separate from workspace %q", sourceRoot, workspaceRoot)
	}
	return nil
}

func ensureBootstrapTargetIsFresh(workspaceRoot string) error {
	checkWiki := func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(workspaceRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == indexFile || relative == logFile {
			return nil
		}
		return fmt.Errorf("workspace %q is not fresh: unexpected canonical file %q is present", workspaceRoot, relative)
	}
	if err := filepath.WalkDir(filepath.Join(workspaceRoot, workspaceWikiDir), checkWiki); err != nil {
		return err
	}
	checkEmpty := func(root, label string) error {
		return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == root || entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(workspaceRoot, path)
			if err != nil {
				return err
			}
			return fmt.Errorf("workspace %q is not fresh: unexpected %s file %q is present", workspaceRoot, label, filepath.ToSlash(relative))
		})
	}
	if err := checkEmpty(filepath.Join(workspaceRoot, "raw"), "raw"); err != nil {
		return err
	}
	if err := checkEmpty(filepath.Join(workspaceRoot, ".knowl"), "state"); err != nil {
		return err
	}
	return nil
}

func collectBootstrapInputs(sourceRoot string, flavor bootstrapFlavor) ([]bootstrapDocument, []bootstrapAsset, bootstrapCatalog, error) {
	documents := make([]bootstrapDocument, 0)
	assets := make([]bootstrapAsset, 0)
	catalog := bootstrapCatalog{
		NoteExact:     make(map[string]string),
		NoteBasename:  make(map[string]string),
		AssetExact:    make(map[string]string),
		AssetBasename: make(map[string]string),
	}
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("bootstrap source contains unsupported symlink %q", path)
		}
		if entry.IsDir() {
			if shouldSkipBootstrapDir(path, sourceRoot, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if shouldSkipBootstrapFile(entry.Name()) {
			return nil
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(filepath.Clean(relative))
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read bootstrap source file %q: %w", relative, err)
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			document, err := newBootstrapDocument(path, relative, content)
			if err != nil {
				return err
			}
			documents = append(documents, document)
			recordUniqueBootstrapTarget(catalog.NoteExact, bootstrapReferenceKey(relative), document.PageID)
			recordUniqueBootstrapTarget(catalog.NoteBasename, strings.TrimSuffix(filepath.Base(relative), filepath.Ext(relative)), document.PageID)
			return nil
		}
		assetTarget := filepath.ToSlash(filepath.Join(bootstrapNotesDir, relative))
		assets = append(assets, bootstrapAsset{
			SourcePath:     path,
			SourceRelative: relative,
			TargetPath:     assetTarget,
			Content:        content,
		})
		recordUniqueBootstrapTarget(catalog.AssetExact, bootstrapReferenceKey(relative), assetTarget)
		recordUniqueBootstrapTarget(catalog.AssetBasename, filepath.Base(relative), assetTarget)
		return nil
	})
	if err != nil {
		return nil, nil, bootstrapCatalog{}, err
	}
	sort.Slice(documents, func(left, right int) bool { return documents[left].TargetPath < documents[right].TargetPath })
	sort.Slice(assets, func(left, right int) bool { return assets[left].TargetPath < assets[right].TargetPath })
	_ = flavor
	return documents, assets, catalog, nil
}

func newBootstrapDocument(sourcePath, relative string, content []byte) (bootstrapDocument, error) {
	body, metadata := splitBootstrapFrontmatter(string(content))
	title := bootstrapTitle(metadata, body, relative)
	normalizedRelative := trimMarkdownExtension(relative) + ".md"
	pageID := filepath.ToSlash(filepath.Join("notes", trimMarkdownExtension(relative)))
	targetPath := filepath.ToSlash(filepath.Join(bootstrapNotesDir, normalizedRelative))
	return bootstrapDocument{
		SourcePath:     sourcePath,
		SourceRelative: relative,
		TargetPath:     targetPath,
		PageID:         pageID,
		Title:          title,
		RawContent:     append([]byte(nil), content...),
		Body:           body,
		Extras:         bootstrapFrontmatterExtras(metadata),
	}, nil
}

func splitBootstrapFrontmatter(content string) (string, map[string]any) {
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

func bootstrapFrontmatterExtras(metadata map[string]any) map[string]any {
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

func bootstrapTitle(metadata map[string]any, body, relative string) string {
	if title, ok := metadata["title"].(string); ok && strings.TrimSpace(title) != "" {
		return strings.TrimSpace(title)
	}
	if title := bootstrapMarkdownTitle(body); strings.TrimSpace(title) != "" {
		return title
	}
	base := strings.TrimSuffix(filepath.Base(relative), filepath.Ext(relative))
	if strings.TrimSpace(base) == "" {
		return "Imported note"
	}
	return base
}

func bootstrapMarkdownTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func acceptBootstrapSource(ctx context.Context, workspace *contentfs.Workspace, scope domain.ScopeRef, adapter string, document *bootstrapDocument) error {
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

func renderBootstrapPage(document bootstrapDocument) ([]byte, error) {
	content := strings.TrimLeft(document.Body, "\n")
	base, err := yaml.Marshal(bootstrapFrontmatter{
		ID:         document.PageID,
		Title:      document.Title,
		Type:       bootstrapPageType,
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

func renderBootstrapIndex(documents []bootstrapDocument, sourceRoot, flavor string) []byte {
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

func writeBootstrapFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create bootstrap parent directory for %q: %w", path, err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("write bootstrap file %q: %w", path, err)
	}
	return nil
}

func shouldSkipBootstrapDir(path, sourceRoot, name string) bool {
	if path == sourceRoot {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case ".git", ".obsidian", ".knowl", ".config":
		return true
	default:
		return false
	}
}

func shouldSkipBootstrapFile(name string) bool {
	return strings.HasPrefix(name, ".")
}

func recordUniqueBootstrapTarget(index map[string]string, key, value string) {
	if existing, ok := index[key]; ok && existing != value {
		index[key] = ""
		return
	}
	index[key] = value
}

func bootstrapReferenceKey(path string) string {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	normalized = strings.TrimPrefix(normalized, "./")
	normalized = trimMarkdownExtension(normalized)
	return normalized
}

func trimMarkdownExtension(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".md") {
		return path[:len(path)-len(filepath.Ext(path))]
	}
	return path
}

func rewriteObsidianReferences(document bootstrapDocument, catalog bootstrapCatalog, body string) string {
	var builder strings.Builder
	for offset := 0; offset < len(body); {
		start := strings.Index(body[offset:], "[[")
		if start < 0 {
			builder.WriteString(body[offset:])
			break
		}
		start += offset
		embed := start > 0 && body[start-1] == '!'
		prefixEnd := start
		if embed {
			prefixEnd--
		}
		builder.WriteString(body[offset:prefixEnd])
		end := strings.Index(body[start+2:], "]]")
		if end < 0 {
			builder.WriteString(body[start:])
			break
		}
		raw := body[start+2 : start+2+end]
		builder.WriteString(rewriteObsidianReference(document.SourceRelative, catalog, raw, embed))
		offset = start + 2 + end + 2
	}
	return builder.String()
}

func rewriteObsidianReference(currentRelative string, catalog bootstrapCatalog, raw string, embed bool) string {
	targetPart := raw
	alias := ""
	if before, after, ok := strings.Cut(raw, "|"); ok {
		targetPart = before
		alias = after
	}
	target := targetPart
	anchor := ""
	if before, after, ok := strings.Cut(targetPart, "#"); ok {
		target = before
		anchor = after
	}
	target = strings.TrimSpace(target)
	if pageID, ok := resolveBootstrapPageTarget(catalog, target); ok {
		var builder strings.Builder
		if embed {
			builder.WriteByte('!')
		}
		builder.WriteString("[[")
		builder.WriteString(pageID)
		if strings.TrimSpace(anchor) != "" {
			builder.WriteString("#")
			builder.WriteString(strings.TrimSpace(anchor))
		}
		if strings.TrimSpace(alias) != "" {
			builder.WriteString("|")
			builder.WriteString(strings.TrimSpace(alias))
		}
		builder.WriteString("]]")
		return builder.String()
	}
	if assetPath, ok := resolveBootstrapAssetTarget(catalog, target); ok {
		relativeAssetPath := bootstrapRelativeAssetPath(currentRelative, assetPath)
		label := strings.TrimSpace(alias)
		if label == "" {
			label = filepath.Base(assetPath)
		}
		if embed {
			return "![](" + relativeAssetPath + ")"
		}
		return "[" + label + "](" + relativeAssetPath + ")"
	}
	if embed {
		return "![unsupported obsidian embed](" + strings.TrimSpace(raw) + ")"
	}
	return "[[" + raw + "]]"
}

func resolveBootstrapPageTarget(catalog bootstrapCatalog, raw string) (string, bool) {
	key := bootstrapReferenceKey(strings.TrimSpace(raw))
	if key == "" {
		return "", false
	}
	if value, ok := catalog.NoteExact[key]; ok && value != "" {
		return value, true
	}
	base := strings.TrimSuffix(filepath.Base(key), filepath.Ext(key))
	if value, ok := catalog.NoteBasename[base]; ok && value != "" {
		return value, true
	}
	return "", false
}

func resolveBootstrapAssetTarget(catalog bootstrapCatalog, raw string) (string, bool) {
	key := filepath.ToSlash(strings.TrimSpace(raw))
	key = strings.TrimPrefix(key, "./")
	if key == "" {
		return "", false
	}
	if value, ok := catalog.AssetExact[key]; ok && value != "" {
		return value, true
	}
	base := filepath.Base(key)
	if value, ok := catalog.AssetBasename[base]; ok && value != "" {
		return value, true
	}
	return "", false
}

func bootstrapRelativeAssetPath(currentRelative, assetTargetPath string) string {
	currentDir := filepath.Dir(filepath.ToSlash(filepath.Join("notes", currentRelative)))
	assetPath := strings.TrimPrefix(filepath.ToSlash(assetTargetPath), "wiki/")
	relative, err := filepath.Rel(filepath.FromSlash(currentDir), filepath.FromSlash(assetPath))
	if err != nil {
		return filepath.ToSlash(assetPath)
	}
	return filepath.ToSlash(relative)
}

func pathsOverlap(left, right string) (bool, error) {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right {
		return true, nil
	}
	isWithin := func(root, child string) (bool, error) {
		relative, err := filepath.Rel(root, child)
		if err != nil {
			return false, err
		}
		return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
	}
	leftContainsRight, err := isWithin(left, right)
	if err != nil {
		return false, err
	}
	rightContainsLeft, err := isWithin(right, left)
	if err != nil {
		return false, err
	}
	return leftContainsRight || rightContainsLeft, nil
}
