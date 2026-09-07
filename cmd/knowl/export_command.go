package main

import (
	"fmt"
	"os"
	"path/filepath"

	knowlruntime "github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/llmstxt"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/spf13/cobra"
)

func newExportCommand() *cobra.Command {
	command := &cobra.Command{Use: exportCommandName, Short: "Export the semantic wiki", Args: cobra.NoArgs}
	command.AddCommand(newExportOKFCommand(), newExportLLMsTxtCommand())
	return command
}

func newExportOKFCommand() *cobra.Command {
	var output string
	command := &cobra.Command{
		Use:   exportOKFName,
		Short: "Copy wiki/ as an OKF directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			workspace, err := exportWorkspace(cmd)
			if err != nil {
				return err
			}
			return workspace.ExportOKF(cmd.Context(), output, contentfs.DefaultExportLimits())
		},
	}
	command.Flags().StringVarP(&output, "output", "o", "", "new OKF destination directory")
	_ = command.MarkFlagRequired("output")
	return command
}

func newExportLLMsTxtCommand() *cobra.Command {
	var options llmstxt.Options
	var output string
	command := &cobra.Command{
		Use:   exportLLMsTxtName,
		Short: "Render an llms.txt navigation file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			workspace, err := exportWorkspace(cmd)
			if err != nil {
				return err
			}
			config, err := configFromContext(cmd.Context())
			if err != nil {
				return err
			}
			scope := config.Document.Knowl.Scope
			if scope == "" {
				scope = knowlruntime.DefaultScope
			}
			inspection, err := workspace.Inspect(cmd.Context(), scope)
			if err != nil {
				return fmt.Errorf("inspect workspace: %w", err)
			}
			catalogs := make([]knowl.PageSnapshot, 0, len(inspection.Catalogs))
			for _, catalog := range inspection.Catalogs {
				if catalog.Path != inspection.Index.Path {
					catalogs = append(catalogs, catalog)
				}
			}
			document, err := llmstxt.Render(cmd.Context(), llmstxt.Input{
				Index: inspection.Index, Catalogs: catalogs, Pages: inspection.Snapshot.Pages,
			}, options)
			if err != nil {
				return fmt.Errorf("render llms.txt: %w", err)
			}
			if output == "" {
				_, err = cmd.OutOrStdout().Write(document)
				return err
			}
			return writeExportFile(output, document)
		},
	}
	command.Flags().StringVar(&options.Title, "title", "", "override the root title")
	command.Flags().StringVar(&options.Summary, "summary", "", "add a blockquote summary")
	command.Flags().StringVar(&options.BaseURL, "base-url", "", "prefix links with an HTTP(S) URL")
	command.Flags().StringVarP(&output, "output", "o", "", "write to a file instead of stdout")
	return command
}

func exportWorkspace(cmd *cobra.Command) (*contentfs.Workspace, error) {
	path, err := workspacePath(cmd.Context())
	if err != nil {
		return nil, err
	}
	return contentfs.New(path)
}

func writeExportFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".knowl-llms-txt-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
