package knowl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	httpserver "github.com/baldaworks/knowl/internal/httpapi/server"
	"github.com/baldaworks/knowl/internal/mcphttp"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	runtimeprovider "github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type composedRuntime struct {
	config           Config
	workspace        *contentfs.Workspace
	closer           io.Closer
	maintainerCloser io.Closer
	operations       app.OperationStore
	index            app.SearchIndex
	worker           *worker
	service          *app.IngestService
	query            *app.QueryService
	lint             *app.LintService
	mcp              *mcp.Server
	handler          http.Handler
}

// New composes and preflights a host. Recovery, migrations, and projection readiness complete before return.
func New(ctx context.Context, options Options) (*Host, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := options.Config.normalized()
	if err != nil {
		return nil, err
	}
	maintainer, maintainerCloser, err := options.maintainer(config)
	if err != nil {
		return nil, err
	}
	runtime, err := composeRuntime(ctx, config, maintainer, maintainerCloser)
	if err != nil {
		return nil, err
	}
	host, err := newHost(runtime)
	if err != nil {
		if runtime.maintainerCloser != nil {
			_ = runtime.maintainerCloser.Close()
		}
		if runtime.closer != nil {
			_ = runtime.closer.Close()
		}
		return nil, err
	}
	return host, nil
}

func composeRuntime(ctx context.Context, config Config, maintainer app.Maintainer, maintainerCloser io.Closer) (_ composedRuntime, err error) {
	runtime := composedRuntime{
		config:           config,
		maintainerCloser: maintainerCloser,
	}
	defer func() {
		if err == nil {
			return
		}
		if runtime.maintainerCloser != nil {
			_ = runtime.maintainerCloser.Close()
		}
		if runtime.closer != nil {
			_ = runtime.closer.Close()
		}
	}()

	runtime.workspace, err = openWorkspace(config.Workspace)
	if err != nil {
		return composedRuntime{}, err
	}
	store, err := openStore(ctx, config)
	if err != nil {
		return composedRuntime{}, err
	}
	runtime.operations = store.operations
	runtime.index = store.index
	runtime.closer = store.closer
	runtime.worker = newWorker(config.WorkerQueueSize)
	runtime.service, runtime.query, runtime.lint, err = composeServices(ctx, config, runtime.workspace, runtime.operations, runtime.index, store.checker, maintainer)
	if err != nil {
		return composedRuntime{}, err
	}
	runtime.mcp, err = mcp.NewServer(runtime.query, runtime.service, workerSubmitter{worker: runtime.worker}, config.Scope, config.ReadLimits)
	if err != nil {
		return composedRuntime{}, fmt.Errorf("compose MCP service: %w", err)
	}
	runtime.handler = httpserver.NewHandler(httpserver.Dependencies{
		Scope:     config.Scope,
		Ingest:    runtime.service,
		Query:     runtime.query,
		Submitter: workerSubmitter{worker: runtime.worker},
		Ready:     func() bool { return false },
	})
	return runtime, nil
}

func openWorkspace(path string) (*contentfs.Workspace, error) {
	workspace, err := contentfs.New(path)
	if err != nil {
		return nil, err
	}
	if err := workspace.Validate(); err != nil {
		return nil, fmt.Errorf("validate Knowl workspace: %w", err)
	}
	return workspace, nil
}

func composeServices(
	ctx context.Context,
	config Config,
	workspace *contentfs.Workspace,
	operations app.OperationStore,
	index app.SearchIndex,
	checker projectionChecker,
	maintainer app.Maintainer,
) (*app.IngestService, *app.QueryService, *app.LintService, error) {
	ingestOptions := config.IngestOptions
	if ingestOptions.ReadLimits == (domain.ReadLimits{}) {
		ingestOptions.ReadLimits = config.ReadLimits
	}
	service, err := app.NewIngestService(workspace, operations, index, maintainer, ingestOptions)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("compose ingest service: %w", err)
	}
	if _, err := service.Recover(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("recover Knowl workspace: %w", err)
	}
	snapshot, err := workspace.Snapshot(ctx, config.Scope)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("snapshot Knowl workspace: %w", err)
	}
	if err := ensureProjection(ctx, index, checker, snapshot); err != nil {
		return nil, nil, nil, fmt.Errorf("prepare Knowl projection: %w", err)
	}
	query, err := app.NewQueryService(workspace, operations, index, service, app.QueryOptions{ReadLimits: config.ReadLimits})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("compose query service: %w", err)
	}
	lint, err := app.NewLintService(workspace, index, app.LintOptions{ReadLimits: config.ReadLimits, Maintainer: maintainer})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("compose lint service: %w", err)
	}
	return service, query, lint, nil
}

func newHost(runtime composedRuntime) (*Host, error) {
	host := &Host{
		config:           runtime.config,
		workspace:        runtime.workspace,
		closer:           runtime.closer,
		maintainerCloser: runtime.maintainerCloser,
		operations:       runtime.operations,
		index:            runtime.index,
		worker:           runtime.worker,
		service:          runtime.service,
		query:            runtime.query,
		lint:             runtime.lint,
		mcp:              runtime.mcp,
		serverErr:        make(chan error, 1),
	}
	httpHandler := httpserver.NewHandler(httpserver.Dependencies{
		Scope:     runtime.config.Scope,
		Ingest:    runtime.service,
		Query:     runtime.query,
		Submitter: workerSubmitter{worker: runtime.worker},
		Ready:     host.Ready,
	})
	mcpHandler, err := mcphttp.NewHandler(runtime.mcp, host.Ready)
	if err != nil {
		return nil, fmt.Errorf("compose Knowl MCP HTTP handler: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/", httpHandler)
	host.handler = mux
	return host, nil
}

func (options Options) maintainer(config Config) (app.Maintainer, io.Closer, error) {
	if options.Maintainer != nil {
		closer, _ := options.Maintainer.(io.Closer)
		return options.Maintainer, closer, nil
	}
	providerID := strings.TrimSpace(options.ProviderID)
	if options.RuntimeFactory == nil && providerID == "" {
		return nil, nil, fmt.Errorf("knowl maintainer is required")
	}
	if providerID == "" {
		return nil, nil, fmt.Errorf("knowl.provider is required")
	}
	if options.RuntimeFactory == nil {
		return nil, nil, fmt.Errorf("runtime provider factory is required")
	}
	if validator, ok := options.RuntimeFactory.(runtimeprovider.RuntimeFactoryValidator); ok {
		if err := validator.ValidateAgent(providerID); err != nil {
			return nil, nil, fmt.Errorf("validate knowl.provider: %w", err)
		}
	}
	maintainer, err := runtimeprovider.NewRuntimeMaintainer(options.RuntimeFactory, providerID, config.Workspace)
	if err != nil {
		return nil, nil, err
	}
	return maintainer, maintainer, nil
}
