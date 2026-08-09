package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	apphost "github.com/baldaworks/knowl/internal/apps/knowl"
	domain "github.com/baldaworks/knowl/pkg/knowl"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/fx"
)

func runStart(cmd *cobra.Command) error {
	config, err := hostConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var host *apphost.Host
	application := apphost.NewApp(ctx, apphost.Options{Config: config}, fx.Populate(&host))
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

func hostConfig() (apphost.Config, error) {
	workspace, err := workspacePath()
	if err != nil {
		return apphost.Config{}, err
	}
	driver, err := storeDriver()
	if err != nil {
		return apphost.Config{}, err
	}
	config := apphost.DefaultConfig()
	config.Workspace = workspace
	config.StoreDriver = driver
	if value := viper.GetString("scope"); value != "" {
		config.Scope = domain.ScopeRef(value)
	}
	if value := viper.GetString("server.listen_addr"); value != "" {
		config.ListenAddr = value
	}
	if value := viper.GetString("operator.token"); value != "" {
		config.OperatorToken = value
	}
	if value := viper.GetString("store.path"); value != "" {
		config.StorePath = value
	}
	if value := viper.GetString("store.postgres_dsn"); value != "" {
		config.PostgresDSN = value
	}
	if viper.IsSet("maintenance.auto_apply") {
		config.IngestOptions.AutoApply = viper.GetBool("maintenance.auto_apply")
	} else if viper.IsSet("maintenance.review") {
		config.IngestOptions.AutoApply = !viper.GetBool("maintenance.review")
	}
	return config, nil
}
