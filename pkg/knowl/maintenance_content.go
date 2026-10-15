package knowl

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/internal/maintenanceauthority"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

// hostMaintenanceContent binds lifecycle decisions and the full apply session
// to the Host. The filesystem receives only sealed, authenticated requests.
type hostMaintenanceContent struct {
	*contentfs.Workspace
	lifecycles  app.SourceLifecycleReader
	coordinator app.ApplyCoordinator
}

func newHostMaintenanceContent(workspace *contentfs.Workspace, lifecycles app.SourceLifecycleReader, coordinator app.ApplyCoordinator) (*hostMaintenanceContent, error) {
	if workspace == nil || lifecycles == nil || coordinator == nil {
		return nil, app.ErrSourceLifecycleInvalid
	}
	if err := workspace.BindMaintenanceAuthority(maintenanceauthority.NewBinding()); err != nil {
		return nil, err
	}
	return &hostMaintenanceContent{Workspace: workspace, lifecycles: lifecycles, coordinator: coordinator}, nil
}

func (content *hostMaintenanceContent) StagePlan(ctx context.Context, plan domain.ValidatedEditPlan) (domain.StagedChange, error) {
	declared := slices.Clone(plan.SourceRefs)
	if plan.RequiredSourceRef != "" && !slices.Contains(declared, plan.RequiredSourceRef) {
		declared = append(declared, plan.RequiredSourceRef)
	}
	if plan.Retirement != nil {
		declared = nil
	}
	stamp, diagnostics, err := app.ValidateMaintenanceAuthority(ctx, content.lifecycles, content.Workspace, plan.Scope, plan.Edits, declared, nil)
	if err != nil {
		combined, normalizeErr := app.NormalizeMaintenanceDiagnostics(append(slices.Clone(plan.Diagnostics), diagnostics...))
		if normalizeErr != nil {
			return domain.StagedChange{}, errors.Join(err, normalizeErr)
		}
		return domain.StagedChange{OperationID: plan.OperationID, Diagnostics: combined}, errors.Join(contentfs.ErrContentInvalid, err)
	}
	return content.StageMaintenanceBound(ctx, maintenanceauthority.NewStageRequest(plan, stamp))
}

func (content *hostMaintenanceContent) StageHierarchyPlan(ctx context.Context, id domain.OperationID, plan domain.ValidatedHierarchyPlan) (domain.StagedChange, error) {
	edits := make([]domain.FileEdit, 0, len(plan.Mutations))
	for _, mutation := range plan.Mutations {
		edits = append(edits, domain.FileEdit{Action: mutation.Action, Path: mutation.Path, Content: mutation.Content})
	}
	stamp, diagnostics, err := app.ValidateMaintenanceAuthority(ctx, content.lifecycles, content.Workspace, plan.Scope, edits, nil, nil)
	if err != nil {
		return domain.StagedChange{OperationID: string(id), Diagnostics: diagnostics}, errors.Join(contentfs.ErrContentInvalid, err)
	}
	return content.StageHierarchyMaintenanceBound(ctx, maintenanceauthority.NewHierarchyStageRequest(id, plan, stamp))
}

func (content *hostMaintenanceContent) inspectCurrent(ctx context.Context, staged domain.StagedChange, hierarchy bool) (maintenanceauthority.Inspection, error) {
	inspection, err := content.InspectMaintenanceStage(ctx, maintenanceauthority.NewInspectRequest(staged, hierarchy))
	if err != nil || inspection.Committed != nil {
		return inspection, err
	}
	if inspection.Stamp == nil {
		return inspection, contentfs.ErrPlanConflict
	}
	_, _, err = app.ValidateMaintenanceAuthority(ctx, content.lifecycles, content.Workspace, inspection.Scope, inspection.Edits, inspection.Declared, inspection.Stamp)
	if err != nil {
		return inspection, errors.Join(contentfs.ErrContentInvalid, err)
	}
	return inspection, nil
}

func (content *hostMaintenanceContent) Commit(ctx context.Context, staged domain.StagedChange) (domain.ContentCommit, error) {
	return content.commit(ctx, staged, false)
}
func (content *hostMaintenanceContent) CommitHierarchy(ctx context.Context, staged domain.StagedChange) (domain.ContentCommit, error) {
	return content.commit(ctx, staged, true)
}
func (content *hostMaintenanceContent) commit(ctx context.Context, staged domain.StagedChange, hierarchy bool) (domain.ContentCommit, error) {
	ctx, release, err := content.acquire(ctx)
	if err != nil {
		return domain.ContentCommit{}, err
	}
	defer release()
	inspection, err := content.inspectCurrent(ctx, staged, hierarchy)
	if err != nil {
		return domain.ContentCommit{}, err
	}
	if inspection.Committed != nil {
		return *inspection.Committed, nil
	}
	return content.CommitMaintenanceBound(ctx, maintenanceauthority.NewCommitRequest(inspection.Stage, hierarchy))
}

type hostMaintenanceSessionKey struct{}
type hostMaintenanceSession struct {
	content *hostMaintenanceContent
	mu      sync.RWMutex
	active  bool
}

func (content *hostMaintenanceContent) acquire(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if session, ok := ctx.Value(hostMaintenanceSessionKey{}).(*hostMaintenanceSession); ok && session.content == content {
		session.mu.RLock()
		if session.active {
			return ctx, session.mu.RUnlock, nil
		}
		session.mu.RUnlock()
	}
	release, err := content.coordinator.Acquire(ctx)
	if err != nil {
		return ctx, nil, err
	}
	session := &hostMaintenanceSession{content: content, active: true}
	return context.WithValue(ctx, hostMaintenanceSessionKey{}, session), func() { session.mu.Lock(); session.active = false; release(); session.mu.Unlock() }, nil
}

func (content *hostMaintenanceContent) MaintenanceAuthorityBound() bool { return true }
func (content *hostMaintenanceContent) RunMaintenanceApply(ctx context.Context, staged domain.StagedChange, hierarchy bool, prepare func(context.Context) (bool, error), complete func(context.Context, domain.ContentCommit) error) (domain.ContentCommit, error) {
	ctx, release, err := content.acquire(ctx)
	if err != nil {
		return domain.ContentCommit{}, err
	}
	defer release()
	proceed, err := prepare(ctx)
	if err != nil || !proceed {
		return domain.ContentCommit{}, err
	}
	commit, err := content.commit(ctx, staged, hierarchy)
	if err != nil {
		return domain.ContentCommit{}, err
	}
	return commit, complete(ctx, commit)
}

func (content *hostMaintenanceContent) Recover(ctx context.Context) ([]domain.RecoveryResult, error) {
	ctx, release, err := content.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return content.Workspace.Recover(ctx)
}
