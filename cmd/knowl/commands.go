package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/spf13/cobra"
)

const (
	schemaFile = "schema.md"
	indexFile  = "wiki/index.md"
	logFile    = "wiki/log.md"

	loopbackHTTPAPIText = "loopback HTTP API"
	startCommandUsage   = "knowl start"

	defaultSchema = `# Knowl schema

This document defines the page, link, citation, ingest, query, and lint conventions for this workspace.

Maintainer plans may read this document but may not modify it.
`
)

func newInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   initCommandName,
		Short: "Initialize the supported local Knowl workspace and config",
		RunE: func(cmd *cobra.Command, _ []string) error {
			config, err := configFromContext(cmd.Context())
			if err != nil {
				return err
			}
			workspace, err := workspacePath(cmd.Context())
			if err != nil {
				return err
			}
			if err := initWorkspace(workspace); err != nil {
				return err
			}
			if err := writeConfig(configOutputPath(config), workspace); err != nil {
				return err
			}
			commandLogger(cmd).Info().
				Str("workspace", workspace).
				Msg("initialized Knowl workspace")
			return nil
		},
	}
}

func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   validateCommandName,
		Short: "Validate the supported local Knowl configuration and workspace",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, _, err := selectedRuntimeProvider(cmd.Context()); err != nil {
				return err
			}
			workspace, err := workspacePath(cmd.Context())
			if err != nil {
				return err
			}
			if err := validateWorkspace(workspace); err != nil {
				return err
			}
			driver, err := storeDriver(cmd.Context())
			if err != nil {
				return err
			}
			commandLogger(cmd).Info().
				Str("workspace", workspace).
				Str("store", driver).
				Msg("validated Knowl workspace")
			return nil
		},
	}
}

func newStartCommand() *cobra.Command {
	return &cobra.Command{Use: startCommandName, Short: "Start the supported local Knowl service", RunE: func(cmd *cobra.Command, _ []string) error {
		return runStart(cmd)
	}}
}

func newIngestCommand() *cobra.Command {
	command := newJSONBodyWorkflowCommand[knowlapi.SourceEnvelope](
		ingestCommandName,
		"Accept and process one immutable source revision",
		"/v1/ingest",
	)
	command.AddCommand(
		newJSONBodyWorkflowCommand[knowlapi.SourceEnvelope]("preview", "Accept and stage one immutable source revision", "/v1/ingest/preview"),
		newApplyWorkflowCommand(),
	)
	return command
}

func newQueryCommand() *cobra.Command {
	return newQueryReadCommand()
}

func newSearchCommand() *cobra.Command {
	return newSearchReadCommand()
}

func newLintCommand() *cobra.Command {
	return newLintReadCommand()
}

func newOperationCommand() *cobra.Command {
	return newOperationReadCommand()
}

func newPageCommand() *cobra.Command {
	return newPageReadCommand()
}

func initWorkspace(workspace string) error {
	for _, path := range []string{
		filepath.Join(workspace, "raw"),
		filepath.Join(workspace, workspaceWikiDir, "entities"),
		filepath.Join(workspace, workspaceWikiDir, "concepts"),
		filepath.Join(workspace, workspaceWikiDir, "syntheses"),
		filepath.Join(workspace, ".knowl", "staging"),
		filepath.Join(workspace, ".knowl", "recovery"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create workspace directory %q: %w", path, err)
		}
	}
	files := map[string]string{
		schemaFile: defaultSchema,
		indexFile:  "# Knowl index\n\nNo pages have been committed yet.\n",
		logFile:    "# Knowl log\n\n",
	}
	for relative, contents := range files {
		path := filepath.Join(workspace, filepath.FromSlash(relative))
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat workspace file %q: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			return fmt.Errorf("write workspace file %q: %w", path, err)
		}
	}
	return nil
}

func validateWorkspace(workspace string) error {
	for _, relative := range []string{schemaFile, indexFile, logFile} {
		path := filepath.Join(workspace, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("required workspace file %q: %w", relative, err)
		}
		if info.IsDir() {
			return fmt.Errorf("required workspace path %q is a directory", relative)
		}
	}
	for _, relative := range []string{"raw", workspaceWikiDir, ".knowl"} {
		path := filepath.Join(workspace, relative)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("required workspace directory %q: %w", relative, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("required workspace path %q is not a directory", relative)
		}
	}
	return nil
}

func writeConfig(path, workspace string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat config %q: %w", path, err)
	}
	contents := fmt.Sprintf("runtime:\n  providers:\n    opencode:\n      type: opencode_acp\n      opencode_acp:\n        model: opencode/big-pickle\nknowl:\n  provider: opencode\n  workspace:\n    path: %q\n  storage:\n    type: sqlite\n    sqlite:\n      path: .knowl/knowl.sqlite\n  ingest:\n    auto_apply: false\n", workspace)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}
