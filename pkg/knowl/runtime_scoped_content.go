package knowl

import (
	"context"
	"fmt"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

// scopedMaintenanceContent preserves the Host-only maintenance authority while
// routing every canonical mutation through the workspace selected by ScopeRef.
type scopedMaintenanceContent struct {
	byScope map[domain.ScopeRef]*hostMaintenanceContent
}

func newScopedMaintenanceContent(resolver *ScopeWorkspaceResolver, lifecycles app.SourceLifecycleReader, coordinator app.ApplyCoordinator) (*scopedMaintenanceContent, error) {
	if resolver == nil || coordinator == nil {
		return nil, app.ErrSourceLifecycleInvalid
	}
	content := &scopedMaintenanceContent{byScope: make(map[domain.ScopeRef]*hostMaintenanceContent, len(resolver.ordered))}
	for _, resolved := range resolver.ordered {
		bound, err := newHostMaintenanceContent(resolved.Workspace, lifecycles, boundApplyCoordinator{coordinator: coordinator, scope: resolved.Ref})
		if err != nil {
			return nil, fmt.Errorf("bind scope %q maintenance authority: %w", resolved.Name, err)
		}
		content.byScope[resolved.Ref] = bound
	}
	return content, nil
}

func (content *scopedMaintenanceContent) scope(ref domain.ScopeRef) (*hostMaintenanceContent, error) {
	if content == nil {
		return nil, domain.ErrScopeUnknown
	}
	workspace, ok := content.byScope[ref]
	if !ok {
		return nil, fmt.Errorf("%w: durable scope is not configured", domain.ErrScopeUnknown)
	}
	return workspace, nil
}

func (content *scopedMaintenanceContent) AcceptSource(ctx context.Context, envelope domain.SourceEnvelope) (domain.AcceptedSource, error) {
	workspace, err := content.scope(envelope.Scope)
	if err != nil {
		return domain.AcceptedSource{}, err
	}
	return workspace.AcceptSource(ctx, envelope)
}
func (content *scopedMaintenanceContent) ReadSource(ctx context.Context, source domain.AcceptedSource, limits domain.ReadLimits) ([]byte, error) {
	workspace, err := content.scope(source.Scope)
	if err != nil {
		return nil, err
	}
	return workspace.ReadSource(ctx, source, limits)
}
func (content *scopedMaintenanceContent) ResolveAcceptedSources(ctx context.Context, ref domain.ScopeRef, refs []string) (map[string]domain.AcceptedSource, error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return nil, err
	}
	return workspace.ResolveAcceptedSources(ctx, ref, refs)
}
func (content *scopedMaintenanceContent) Schema(ctx context.Context, ref domain.ScopeRef) (domain.SchemaDocument, error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return domain.SchemaDocument{}, err
	}
	return workspace.Schema(ctx, ref)
}
func (content *scopedMaintenanceContent) ReadPages(ctx context.Context, ref domain.ScopeRef, ids []domain.PageID, limits domain.ReadLimits) ([]domain.PageSnapshot, error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return nil, err
	}
	return workspace.ReadPages(ctx, ref, ids, limits)
}
func (content *scopedMaintenanceContent) CatalogChildren(ctx context.Context, ref domain.ScopeRef, parent domain.PageID, options app.OperatorReadOptions) (app.OperatorCatalogRead, error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return app.OperatorCatalogRead{}, err
	}
	return workspace.CatalogChildren(ctx, ref, parent, options)
}
func (content *scopedMaintenanceContent) WikiDirectoryChildren(ctx context.Context, ref domain.ScopeRef, directory string, options app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorWikiEntry], error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return app.OperatorReadPage[domain.OperatorWikiEntry]{}, err
	}
	return workspace.WikiDirectoryChildren(ctx, ref, directory, options)
}
func (content *scopedMaintenanceContent) WikiPageChildren(ctx context.Context, ref domain.ScopeRef, directory string, options app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorWikiPage], error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return app.OperatorReadPage[domain.OperatorWikiPage]{}, err
	}
	return workspace.WikiPageChildren(ctx, ref, directory, options)
}
func (content *scopedMaintenanceContent) PageSummaries(ctx context.Context, ref domain.ScopeRef, options app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorPageSummary], error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return app.OperatorReadPage[domain.OperatorPageSummary]{}, err
	}
	return workspace.PageSummaries(ctx, ref, options)
}
func (content *scopedMaintenanceContent) Page(ctx context.Context, ref domain.ScopeRef, id domain.PageID, limits domain.ReadLimits) (domain.OperatorPage, error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return domain.OperatorPage{}, err
	}
	return workspace.Page(ctx, ref, id, limits)
}
func (content *scopedMaintenanceContent) SourceRevision(ctx context.Context, ref domain.ScopeRef, sourceRef string, limits domain.ReadLimits) (domain.OperatorSourceRevision, error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return domain.OperatorSourceRevision{}, err
	}
	return workspace.SourceRevision(ctx, ref, sourceRef, limits)
}
func (content *scopedMaintenanceContent) HierarchySnapshotDigest(ctx context.Context, ref domain.ScopeRef) (string, error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return "", err
	}
	return workspace.HierarchySnapshotDigest(ctx, ref)
}
func (content *scopedMaintenanceContent) StagePlan(ctx context.Context, plan domain.ValidatedEditPlan) (domain.StagedChange, error) {
	workspace, err := content.scope(plan.Scope)
	if err != nil {
		return domain.StagedChange{}, err
	}
	return workspace.StagePlan(ctx, plan)
}
func (content *scopedMaintenanceContent) LoadStage(ctx context.Context, ref domain.ScopeRef, id domain.OperationID) (domain.StagedChange, error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return domain.StagedChange{}, err
	}
	return workspace.LoadStage(ctx, ref, id)
}
func (content *scopedMaintenanceContent) Commit(ctx context.Context, staged domain.StagedChange) (domain.ContentCommit, error) {
	workspace, err := content.scope(staged.Scope)
	if err != nil {
		return domain.ContentCommit{}, err
	}
	return workspace.Commit(ctx, staged)
}
func (content *scopedMaintenanceContent) StageHierarchyPlan(ctx context.Context, id domain.OperationID, plan domain.ValidatedHierarchyPlan) (domain.StagedChange, error) {
	workspace, err := content.scope(plan.Scope)
	if err != nil {
		return domain.StagedChange{}, err
	}
	return workspace.StageHierarchyPlan(ctx, id, plan)
}
func (content *scopedMaintenanceContent) LoadHierarchyStage(ctx context.Context, ref domain.ScopeRef, id domain.OperationID) (domain.StagedChange, error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return domain.StagedChange{}, err
	}
	return workspace.LoadHierarchyStage(ctx, ref, id)
}
func (content *scopedMaintenanceContent) CommitHierarchy(ctx context.Context, staged domain.StagedChange) (domain.ContentCommit, error) {
	workspace, err := content.scope(staged.Scope)
	if err != nil {
		return domain.ContentCommit{}, err
	}
	return workspace.CommitHierarchy(ctx, staged)
}
func (content *scopedMaintenanceContent) Recover(ctx context.Context) ([]domain.RecoveryResult, error) {
	var recovered []domain.RecoveryResult
	for _, workspace := range content.byScope {
		results, err := workspace.Recover(ctx)
		if err != nil {
			return recovered, err
		}
		recovered = append(recovered, results...)
	}
	return recovered, nil
}
func (content *scopedMaintenanceContent) Snapshot(ctx context.Context, ref domain.ScopeRef) (domain.WorkspaceSnapshot, error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return domain.WorkspaceSnapshot{}, err
	}
	return workspace.Snapshot(ctx, ref)
}
func (content *scopedMaintenanceContent) Inspect(ctx context.Context, ref domain.ScopeRef) (domain.WorkspaceInspection, error) {
	workspace, err := content.scope(ref)
	if err != nil {
		return domain.WorkspaceInspection{}, err
	}
	return workspace.Inspect(ctx, ref)
}
func (content *scopedMaintenanceContent) MaintenanceAuthorityBound() bool { return true }
func (content *scopedMaintenanceContent) RunMaintenanceApply(ctx context.Context, staged domain.StagedChange, hierarchy bool, prepare func(context.Context) (bool, error), complete func(context.Context, domain.ContentCommit) error) (domain.ContentCommit, error) {
	workspace, err := content.scope(staged.Scope)
	if err != nil {
		return domain.ContentCommit{}, err
	}
	return workspace.RunMaintenanceApply(ctx, staged, hierarchy, prepare, complete)
}

type scopedRetirementCommitter struct {
	byScope map[domain.ScopeRef]*hostRetirementCommitter
}

func (committer *scopedRetirementCommitter) RunRetirementApply(ctx context.Context, staged domain.StagedChange, prepare func(context.Context) (bool, error), complete func(context.Context, domain.ContentCommit) error) (domain.ContentCommit, error) {
	if committer == nil {
		return domain.ContentCommit{}, app.ErrRetirementLifecycleConflict
	}
	selected, ok := committer.byScope[staged.Scope]
	if !ok {
		return domain.ContentCommit{}, app.ErrRetirementLifecycleConflict
	}
	return selected.RunRetirementApply(ctx, staged, prepare, complete)
}
