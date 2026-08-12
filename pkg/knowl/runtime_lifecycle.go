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
	if err := host.worker.start(serverCtx); err != nil {
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

type workerSubmitter struct {
	worker *worker
}

func (submitter workerSubmitter) Submit(ctx context.Context, fn func(context.Context) error) error {
	if submitter.worker == nil {
		return fn(ctx)
	}
	return submitter.worker.submit(ctx, fn)
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
	if err := host.worker.stop(ctx); err != nil {
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
	if host.maintainerCloser != nil {
		if err := host.maintainerCloser.Close(); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("close Knowl maintainer: %w", err))
		}
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
