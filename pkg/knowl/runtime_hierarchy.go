package knowl

import (
	"context"
	"fmt"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

// ReconcileHierarchy runs the explicit, synchronous hierarchy mutation without
// starting the HTTP listener, general operation scheduler, or source jobs.
func (host *Host) ReconcileHierarchy(ctx context.Context) (app.IngestResult, error) {
	ctx = nonNilHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return app.IngestResult{}, err
	}
	host.mu.Lock()
	if host.closed || host.resourcesClosed {
		host.mu.Unlock()
		return app.IngestResult{}, ErrHostClosed
	}
	service := host.hierarchy
	scope := host.config.Scope
	host.mu.Unlock()
	if service == nil {
		return app.IngestResult{}, app.ErrMaintainerUnavailable
	}
	result, err := service.Reconcile(ctx, scope)
	if err != nil {
		return result, fmt.Errorf("reconcile Knowl hierarchy: %w", err)
	}
	if err := host.workspace.Validate(); err != nil {
		return result, fmt.Errorf("validate reconciled Knowl hierarchy: %w", err)
	}
	return result, nil
}
