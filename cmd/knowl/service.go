package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowlfx"
	"github.com/normahq/runtime/v2/agentfactory"
	"github.com/normahq/runtime/v2/mcpregistry"
	"github.com/spf13/cobra"
	"go.uber.org/fx"
)

func runStart(cmd *cobra.Command) error {
	runtimeFactory, providerID, err := selectedRuntimeProvider(cmd.Context())
	if err != nil {
		return err
	}
	config, err := hostConfig(cmd.Context())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var host *knowl.Host
	application := knowlfx.NewApp(ctx, knowlfx.Options{
		Config:         config,
		RuntimeFactory: runtimeFactory,
		ProviderID:     providerID,
	}, fx.Populate(&host))
	cleanupHost := func() error {
		if host == nil {
			return nil
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
		defer cancel()
		return host.Stop(cleanupCtx)
	}
	if err := application.Err(); err != nil {
		return errors.Join(err, cleanupHost())
	}
	if err := application.Start(ctx); err != nil {
		return errors.Join(err, cleanupHost())
	}
	runErr := host.Wait(ctx)
	stopCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()
	stopErr := application.Stop(stopCtx)
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		runErr = nil
	}
	return errors.Join(runErr, stopErr)
}

func hostConfig(ctx context.Context) (knowl.Config, error) {
	loaded, err := configFromContext(ctx)
	if err != nil {
		return knowl.Config{}, err
	}
	workspace, err := workspacePath(ctx)
	if err != nil {
		return knowl.Config{}, err
	}
	storage, err := storageSettings(ctx)
	if err != nil {
		return knowl.Config{}, err
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace
	config.StoreDriver = storage.Driver
	if value := loaded.Document.Knowl.Scope; value != "" {
		config.Scope = value
	}
	if value := loaded.Document.Knowl.Server.ListenAddr; value != "" {
		config.ListenAddr = value
	}
	config.StorePath = storage.Path
	config.PostgresDSN = storage.DSN
	return config, nil
}

func selectedRuntimeProvider(ctx context.Context) (*agentfactory.Factory, string, error) {
	loaded, err := configFromContext(ctx)
	if err != nil {
		return nil, "", err
	}
	providerID := strings.TrimSpace(loaded.Document.Knowl.Provider)
	if providerID == "" {
		return nil, "", fmt.Errorf("knowl.provider is required")
	}
	factory := agentfactory.New(
		loaded.Document.Runtime.Providers,
		mcpregistry.New(loaded.Document.Runtime.MCPServers),
	)
	if err := factory.ValidateAgent(providerID); err != nil {
		return nil, "", fmt.Errorf("validate knowl.provider: %w", err)
	}
	return factory, providerID, nil
}
