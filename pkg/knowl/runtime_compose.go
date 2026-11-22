package knowl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	httpserver "github.com/baldaworks/knowl/internal/httpapi/server"
	"github.com/baldaworks/knowl/internal/mcphttp"
	sourcefilesystem "github.com/baldaworks/knowl/internal/source/filesystem"
	sourcegit "github.com/baldaworks/knowl/internal/source/git"
	"github.com/baldaworks/knowl/internal/source/reconcile"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	runtimeprovider "github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type composedRuntime struct {
	config              Config
	workspace           *contentfs.Workspace
	workspaces          *ScopeWorkspaceResolver
	content             *ScopedContentStore
	closer              io.Closer
	maintainerCloser    io.Closer
	slots               *executionSlots
	operations          app.OperationStore
	index               app.SearchIndex
	sourceState         app.SourceStateStore
	sourceLifecycle     app.SourceLifecycleStore
	retirementCommitter app.RetirementCommitter
	sourceSync          *reconcile.Service
	sources             []domain.Source
	sourceObserver      SourceObserver
	scheduler           *operationScheduler
	service             *app.IngestService
	hierarchy           *app.HierarchyService
	retirement          *app.RetirementPageService
	retirementControl   *app.SourceRetirementCoordinator
	retirementControls  map[domain.ScopeRef]*app.SourceRetirementCoordinator
	query               *app.QueryService
	lint                *app.LintService
	mcp                 *mcp.Server
	handler             http.Handler
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
	slots, err := options.executionSlots(config)
	if err != nil {
		return nil, err
	}
	adapters, err := composeSourceAdapters(config.Workspace, options.SourceAdapters)
	if err != nil {
		_ = slots.Close()
		return nil, err
	}
	runtime, err := composeRuntime(ctx, config, slots, adapters, options.SourceObserver)
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

