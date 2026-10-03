package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func (store *Store) Rebuild(ctx context.Context, snapshot knowl.WorkspaceSnapshot) error {
	return store.rebuildWithTimeout(ctx, snapshot, hybrid.RebuildTimeout)
}

func (store *Store) rebuildWithTimeout(ctx context.Context, snapshot knowl.WorkspaceSnapshot, timeout time.Duration) error {
	if store.embedding != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := store.rebuildLexical(ctx, snapshot); err != nil {
		return err
	}
	if store.embedding == nil {
		return nil
	}
	state, chunks, err := store.embedding.Build(ctx, snapshot, snapshotDigest(snapshot))
	if err != nil {
		report, failure := store.embedding.Failure(ctx, store.embedding.Report(), err)
		if ctx.Err() != nil || report.Reason == knowl.RetrievalInvalidInput || report.Reason == knowl.RetrievalInvalidConfiguration {
			return failure
		}
		state = hybrid.ProjectionState{Space: store.embedding.Fingerprint, SnapshotDigest: snapshotDigest(snapshot), Dimensions: store.embedding.Space.Dimensions, Mode: knowl.RetrievalDegraded, Reason: report.Reason}
		return errors.Join(failure, store.publishEmbeddings(ctx, snapshot.Scope, state, nil))
	}
	return store.publishEmbeddings(ctx, snapshot.Scope, state, chunks)
}

func (store *Store) CheckProjection(ctx context.Context, snapshot knowl.WorkspaceSnapshot) error {
	if store.embedding == nil {
		return store.checkLexicalProjection(ctx, snapshot)
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkLexicalProjectionUsing(ctx, tx, snapshot); err != nil {
		return err
	}
	state, _, err := embeddingProjectionTx(ctx, tx, snapshot.Scope, store.embedding.Fingerprint, store.embedding.Space.Dimensions)
	if err != nil {
		return err
	}
	if state.Mode == knowl.RetrievalDegraded {
		_, err = store.embedding.Failure(ctx, store.embedding.Report(), embeddingFailure(state.Reason))
	}
	return err
}

// ProjectionDegraded permits one startup repair attempt even when the current
// fallback policy accepts a durable current-snapshot degraded projection.
func (store *Store) ProjectionDegraded(ctx context.Context, scope knowl.ScopeRef) (bool, error) {
	if store.embedding == nil {
		return false, nil
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, _, err := embeddingProjectionTx(ctx, tx, scope, store.embedding.Fingerprint, store.embedding.Space.Dimensions)
	return state.Mode == knowl.RetrievalDegraded, err
}

func (store *Store) MaintenanceRetrievalPolicy() *app.MaintenanceRetrievalPolicy {
	return store.embedding.MaintenancePolicy()
}

// ProjectWithoutInference finishes an existing historical stage without calling
// a model introduced by a newer policy. Its dense projection requires repair.
func (store *Store) ProjectWithoutInference(ctx context.Context, commit knowl.ContentCommit) error {
	if store.embedding != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, hybrid.RebuildTimeout)
		defer cancel()
	}
	snapshot := commit.Snapshot
	if err := store.rebuildLexical(ctx, snapshot); err != nil {
		return err
	}
	if store.embedding == nil {
		return nil
	}
	state := hybrid.ProjectionState{Space: store.embedding.Fingerprint, SnapshotDigest: snapshotDigest(snapshot), Dimensions: store.embedding.Space.Dimensions, Mode: knowl.RetrievalDegraded, Reason: knowl.RetrievalProjectionNotReady}
	return store.publishEmbeddings(ctx, snapshot.Scope, state, nil)
}
