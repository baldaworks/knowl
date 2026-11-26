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
	"github.com/baldaworks/knowl/pkg/knowl/app"
	types "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowlfx"
	"github.com/baldaworks/runtime/v2/agentfactory"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	"github.com/spf13/cobra"
	"go.uber.org/fx"
)

type cliScopeContextKey struct{}

func cliScopeName(ctx context.Context) types.ScopeName {
	selected, _ := ctx.Value(cliScopeContextKey{}).(types.ScopeName)
	return selected
}

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
	application := knowlfx.NewApp(ctx, selectedHostOptions(config, runtimeFactory, providerID), fx.Populate(&host))
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
	if selected, ok := ctx.Value(cliScopeContextKey{}).(types.ScopeName); ok {
		config.ActiveScopeName = selected
	}
	config.Workspace = workspace
	config.StoreDriver = storage.Driver
	if value := loaded.Document.Knowl.Scope; value != "" {
		config.Scope = value
	}
	config.DefaultScopeName = loaded.Document.Knowl.DefaultScope
	if value := loaded.Document.Knowl.Server.ListenAddr; value != "" {
		config.ListenAddr = value
	}
	config.StorePath = storage.Path
	config.PostgresDSN = storage.DSN
	config.OperatorToken = loaded.Document.Knowl.Operator.Token
	config.API = loaded.Document.Knowl.API
	config.MCP = loaded.Document.Knowl.MCP
	config.Web = loaded.Document.Knowl.Web
	config.Embeddings = loaded.Document.Knowl.Embeddings
	config.Output = loaded.Document.Knowl.Output
	config.Timeouts = loaded.Document.Knowl.Timeouts
	config.Workers = loaded.Document.Knowl.Workers
	sources := configuredSources(loaded.Document.Knowl.Sources)
	config.Sources, err = knowl.NormalizeSources(workspace, loaded.WorkingDir, sources)
	if err != nil {
		return knowl.Config{}, fmt.Errorf("normalize knowl sources: %w", err)
	}
	if len(loaded.Document.Knowl.Scopes) != 0 {
		config.Scopes = make([]knowl.ScopeConfig, 0, len(loaded.Document.Knowl.Scopes))
		for name, configured := range loaded.Document.Knowl.Scopes {
			scopeSources, normalizeErr := knowl.NormalizeSources(
				workspace,
				loaded.WorkingDir,
				configuredSources(configured.Sources),
			)
			if normalizeErr != nil {
				return knowl.Config{}, fmt.Errorf("normalize knowl scope %q sources: %w", name, normalizeErr)
			}
			config.Scopes = append(config.Scopes, knowl.ScopeConfig{
				Name: types.ScopeName(name), Ref: configured.Ref,
				Disabled: configured.Disabled, Sources: scopeSources,
			})
		}
	}
	return config, nil
}

func authorizeCLI(ctx context.Context, operation app.OperationKind) (knowl.Config, knowl.ScopeContext, error) {
	config, err := hostConfig(ctx)
	if err != nil {
		return knowl.Config{}, knowl.ScopeContext{}, err
	}
	selected, err := config.ResolveAuthorizedScope(ctx, config.ActiveScopeName, operation)
	if err != nil {
		return knowl.Config{}, knowl.ScopeContext{}, err
	}
	return config, selected, nil
}

func authorizeCanonicalCLI(ctx context.Context, operation app.OperationKind) (knowl.Config, knowl.ScopeContext, error) {
	loaded, err := configFromContext(ctx)
	if err != nil {
		return knowl.Config{}, knowl.ScopeContext{}, err
	}
	workspace, err := workspacePath(ctx)
	if err != nil {
		return knowl.Config{}, knowl.ScopeContext{}, err
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace
	config.Scope = loaded.Document.Knowl.Scope
	config.DefaultScopeName = loaded.Document.Knowl.DefaultScope
	config.ActiveScopeName = cliScopeName(ctx)
	for name, configured := range loaded.Document.Knowl.Scopes {
		config.Scopes = append(config.Scopes, knowl.ScopeConfig{Name: types.ScopeName(name), Ref: configured.Ref, Disabled: configured.Disabled})
	}
	selected, err := config.ResolveAuthorizedScope(ctx, config.ActiveScopeName, operation)
	if err != nil {
		return knowl.Config{}, knowl.ScopeContext{}, err
	}
	return config, selected, nil
}

func requireCLIAuthorization(ctx context.Context, operation app.OperationKind) error {
	if _, err := configFromContext(ctx); err != nil {
		// Standalone command instances are used by focused unit tests; the real
		// root command always installs configuration before execution.
		return nil
	}
	_, _, err := authorizeCLI(ctx, operation)
	return err
}

func configuredSources(configuredSources []SourceConfig) []types.Source {
	sources := make([]types.Source, 0, len(configuredSources))
	for _, configured := range configuredSources {
		enabled := true
		if configured.Enabled != nil {
			enabled = *configured.Enabled
		}
		var filesystem *types.FilesystemSourceConfig
		if configured.Filesystem != nil {
			filesystem = &types.FilesystemSourceConfig{
				Root: configured.Filesystem.Root, Include: configured.Filesystem.Include,
				Flavor: configured.Filesystem.Flavor, URIBase: configured.Filesystem.URIBase,
			}
		}
		var git *types.GitSourceConfig
		if configured.Git != nil {
			git = &types.GitSourceConfig{
				Remote: configured.Git.Remote, Ref: configured.Git.Ref,
				RefKind: configured.Git.RefKind, Include: configured.Git.Include,
				Flavor: configured.Git.Flavor, URIBase: configured.Git.URIBase,
				Auth: types.GitAuthConfig{
					SecretEnv: configured.Git.Auth.SecretEnv,
					KeyFile:   configured.Git.Auth.KeyFile,
				},
				AllowRewrite:     configured.Git.AllowRewrite,
				RebindAck:        configured.Git.RebindAck,
				KnownHosts:       configured.Git.KnownHosts,
				RepositoryID:     configured.Git.RepositoryID,
				MaxTransferBytes: configured.Git.MaxTransferBytes,
				MaxCacheBytes:    configured.Git.MaxCacheBytes,
			}
		}
		maxDocuments := 0
		if configured.Sync.MaxDocuments != nil {
			maxDocuments = *configured.Sync.MaxDocuments
		}
		sources = append(sources, types.Source{
			ID: configured.ID, Type: configured.Type, Enabled: enabled,
			Config: types.SourceConfig{Filesystem: filesystem, Git: git},
			Sync: types.SourceSyncPolicy{
				OnStart: configured.Sync.OnStart, Interval: configured.Sync.Interval,
				RetryInitial: configured.Sync.RetryInitial, RetryMaximum: configured.Sync.RetryMaximum,
				MaxDocuments: maxDocuments,
			},
		})
	}
	return sources
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

func selectedHostOptions(config knowl.Config, runtimeFactory *agentfactory.Factory, providerID string) knowl.Options {
	options := knowl.Options{Config: config, ProviderID: providerID}
	if runtimeFactory != nil {
		options.RuntimeFactory = runtimeFactory
	}
	return options
}
