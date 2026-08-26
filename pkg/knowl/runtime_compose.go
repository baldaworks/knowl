package knowl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"

	httpserver "github.com/baldaworks/knowl/internal/httpapi/server"
	"github.com/baldaworks/knowl/internal/mcphttp"
	sourcefilesystem "github.com/baldaworks/knowl/internal/source/filesystem"
	"github.com/baldaworks/knowl/internal/source/reconcile"
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
	sourceState      app.SourceStateStore
	sourceSync       *reconcile.Service
	sources          []domain.Source
	sourceObserver   SourceObserver
	scheduler        *operationScheduler
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
	adapters, err := composeSourceAdapters(options.SourceAdapters)
	if err != nil {
		if maintainerCloser != nil {
			_ = maintainerCloser.Close()
		}
		return nil, err
	}
	runtime, err := composeRuntime(ctx, config, maintainer, maintainerCloser, adapters, options.SourceObserver)
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

func composeRuntime(ctx context.Context, config Config, maintainer app.Maintainer, maintainerCloser io.Closer, adapters map[domain.SourceType]app.SourceAdapter, observer SourceObserver) (_ composedRuntime, err error) {
	runtime := composedRuntime{
		config:           config,
		maintainerCloser: maintainerCloser,
		sourceObserver:   observer,
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
	runtime.sourceState = store.sources
	runtime.closer = store.closer
	runtime.service, runtime.query, runtime.lint, err = composeServices(ctx, config, runtime.workspace, runtime.operations, runtime.index, store.checker, maintainer)
	if err != nil {
		return composedRuntime{}, err
	}
	runtime.sources = cloneSources(config.Sources)
	runtime.scheduler, err = newOperationScheduler(runtime.operations, runtime.service, config.Scope, schedulerOptions{wakeSize: config.WorkerQueueSize})
	if err != nil {
		return composedRuntime{}, fmt.Errorf("compose operation scheduler: %w", err)
	}
	runtime.sourceSync, err = reconcile.NewService(reconcile.Dependencies{
		Adapters: adapters, State: runtime.sourceState, Content: runtime.workspace,
		SourceContent: runtime.workspace, Search: runtime.index,
		Maintenance: sourceMaintenanceQueue{service: runtime.service, waker: runtime.scheduler},
	}, reconcile.Options{})
	if err != nil {
		return composedRuntime{}, fmt.Errorf("compose source reconciliation: %w", err)
	}
	if _, err = runtime.sourceSync.Recover(ctx, config.Scope, runtime.sources); err != nil && !errors.Is(err, reconcile.ErrSyncPartial) {
		return composedRuntime{}, fmt.Errorf("recover Knowl sources: %w", err)
	}
	runtime.mcp, err = mcp.NewServer(runtime.query, runtime.service, runtime.scheduler, config.Scope, config.ReadLimits)
	if err != nil {
		return composedRuntime{}, fmt.Errorf("compose MCP service: %w", err)
	}
	runtime.handler = httpserver.NewHandler(httpserver.Dependencies{
		Scope:  config.Scope,
		Ingest: runtime.service,
		Query:  runtime.query,
		Waker:  runtime.scheduler,
		Ready:  func() bool { return false },
	})
	return runtime, nil
}

type sourceMaintenanceQueue struct {
	service app.SourceMaintenanceQueue
	waker   interface{ Wake(id domain.OperationID) }
}

func (queue sourceMaintenanceQueue) ReserveAccepted(ctx context.Context, request app.AcceptedMaintenanceRequest) (app.MaintenanceReservation, error) {
	reservation, err := queue.service.ReserveAccepted(ctx, request)
	if err != nil {
		return app.MaintenanceReservation{}, err
	}
	queue.waker.Wake(reservation.OperationID)
	return reservation, nil
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
	sourceJobs, err := newSourceScheduler(runtime.sourceSync.SyncSource, runtime.config.Scope, runtime.sources, runtime.sourceObserver, sourceSchedulerOptions{})
	if err != nil {
		return nil, fmt.Errorf("compose source scheduler: %w", err)
	}
	host := &Host{
		config:           runtime.config,
		workspace:        runtime.workspace,
		closer:           runtime.closer,
		maintainerCloser: runtime.maintainerCloser,
		operations:       runtime.operations,
		index:            runtime.index,
		sourceState:      runtime.sourceState,
		sourceSync:       runtime.sourceSync,
		sources:          cloneSources(runtime.sources),
		sourceByID:       sourceIndex(runtime.sources),
		sourceJobs:       sourceJobs,
		scheduler:        runtime.scheduler,
		service:          runtime.service,
		query:            runtime.query,
		lint:             runtime.lint,
		mcp:              runtime.mcp,
		serverErr:        make(chan error, 1),
	}
	httpHandler := httpserver.NewHandler(httpserver.Dependencies{
		Scope:  runtime.config.Scope,
		Ingest: runtime.service,
		Query:  runtime.query,
		Waker:  runtime.scheduler,
		Ready:  host.Ready,
	})
	mcpHandler, err := mcphttp.NewHandler(runtime.mcp, host.Ready)
	if err != nil {
		return nil, fmt.Errorf("compose Knowl MCP HTTP handler: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/", httpHandler)
	host.handler = httpserver.WithOperatorAuth(mux, runtime.config.OperatorToken)
	return host, nil
}

func composeSourceAdapters(overrides map[domain.SourceType]app.SourceAdapter) (map[domain.SourceType]app.SourceAdapter, error) {
	adapters := map[domain.SourceType]app.SourceAdapter{domain.SourceTypeFilesystem: sourcefilesystem.NewDefault()}
	types := make([]domain.SourceType, 0, len(overrides))
	for sourceType, adapter := range overrides {
		if strings.TrimSpace(string(sourceType)) == "" || nilSourceAdapter(adapter) {
			return nil, fmt.Errorf("source adapter registration is invalid: %w", app.ErrSourceInvalid)
		}
		types = append(types, sourceType)
	}
	sort.Slice(types, func(left, right int) bool { return types[left] < types[right] })
	for _, sourceType := range types {
		adapters[sourceType] = overrides[sourceType]
	}
	return adapters, nil
}

func nilSourceAdapter(adapter app.SourceAdapter) bool {
	if adapter == nil {
		return true
	}
	value := reflect.ValueOf(adapter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (options Options) maintainer(config Config) (app.Maintainer, io.Closer, error) {
	if !nilMaintainer(options.Maintainer) {
		closer, _ := options.Maintainer.(io.Closer)
		return options.Maintainer, closer, nil
	}
	providerID := strings.TrimSpace(options.ProviderID)
	if options.RuntimeFactory == nil && providerID == "" {
		return nil, nil, fmt.Errorf("knowl.provider is required when no maintainer is provided")
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

func nilMaintainer(maintainer app.Maintainer) bool {
	if maintainer == nil {
		return true
	}
	value := reflect.ValueOf(maintainer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
