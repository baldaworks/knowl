package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"

	"github.com/baldaworks/knowl/internal/releaseinfo"
	"github.com/spf13/cobra"
)

const (
	knowlMarketplaceName   = "knowl"
	knowlMarketplaceSource = "baldaworks/knowl"
	knowlPluginID          = "knowl@knowl"
	setupStatusReplaced    = "replaced"
	setupStatusUnchanged   = "unchanged"
	codexAddAction         = "add"
	codexJSONFlag          = "--json"
	codexMarketplaceAction = "marketplace"
	codexPluginAction      = "plugin"
)

var newCodexProcess = func(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "codex", args...)
}

type setupCodexResult struct {
	Version         string `json:"version"`
	Project         string `json:"project"`
	Marketplace     string `json:"marketplace"`
	Plugin          string `json:"plugin"`
	RestartRequired bool   `json:"restart_required"`
}

type setupBoundaryError struct {
	Boundary string
	Err      error
}

func (err *setupBoundaryError) Error() string {
	return fmt.Sprintf("setup codex %s: %v", err.Boundary, err.Err)
}

func (err *setupBoundaryError) Unwrap() error { return err.Err }

func setupError(boundary string, err error) error {
	return &setupBoundaryError{Boundary: boundary, Err: err}
}

func newSetupCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   setupCommandName,
		Short: "Set up a supported coding-agent integration",
	}
	var replace bool
	var skipProject bool
	codex := &cobra.Command{
		Use:   setupCodexCommandName,
		Short: "Initialize this Knowl project and install or repair its Codex plugin",
		Long: "Initialize and validate the current Knowl project without replacing existing files, then install the release-matched Codex marketplace and plugin. " +
			"Use --skip-project to change only Codex state. Conflicting Codex state is removed only with --replace.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := runSetupCodex(cmd, replace, skipProject)
			if err != nil {
				return err
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
				return setupError("result", err)
			}
			return nil
		},
	}
	codex.Flags().BoolVar(&replace, "replace", false, "remove and replace conflicting Knowl-managed Codex state")
	codex.Flags().BoolVar(&skipProject, "skip-project", false, "install or repair Codex integration without changing this project")
	command.AddCommand(codex)
	return command
}

