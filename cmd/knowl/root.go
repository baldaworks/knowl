package main

import (
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

const (
	appName               = "knowl"
	initCommandName       = "init"
	validateCommandName   = "validate"
	bootstrapCommandName  = "bootstrap"
	bootstrapWikiName     = "wiki"
	bootstrapObsidianName = "obsidian"
	startCommandName      = "start"
	ingestCommandName     = "ingest"
	queryCommandName      = "query"
	searchCommandName     = "search"
	lintCommandName       = "lint"
	operationCommandName  = "operation"
	pageCommandName       = "page"
	postgresStore         = "postgres"
	defaultStore          = "sqlite"
	defaultWorkspace      = "."
	workspaceWikiDir      = "wiki"
)

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           appName,
		Short:         "Bootstrap or run a local Knowl workspace, then use the KISS retrieve/ingest/operation workflow",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Supported local workflow:
1. Bootstrap a fresh Knowl workspace from an existing wiki or Obsidian vault:
   - knowl bootstrap wiki <path>
   - knowl bootstrap obsidian <path>
2. Or initialize an empty workspace explicitly:
   - knowl init
   - knowl validate
3. Run the supported local workflow commands:
   - knowl query <text>
   - knowl ingest --input request.json
   - knowl operation <operation-id>
4. Run knowl start when you need the retained loopback HTTP/OpenAPI service mode.

Bootstrap creates a Knowl-owned workspace. Local workflow commands execute
in-process and print structured JSON results. The retained loopback HTTP API
exposes the same KISS contract for retrieve, ingest, operation, and health.`,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			initCommandLogging(cmd.ErrOrStderr())
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
			cmd.SetContext(log.Logger.WithContext(loaded))
			return nil
		},
	}
	root.PersistentFlags().String("config-dir", "", "extra config root directory (highest priority)")
	root.PersistentFlags().String("profile", "", "config profile name")

	for _, command := range []*cobra.Command{
		newInitCommand(),
		newValidateCommand(),
		newBootstrapCommand(),
		newStartCommand(),
		newIngestCommand(),
		newQueryCommand(),
		newOperationCommand(),
	} {
		root.AddCommand(command)
	}
	return root
}
