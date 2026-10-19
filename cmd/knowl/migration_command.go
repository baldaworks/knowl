package main

import (
	"encoding/json"
	"fmt"

	knowlruntime "github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/spf13/cobra"
)

const (
	databaseMigrationAnnotation = "knowl.io/database-migration"
	databaseMigrationBaseStream = "base"
)

func newMigrateCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   migrateCommandName,
		Short: "Migrate the configured database or canonical workspace",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(newMigrateOKFV02Command(), newDatabaseMigrationCommand(domain.MigrationUp), newDatabaseMigrationCommand(domain.MigrationDown))
	return command
}

func newDatabaseMigrationCommand(direction domain.MigrationDirection) *cobra.Command {
	description := "Apply all pending database schema migrations and exit"
	if direction == domain.MigrationDown {
		description = "Roll back one database schema migration and exit (stop writers and back up first)"
	}
	return &cobra.Command{
		Use: string(direction), Short: description, Args: cobra.NoArgs,
		Annotations: map[string]string{databaseMigrationAnnotation: string(direction)},
		RunE: func(cmd *cobra.Command, _ []string) error {
			storage, err := storageSettings(cmd.Context())
			if err != nil {
				return databaseMigrationConfigError(err)
			}
			config, err := configFromContext(cmd.Context())
			if err != nil {
				return databaseMigrationConfigError(err)
			}
			result, err := knowlruntime.MigrateDatabase(cmd.Context(), knowlruntime.MigrationConfig{
				StoreDriver: storage.Driver, StorePath: storage.Path, PostgresDSN: storage.DSN,
				NativeVectors: config.Document.Knowl.Embeddings.Enabled,
			}, direction)
			if err != nil {
				return err
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
				return fmt.Errorf("encode database migration result: %w", err)
			}
			return nil
		},
	}
}

func databaseMigrationConfigError(err error) error {
	return &domain.MigrationError{Stage: domain.MigrationStageConfiguration, Stream: databaseMigrationBaseStream, Cause: err}
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
