package knowl

import (
	"context"
	"sync"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/internal/retirementcommit"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type hostApplyCoordinator struct {
	mu    sync.Mutex
	gates map[knowl.ScopeRef]chan struct{}
}

func newHostApplyCoordinator() *hostApplyCoordinator {
	return &hostApplyCoordinator{gates: make(map[knowl.ScopeRef]chan struct{})}
}
func (coordinator *hostApplyCoordinator) Acquire(ctx context.Context) (func(), error) {
	return coordinator.AcquireScope(ctx, "")
}

func (coordinator *hostApplyCoordinator) AcquireScope(ctx context.Context, scope knowl.ScopeRef) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	coordinator.mu.Lock()
	gate := coordinator.gates[scope]
	if gate == nil {
		gate = make(chan struct{}, 1)
		coordinator.gates[scope] = gate
	}
	coordinator.mu.Unlock()
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-gate
		return nil, err
	}
	return func() { <-gate }, nil
}

type boundApplyCoordinator struct {
	coordinator app.ApplyCoordinator
	scope       knowl.ScopeRef
}

func (coordinator boundApplyCoordinator) Acquire(ctx context.Context) (func(), error) {
	if scoped, ok := coordinator.coordinator.(app.ScopedApplyCoordinator); ok {
		return scoped.AcquireScope(ctx, coordinator.scope)
	}
	return coordinator.coordinator.Acquire(ctx)
}

// hostRetirementCommitter privately binds the exact host coordinator,
// lifecycle store, and canonical workspace into the only production
// retirement commit capability.
type hostRetirementCommitter struct {
	coordinator app.ApplyCoordinator
	lifecycles  app.SourceLifecycleReader
	workspace   *contentfs.Workspace
	committed   func(context.Context, knowl.StagedChange) (knowl.ContentCommit, bool, error)
	commit      func(context.Context, retirementcommit.Request) (knowl.ContentCommit, error)
	maintenance *hostMaintenanceContent
}

func newHostRetirementCommitter(coordinator app.ApplyCoordinator, lifecycles app.SourceLifecycleReader, workspace *contentfs.Workspace, maintenance ...*hostMaintenanceContent) (*hostRetirementCommitter, error) {
	if coordinator == nil || lifecycles == nil || workspace == nil {
		return nil, app.ErrRetirementLifecycleConflict
	}
	result := &hostRetirementCommitter{
		coordinator: coordinator, lifecycles: lifecycles, workspace: workspace,
		committed: workspace.RetirementCommitReceipt,
		commit:    workspace.CommitRetirementBound,
	}
	if len(maintenance) > 0 {
		result.maintenance = maintenance[0]
	}
	return result, nil
}

func (committer *hostRetirementCommitter) RunRetirementApply(
	ctx context.Context,
	staged knowl.StagedChange,
	prepare func(context.Context) (bool, error),
	complete func(context.Context, knowl.ContentCommit) error,
) (knowl.ContentCommit, error) {
	if committer == nil || committer.coordinator == nil || committer.lifecycles == nil || committer.workspace == nil || committer.committed == nil || committer.commit == nil {
		return knowl.ContentCommit{}, app.ErrRetirementLifecycleConflict
	}
	if prepare == nil || complete == nil {
		return knowl.ContentCommit{}, app.ErrRetirementLifecycleConflict
	}
	if err := ctx.Err(); err != nil {
		return knowl.ContentCommit{}, err
	}
	release, err := committer.coordinator.Acquire(ctx)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	defer release()
	proceed, err := prepare(ctx)
	if err != nil || !proceed {
		return knowl.ContentCommit{}, err
	}
	commit, committed, err := committer.committed(ctx, staged)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	if !committed {
		if committer.maintenance != nil {
			if _, err := committer.maintenance.inspectCurrent(ctx, staged, false); err != nil {
				return knowl.ContentCommit{}, err
			}
		}
		if err := app.RevalidateRetirementStage(ctx, committer.lifecycles, staged); err != nil {
			return knowl.ContentCommit{}, err
		}
		commit, err = committer.commit(ctx, retirementcommit.NewRequest(staged))
		if err != nil {
			return knowl.ContentCommit{}, err
		}
	}
	return commit, complete(ctx, commit)
}
