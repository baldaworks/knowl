package knowl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	domain "github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	"github.com/baldaworks/knowl/pkg/knowl/store/postgres"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	"go.uber.org/fx"
)

var ErrMaintainerUnavailable = errors.New("knowl maintainer is unavailable")

// Options supplies the host configuration and an independent maintainer boundary.
type Options struct {
	Config     Config
	Maintainer app.Maintainer
}

// Host composes canonical content, operational state, application services, HTTP, and MCP.
type Host struct {
	config    Config
	workspace *contentfs.Workspace
	closer    io.Closer

	operations app.OperationStore
	index      app.SearchIndex
	worker     *Worker
	service    *app.IngestService
	query      *app.QueryService
	lint       *app.LintService
	mcp        *mcp.Server
	handler    http.Handler

	ready     atomic.Bool
	mu        sync.Mutex
	server    *http.Server
	listener  net.Listener
	cancel    context.CancelFunc
	started   bool
	closed    bool
	serverErr chan error
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
	workspace, err := contentfs.New(config.Workspace)
	if err != nil {
		return nil, err
	}
	if err := workspace.Validate(); err != nil {
		return nil, fmt.Errorf("validate Knowl workspace: %w", err)
	}
	operations, index, closer, checker, err := openOperationalStore(ctx, config)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = closer.Close()
		}
	}()
	maintainer := options.Maintainer
	if maintainer == nil {
		maintainer = unavailableMaintainer{}
	}
	ingestOptions := config.IngestOptions
	if ingestOptions.ReadLimits == (domain.ReadLimits{}) {
		ingestOptions.ReadLimits = config.ReadLimits
	}
	service, err := app.NewIngestService(workspace, operations, index, maintainer, ingestOptions)
	if err != nil {
		return nil, fmt.Errorf("compose ingest service: %w", err)
	}
	if _, err := service.Recover(ctx); err != nil {
		return nil, fmt.Errorf("recover Knowl workspace: %w", err)
	}
	snapshot, err := workspace.Snapshot(ctx, config.Scope)
	if err != nil {
		return nil, fmt.Errorf("snapshot Knowl workspace: %w", err)
	}
	if err := ensureProjection(ctx, index, checker, snapshot); err != nil {
		return nil, fmt.Errorf("prepare Knowl projection: %w", err)
	}
	query, err := app.NewQueryService(workspace, operations, index, service, app.QueryOptions{ReadLimits: config.ReadLimits})
	if err != nil {
		return nil, fmt.Errorf("compose query service: %w", err)
	}
	lint, err := app.NewLintService(workspace, index, app.LintOptions{ReadLimits: config.ReadLimits, Maintainer: maintainer})
	if err != nil {
		return nil, fmt.Errorf("compose lint service: %w", err)
	}
	mcpServer, err := mcp.NewServer(query, lint, config.Scope, config.ReadLimits)
	if err != nil {
		return nil, fmt.Errorf("compose MCP service: %w", err)
	}
	host := &Host{
		config:     config,
		workspace:  workspace,
		closer:     closer,
		operations: operations,
		index:      index,
		worker:     NewWorker(config.WorkerQueueSize),
		service:    service,
		query:      query,
		lint:       lint,
		mcp:        mcpServer,
		serverErr:  make(chan error, 1),
	}
	host.handler = NewHTTPHandler(HTTPDependencies{
		Scope:         config.Scope,
		OperatorToken: config.OperatorToken,
		Ingest:        service,
		Query:         query,
		Lint:          lint,
		Worker:        host.worker,
		Ready:         host.Ready,
	})
	keep = true
	return host, nil
}

// NewHost is an explicit constructor alias for callers composing one local host.
func NewHost(ctx context.Context, config Config, maintainer app.Maintainer) (*Host, error) {
	return New(ctx, Options{Config: config, Maintainer: maintainer})
}

// NewApp builds the Fx composition root for one local Knowl host.
//
// The plain New constructor remains available for embedding and unit tests; this
// function is the service entry point and owns host start/stop through Fx.
func NewApp(ctx context.Context, options Options, additional ...fx.Option) *fx.App {
	if ctx == nil {
		ctx = context.Background()
	}
	return fx.New(
		fx.Module("knowl.host",
			fx.Provide(
				func() (*Host, error) { return New(ctx, options) },
				func(host *Host) hostLifecycle { return host },
			),
			fx.Invoke(registerHostLifecycle),
		),
		fx.Options(additional...),
	)
}

type hostLifecycle interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

type hostLifecycleParams struct {
	fx.In

	Lifecycle fx.Lifecycle
	Host      hostLifecycle
}

func registerHostLifecycle(params hostLifecycleParams) {
	// Preflight opens the operational store before Fx starts the host. The
	// no-op start hook makes that ownership visible to Fx, so a failed host
	// start still rolls back the preflight resources.
	params.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error { return nil },
		OnStop:  params.Host.Stop,
	})
	params.Lifecycle.Append(fx.Hook{
		OnStart: params.Host.Start,
	})
}

