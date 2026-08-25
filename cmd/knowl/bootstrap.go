package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	sourcefilesystem "github.com/baldaworks/knowl/internal/source/filesystem"
	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	bootstrapWikiSourceID     = domain.SourceID("bootstrap-wiki")
	bootstrapObsidianSourceID = domain.SourceID("bootstrap-obsidian")
)

var errBootstrapNoMarkdown = errors.New("bootstrap source contains no Markdown files")

type bootstrapFlavor struct {
	Name     string
	SourceID domain.SourceID
	Flavor   string
}

func newBootstrapCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   bootstrapCommandName,
		Short: "Bootstrap a Knowl workspace from an existing Markdown wiki or Obsidian vault",
		Long: `Bootstrap reads an existing Markdown tree or Obsidian vault and creates a
normalized Knowl-owned workspace in the configured workspace path.

The bootstrap command is a one-time adoption flow. It initializes the workspace
and local config if needed, then performs the first synchronization through the
production source engine. Mirrored content is source-owned below wiki/sources/**,
immutable revisions are retained below raw/**, and later source commands use the
same engine for ongoing sync.`,
	}
	command.AddCommand(
		newBootstrapSourceCommand(bootstrapFlavor{
			Name:     bootstrapWikiName,
			SourceID: bootstrapWikiSourceID,
			Flavor:   domain.SourceFlavorMarkdown,
		}),
		newBootstrapSourceCommand(bootstrapFlavor{
			Name:     bootstrapObsidianName,
			SourceID: bootstrapObsidianSourceID,
			Flavor:   domain.SourceFlavorObsidian,
		}),
	)
	return command
}

func newBootstrapSourceCommand(flavor bootstrapFlavor) *cobra.Command {
	return &cobra.Command{
		Use:   flavor.Name + " <path>",
		Short: "Bootstrap Knowl from an existing " + flavor.Name + " source tree",
		Long: "Read one existing " + flavor.Name + " source tree from PATH and create a fresh Knowl workspace in the configured workspace directory.\n\n" +
			"The configured workspace must be fresh and separate from the source path. Content is synchronized into wiki/sources/" + string(flavor.SourceID) + "/** through the same source engine used for ongoing synchronization.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrap(cmd, flavor, args[0])
		},
	}
}

func runBootstrap(cmd *cobra.Command, flavor bootstrapFlavor, sourceArg string) (returnErr error) {
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
	if err := ensureBootstrapTargetIsFresh(workspaceRoot); err != nil {
		return err
	}
	source := domain.Source{
		ID: flavor.SourceID, Type: domain.SourceTypeFilesystem, Enabled: true,
		Config: domain.SourceConfig{Filesystem: &domain.FilesystemSourceConfig{
			Root: sourceRoot, Include: []string{"**/*"}, Flavor: flavor.Flavor,
		}},
	}
	if err := initWorkspace(workspaceRoot); err != nil {
		return err
	}
	configPath := configOutputPath(config)
	if err := writeBootstrapConfig(configPath, workspaceRoot, source); err != nil {
		return err
	}
	runtimeConfig, err := hostConfig(cmd.Context())
	if err != nil {
		return err
	}
	runtimeConfig.Workspace = workspaceRoot
	runtimeConfig.Sources = []domain.Source{source}
	counter := &bootstrapSourceAdapter{inner: sourcefilesystem.NewDefault()}
	host, err := knowl.New(cmd.Context(), knowl.Options{
		Config: runtimeConfig,
		SourceAdapters: map[domain.SourceType]app.SourceAdapter{
			domain.SourceTypeFilesystem: counter,
		},
	})
	if err != nil {
		return err
	}
	shutdownTimeout := runtimeConfig.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = 10 * time.Second
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if stopErr := host.Stop(stopCtx); stopErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("stop bootstrap Host: %w", stopErr))
		}
	}()
	if _, err := host.SyncSource(cmd.Context(), flavor.SourceID); err != nil {
		if errors.Is(err, errBootstrapNoMarkdown) {
			return fmt.Errorf("bootstrap source path %q contains no Markdown files", sourceRoot)
		}
		return err
	}
	markdownFiles, auxiliaryFiles := counter.counts()
	if err := validateWorkspace(workspaceRoot); err != nil {
		return err
	}
	commandLogger(cmd).Info().
		Str("flavor", flavor.Name).
		Str("workspace", workspaceRoot).
		Str("config_path", configPath).
		Int("markdown_files", markdownFiles).
		Int("auxiliary_files", auxiliaryFiles).
		Msg("bootstrapped Knowl workspace")
	return nil
}

