package app

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

type fakeContentStore struct{}

func (fakeContentStore) AcceptSource(context.Context, knowl.SourceEnvelope) (knowl.AcceptedSource, error) {
	return knowl.AcceptedSource{}, nil
}

func (fakeContentStore) ReadSource(context.Context, knowl.AcceptedSource, knowl.ReadLimits) ([]byte, error) {
	return nil, errors.New("fake content store does not implement ReadSource")
}

func (fakeContentStore) Schema(context.Context, knowl.ScopeRef) (knowl.SchemaDocument, error) {
	return knowl.SchemaDocument{}, nil
}

func (fakeContentStore) ReadPages(context.Context, knowl.ScopeRef, []knowl.PageID, knowl.ReadLimits) ([]knowl.PageSnapshot, error) {
	return nil, nil
}

func (fakeContentStore) HierarchySnapshotDigest(context.Context, knowl.ScopeRef) (string, error) {
	return "", nil
}

func (fakeContentStore) StagePlan(context.Context, knowl.ValidatedEditPlan) (knowl.StagedChange, error) {
	return knowl.StagedChange{}, nil
}

func (fakeContentStore) LoadStage(context.Context, knowl.ScopeRef, knowl.OperationID) (knowl.StagedChange, error) {
	return knowl.StagedChange{}, ErrStageNotFound
}

func (fakeContentStore) Commit(context.Context, knowl.StagedChange) (knowl.ContentCommit, error) {
	return knowl.ContentCommit{}, nil
}

func (fakeContentStore) StageHierarchyPlan(context.Context, knowl.OperationID, knowl.ValidatedHierarchyPlan) (knowl.StagedChange, error) {
	return knowl.StagedChange{}, nil
}

func (fakeContentStore) LoadHierarchyStage(context.Context, knowl.ScopeRef, knowl.OperationID) (knowl.StagedChange, error) {
	return knowl.StagedChange{}, nil
}

func (fakeContentStore) CommitHierarchy(context.Context, knowl.StagedChange) (knowl.ContentCommit, error) {
	return knowl.ContentCommit{}, nil
}

func (fakeContentStore) Recover(context.Context) ([]knowl.RecoveryResult, error) {
	return nil, nil
}

func (fakeContentStore) Snapshot(context.Context, knowl.ScopeRef) (knowl.WorkspaceSnapshot, error) {
	return knowl.WorkspaceSnapshot{}, nil
}

func (fakeContentStore) Inspect(context.Context, knowl.ScopeRef) (knowl.WorkspaceInspection, error) {
	return knowl.WorkspaceInspection{}, nil
}

func TestFakeCanConstructContentPort(t *testing.T) {
	var _ ContentStore = fakeContentStore{}
}