// Start binds the loopback HTTP listener and marks the host ready after preflight.
// The context is used for the start operation; Stop owns the server lifetime.
func (host *Host) Start(_ context.Context) error {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed {
		return fmt.Errorf("host is closed")
	}
	if host.started {
		return nil
	}
	listener, err := net.Listen("tcp", host.config.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen Knowl HTTP endpoint: %w", err)
	}
	serverCtx, cancel := context.WithCancel(context.Background())
	host.listener = listener
	host.server = &http.Server{Handler: host.handler, ReadHeaderTimeout: host.config.ReadLimits.Deadline}
	host.cancel = cancel
	if err := host.worker.Start(serverCtx); err != nil {
		cancel()
		_ = listener.Close()
		host.listener = nil
		host.server = nil
		host.cancel = nil
		return fmt.Errorf("start Knowl worker: %w", err)
	}
	host.started = true
	host.ready.Store(true)
	go func() {
		err := host.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			host.ready.Store(false)
			host.serverErr <- err
		}
	}()
	return nil
}

// Run starts the host and blocks until ctx is canceled or the HTTP server fails.
func (host *Host) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := host.Start(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), host.config.ShutdownTimeout)
		defer cancel()
		return host.Stop(shutdownCtx)
	case err := <-host.serverErr:
		_ = host.Stop(context.Background())
		return err
	}
}

// Wait blocks until the host context is canceled or the HTTP server reports a fatal error.
func (host *Host) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-host.serverErr:
		return err
	}
}

// Stop stops HTTP and queued work, recovers content, and closes operational state.
func (host *Host) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	host.mu.Lock()
	if host.closed {
		host.mu.Unlock()
		return nil
	}
	host.closed = true
	host.ready.Store(false)
	server := host.server
	cancel := host.cancel
	host.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	var shutdownErrs []error
	if err := host.worker.Stop(ctx); err != nil {
		shutdownErrs = append(shutdownErrs, fmt.Errorf("stop Knowl worker: %w", err))
	}
	if server != nil {
		if err := server.Shutdown(ctx); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("shutdown Knowl HTTP endpoint: %w", err))
		}
	}
	if _, err := host.service.Recover(ctx); err != nil {
		shutdownErrs = append(shutdownErrs, fmt.Errorf("recover Knowl workspace during shutdown: %w", err))
	}
	if err := host.closer.Close(); err != nil {
		shutdownErrs = append(shutdownErrs, fmt.Errorf("close Knowl operational store: %w", err))
	}
	return errors.Join(shutdownErrs...)
}

// Shutdown is retained as an explicit alias for callers using the pre-Fx host API.
func (host *Host) Shutdown(ctx context.Context) error { return host.Stop(ctx) }

// Close shuts down the host with its configured timeout.
func (host *Host) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), host.config.ShutdownTimeout)
	defer cancel()
	return host.Stop(ctx)
}

// Ready reports whether recovery, migrations, projection preparation, and HTTP start completed.
func (host *Host) Ready() bool { return host.ready.Load() }

// Addr returns the bound HTTP address, or the configured address before Start.
func (host *Host) Addr() string {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.listener != nil {
		return host.listener.Addr().String()
	}
	return host.config.ListenAddr
}

// Handler returns the loopback HTTP handler for in-process integration tests and embedding.
func (host *Host) Handler() http.Handler { return host.handler }

// MCP returns the server-bound read-only MCP tool registry.
func (host *Host) MCP() *mcp.Server { return host.mcp }

// Workspace returns the canonical filesystem adapter.
func (host *Host) Workspace() *contentfs.Workspace { return host.workspace }

// Query returns the composed bounded query service.
func (host *Host) Query() *app.QueryService { return host.query }

// Lint returns the composed deterministic lint service.
func (host *Host) Lint() *app.LintService { return host.lint }

// Operations returns the redacted operational-state port.
func (host *Host) Operations() app.OperationStore { return host.operations }

// Index returns the rebuildable search projection port.
func (host *Host) Index() app.SearchIndex { return host.index }

func openOperationalStore(ctx context.Context, config Config) (app.OperationStore, app.SearchIndex, io.Closer, projectionChecker, error) {
	switch config.StoreDriver {
	case StoreSQLite:
		store, err := sqlite.Open(ctx, config.StorePath)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("open sqlite operational store: %w", err)
		}
		return store, store, store, store, nil
	case StorePostgres:
		store, err := postgres.Open(ctx, config.PostgresDSN)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("open postgres operational store: %w", err)
		}
		return store, store, store, store, nil
	default:
		return nil, nil, nil, nil, fmt.Errorf("unsupported store driver %q", config.StoreDriver)
	}
}

func ensureProjection(ctx context.Context, index app.SearchIndex, checker projectionChecker, snapshot domain.WorkspaceSnapshot) error {
	if checker == nil {
		return fmt.Errorf("operational store does not expose projection readiness")
	}
	if err := checker.CheckProjection(ctx, snapshot); err == nil {
		return nil
	}
	if err := index.Rebuild(ctx, snapshot); err != nil {
		return fmt.Errorf("rebuild search projection: %w", err)
	}
	if err := checker.CheckProjection(ctx, snapshot); err != nil {
		return fmt.Errorf("verify search projection: %w", err)
	}
	return nil
}

type projectionChecker interface {
	CheckProjection(ctx context.Context, snapshot domain.WorkspaceSnapshot) error
}

type unavailableMaintainer struct{}

func (unavailableMaintainer) Plan(ctx context.Context, _ domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	if err := ctx.Err(); err != nil {
		return domain.ModelEditPlan{}, err
	}
	return domain.ModelEditPlan{}, ErrMaintainerUnavailable
}
