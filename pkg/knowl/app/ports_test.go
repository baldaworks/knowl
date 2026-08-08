package app

import (
	"context"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl"
)

type fakeContentStore struct{}

func (fakeContentStore) AcceptSource(context.Context, knowl.SourceEnvelope) (knowl.AcceptedSource, error) {
	return knowl.AcceptedSource{}, nil
}

func (fakeContentStore) Schema(context.Context, knowl.ScopeRef) (knowl.SchemaDocument, error) {
	return knowl.SchemaDocument{}, nil
}

func (fakeContentStore) ReadPages(context.Context, knowl.ScopeRef, []knowl.PageID, knowl.ReadLimits) ([]knowl.PageSnapshot, error) {
	return nil, nil
}

func (fakeContentStore) StagePlan(context.Context, knowl.ValidatedEditPlan) (knowl.StagedChange, error) {
	return knowl.StagedChange{}, nil
}

func (fakeContentStore) Commit(context.Context, knowl.StagedChange) (knowl.ContentCommit, error) {
	return knowl.ContentCommit{}, nil
}

func (fakeContentStore) Recover(context.Context) ([]knowl.RecoveryResult, error) {
	return nil, nil
}

func (fakeContentStore) Snapshot(context.Context, knowl.ScopeRef) (knowl.WorkspaceSnapshot, error) {
	return knowl.WorkspaceSnapshot{}, nil
}

func TestFakeCanConstructContentPort(t *testing.T) {
	var _ ContentStore = fakeContentStore{}
}
