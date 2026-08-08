package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	schemaFile = "schema.md"
	indexFile  = "wiki/index.md"
	logFile    = "wiki/log.md"

	defaultSchema = `# Knowl schema

This document defines the page, link, citation, ingest, query, and lint conventions for this workspace.

Maintainer plans may read this document but may not modify it.
`
)

func newInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   initCommandName,
		Short: "Initialize a Knowl workspace and config",
		RunE: func(_ *cobra.Command, _ []string) error {
			workspace, err := workspacePath()
			if err != nil {
				return err
			}
			if err := initWorkspace(workspace); err != nil {
				return err
			}
			configPath := viper.ConfigFileUsed()
			if configPath == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				configPath = filepath.Join(cwd, configRelative)
			}
			if err := writeConfig(configPath, workspace); err != nil {
				return err
			}
			fmt.Printf("initialized Knowl workspace at %s\n", workspace)
			return nil
		},
	}
}

func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate Knowl configuration and workspace",
		RunE: func(_ *cobra.Command, _ []string) error {
			workspace, err := workspacePath()
			if err != nil {
				return err
			}
			if err := validateWorkspace(workspace); err != nil {
				return err
			}
			driver, err := storeDriver()
			if err != nil {
				return err
			}
			fmt.Printf("valid Knowl workspace %s (store: %s)\n", workspace, driver)
			return nil
		},
	}
}

func newStartCommand() *cobra.Command {
	return &cobra.Command{Use: "start", Short: "Start the Knowl service", RunE: func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("knowl service runtime is not implemented yet")
	}}
}

func newIngestCommand() *cobra.Command {
	return &cobra.Command{Use: "ingest", Short: "Ingest a source into the Knowl workspace", RunE: func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("knowl ingest workflow is not implemented yet")
	}}
}

func newLintCommand() *cobra.Command {
	return &cobra.Command{Use: "lint", Short: "Check Knowl workspace health", RunE: func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("knowl lint is not implemented yet")
	}}
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
	contents := fmt.Sprintf("workspace:\n  path: %q\nstore:\n  driver: sqlite\nmaintenance:\n  review: true\n", workspace)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}
