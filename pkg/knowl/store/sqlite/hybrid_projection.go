package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/projectionmeta"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func (store *Store) project(ctx context.Context, snapshot knowl.WorkspaceSnapshot) error {
	if err := validateScope(snapshot.Scope); err != nil {
		return err
	}
	if store.embedding == nil {
		healthy, err := store.lexicalSnapshotHealthy(ctx, snapshot)
		if err != nil {
			return err
		}
		if healthy {
			return nil
		}
		return store.Rebuild(ctx, snapshot)
	}
	ctx, cancel := context.WithTimeout(ctx, hybrid.RebuildTimeout)
	defer cancel()
	previous, chunks, pages, same, err := store.previousProjection(ctx, snapshot)
	if err != nil {
		return err
	}
	if same && previous != nil && previousInputsMatch(ctx, pages, chunks, store.embedding.Space) {
		return nil
	}
	if err := store.rebuildLexical(ctx, snapshot); err != nil {
		return err
	}
	state, staged, err := store.embedding.BuildWithReuse(ctx, snapshot, snapshotDigest(snapshot), previous, chunks, pages)
	if err != nil {
		report, failure := store.embedding.Failure(ctx, store.embedding.Report(), err)
		if ctx.Err() != nil || report.Reason == knowl.RetrievalInvalidInput || report.Reason == knowl.RetrievalInvalidConfiguration {
			return failure
		}
		state = hybrid.ProjectionState{Space: store.embedding.Fingerprint, SnapshotDigest: snapshotDigest(snapshot), Dimensions: store.embedding.Space.Dimensions, Mode: knowl.RetrievalDegraded, Reason: report.Reason}
		return errors.Join(failure, store.publishEmbeddings(ctx, snapshot.Scope, state, nil))
	}
	return store.publishEmbeddings(ctx, snapshot.Scope, state, staged)
}

func (store *Store) lexicalSnapshotHealthy(ctx context.Context, snapshot knowl.WorkspaceSnapshot) (bool, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := projectionStatusUsing(ctx, tx, snapshot.Scope)
	if errors.Is(err, ErrProjectionNotReady) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state.SchemaDigest != snapshot.SchemaDigest || state.SnapshotDigest != snapshotDigest(snapshot) {
		return false, nil
	}
	return lexicalProjectionHealthyTx(ctx, tx, state)
}

// previousProjection captures the old lexical pages and dense rows together;
// inference runs only after this read transaction has released its locks.
func (store *Store) previousProjection(ctx context.Context, snapshot knowl.WorkspaceSnapshot) (*hybrid.ProjectionState, []hybrid.Chunk, []knowl.PageSnapshot, bool, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	lexical, err := projectionStatusUsing(ctx, tx, snapshot.Scope)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, nil, false, ctx.Err()
		}
		if errors.Is(err, ErrProjectionNotReady) {
			return nil, nil, nil, false, nil
		}
		return nil, nil, nil, false, err
	}
	healthy, err := lexicalProjectionHealthyTx(ctx, tx, lexical)
	if err != nil {
		return nil, nil, nil, false, err
	}
	if !healthy {
		return nil, nil, nil, false, nil
	}
	state, chunks, err := embeddingProjectionTx(ctx, tx, snapshot.Scope, store.embedding.Fingerprint, store.embedding.Space.Dimensions)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, nil, false, ctx.Err()
		}
		var failure *app.EmbeddingError
		if !errors.As(err, &failure) {
			return nil, nil, nil, false, err
		}
		return nil, nil, nil, false, nil
	}
	if state.Mode != knowl.RetrievalHybrid {
		return nil, nil, nil, false, nil
	}
	pages, err := previousPagesTx(ctx, tx, snapshot.Scope)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, nil, false, ctx.Err()
		}
		var failure *app.EmbeddingError
		if errors.As(err, &failure) {
			return nil, nil, nil, false, nil
		}
		return nil, nil, nil, false, err
	}
	return &state, chunks, pages, lexical.SchemaDigest == snapshot.SchemaDigest && lexical.SnapshotDigest == snapshotDigest(snapshot), nil
}

func lexicalProjectionHealthyTx(ctx context.Context, tx *sql.Tx, state ProjectionState) (bool, error) {
	for _, check := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM knowl_pages WHERE scope=?`, state.PageCount},
		{`SELECT COUNT(*) FROM knowl_pages_fts WHERE scope=?`, state.PageCount},
		{`SELECT COUNT(*) FROM knowl_links WHERE scope=?`, state.LinkCount},
	} {
		var count int
		if err := tx.QueryRowContext(ctx, check.query, state.Scope).Scan(&count); err != nil {
			return false, err
		}
		if count != check.want {
			return false, nil
		}
	}
	return true, nil
}

func previousPagesTx(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef) ([]knowl.PageSnapshot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT page_id,path,digest,title,body,format,okf_metadata FROM knowl_pages WHERE scope=? ORDER BY page_id LIMIT ?`, scope, hybrid.MaxChunks+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	pages := make([]knowl.PageSnapshot, 0)
	for rows.Next() {
		var page knowl.PageSnapshot
		var format string
		var metadata []byte
		if err := rows.Scan(&page.ID, &page.Path, &page.Digest, &page.Title, &page.Body, &format, &metadata); err != nil {
			return nil, err
		}
		page.OKF, err = projectionmeta.Decode(format, metadata)
		if err != nil {
			return nil, embeddingFailure(knowl.RetrievalProjectionDrift)
		}
		pages = append(pages, page)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pages) > hybrid.MaxChunks {
		return nil, embeddingFailure(knowl.RetrievalProjectionCapacity)
	}
	return pages, nil
}

func previousInputsMatch(ctx context.Context, pages []knowl.PageSnapshot, chunks []hybrid.Chunk, space app.EmbeddingSpace) bool {
	byPage := make(map[knowl.PageID][]hybrid.Chunk)
	for _, chunk := range chunks {
		byPage[chunk.PageID] = append(byPage[chunk.PageID], chunk)
	}
	for _, page := range pages {
		prepared, err := hybrid.PreparePage(ctx, page, space)
		if err != nil || len(prepared.Inputs) != len(byPage[page.ID]) {
			return false
		}
		for ordinal, input := range prepared.Inputs {
			hash := sha256.Sum256([]byte(input))
			if byPage[page.ID][ordinal].ContentHash != hex.EncodeToString(hash[:]) {
				return false
			}
		}
	}
	return true
}

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
