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
	bounded, cancel := context.WithTimeout(ctx, app.MaxCorrectionDeadline)
	defer cancel()
	use, err := host.slots.acquire(bounded)
	if err != nil {
		return app.IngestResult{}, err
	}
	defer use.release()
	service = use.slot.hierarchy
	if service == nil {
		return app.IngestResult{}, app.ErrMaintainerUnavailable
	}
	result, err := service.Reconcile(use.ctx, scope)
	if err != nil {
		return result, fmt.Errorf("reconcile Knowl hierarchy: %w", err)
	}
	if err := host.workspace.Validate(); err != nil {
		return result, fmt.Errorf("validate reconciled Knowl hierarchy: %w", err)
	}
	return result, nil
}
