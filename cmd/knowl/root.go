package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

const (
	appName              = "knowl"
	initCommandName      = "init"
	validateCommandName  = "validate"
	startCommandName     = "start"
	ingestCommandName    = "ingest"
	queryCommandName     = "query"
	searchCommandName    = "search"
	lintCommandName      = "lint"
	operationCommandName = "operation"
	pageCommandName      = "page"
	postgresStore        = "postgres"
	defaultStore         = "sqlite"
	defaultWorkspace     = "."
)

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   appName,
		Short: "Run Knowl locally: init, validate, then use CLI workflows or start the loopback HTTP API",
		Long: `Supported local workflow:
1. knowl init
2. knowl validate
3. Run one-shot CLI workflows directly:
   - knowl ingest --input FILE|-
   - knowl ingest preview --input FILE|-
   - knowl ingest apply <operation-id>
   - knowl query <text>
   - knowl query file --input FILE|-
   - knowl search <text>
   - knowl lint
   - knowl operation <operation-id>
   - knowl page <page-id>
   - knowl page links <page-id>
4. Run knowl start to keep the retained loopback HTTP/OpenAPI service mode available.

Direct CLI workflows execute in-process and print structured JSON results.
The retained loopback HTTP API remains supported for health checks, OpenAPI
tooling, and local external clients.`,
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
