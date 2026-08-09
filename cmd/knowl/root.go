package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

const (
	appName          = "knowl"
	initCommandName  = "init"
	postgresStore    = "postgres"
	defaultStore     = "sqlite"
	defaultWorkspace = "."
)

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   appName,
		Short: "Maintain a local Markdown knowledge wiki",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			configDir, err := cmd.Flags().GetString("config-dir")
			if err != nil {
				return fmt.Errorf("read config-dir flag: %w", err)
			}
			profile, err := cmd.Flags().GetString("profile")
			if err != nil {
				return fmt.Errorf("read profile flag: %w", err)
			}
			loaded, err := loadConfig(cmd.Context(), configDir, profile)
			if err != nil {
				return err
			}
			cmd.SetContext(loaded)
			return nil
		},
	}
	root.PersistentFlags().String("config-dir", "", "extra config root directory (highest priority)")
	root.PersistentFlags().String("profile", "", "config profile name")

	for _, command := range []*cobra.Command{
		newInitCommand(),
		newValidateCommand(),
		newStartCommand(),
		newIngestCommand(),
		newLintCommand(),
	} {
		root.AddCommand(command)
	}
	return root
}
