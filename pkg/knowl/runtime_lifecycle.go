package knowl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	"github.com/metalagman/appkit/lifecycle"
)

var _ lifecycle.Lifecycle = (*Host)(nil)

// PrepareReadOnly marks a composed, preflighted host ready for in-process read
// handlers without binding a listener or starting operation and source jobs.
func (host *Host) PrepareReadOnly() error {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed {
		return fmt.Errorf("host is closed")
	}
	host.ready.Store(true)
	return nil
}

// StartOperationWorker starts only durable operation processing. It does not
// bind an HTTP listener or start periodic source synchronization.
func (host *Host) StartOperationWorker(ctx context.Context) error {
	return host.start(ctx, true)
}

// Start binds the loopback HTTP listener and marks the host ready after preflight.
// The context is used for the start operation; Stop owns the server lifetime.
func (host *Host) Start(ctx context.Context) error {
	return host.start(ctx, false)
}

func (host *Host) start(ctx context.Context, operationOnly bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := host.acquireStartup(ctx); err != nil {
		return err
	}
	defer func() { <-host.startGate }()
	host.mu.Lock()
	if host.closed {
		host.mu.Unlock()
		return ErrHostClosed
	}
	if host.started {
		matches := host.operationOnly == operationOnly
		host.mu.Unlock()
		if !matches {
			return fmt.Errorf("host lifecycle is already started in another mode")
		}
		return nil
	}
	parent := context.Background()
	if operationOnly {
		parent = ctx
	}
	runCtx, cancel := context.WithCancel(parent)
	host.cancel = cancel
	host.mu.Unlock()

	var listener net.Listener
	var server *http.Server
	started := false
	defer func() {
		if started {
			return
		}
		cancel()
		if listener != nil {
			_ = listener.Close()
		}
		host.mu.Lock()
		host.listener = nil
		host.server = nil
		host.cancel = nil
		host.mu.Unlock()
	}()
	if !operationOnly {
		var err error
		listener, err = new(net.ListenConfig).Listen(ctx, "tcp", host.config.ListenAddr)
		if err != nil {
			return fmt.Errorf("listen Knowl HTTP endpoint: %w", err)
		}
		server = &http.Server{Handler: host.handler, ReadHeaderTimeout: host.config.ReadLimits.Deadline}
		host.mu.Lock()
		if host.closed {
			host.mu.Unlock()
			return ErrHostClosed
		}
		host.listener = listener
		host.server = server
		host.mu.Unlock()
	}
	if err := host.scheduler.start(runCtx); err != nil {
		return fmt.Errorf("start Knowl scheduler: %w", err)
	}
	if !operationOnly {
		if err := host.sourceJobs.start(runCtx); err != nil {
			cancel()
			_ = host.scheduler.stop(ctx)
			return fmt.Errorf("start Knowl source scheduler: %w", err)
		}
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed {
		return ErrHostClosed
	}
	host.started = true
	host.operationOnly = operationOnly
	host.ready.Store(true)
	started = true
	if server != nil {
		go func() {
			err := server.Serve(listener)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				host.ready.Store(false)
				host.serverErr <- err
			}
		}()
	}
	return nil
}

// acquireStartup serializes startup without holding the state mutex across I/O.
// Stop also acquires this gate before closing resources used during startup.
func (host *Host) acquireStartup(ctx context.Context) error {
	select {
	case host.startGate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-host.startGate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), host.config.ShutdownTimeout)
		defer cancel()
		return errors.Join(err, host.Stop(shutdownCtx))
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

// Stop makes the host unavailable, stops request intake and new claims, gives
// active work the caller's bound, and then closes owned resources.
func (host *Host) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case host.stopGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-host.stopGate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	host.mu.Lock()
	if host.resourcesClosed {
		host.mu.Unlock()
		return nil
	}
	host.closed = true
	host.ready.Store(false)
	host.slots.stopAdmission()
	server := host.server
	cancel := host.cancel
	host.mu.Unlock()
	var shutdownErrs []error
	type shutdownResult struct {
		component string
		err       error
	}
	results := make(chan shutdownResult, 5)
	components := 4
	go func() {
		err := host.acquireStartup(ctx)
		if err == nil {
			<-host.startGate
		}
		results <- shutdownResult{component: "startup", err: err}
	}()
	go func() {
		err := host.slots.wait(ctx)
		if err != nil {
			host.slots.cancel()
		}
		results <- shutdownResult{component: "execution owners", err: err}
	}()
	go func() { results <- shutdownResult{component: "scheduler", err: host.scheduler.stop(ctx)} }()
	go func() { results <- shutdownResult{component: "source scheduler", err: host.sourceJobs.stop(ctx)} }()
	if server != nil {
		components++
		// MCP stream requests may intentionally remain open for the lifetime of a
		// client session, so stop intake and active transport streams promptly.
		// Accepted ingest work is already durable and does not depend on them.
		go func() { results <- shutdownResult{component: "HTTP endpoint", err: server.Close()} }()
	}
	for range components {
		result := <-results
		if result.err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("stop Knowl %s: %w", result.component, result.err))
		}
	}
	if cancel != nil {
		cancel()
	}
	if len(shutdownErrs) != 0 {
		return errors.Join(shutdownErrs...)
	}
	if host.maintainerCloser != nil {
		if err := host.maintainerCloser.Close(); err != nil {
			return fmt.Errorf("close Knowl maintainer: %w", err)
		}
	}
	if err := host.closer.Close(); err != nil {
		shutdownErrs = append(shutdownErrs, fmt.Errorf("close Knowl operational store: %w", err))
	}
	if len(shutdownErrs) == 0 {
		host.mu.Lock()
		host.resourcesClosed = true
		host.mu.Unlock()
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

// Ready reports whether preflight and the selected lifecycle mode completed.
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
