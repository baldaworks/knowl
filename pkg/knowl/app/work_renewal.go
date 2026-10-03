package app

import (
	"context"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// renewExecutionClaim fences a synchronous execution until it finishes. The
// final token lets its owner release non-terminal work after cancellation.
func renewExecutionClaim(ctx context.Context, cancelExecution context.CancelFunc, operations OperationStore, claim knowl.WorkClaim, duration time.Duration) string {
	interval := duration / 3
	if interval <= 0 {
		interval = time.Nanosecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	currentToken := claim.Lease.Token
	for {
		select {
		case <-ctx.Done():
			return currentToken
		case <-ticker.C:
			lease, err := newLease(time.Now().UTC(), duration)
			if err != nil {
				cancelExecution()
				return currentToken
			}
			next := knowl.WorkLease(lease)
			if err := operations.RenewClaim(ctx, claim.Descriptor.Schema.Scope, claim.Operation.ID, currentToken, next); err != nil {
				cancelExecution()
				return currentToken
			}
			currentToken = next.Token
		}
	}
}
