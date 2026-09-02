package main

import (
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

const (
	appName                       = "knowl"
	initCommandName               = "init"
	validateCommandName           = "validate"
	migrateCommandName            = "migrate"
	migrateOKFV02Name             = "okf-v0.2"
	hierarchyCommandName          = "hierarchy"
	hierarchyReconcileCommandName = "reconcile"
	bootstrapCommandName          = "bootstrap"
	bootstrapWikiName             = "wiki"
	bootstrapObsidianName         = "obsidian"
	bootstrapOKFName              = "okf"
	startCommandName              = "start"
	ingestCommandName             = "ingest"
	retrieveCommandName           = "retrieve"
	searchCommandName             = "search"
	lintCommandName               = "lint"
	operationCommandName          = "operation"
	pageCommandName               = "page"
	sourceCommandName             = "source"
	sourceListCommandName         = "list"
	sourceSyncCommandName         = "sync"
	sourceStatusCommandName       = "status"
	sourceRetryCommandName        = "retry"
	postgresStore                 = "postgres"
	defaultStore                  = "sqlite"
	defaultWorkspace              = "."
	workspaceWikiDir              = "wiki"
)

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           appName,
		Short:         "Bootstrap or run a local Knowl workspace, then use retrieve, ingest, source, and operation workflows",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Supported local workflow:
1. Bootstrap a fresh Knowl workspace from an existing wiki or Obsidian vault:
   - knowl bootstrap wiki <path>
   - knowl bootstrap obsidian <path>
   - knowl bootstrap okf <path>
2. Or initialize an empty workspace explicitly:
   - knowl init
   - knowl validate
3. Run the supported local workflow commands:
   - knowl retrieve <text>
   - knowl ingest --input request.json
   - knowl operation <operation-id>
4. Inspect or synchronize sources with the configured maintainer provider:
   - knowl source list
   - knowl source sync <source-id>
   - knowl source status <source-id>
   - knowl source retry <source-id> --failure-class provider --dry-run
5. Explicitly reconcile a flat semantic wiki into nested OKF catalogs:
   - knowl hierarchy reconcile
6. Run one complete knowledge processing cycle (sync + drain + hierarchy) and exit:
   - knowl run
   - knowl run --source <source-id>
7. Run knowl start when you need the retained loopback HTTP/OpenAPI service mode.

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
		newMigrateCommand(),
		newHierarchyCommand(),
		newBootstrapCommand(),
		newStartCommand(),
		newRunCommand(),
		newIngestCommand(),
		newRetrieveCommand(),
		newOperationCommand(),
		newSourceCommand(),
	} {
		root.AddCommand(command)
	}
	return root
}
