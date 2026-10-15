package knowl

import (
	"context"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type operatorRetirementReader struct{ host *Host }

func (reader operatorRetirementReader) SourceRetirement(ctx context.Context, scope domain.ScopeRef, owner domain.SourceOwner) (domain.SourceRetirementStatus, error) {
	reader.host.mu.Lock()
	validScope := !reader.host.closed && !reader.host.resourcesClosed && reader.host.config.Scope == scope
	reader.host.mu.Unlock()
	if !validScope {
		return domain.SourceRetirementStatus{}, app.ErrOperatorWorkspaceUnavailable
	}
	return reader.host.SourceRetirementStatus(ctx, owner)
}

// SourceRetirementStatus returns one exact adapter-qualified lifecycle view.
func (host *Host) SourceRetirementStatus(ctx context.Context, owner domain.SourceOwner) (domain.SourceRetirementStatus, error) {
	ctx = nonNilHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return domain.SourceRetirementStatus{}, err
	}
	host.mu.Lock()
	if host.closed || host.resourcesClosed {
		host.mu.Unlock()
		return domain.SourceRetirementStatus{}, ErrHostClosed
	}
	coordinator := host.retirementControl
	host.mu.Unlock()
	if coordinator == nil {
		return domain.SourceRetirementStatus{}, app.ErrOperatorCapabilityUnavailable
	}
	return coordinator.Status(ctx, owner)
}

// RetrySourceRetirement previews or requeues failed current-generation page units.
func (host *Host) RetrySourceRetirement(ctx context.Context, request app.SourceRetirementRetryRequest) (app.SourceRetirementRetryResult, error) {
	ctx = nonNilHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return app.SourceRetirementRetryResult{Owner: request.Owner, PageID: request.PageID, DryRun: request.DryRun, OperationIDs: make([]domain.OperationID, 0)}, err
	}
	host.mu.Lock()
	if host.closed || host.resourcesClosed {
		host.mu.Unlock()
		return app.SourceRetirementRetryResult{}, ErrHostClosed
	}
	coordinator, scheduler := host.retirementControl, host.scheduler
	host.mu.Unlock()
	if coordinator == nil {
		return app.SourceRetirementRetryResult{}, app.ErrOperatorCapabilityUnavailable
	}
	result, err := coordinator.Retry(ctx, request)
	if err == nil && result.Requeued != 0 && scheduler != nil {
		scheduler.Wake("")
	}
	return result, err
}