func composeRuntime(ctx context.Context, config Config, slots *executionSlots, adapters map[domain.SourceType]app.SourceAdapter, observer SourceObserver) (_ composedRuntime, err error) {
	config.IngestOptions.ApplyCoordinator = newHostApplyCoordinator()
	runtime := composedRuntime{
		config:           config,
		maintainerCloser: slots,
		slots:            slots,
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

	runtime.workspaces, err = OpenScopeWorkspaces(config)
	if err != nil {
		return composedRuntime{}, err
	}
	runtime.content, err = NewScopedContentStore(runtime.workspaces)
	if err != nil {
		return composedRuntime{}, err
	}
	defaultWorkspace, err := runtime.workspaces.Resolve(config.Scope)
	if err != nil {
		return composedRuntime{}, err
	}
	runtime.workspace = defaultWorkspace.Workspace
	store, err := openStore(ctx, config)
	if err != nil {
		return composedRuntime{}, err
	}
	runtime.operations = store.operations
	runtime.index = store.index
	runtime.sourceState = store.sources
	runtime.sourceLifecycle = store.lifecycle
	maintenance, err := newScopedMaintenanceContent(runtime.workspaces, runtime.sourceLifecycle, config.IngestOptions.ApplyCoordinator)
	if err != nil {
		return composedRuntime{}, err
	}
	retirementCommitter := &scopedRetirementCommitter{byScope: make(map[domain.ScopeRef]*hostRetirementCommitter, len(runtime.workspaces.ordered))}
	for _, resolved := range runtime.workspaces.ordered {
		bound := maintenance.byScope[resolved.Ref]
		committer, commitErr := newHostRetirementCommitter(bound.coordinator, runtime.sourceLifecycle, resolved.Workspace, bound)
		if commitErr != nil {
			return composedRuntime{}, fmt.Errorf("compose scope %q retirement committer: %w", resolved.Name, commitErr)
		}
		retirementCommitter.byScope[resolved.Ref] = committer
	}
	runtime.retirementCommitter = retirementCommitter
	runtime.closer = store.closer
	for _, scope := range config.scopeRegistry.All() {
		if scope.Disabled {
			continue
		}
		if _, resolveErr := runtime.workspaces.Resolve(scope.Ref); errors.Is(resolveErr, ErrScopeUnavailable) {
			continue
		} else if resolveErr != nil {
			return composedRuntime{}, resolveErr
		}
		if err = reconcileConfiguredSourceLifecycles(ctx, runtime.sourceLifecycle, scope.Ref, scope.Sources, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
			return composedRuntime{}, fmt.Errorf("prepare scope %q source lifecycles: %w", scope.Name, err)
		}
	}
	maintainer := slots.all[0].maintainer
	runtime.service, runtime.query, runtime.lint, err = composeServices(ctx, config, maintenance, runtime.operations, runtime.index, store.checker, maintainer, slots)
	if err != nil {
		return composedRuntime{}, err
	}
	runtime.sources = cloneSources(config.Sources)
	runtime.retirement, err = app.NewRetirementPageService(maintenance, runtime.operations, runtime.index, runtime.sourceLifecycle, runtime.retirementCommitter, maintainer, hostRetirementPageOptions(config))
	if err != nil {
		return composedRuntime{}, fmt.Errorf("compose retirement page service: %w", err)
	}
	runner := terminalRunner(terminalRouter{source: runtime.service, retirement: runtime.retirement})
	if hierarchyMaintainer, ok := maintainer.(app.HierarchyMaintainer); ok {
		runtime.hierarchy, err = app.NewHierarchyService(maintenance, runtime.operations, runtime.index, hierarchyMaintainer, hostHierarchyOptions(config))
		if err != nil {
			return composedRuntime{}, fmt.Errorf("compose hierarchy service: %w", err)
		}
		runner = terminalRouter{source: runtime.service, hierarchy: runtime.hierarchy, retirement: runtime.retirement}
	}
	slots.all[0].source = runtime.service
	slots.all[0].hierarchy = runtime.hierarchy
	slots.all[0].retirement = runtime.retirement
	for _, slot := range slots.all[1:] {
		slot.source, err = app.NewIngestService(maintenance, runtime.operations, runtime.index, slot.maintainer, hostIngestOptions(config))
		if err != nil {
			return composedRuntime{}, err
		}
		if maintainer, ok := slot.maintainer.(app.HierarchyMaintainer); ok {
			slot.hierarchy, err = app.NewHierarchyService(maintenance, runtime.operations, runtime.index, maintainer, hostHierarchyOptions(config))
			if err != nil {
				return composedRuntime{}, err
			}
		}
		slot.retirement, err = app.NewRetirementPageService(maintenance, runtime.operations, runtime.index, runtime.sourceLifecycle, runtime.retirementCommitter, slot.maintainer, hostRetirementPageOptions(config))
		if err != nil {
			return composedRuntime{}, fmt.Errorf("compose retirement page service: %w", err)
		}
	}
	retirementOperations, ok := runtime.operations.(app.RetirementOperationStore)
	if !ok {
		return composedRuntime{}, fmt.Errorf("compose retirement operation store: %w", app.ErrSourceLifecycleInvalid)
	}
	var scheduler *operationScheduler
	runtime.retirementControls = make(map[domain.ScopeRef]*app.SourceRetirementCoordinator, len(config.scopeRegistry.ordered))
	for _, scope := range config.scopeRegistry.All() {
		if scope.Disabled {
			continue
		}
		if _, resolveErr := runtime.workspaces.Resolve(scope.Ref); errors.Is(resolveErr, ErrScopeUnavailable) {
			continue
		} else if resolveErr != nil {
			return composedRuntime{}, resolveErr
		}
		control, controlErr := app.NewSourceRetirementCoordinator(app.SourceRetirementCoordinatorDependencies{
			Scope: scope.Ref, Lifecycles: runtime.sourceLifecycle, Provenance: runtime.content,
			Pages: runtime.retirement, Operations: retirementOperations, Projection: runtime.content,
			ApplyCoordinator: config.IngestOptions.ApplyCoordinator,
			Wake: func(id domain.OperationID) {
				if scheduler != nil {
					scheduler.Wake(id)
				}
			},
		})
		if controlErr != nil {
			return composedRuntime{}, fmt.Errorf("compose scope %q source retirement coordinator: %w", scope.Name, controlErr)
		}
		runtime.retirementControls[scope.Ref] = control
	}
	runtime.retirementControl = runtime.retirementControls[config.Scope]
	schedulerScopes := make([]domain.ScopeRef, 0, len(runtime.workspaces.ordered))
	for _, resolved := range runtime.workspaces.ordered {
		schedulerScopes = append(schedulerScopes, resolved.Ref)
	}
	scheduler, err = newMultiScopeOperationScheduler(runtime.operations, runner, schedulerScopes, schedulerOptions{
		wakeSize: config.WorkerQueueSize, slots: slots,
		discover: func(ctx context.Context) error {
			for _, scope := range config.scopeRegistry.All() {
				control := runtime.retirementControls[scope.Ref]
				if control == nil {
					continue
				}
				if _, discoverErr := control.Advance(ctx); discoverErr != nil {
					return discoverErr
				}
			}
			return nil
		},
	})
	runtime.scheduler = scheduler
	if err != nil {
		return composedRuntime{}, fmt.Errorf("compose operation scheduler: %w", err)
	}
	runtime.sourceSync, err = reconcile.NewService(reconcile.Dependencies{
		ApplyCoordinator: config.IngestOptions.ApplyCoordinator,
		Adapters:         adapters, State: runtime.sourceState, Content: runtime.content,
		SourceContent: runtime.content, Search: runtime.index,
		Maintenance: sourceMaintenanceQueue{service: runtime.service, waker: runtime.scheduler},
		Lifecycle:   runtime.sourceLifecycle,
	}, reconcile.Options{})
	if err != nil {
		return composedRuntime{}, fmt.Errorf("compose source reconciliation: %w", err)
	}
	for _, scope := range config.scopeRegistry.All() {
		if scope.Disabled {
			continue
		}
		if _, resolveErr := runtime.workspaces.Resolve(scope.Ref); errors.Is(resolveErr, ErrScopeUnavailable) {
			continue
		} else if resolveErr != nil {
			return composedRuntime{}, resolveErr
		}
		if _, err = runtime.sourceSync.Recover(ctx, scope.Ref, scope.Sources); err != nil && !errors.Is(err, reconcile.ErrSyncPartial) {
			return composedRuntime{}, fmt.Errorf("recover scope %q sources: %w", scope.Name, err)
		}
	}
	resolveScope := func(name domain.ScopeName) (domain.ScopeRef, error) {
		selected, resolveErr := config.scopeRegistry.Resolve(name)
		return selected.Ref, resolveErr
	}
	publicDefault := config.scopeRegistry.Default()
	runtime.mcp, err = mcp.NewMultiScopeServer(runtime.query, runtime.service, runtime.scheduler, publicDefault.Ref, resolveScope, config.ScopeAuthorizer, config.ReadLimits)
	if err != nil {
		return composedRuntime{}, fmt.Errorf("compose MCP service: %w", err)
	}
	runtime.handler = httpserver.NewHandler(httpserver.Dependencies{
		Scope:        publicDefault.Ref,
		PublicScope:  config.scopeRegistry.DefaultName(),
		ResolveScope: resolveScope,
		Authorizer:   config.ScopeAuthorizer,
		Ingest:       runtime.service,
		Query:        runtime.query,
		Waker:        runtime.scheduler,
		Ready:        func() bool { return false },
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

func composeServices(
	ctx context.Context,
	config Config,
	workspace app.ContentStore,
	operations app.OperationStore,
	index app.SearchIndex,
	checker projectionChecker,
	maintainer app.Maintainer,
	slots *executionSlots,
) (*app.IngestService, *app.QueryService, *app.LintService, error) {
	ingestOptions := hostIngestOptions(config)
	service, err := app.NewIngestService(workspace, operations, index, maintainer, ingestOptions)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("compose ingest service: %w", err)
	}
	if _, err := service.Recover(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("recover Knowl workspace: %w", err)
	}
	for _, scope := range config.scopeRegistry.All() {
		if scope.Disabled {
			continue
		}
		if resolver, ok := workspace.(*scopedMaintenanceContent); ok {
			if _, resolveErr := resolver.scope(scope.Ref); errors.Is(resolveErr, domain.ErrScopeUnknown) {
				continue
			} else if resolveErr != nil {
				return nil, nil, nil, resolveErr
			}
		}
		snapshot, snapshotErr := workspace.Snapshot(ctx, scope.Ref)
		if snapshotErr != nil {
			return nil, nil, nil, fmt.Errorf("snapshot scope %q workspace: %w", scope.Name, snapshotErr)
		}
		if projectionErr := ensureProjection(ctx, index, checker, snapshot); projectionErr != nil {
			return nil, nil, nil, fmt.Errorf("prepare scope %q projection: %w", scope.Name, projectionErr)
		}
	}
	query, err := app.NewQueryService(workspace, operations, index, service, app.QueryOptions{ReadLimits: config.ReadLimits})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("compose query service: %w", err)
	}
	var lintMaintainer app.Maintainer
	if maintainer != nil {
		lintMaintainer = slotLintMaintainer{slots: slots}
	}
	lint, err := app.NewLintService(workspace, index, app.LintOptions{ReadLimits: config.ReadLimits, Maintainer: lintMaintainer})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("compose lint service: %w", err)
	}
	return service, query, lint, nil
}

func newHost(runtime composedRuntime) (*Host, error) {
	var scheduled []scopedSource
	for _, scope := range runtime.config.scopeRegistry.All() {
		if _, resolveErr := runtime.workspaces.Resolve(scope.Ref); resolveErr != nil {
			continue
		}
		for _, source := range scope.Sources {
			scheduled = append(scheduled, scopedSource{Scope: scope.Ref, Source: source})
		}
	}
	var sourceJobs *sourceScheduler
	var err error
	if len(scheduled) == 0 {
		sourceJobs, err = newSourceScheduler(runtime.sourceSync.SyncSource, runtime.config.Scope, nil, runtime.sourceObserver, sourceSchedulerOptions{})
	} else {
		sourceJobs, err = newMultiScopeSourceScheduler(runtime.sourceSync.SyncSource, scheduled, runtime.sourceObserver, sourceSchedulerOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("compose source scheduler: %w", err)
	}
	host := &Host{
		config:              runtime.config,
		workspace:           runtime.workspace,
		workspaces:          runtime.workspaces,
		content:             runtime.content,
		closer:              runtime.closer,
		maintainerCloser:    runtime.maintainerCloser,
		slots:               runtime.slots,
		operations:          runtime.operations,
		index:               runtime.index,
		sourceState:         runtime.sourceState,
		sourceLifecycle:     runtime.sourceLifecycle,
		retirementCommitter: runtime.retirementCommitter,
		retirementControl:   runtime.retirementControl,
		retirementControls:  runtime.retirementControls,
		sourceSync:          runtime.sourceSync,
		sources:             cloneSources(runtime.sources),
		sourceByID:          sourceIndex(runtime.sources),
		sourceJobs:          sourceJobs,
		startGate:           make(chan struct{}, 1),
		stopGate:            make(chan struct{}, 1),
		scheduler:           runtime.scheduler,
		service:             runtime.service,
		hierarchy:           runtime.hierarchy,
		retirement:          runtime.retirement,
		query:               runtime.query,
		lint:                runtime.lint,
		mcp:                 runtime.mcp,
		serverErr:           make(chan error, 1),
	}
	readers := app.OperatorReaders{Sources: newOperatorSourceReader(host)}
	if runtime.retirementControl != nil {
		readers.Retirements = operatorRetirementReader{host: host}
	}
	if runtime.content != nil {
		readers.Catalogs = runtime.content
		readers.Directories = runtime.content
		readers.Pages = runtime.content
		readers.Page = runtime.content
		readers.Revisions = runtime.content
	}
	readers.Operations, _ = runtime.operations.(app.OperationLister)
	readers.Documents, _ = runtime.sourceState.(app.SourceDocumentLister)
	statistics, _ := runtime.operations.(app.StatisticsReader)
	readers.Statistics = &operatorStatisticsReader{host: host, reader: statistics}
	operators := make(map[domain.ScopeRef]*app.OperatorService, len(runtime.config.scopeRegistry.ordered))
	for _, scope := range runtime.config.scopeRegistry.All() {
		if _, resolveErr := runtime.workspaces.Resolve(scope.Ref); resolveErr != nil {
			continue
		}
		operator, operatorErr := app.NewOperatorService(scope.Ref, readers, app.OperatorOptions{ReadLimits: runtime.config.ReadLimits})
		if operatorErr != nil {
			return nil, fmt.Errorf("compose scope %q operator reads: %w", scope.Name, operatorErr)
		}
		operators[scope.Ref] = operator
	}
	host.operator = operators[runtime.config.Scope]
	httpHandler := httpserver.NewHandler(httpserver.Dependencies{
		Scope:       runtime.config.scopeRegistry.Default().Ref,
		PublicScope: runtime.config.scopeRegistry.DefaultName(),
		ResolveScope: func(name domain.ScopeName) (domain.ScopeRef, error) {
			selected, resolveErr := runtime.config.scopeRegistry.Resolve(name)
			return selected.Ref, resolveErr
		},
		Authorizer: runtime.config.ScopeAuthorizer,
		Ingest:     runtime.service,
		Query:      runtime.query,
		Waker:      runtime.scheduler,
		Ready:      host.Ready,
		ScopesReadiness: func() []httpserver.ScopeReadiness {
			states := host.ScopesReadiness()
			result := make([]httpserver.ScopeReadiness, len(states))
			for index, state := range states {
				result[index] = httpserver.ScopeReadiness{Name: state.Name, Ready: state.Ready, Reason: state.Reason}
			}
			return result
		},
	})
	mcpHandler, err := mcphttp.NewHandlerWithOptions(runtime.mcp, host.Ready, mcphttp.HandlerOptions{
		ReadOnly: runtime.config.MCP.AccessMode == AccessModeReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("compose Knowl MCP HTTP handler: %w", err)
	}
	mux := http.NewServeMux()
	mcpPolicy := httpserver.FacadePolicy{RequireToken: runtime.config.MCP.AuthMode == AuthModeToken, ReadOnly: runtime.config.MCP.AccessMode == AccessModeReadOnly, ProtocolHTTP: true}
	mcpFallbackPolicy := mcpPolicy
	mcpFallbackPolicy.ReadOnly = true
	mcpFallbackPolicy.ProtocolHTTP = false
	apiPolicy := httpserver.FacadePolicy{RequireToken: runtime.config.API.AuthMode == AuthModeToken, ReadOnly: runtime.config.API.AccessMode == AccessModeReadOnly}
	mux.Handle("/mcp", httpserver.WithFacadePolicy(mcpHandler, runtime.config.OperatorToken, mcpPolicy))
	mux.Handle("/mcp/", httpserver.WithFacadePolicy(httpHandler, runtime.config.OperatorToken, mcpFallbackPolicy))
	mux.Handle("/v1", httpserver.WithFacadePolicy(httpHandler, runtime.config.OperatorToken, apiPolicy))
	mux.Handle("/v1/", httpserver.WithFacadePolicy(httpHandler, runtime.config.OperatorToken, apiPolicy))
	mux.Handle("/", httpHandler)
	if runtime.config.Web.Enabled {
		webToken := ""
		if runtime.config.Web.AuthMode != AuthModePublic {
			webToken = runtime.config.OperatorToken
		}
		ui, uiErr := httpserver.NewWebHandler(host.operator, httpserver.Dependencies{EmbeddingsEnabled: runtime.config.Embeddings.Enabled, Scope: runtime.config.scopeRegistry.Default().Ref, PublicScope: runtime.config.scopeRegistry.DefaultName(),
			ResolveScope: func(name domain.ScopeName) (domain.ScopeRef, error) {
				selected, resolveErr := runtime.config.scopeRegistry.Resolve(name)
				return selected.Ref, resolveErr
			}, Authorizer: runtime.config.ScopeAuthorizer, OperatorFor: func(ref domain.ScopeRef) *app.OperatorService { return operators[ref] }, Query: runtime.query, Ready: host.Ready}, httpserver.WebOptions{
			AuthMode: string(runtime.config.Web.AuthMode),
			Token:    webToken,
		})
		if uiErr != nil {
			return nil, fmt.Errorf("compose web UI: %w", uiErr)
		}
		mux.Handle("/ui/", ui)
		mux.Handle("/operator/v1/", httpserver.WithFacadePolicy(httpserver.NewOperatorHandler(host.operator, host.Ready), runtime.config.OperatorToken, httpserver.FacadePolicy{RequireToken: runtime.config.Web.AuthMode != AuthModePublic, ReadOnly: true}))
	}
	host.handler = mux
	return host, nil
}

func composeSourceAdapters(workspace string, overrides map[domain.SourceType]app.SourceAdapter) (map[domain.SourceType]app.SourceAdapter, error) {
	adapters := map[domain.SourceType]app.SourceAdapter{
		domain.SourceTypeFilesystem: sourcefilesystem.NewDefault(),
		domain.SourceTypeGit:        sourcegit.NewDefaultAdapter(sourcegit.NewCacheManager(filepath.Join(workspace, ".knowl", "cache", "git"), nil)),
	}
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
		if config.Workers != 1 {
			return nil, nil, ErrWorkerConfigInvalid
		}
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

func hostIngestOptions(config Config) app.IngestOptions {
	options := config.IngestOptions
	options.Output = config.Output
	if options.ReadLimits == (domain.ReadLimits{}) {
		options.ReadLimits = config.ReadLimits
	}
	return options
}

func hostHierarchyOptions(config Config) app.HierarchyOptions {
	return app.HierarchyOptions{Output: config.Output, ApplyCoordinator: config.IngestOptions.ApplyCoordinator}
}

func hostRetirementPageOptions(config Config) app.RetirementPageOptions {
	ingest := hostIngestOptions(config)
	return app.RetirementPageOptions{
		Planner: app.RetirementPlannerOptions{
			InputLimits: ingest.InputLimits, CatalogLimits: ingest.CatalogLimits,
			PlanLimits: ingest.PlanLimits, ReadLimits: ingest.ReadLimits,
		},
		LeaseDuration: ingest.LeaseDuration,
	}
}