func runSetupCodex(cmd *cobra.Command, replace, skipProject bool) (setupCodexResult, error) {
	identity, err := releaseinfo.CurrentRelease()
	if err != nil {
		return setupCodexResult{}, setupError("release_identity", err)
	}
	result := setupCodexResult{Version: identity.Version, Project: "skipped"}
	if !skipProject {
		if err := setupCodexProject(cmd.Context()); err != nil {
			return setupCodexResult{}, err
		}
		result.Project = "ready"
	}

	marketplaces, err := inspectCodexMarketplaces(cmd.Context())
	if err != nil {
		return setupCodexResult{}, err
	}
	marketplace := findMarketplace(marketplaces, knowlMarketplaceName)
	plugins := codexPluginList{}
	pluginsInspected := false
	if marketplace != nil && !compatibleMarketplaceSource(marketplace.MarketplaceSource) {
		if !replace {
			return setupCodexResult{}, setupError("marketplace_conflict", errors.New("marketplace knowl has a different source; rerun with --replace to replace it"))
		}
		plugins, err = inspectCodexPlugins(cmd.Context())
		if err != nil {
			return setupCodexResult{}, err
		}
		if findPlugin(plugins.Installed, knowlPluginID) != nil {
			if err := removeCodexPlugin(cmd.Context()); err != nil {
				return setupCodexResult{}, err
			}
			result.Plugin = setupStatusReplaced
		}
		if err := runCodexJSON(cmd.Context(), "marketplace_remove", nil, codexPluginAction, codexMarketplaceAction, "remove", knowlMarketplaceName, codexJSONFlag); err != nil {
			return setupCodexResult{}, err
		}
		marketplace = nil
		pluginsInspected = false
		result.Marketplace = setupStatusReplaced
		result.RestartRequired = true
	}

	addOutput := codexMarketplaceAddOutput{}
	addErr := runCodexJSON(cmd.Context(), "marketplace_add", &addOutput,
		codexPluginAction, codexMarketplaceAction, codexAddAction, knowlMarketplaceSource,
		"--ref", identity.Tag,
		"--sparse", ".agents/plugins",
		"--sparse", "plugins/knowl",
		codexJSONFlag,
	)
	if addErr != nil && marketplace != nil && replace {
		if !pluginsInspected {
			plugins, err = inspectCodexPlugins(cmd.Context())
			if err != nil {
				return setupCodexResult{}, err
			}
		}
		if findPlugin(plugins.Installed, knowlPluginID) != nil {
			if err := removeCodexPlugin(cmd.Context()); err != nil {
				return setupCodexResult{}, err
			}
			result.Plugin = setupStatusReplaced
		}
		if err := runCodexJSON(cmd.Context(), "marketplace_remove", nil, codexPluginAction, codexMarketplaceAction, "remove", knowlMarketplaceName, codexJSONFlag); err != nil {
			return setupCodexResult{}, err
		}
		addErr = runCodexJSON(cmd.Context(), "marketplace_add", &addOutput,
			codexPluginAction, codexMarketplaceAction, codexAddAction, knowlMarketplaceSource,
			"--ref", identity.Tag,
			"--sparse", ".agents/plugins",
			"--sparse", "plugins/knowl",
			codexJSONFlag,
		)
		pluginsInspected = false
		result.Marketplace = setupStatusReplaced
		result.RestartRequired = true
	}
	if addErr != nil {
		return setupCodexResult{}, addErr
	}
	if addOutput.MarketplaceName != knowlMarketplaceName {
		return setupCodexResult{}, setupError("marketplace_add", errors.New("codex returned an unexpected marketplace"))
	}
	if result.Marketplace == "" {
		if addOutput.AlreadyAdded {
			result.Marketplace = setupStatusUnchanged
		} else {
			result.Marketplace = "added"
			result.RestartRequired = true
		}
	}

	if !pluginsInspected {
		plugins, err = inspectCodexPlugins(cmd.Context())
		if err != nil {
			return setupCodexResult{}, err
		}
	}
	installed := findPlugin(plugins.Installed, knowlPluginID)
	installAfterRemoval := false
	if installed != nil && (!installed.Enabled || installed.Version != identity.Version) {
		if !replace {
			return setupCodexResult{}, setupError("plugin_conflict", errors.New("installed plugin knowl@knowl does not match the required release; rerun with --replace to replace it"))
		}
		if err := removeCodexPlugin(cmd.Context()); err != nil {
			return setupCodexResult{}, err
		}
		installed = nil
		installAfterRemoval = true
		result.Plugin = setupStatusReplaced
		result.RestartRequired = true
	}
	if installed != nil {
		if result.Plugin == "" {
			result.Plugin = setupStatusUnchanged
		}
		return result, nil
	}

	if !installAfterRemoval {
		available := findPlugin(plugins.Available, knowlPluginID)
		if available == nil || available.Version != identity.Version {
			return setupCodexResult{}, setupError("plugin_inspection", errors.New("release-matched plugin knowl@knowl is not available from marketplace knowl"))
		}
	}
	added := codexPluginAddOutput{}
	if err := runCodexJSON(cmd.Context(), "plugin_install", &added, codexPluginAction, codexAddAction, knowlPluginID, codexJSONFlag); err != nil {
		return setupCodexResult{}, err
	}
	if added.PluginID != knowlPluginID || added.MarketplaceName != knowlMarketplaceName || added.Version != identity.Version {
		return setupCodexResult{}, setupError("plugin_install", errors.New("codex installed an unexpected plugin release"))
	}
	if result.Plugin == "" {
		result.Plugin = "installed"
	}
	result.RestartRequired = true
	return result, nil
}

func setupCodexProject(ctx context.Context) error {
	config, err := configFromContext(ctx)
	if err != nil {
		return setupError("project_initialization", err)
	}
	workspace, err := workspacePath(ctx)
	if err != nil {
		return setupError("project_initialization", err)
	}
	if err := initWorkspace(workspace); err != nil {
		return setupError("project_initialization", err)
	}
	if err := writeConfig(configOutputPath(config), workspace); err != nil {
		return setupError("project_initialization", err)
	}
	if err := validateWorkspace(workspace); err != nil {
		return setupError("project_validation", err)
	}
	if _, _, err := selectedRuntimeProvider(ctx); err != nil {
		return setupError("project_validation", err)
	}
	if _, err := storeDriver(ctx); err != nil {
		return setupError("project_validation", err)
	}
	return nil
}

