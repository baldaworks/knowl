package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const (
	schemaFile = "schema.md"
	indexFile  = "wiki/index.md"
	logFile    = "wiki/log.md"

	loopbackHTTPAPIText      = "loopback HTTP API"
	startCommandUsage        = "knowl start"
	placeholderCommandShort  = "Placeholder command; use the loopback HTTP API after knowl start"
	unsupportedWorkflowToday = "not part of the supported local workflow today"

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
			fmt.Printf("initialized Knowl workspace at %s\n", workspace)
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
			fmt.Printf("valid Knowl workspace %s (store: %s)\n", workspace, driver)
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
	return &cobra.Command{
		Use:           ingestCommandName,
		Short:         placeholderCommandShort,
		SilenceErrors: true,
		SilenceUsage:  true,
		Long:          unsupportedWorkflowLong(ingestCommandName),
		RunE: func(_ *cobra.Command, _ []string) error {
			return unsupportedWorkflowError(ingestCommandName)
		},
	}
}

func newLintCommand() *cobra.Command {
	return &cobra.Command{
		Use:           lintCommandName,
		Short:         placeholderCommandShort,
		SilenceErrors: true,
		SilenceUsage:  true,
		Long:          unsupportedWorkflowLong(lintCommandName),
		RunE: func(_ *cobra.Command, _ []string) error {
			return unsupportedWorkflowError(lintCommandName)
		},
	}
}

func unsupportedWorkflowLong(operation string) string {
	return fmt.Sprintf(
		"knowl %s is %s.\n\nStart the local service with %q, then use the %s for\n%s operations.",
		operation,
		unsupportedWorkflowToday,
		startCommandUsage,
		loopbackHTTPAPIText,
		operation,
	)
}

func unsupportedWorkflowError(operation string) error {
	return fmt.Errorf(
		`knowl %s is %s; run %q and use the %s for %s operations`,
		operation,
		unsupportedWorkflowToday,
		startCommandUsage,
		loopbackHTTPAPIText,
		operation,
	)
}

func initWorkspace(workspace string) error {
	for _, path := range []string{
		filepath.Join(workspace, "raw"),
		filepath.Join(workspace, "wiki", "entities"),
		filepath.Join(workspace, "wiki", "concepts"),
		filepath.Join(workspace, "wiki", "syntheses"),
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
	for _, relative := range []string{"raw", "wiki", ".knowl"} {
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
	contents := fmt.Sprintf("runtime:\n  providers:\n    opencode:\n      type: opencode_acp\n      opencode_acp:\n        model: opencode/big-pickle\nknowl:\n  provider: opencode\n  workspace:\n    path: %q\n  storage:\n    type: sqlite\n    sqlite:\n      path: .knowl/knowl.sqlite\n  maintenance:\n    review: true\n", workspace)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}
