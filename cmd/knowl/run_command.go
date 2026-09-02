package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	knowlruntime "github.com/baldaworks/knowl/pkg/knowl"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/spf13/cobra"
)

const runCommandName = "run"

type localRunHost interface {
	RunOnce(ctx context.Context, options knowlruntime.RunOnceOptions) (knowlruntime.RunOnceResult, error)
	Stop(ctx context.Context) error
}

type localRunSession struct {
	Host            localRunHost
	ShutdownTimeout time.Duration
}

var newLocalRunSession = newProductionLocalRunSession

func newRunCommand() *cobra.Command {
	var (
		sourceID     string
		noSync       bool
		noDrain      bool
		noHierarchy  bool
	)

	command := &cobra.Command{
		Use:           runCommandName,
		Short:         "Run one complete, bounded knowledge processing cycle and exit",
		Long: "Run one complete, bounded knowledge processing cycle and exit.\n\n" +
			"Executes in-process without starting HTTP or MCP network listeners:\n" +
			"1. Synchronizes configured sources (all or single).\n" +
			"2. Drains ready maintenance operations to terminal states.\n" +
			"3. Reconciles semantic OKF hierarchy if enabled and supported.\n" +
			"Outputs structured JSON execution results to stdout.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options := knowlruntime.RunOnceOptions{
				SyncSources:        !noSync,
				SourceID:           domain.SourceID(strings.TrimSpace(sourceID)),
				DrainOperations:    !noDrain,
				ReconcileHierarchy: !noHierarchy,
			}
			return withLocalRunHost(cmd.Context(), func(host localRunHost) error {
				result, runErr := host.RunOnce(cmd.Context(), options)
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
					return fmt.Errorf("encode run result: %w", err)
				}
				return runErr
			})
		},
	}

	command.Flags().StringVar(&sourceID, "source", "", "restrict synchronization to a single source ID")
	command.Flags().BoolVar(&noSync, "no-sync", false, "skip source synchronization phase")
	command.Flags().BoolVar(&noDrain, "no-drain", false, "skip operation drain phase")
	command.Flags().BoolVar(&noHierarchy, "no-hierarchy", false, "skip hierarchy reconciliation phase")

	return command
}

func newProductionLocalRunSession(ctx context.Context) (localRunSession, error) {
	runtimeFactory, providerID, err := selectedRuntimeProvider(ctx)
	if err != nil {
		return localRunSession{}, err
	}
	config, err := hostConfig(ctx)
	if err != nil {
		return localRunSession{}, err
	}
	host, err := knowlruntime.New(ctx, selectedHostOptions(config, runtimeFactory, providerID))
	if err != nil {
		return localRunSession{}, err
	}
	return localRunSession{Host: host, ShutdownTimeout: config.ShutdownTimeout}, nil
}

func withLocalRunHost(ctx context.Context, operation func(localRunHost) error) (returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := newLocalRunSession(ctx)
	if err != nil {
		return err
	}
	if session.Host == nil {
		return fmt.Errorf("local run Host is required")
	}
	if session.ShutdownTimeout <= 0 {
		session.ShutdownTimeout = 10 * time.Second
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), session.ShutdownTimeout)
		defer cancel()
		if stopErr := session.Host.Stop(stopCtx); stopErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("stop local run Host: %w", stopErr))
		}
	}()
	return operation(session.Host)
}
