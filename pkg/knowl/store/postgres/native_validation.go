package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/jackc/pgx/v5/pgconn"
)

type publishedDenseProjection struct {
	State           hybrid.ProjectionState
	PublishedChunks int
}

// readDenseProjectionTx admits existing publication state without coverage or
// vector audits. Full integrity is established by publication and maintenance.
func (store *Store) readDenseProjectionTx(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, space string, dimensions int) (result publishedDenseProjection, err error) {
	defer func() { err = denseValidationError(ctx, err) }()
	state := &result.State
	var bounded, currentFormat bool
	err = tx.QueryRowContext(ctx, `SELECT
		octet_length(e.space)=64 AND octet_length(e.snapshot_digest)=64
		AND octet_length(p.snapshot_digest)=64 AND octet_length(e.mode)<=16
		AND octet_length(e.reason)<=64
		AND isfinite(e.ready_at), e.native_format=1
		FROM knowl_embedding_state e JOIN knowl_projection_state p ON p.scope=e.scope WHERE e.scope=$1`, scope).Scan(&bounded, &currentFormat)
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
	var lexicalSnapshot string
	err = tx.QueryRowContext(ctx, `SELECT e.space,e.snapshot_digest,e.dimensions,e.mode,e.reason,e.chunk_count,e.omitted_chunks,e.omitted_runes,e.ready_at,p.snapshot_digest
		FROM knowl_embedding_state e JOIN knowl_projection_state p ON p.scope=e.scope WHERE e.scope=$1`, scope).Scan(&state.Space, &state.SnapshotDigest, &state.Dimensions, &state.Mode, &state.Reason, &state.ChunkCount, &state.OmittedChunks, &state.OmittedRunes, &state.ReadyAt, &lexicalSnapshot)
	if err != nil {
		return result, err
	}
	state.ReadyAt = state.ReadyAt.UTC()
	if state.Space != space || state.Dimensions != dimensions || state.SnapshotDigest != lexicalSnapshot {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	if err := hybrid.ValidateProjectionState(ctx, *state); err != nil {
		return result, err
	}
	result.PublishedChunks = state.ChunkCount
	return result, nil
}

func denseValidationError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var failure *pgconn.PgError
	if errors.As(err, &failure) {
		switch failure.Code {
		case "42501", "42883", "42704", "42P01", "42703", "3F000":
			return embeddingFailure(knowl.RetrievalUnavailable)
		case "22000", "22003", "22P02", "22P05":
			return embeddingFailure(knowl.RetrievalProjectionDrift)
		}
	}
	return err
}
