package main

import (
	"encoding/json"
	"fmt"

	knowlruntime "github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/spf13/cobra"
)

func newMigrateCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   migrateCommandName,
		Short: "Run an explicit canonical workspace migration",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(newMigrateOKFV02Command())
	return command
}

func newMigrateOKFV02Command() *cobra.Command {
	return &cobra.Command{
		Use:   migrateOKFV02Name,
		Short: "Migrate a legacy canonical workspace to OKF v0.2",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			workspacePath, err := workspacePath(cmd.Context())
			if err != nil {
				return err
			}
			workspace, err := contentfs.New(workspacePath)
			if err != nil {
				return err
			}
			result, err := workspace.MigrateOKFV02(cmd.Context())
			if err != nil {
				return err
			}
			if err := workspace.Validate(); err != nil {
				return fmt.Errorf("validate migrated workspace: %w", err)
			}
			config, err := hostConfig(cmd.Context())
			if err != nil {
				return err
			}
			snapshot, err := workspace.Snapshot(cmd.Context(), config.Scope)
			if err != nil {
				return fmt.Errorf("snapshot migrated workspace: %w", err)
			}
			if err := knowlruntime.RebuildProjection(cmd.Context(), config, snapshot); err != nil {
				return fmt.Errorf("rebuild migrated projection: %w", err)
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
				return fmt.Errorf("encode migration result: %w", err)
			}
			return nil
		},
	}
}
