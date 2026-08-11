package bootstrap

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	notesDir = "wiki/notes"
	pageType = "note"
)

type Flavor struct {
	Name               string
	Adapter            string
	RewriteObsidianRef bool
}

type Options struct {
	WorkspaceRoot string
	SourceRoot    string
	Scope         domain.ScopeRef
	Flavor        Flavor
}

type Summary struct {
	MarkdownFiles  int
	AuxiliaryFiles int
}

type document struct {
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

type asset struct {
	SourcePath     string
	SourceRelative string
	TargetPath     string
	Content        []byte
}

type catalog struct {
	NoteExact     map[string]string
	NoteBasename  map[string]string
	AssetExact    map[string]string
	AssetBasename map[string]string
}

func Import(ctx context.Context, options Options) (Summary, error) {
	if err := validateOptions(options); err != nil {
		return Summary{}, err
	}
	if err := ensurePathsAreSeparate(options.WorkspaceRoot, options.SourceRoot); err != nil {
		return Summary{}, err
	}
	if err := ensureTargetIsFresh(options.WorkspaceRoot); err != nil {
		return Summary{}, err
	}
	documents, assets, catalog, err := collectInputs(options.SourceRoot)
	if err != nil {
		return Summary{}, err
	}
	if len(documents) == 0 {
		return Summary{}, fmt.Errorf("bootstrap source path %q contains no Markdown files", options.SourceRoot)
	}
	if options.Flavor.RewriteObsidianRef {
		for index := range documents {
			documents[index].Body = rewriteObsidianReferences(documents[index], catalog, documents[index].Body)
		}
	}
	workspace, err := contentfs.New(options.WorkspaceRoot)
	if err != nil {
		return Summary{}, fmt.Errorf("open bootstrap workspace: %w", err)
	}
	scope := options.Scope
	if strings.TrimSpace(string(scope)) == "" {
		scope = domain.ScopeRef("local")
	}
	for index := range documents {
		if err := acceptSource(ctx, workspace, scope, options.Flavor.Adapter, &documents[index]); err != nil {
			return Summary{}, err
		}
		pageContent, renderErr := renderPage(documents[index])
		if renderErr != nil {
			return Summary{}, renderErr
		}
		if err := writeFile(filepath.Join(options.WorkspaceRoot, filepath.FromSlash(documents[index].TargetPath)), pageContent); err != nil {
			return Summary{}, err
		}
	}
	for _, asset := range assets {
		if err := writeFile(filepath.Join(options.WorkspaceRoot, filepath.FromSlash(asset.TargetPath)), asset.Content); err != nil {
			return Summary{}, err
		}
	}
	if err := writeFile(filepath.Join(options.WorkspaceRoot, filepath.FromSlash(indexFile)), renderIndex(documents, options.SourceRoot, options.Flavor.Name)); err != nil {
		return Summary{}, err
	}
	return Summary{
		MarkdownFiles:  len(documents),
		AuxiliaryFiles: len(assets),
	}, nil
}

func validateOptions(options Options) error {
	if strings.TrimSpace(options.WorkspaceRoot) == "" {
		return fmt.Errorf("bootstrap workspace root is required")
	}
	if strings.TrimSpace(options.SourceRoot) == "" {
		return fmt.Errorf("bootstrap source root is required")
	}
	if strings.TrimSpace(options.Flavor.Name) == "" {
		return fmt.Errorf("bootstrap flavor name is required")
	}
	if strings.TrimSpace(options.Flavor.Adapter) == "" {
		return fmt.Errorf("bootstrap flavor adapter is required")
	}
	return nil
}

func ensureTargetIsFresh(workspaceRoot string) error {
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

func collectInputs(sourceRoot string) ([]document, []asset, catalog, error) {
	documents := make([]document, 0)
	assets := make([]asset, 0)
	index := catalog{
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
			if shouldSkipDir(path, sourceRoot, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if shouldSkipFile(entry.Name()) {
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
			document, err := newDocument(path, relative, content)
			if err != nil {
				return err
			}
			documents = append(documents, document)
			recordUniqueTarget(index.NoteExact, referenceKey(relative), document.PageID)
			recordUniqueTarget(index.NoteBasename, strings.TrimSuffix(filepath.Base(relative), filepath.Ext(relative)), document.PageID)
			return nil
		}
		assetTarget := filepath.ToSlash(filepath.Join(notesDir, relative))
		assets = append(assets, asset{
			SourcePath:     path,
			SourceRelative: relative,
			TargetPath:     assetTarget,
			Content:        content,
		})
		recordUniqueTarget(index.AssetExact, referenceKey(relative), assetTarget)
		recordUniqueTarget(index.AssetBasename, filepath.Base(relative), assetTarget)
		return nil
	})
	if err != nil {
		return nil, nil, catalog{}, err
	}
	sort.Slice(documents, func(left, right int) bool { return documents[left].TargetPath < documents[right].TargetPath })
	sort.Slice(assets, func(left, right int) bool { return assets[left].TargetPath < assets[right].TargetPath })
	return documents, assets, index, nil
}

func shouldSkipDir(path, sourceRoot, name string) bool {
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

func shouldSkipFile(name string) bool {
	return strings.HasPrefix(name, ".")
}

func recordUniqueTarget(index map[string]string, key, value string) {
	if existing, ok := index[key]; ok && existing != value {
		index[key] = ""
		return
	}
	index[key] = value
}