type codexMarketplaceList struct {
	Marketplaces []codexMarketplace `json:"marketplaces"`
}

type codexMarketplace struct {
	Name              string                  `json:"name"`
	MarketplaceSource *codexMarketplaceSource `json:"marketplaceSource"`
}

type codexMarketplaceSource struct {
	SourceType string `json:"sourceType"`
	Source     string `json:"source"`
}

type codexMarketplaceAddOutput struct {
	MarketplaceName string `json:"marketplaceName"`
	AlreadyAdded    bool   `json:"alreadyAdded"`
}

type codexPluginList struct {
	Installed []codexPlugin `json:"installed"`
	Available []codexPlugin `json:"available"`
}

type codexPlugin struct {
	PluginID        string `json:"pluginId"`
	Name            string `json:"name"`
	MarketplaceName string `json:"marketplaceName"`
	Version         string `json:"version"`
	Installed       bool   `json:"installed"`
	Enabled         bool   `json:"enabled"`
}

type codexPluginAddOutput struct {
	PluginID        string `json:"pluginId"`
	MarketplaceName string `json:"marketplaceName"`
	Version         string `json:"version"`
}

func inspectCodexMarketplaces(ctx context.Context) ([]codexMarketplace, error) {
	output := codexMarketplaceList{}
	if err := runCodexJSON(ctx, "marketplace_inspection", &output, codexPluginAction, codexMarketplaceAction, "list", codexJSONFlag); err != nil {
		return nil, err
	}
	if output.Marketplaces == nil {
		return nil, setupError("marketplace_inspection", errors.New("codex JSON did not contain a marketplaces array"))
	}
	return output.Marketplaces, nil
}

func inspectCodexPlugins(ctx context.Context) (codexPluginList, error) {
	output := codexPluginList{}
	if err := runCodexJSON(ctx, "plugin_inspection", &output, codexPluginAction, "list", "--marketplace", knowlMarketplaceName, "--available", codexJSONFlag); err != nil {
		return codexPluginList{}, err
	}
	if output.Installed == nil || output.Available == nil {
		return codexPluginList{}, setupError("plugin_inspection", errors.New("codex JSON did not contain installed and available arrays"))
	}
	return output, nil
}

func runCodexJSON(ctx context.Context, boundary string, output any, args ...string) error {
	command := newCodexProcess(ctx, args...)
	command.Stdin = bytes.NewReader(nil)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return setupError(boundary, fmt.Errorf("codex command failed: %w", err))
	}
	if output == nil {
		var value any
		output = &value
	}
	decoder := json.NewDecoder(&stdout)
	if err := decoder.Decode(output); err != nil {
		return setupError(boundary, fmt.Errorf("decode Codex JSON: %w", err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return setupError(boundary, fmt.Errorf("decode Codex JSON: %w", err))
	}
	return nil
}

func findMarketplace(marketplaces []codexMarketplace, name string) *codexMarketplace {
	for index := range marketplaces {
		if marketplaces[index].Name == name {
			return &marketplaces[index]
		}
	}
	return nil
}

func compatibleMarketplaceSource(source *codexMarketplaceSource) bool {
	if source == nil || source.SourceType != "git" {
		return false
	}
	normalized := strings.TrimSuffix(source.Source, ".git")
	return slices.Contains([]string{
		knowlMarketplaceSource,
		"https://github.com/" + knowlMarketplaceSource,
		"git@github.com:" + knowlMarketplaceSource,
	}, normalized)
}

func findPlugin(plugins []codexPlugin, id string) *codexPlugin {
	for index := range plugins {
		if plugins[index].PluginID == id && plugins[index].Name == "knowl" && plugins[index].MarketplaceName == knowlMarketplaceName {
			return &plugins[index]
		}
	}
	return nil
}

func removeCodexPlugin(ctx context.Context) error {
	var ignored any
	return runCodexJSON(ctx, "plugin_remove", &ignored, codexPluginAction, "remove", knowlPluginID, codexJSONFlag)
}
