package knowl

import (
	"context"
	"errors"
	"fmt"

	"github.com/baldaworks/knowl/internal/source/reconcile"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

// RunOnceOptions controls the execution of a synchronous one-shot run.
type RunOnceOptions struct {
	SyncSources        bool            `json:"sync_sources"`
	SourceID           domain.SourceID `json:"source_id,omitempty"`
	DrainOperations    bool            `json:"drain_operations"`
	ReconcileHierarchy bool            `json:"reconcile_hierarchy"`
}

// HierarchyRunResult contains the outcome of hierarchy reconciliation if performed.
type HierarchyRunResult struct {
	OperationID domain.OperationID     `json:"operation_id"`
	Status      domain.OperationStatus `json:"status"`
	Changed     bool                   `json:"changed"`
	Generation  string                 `json:"generation,omitempty"`
	Files       []string               `json:"files,omitempty"`
}

// RunOnceResult aggregates the results of all phases executed during RunOnce.
type RunOnceResult struct {
	Sources    []SourceSyncResult  `json:"sources,omitempty"`
	Operations DrainResult         `json:"operations"`
	Hierarchy  *HierarchyRunResult `json:"hierarchy,omitempty"`
}

// Drain processes ready maintenance operations to completion without starting
// the HTTP listener or periodic background tickers.
func (host *Host) Drain(ctx context.Context) (DrainResult, error) {
	ctx = nonNilHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return DrainResult{}, err
	}
	host.mu.Lock()
	if host.closed || host.resourcesClosed {
		host.mu.Unlock()
		return DrainResult{}, ErrHostClosed
	}
	scheduler := host.scheduler
	host.mu.Unlock()

	return scheduler.Drain(ctx)
}

// RunOnce performs one complete, bounded knowledge pipeline execution:
// 1. Optionally synchronizes configured sources (all or single).
// 2. Optionally drains ready maintenance operations to terminal states.
// 3. Optionally reconciles semantic OKF hierarchy if enabled and supported.
func (host *Host) RunOnce(ctx context.Context, options RunOnceOptions) (RunOnceResult, error) {
	ctx = nonNilHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return RunOnceResult{}, err
	}
	host.mu.Lock()
	if host.closed || host.resourcesClosed {
		host.mu.Unlock()
		return RunOnceResult{}, ErrHostClosed
	}
	hasHierarchy := host.hierarchy != nil
	host.mu.Unlock()

	var (
		result       RunOnceResult
		combinedErrs []error
	)

	if options.SyncSources {
		if options.SourceID != "" {
			syncRes, syncErr := host.SyncSource(ctx, options.SourceID)
			result.Sources = []SourceSyncResult{syncRes}
			if syncErr != nil {
				combinedErrs = append(combinedErrs, fmt.Errorf("sync source %s: %w", options.SourceID, syncErr))
			}
		} else {
			syncAllRes, syncErr := host.SyncAll(ctx)
			result.Sources = syncAllRes.Results
			if syncErr != nil && !errors.Is(syncErr, reconcile.ErrSyncPartial) {
				combinedErrs = append(combinedErrs, fmt.Errorf("sync sources: %w", syncErr))
			}
		}
	}

	if options.DrainOperations {
		drainRes, drainErr := host.Drain(ctx)
		result.Operations = drainRes
		if drainErr != nil {
			combinedErrs = append(combinedErrs, fmt.Errorf("drain operations: %w", drainErr))
		}
	}

	if options.ReconcileHierarchy && hasHierarchy {
		hResult, hErr := host.ReconcileHierarchy(ctx)
		if hErr != nil && !errors.Is(hErr, app.ErrMaintainerUnavailable) {
			combinedErrs = append(combinedErrs, fmt.Errorf("reconcile hierarchy: %w", hErr))
		} else if hErr == nil {
			result.Hierarchy = &HierarchyRunResult{
				OperationID: hResult.Operation.ID,
				Status:      hResult.Operation.Status,
				Changed:     hResult.Commit != nil,
			}
			if hResult.Commit != nil {
				result.Hierarchy.Generation = hResult.Commit.Generation
				result.Hierarchy.Files = append([]string(nil), hResult.Commit.Files...)
			}
		}
	}

	if len(combinedErrs) != 0 {
		return result, errors.Join(combinedErrs...)
	}
	return result, nil
}
