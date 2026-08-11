package main

import (
	"fmt"
	bootstrap "github.com/baldaworks/knowl/internal/bootstrap"
	"os"
	"path/filepath"
	"strings"

	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/spf13/cobra"
)

const (
	bootstrapWikiAdapter     = "bootstrap_wiki"
	bootstrapObsidianAdapter = "bootstrap_obsidian"
)

type bootstrapFlavor struct {
	Name               string
	Adapter            string
	RewriteObsidianRef bool
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
	scope := config.Document.Knowl.Scope
	if strings.TrimSpace(string(scope)) == "" {
		scope = domain.ScopeRef("local")
	}
	summary, err := bootstrap.Import(cmd.Context(), bootstrap.Options{
		WorkspaceRoot: workspaceRoot,
		SourceRoot:    sourceRoot,
		Scope:         scope,
		Flavor: bootstrap.Flavor{
			Name:               flavor.Name,
			Adapter:            flavor.Adapter,
			RewriteObsidianRef: flavor.RewriteObsidianRef,
		},
	})
	if err != nil {
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
		Int("markdown_files", summary.MarkdownFiles).
		Int("auxiliary_files", summary.AuxiliaryFiles).
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
