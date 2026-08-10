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
		Short:         "Bootstrap or run a local Knowl workspace, then query/search/lint or use advanced ingest flows",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Supported local workflow:
1. Bootstrap a fresh Knowl workspace from an existing wiki or Obsidian vault:
   - knowl bootstrap wiki <path>
   - knowl bootstrap obsidian <path>
2. Or initialize an empty workspace explicitly:
   - knowl init
   - knowl validate
3. Run one-shot CLI workflows directly:
   - knowl query <text>
   - knowl search <text>
   - knowl lint
   - knowl page <page-id>
   - knowl page links <page-id>
4. Use advanced low-level ingest workflows when you need explicit source-envelope or operation control:
   - knowl ingest --input FILE|-
   - knowl ingest preview --input FILE|-
   - knowl ingest apply <operation-id>
   - knowl query file --input FILE|-
   - knowl operation <operation-id>
5. Run knowl start to keep the retained loopback HTTP/OpenAPI service mode available.

Bootstrap creates a Knowl-owned workspace. Direct CLI workflows execute
in-process and print structured JSON results. The retained loopback HTTP API
		remains supported for health checks, OpenAPI tooling, and local external
		clients.`,
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
		newSearchCommand(),
		newLintCommand(),
		newOperationCommand(),
		newPageCommand(),
	} {
		root.AddCommand(command)
	}
	return root
}
