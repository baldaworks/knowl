package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	knowlruntime "github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/spf13/cobra"
)

type localHierarchyHost interface {
	ReconcileHierarchy(ctx context.Context) (app.IngestResult, error)
	Stop(ctx context.Context) error
}

type localHierarchySession struct {
	Host            localHierarchyHost
	ShutdownTimeout time.Duration
}

var newLocalHierarchySession = newProductionLocalHierarchySession

type hierarchyReconcileResult struct {
	OperationID knowl.OperationID     `json:"operation_id"`
	Status      knowl.OperationStatus `json:"status"`
	Changed     bool                  `json:"changed"`
	Generation  string                `json:"generation,omitempty"`
	Files       []string              `json:"files,omitempty"`
}

func newHierarchyCommand() *cobra.Command {
	command := &cobra.Command{
		Use:           hierarchyCommandName,
		Short:         "Manage the canonical semantic wiki hierarchy",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	command.AddCommand(newHierarchyReconcileCommand())
	return command
}

func newHierarchyReconcileCommand() *cobra.Command {
	return &cobra.Command{
		Use:   hierarchyReconcileCommandName,
		Short: "Explicitly reconcile semantic OKF catalogs",
		Long: "Explicitly reconcile semantic OKF catalogs.\n\n" +
			"This is a canonical mutation. It does not start source synchronization or the service listener and prints a structured JSON operation result.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withLocalHierarchyHost(cmd.Context(), func(host localHierarchyHost) error {
				result, err := host.ReconcileHierarchy(cmd.Context())
				if err != nil {
					return err
				}
				output := hierarchyReconcileResult{
					OperationID: result.Operation.ID,
					Status:      result.Operation.Status,
					Changed:     result.Commit != nil,
				}
				if result.Commit != nil {
					output.Generation = result.Commit.Generation
					output.Files = append([]string(nil), result.Commit.Files...)
				}
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(output); err != nil {
					return fmt.Errorf("encode hierarchy reconcile result: %w", err)
				}
				return nil
			})
		},
	}
}

func newProductionLocalHierarchySession(ctx context.Context) (localHierarchySession, error) {
	runtimeFactory, providerID, err := selectedRuntimeProvider(ctx)
	if err != nil {
		return localHierarchySession{}, err
	}
	config, err := hostConfig(ctx)
	if err != nil {
		return localHierarchySession{}, err
	}
	host, err := knowlruntime.New(ctx, selectedHostOptions(config, runtimeFactory, providerID))
	if err != nil {
		return localHierarchySession{}, err
	}
	return localHierarchySession{Host: host, ShutdownTimeout: config.ShutdownTimeout}, nil
}

func withLocalHierarchyHost(ctx context.Context, operation func(localHierarchyHost) error) (returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := newLocalHierarchySession(ctx)
	if err != nil {
		return err
	}
	if session.Host == nil {
		return fmt.Errorf("local hierarchy Host is required")
	}
	if session.ShutdownTimeout <= 0 {
		session.ShutdownTimeout = 10 * time.Second
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), session.ShutdownTimeout)
		defer cancel()
		if stopErr := session.Host.Stop(stopCtx); stopErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("stop local hierarchy Host: %w", stopErr))
		}
	}()
	return operation(session.Host)
}
