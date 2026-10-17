package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type publishedDenseProjection struct {
	State           hybrid.ProjectionState
	PublishedChunks int
}

// readDenseProjectionTx admits existing publication state without coverage or
// vector audits. Full integrity is established by publication and maintenance.
func readDenseProjectionTx(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, space string, dimensions int) (publishedDenseProjection, error) {
	var result publishedDenseProjection
	state := &result.State
	var bounded, currentFormat bool
	err := tx.QueryRowContext(ctx, `SELECT
		typeof(e.space)='text' AND length(CAST(e.space AS BLOB))=64
		AND typeof(e.snapshot_digest)='text' AND length(CAST(e.snapshot_digest AS BLOB))=64
		AND typeof(p.snapshot_digest)='text' AND length(CAST(p.snapshot_digest AS BLOB))=64
		AND typeof(e.mode)='text' AND length(CAST(e.mode AS BLOB))<=16
		AND typeof(e.reason)='text' AND length(CAST(e.reason AS BLOB))<=64
		AND typeof(e.ready_at)='text' AND length(CAST(e.ready_at AS BLOB)) BETWEEN 1 AND 64
		AND typeof(e.dimensions)='integer' AND typeof(e.chunk_count)='integer'
		AND typeof(e.omitted_chunks)='integer' AND typeof(e.omitted_runes)='integer',
		typeof(e.native_format)='integer' AND e.native_format=1
		FROM knowl_embedding_state e JOIN knowl_projection_state p ON p.scope=e.scope WHERE e.scope=?`, scope).Scan(&bounded, &currentFormat)
	if errors.Is(err, sql.ErrNoRows) {
		return result, embeddingFailure(knowl.RetrievalProjectionNotReady)
	}
	if err != nil {
		return result, err
	}
	if !currentFormat {
		return result, embeddingFailure(knowl.RetrievalProjectionNotReady)
	}
	if !bounded {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	var lexicalSnapshot, readyAt string
	err = tx.QueryRowContext(ctx, `SELECT e.space,e.snapshot_digest,e.dimensions,e.mode,e.reason,e.chunk_count,e.omitted_chunks,e.omitted_runes,e.ready_at,p.snapshot_digest
		FROM knowl_embedding_state e JOIN knowl_projection_state p ON p.scope=e.scope WHERE e.scope=?`, scope).Scan(&state.Space, &state.SnapshotDigest, &state.Dimensions, &state.Mode, &state.Reason, &state.ChunkCount, &state.OmittedChunks, &state.OmittedRunes, &readyAt, &lexicalSnapshot)
	if err != nil {
		return result, err
	}
	if state.Space != space || state.Dimensions != dimensions || state.SnapshotDigest != lexicalSnapshot {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	state.ReadyAt, err = time.Parse(time.RFC3339Nano, readyAt)
	if err != nil {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	if err := hybrid.ValidateProjectionState(ctx, *state); err != nil {
		return result, err
	}
	result.PublishedChunks = state.ChunkCount
	return result, nil
}
