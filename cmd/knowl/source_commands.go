package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/spf13/cobra"
)

type localSourceHost interface {
	Sources() []domain.Source
	SyncSource(ctx context.Context, id domain.SourceID) (knowl.SourceSyncResult, error)
	SyncAll(ctx context.Context) (knowl.SourceSyncAllResult, error)
	SourceStatus(ctx context.Context, id domain.SourceID) (domain.SourceStatus, error)
	RetrySourceMaintenance(ctx context.Context, id domain.SourceID, failureClasses []string, dryRun bool) (app.SourceMaintenanceRetryResult, error)
	Stop(ctx context.Context) error
}

type localSourceSession struct {
	Host            localSourceHost
	ShutdownTimeout time.Duration
}

var newLocalSourceSession = newProductionLocalSourceSession

type sourceListItem struct {
	ID      domain.SourceID         `json:"id"`
	Type    domain.SourceType       `json:"type"`
	Enabled bool                    `json:"enabled"`
	Sync    domain.SourceSyncPolicy `json:"sync"`
}

type sourceListResult struct {
	Sources []sourceListItem `json:"sources"`
}

func newSourceCommand() *cobra.Command {
	command := &cobra.Command{
		Use:           sourceCommandName,
		Short:         "Inspect and synchronize configured knowledge sources",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	command.AddCommand(newSourceListCommand(), newSourceSyncCommand(), newSourceStatusCommand(), newSourceRetryCommand())
	return command
}

func newSourceRetryCommand() *cobra.Command {
	var failureClasses []string
	var dryRun bool
	command := &cobra.Command{
		Use:           sourceRetryCommandName + " <source-id>",
		Short:         "Preview or requeue failed source maintenance operations",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("exactly one source ID is required")
			}
			id := domain.SourceID(strings.TrimSpace(args[0]))
			if app.ValidateSourceID(id) != nil {
				return app.ErrSourceInvalid
			}
			_, err := app.NormalizeRetryFailureClasses(failureClasses)
			return err
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id := domain.SourceID(strings.TrimSpace(args[0]))
			classes, err := app.NormalizeRetryFailureClasses(failureClasses)
			if err != nil {
				return err
			}
			return withLocalSourceHost(cmd.Context(), func(host localSourceHost) error {
				result, retryErr := host.RetrySourceMaintenance(cmd.Context(), id, classes, dryRun)
				if err := writeSourceResult(cmd, result); err != nil {
					return err
				}
				return retryErr
			})
		},
	}
	command.Flags().StringArrayVar(&failureClasses, "failure-class", nil, "terminal failure class to requeue (repeatable; required)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "preview the exact eligible set without changing state")
	return command
}

func newSourceListCommand() *cobra.Command {
	return &cobra.Command{
		Use:           sourceListCommandName,
		Short:         "List configured knowledge sources",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withLocalSourceHost(cmd.Context(), func(host localSourceHost) error {
				sources := host.Sources()
				result := sourceListResult{Sources: make([]sourceListItem, len(sources))}
				for index, source := range sources {
					result.Sources[index] = sourceListItem{ID: source.ID, Type: source.Type, Enabled: source.Enabled, Sync: source.Sync}
				}
				return writeSourceResult(cmd, result)
			})
		},
	}
}

func newSourceSyncCommand() *cobra.Command {
	var all bool
	command := &cobra.Command{
		Use:           sourceSyncCommandName + " <source-id>",
		Short:         "Synchronize one configured source or all enabled sources",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args: func(_ *cobra.Command, args []string) error {
			switch {
			case all && len(args) != 0:
				return fmt.Errorf("source ID and --all are mutually exclusive")
			case all:
				return nil
			case len(args) != 1 || strings.TrimSpace(args[0]) == "":
				return fmt.Errorf("exactly one source ID or --all is required")
			default:
				return nil
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withLocalSourceHost(cmd.Context(), func(host localSourceHost) error {
				if all {
					result, syncErr := host.SyncAll(cmd.Context())
					if err := writeSourceResult(cmd, result); err != nil {
						return err
					}
					return syncErr
				}
				result, syncErr := host.SyncSource(cmd.Context(), domain.SourceID(strings.TrimSpace(args[0])))
				if syncErr != nil {
					return syncErr
				}
				return writeSourceResult(cmd, result)
			})
		},
	}
	command.Flags().BoolVar(&all, "all", false, "synchronize all enabled configured sources")
	return command
}

func newSourceStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:           sourceStatusCommandName + " <source-id>",
		Short:         "Read durable status for one configured source",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := domain.SourceID(strings.TrimSpace(args[0]))
			if id == "" {
				return fmt.Errorf("source ID is required")
			}
			return withLocalSourceHost(cmd.Context(), func(host localSourceHost) error {
				status, err := host.SourceStatus(cmd.Context(), id)
				if err != nil {
					return err
				}
				return writeSourceResult(cmd, status)
			})
		},
	}
}

func newProductionLocalSourceSession(ctx context.Context) (localSourceSession, error) {
	runtimeFactory, providerID, err := selectedRuntimeProvider(ctx)
	if err != nil {
		return localSourceSession{}, err
	}
	config, err := hostConfig(ctx)
	if err != nil {
		return localSourceSession{}, err
	}
	host, err := knowl.New(ctx, selectedHostOptions(config, runtimeFactory, providerID))
	if err != nil {
		return localSourceSession{}, err
	}
	return localSourceSession{Host: host, ShutdownTimeout: config.ShutdownTimeout}, nil
}

func withLocalSourceHost(ctx context.Context, operation func(localSourceHost) error) (returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := newLocalSourceSession(ctx)
	if err != nil {
		return err
	}
	if session.Host == nil {
		return fmt.Errorf("local source Host is required")
	}
	if session.ShutdownTimeout <= 0 {
		session.ShutdownTimeout = 10 * time.Second
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), session.ShutdownTimeout)
		defer cancel()
		if stopErr := session.Host.Stop(stopCtx); stopErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("stop local source Host: %w", stopErr))
		}
	}()
	return operation(session.Host)
}

func writeSourceResult(cmd *cobra.Command, value any) error {
	if err := json.NewEncoder(cmd.OutOrStdout()).Encode(value); err != nil {
		return fmt.Errorf("encode source result: %w", err)
	}
	return nil
}