type bootstrapSourceAdapter struct {
	inner          app.SourceAdapter
	markdownFiles  int
	auxiliaryFiles int
}

func (adapter *bootstrapSourceAdapter) List(ctx context.Context, source domain.Source, pageToken string) (domain.DocumentPage, error) {
	page, err := adapter.inner.List(ctx, source, pageToken)
	if err != nil {
		return domain.DocumentPage{}, err
	}
	for _, document := range page.Documents {
		if document.Metadata["kind"] == "markdown" {
			adapter.markdownFiles++
		} else {
			adapter.auxiliaryFiles++
		}
	}
	if page.NextPageToken == "" && adapter.markdownFiles == 0 {
		return domain.DocumentPage{}, errBootstrapNoMarkdown
	}
	return page, nil
}

func (adapter *bootstrapSourceAdapter) Fetch(ctx context.Context, source domain.Source, document domain.DocumentRef) (domain.Document, error) {
	return adapter.inner.Fetch(ctx, source, document)
}

func (adapter *bootstrapSourceAdapter) counts() (int, int) {
	return adapter.markdownFiles, adapter.auxiliaryFiles
}

type bootstrapConfigDocument struct {
	Knowl bootstrapKnowlConfig `yaml:"knowl"`
}

type bootstrapKnowlConfig struct {
	Provider  string                   `yaml:"provider"`
	Workspace bootstrapWorkspaceConfig `yaml:"workspace"`
	Sources   []bootstrapSourceConfig  `yaml:"sources"`
	Storage   bootstrapStorageConfig   `yaml:"storage"`
	Scope     domain.ScopeRef          `yaml:"scope"`
}

type bootstrapWorkspaceConfig struct {
	Path string `yaml:"path"`
}

type bootstrapSourceConfig struct {
	ID         domain.SourceID               `yaml:"id"`
	Type       domain.SourceType             `yaml:"type"`
	Enabled    bool                          `yaml:"enabled"`
	Filesystem domain.FilesystemSourceConfig `yaml:"filesystem"`
}

type bootstrapStorageConfig struct {
	Type   string                `yaml:"type"`
	SQLite bootstrapSQLiteConfig `yaml:"sqlite"`
}

type bootstrapSQLiteConfig struct {
	Path string `yaml:"path"`
}

func writeBootstrapConfig(path, workspace string, source domain.Source) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat config %q: %w", path, err)
	}
	if source.Config.Filesystem == nil {
		return fmt.Errorf("bootstrap filesystem source is required")
	}
	document := bootstrapConfigDocument{Knowl: bootstrapKnowlConfig{
		Provider: "", Workspace: bootstrapWorkspaceConfig{Path: workspace},
		Sources: []bootstrapSourceConfig{{
			ID: source.ID, Type: source.Type, Enabled: source.Enabled, Filesystem: *source.Config.Filesystem,
		}},
		Storage: bootstrapStorageConfig{Type: knowl.StoreSQLite, SQLite: bootstrapSQLiteConfig{Path: ".knowl/knowl.sqlite"}},
		Scope:   knowl.DefaultScope,
	}}
	contents, err := yaml.Marshal(document)
	if err != nil {
		return fmt.Errorf("marshal bootstrap config: %w", err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
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
	if err := walkBootstrapPath(filepath.Join(workspaceRoot, workspaceWikiDir), checkWiki); err != nil {
		return err
	}
	checkEmpty := func(root, label string) error {
		return walkBootstrapPath(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
	return checkEmpty(filepath.Join(workspaceRoot, ".knowl"), "state")
}

func walkBootstrapPath(root string, walkFn fs.WalkDirFunc) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(root, walkFn)
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

func pathsOverlap(left, right string) (bool, error) {
	left, err := canonicalBootstrapPath(left)
	if err != nil {
		return false, err
	}
	right, err = canonicalBootstrapPath(right)
	if err != nil {
		return false, err
	}
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

func canonicalBootstrapPath(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	prefix := filepath.Clean(absolute)
	var suffix []string
	for {
		_, statErr := os.Lstat(prefix)
		if statErr == nil {
			resolved, evalErr := filepath.EvalSymlinks(prefix)
			if evalErr != nil {
				return "", evalErr
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(prefix)
		if parent == prefix {
			return filepath.Clean(absolute), nil
		}
		suffix = append(suffix, filepath.Base(prefix))
		prefix = parent
	}
}
