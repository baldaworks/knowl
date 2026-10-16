package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type validatedDenseProjection struct {
	State           hybrid.ProjectionState
	ValidatedChunks int
}

// validateDenseProjectionTx returns bounded state and scalar summaries only.
// Each statement completes before the next stage can inspect vector elements.
// The caller retains this repeatable-read snapshot for ranking and evidence.
func (store *Store) validateDenseProjectionTx(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, space string, dimensions int) (result validatedDenseProjection, err error) {
	defer func() { err = denseValidationError(ctx, err) }()
	state := &result.State
	var bounded, currentFormat bool
	err = tx.QueryRowContext(ctx, `SELECT
		octet_length(e.space)=64 AND octet_length(e.snapshot_digest)=64
		AND octet_length(p.snapshot_digest)=64 AND octet_length(e.mode)<=16
		AND octet_length(e.reason)<=64 AND octet_length(e.coverage)<=1048576
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
	err = tx.QueryRowContext(ctx, `SELECT e.space,e.snapshot_digest,e.dimensions,e.mode,e.reason,e.chunk_count,e.omitted_chunks,e.omitted_runes,e.ready_at,e.coverage,p.snapshot_digest
		FROM knowl_embedding_state e JOIN knowl_projection_state p ON p.scope=e.scope WHERE e.scope=$1`, scope).Scan(&state.Space, &state.SnapshotDigest, &state.Dimensions, &state.Mode, &state.Reason, &state.ChunkCount, &state.OmittedChunks, &state.OmittedRunes, &state.ReadyAt, &state.Coverage, &lexicalSnapshot)
	if err != nil {
		return result, err
	}
	state.ReadyAt = state.ReadyAt.UTC()
	if state.Space != space || state.Dimensions != dimensions || state.SnapshotDigest != lexicalSnapshot {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	coverage, err := hybrid.ValidateProjectionMetadata(ctx, *state)
	if err != nil {
		return result, err
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM knowl_embedding_chunks WHERE scope=$1 LIMIT $2) c`, scope, hybrid.MaxChunks+1).Scan(&count)
	if err != nil {
		return result, err
	}
	if count > hybrid.MaxChunks {
		return result, embeddingFailure(knowl.RetrievalProjectionCapacity)
	}
	if count != state.ChunkCount {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	var invalid bool
	// The native type rejects NaN/infinity at input. Independently check every
	// row's metadata and actual dimensions, including foreign model spaces.
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowl_embedding_chunks WHERE scope=$1 AND NOT (
		space=$2 AND octet_length(page_id) BETWEEN 1 AND 4096
		AND octet_length(page_digest) BETWEEN 1 AND 256
		AND octet_length(content_hash)=64 AND content_hash COLLATE "C" ~ '^[0-9a-f]{64}$'
		AND ordinal BETWEEN 0 AND 8191 AND dimensions BETWEEN 1 AND 4096 AND dimensions=$3
		AND `+pgx.Identifier{store.native.schema, "vector_dims"}.Sanitize()+`(vector)=dimensions))`, scope, space, dimensions).Scan(&invalid)
	if err != nil {
		return result, err
	}
	if invalid {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	var bytes int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(4*dimensions::bigint+octet_length(page_id)+octet_length(page_digest)+octet_length(content_hash)+octet_length(space)+32),0) FROM knowl_embedding_chunks WHERE scope=$1`, scope).Scan(&bytes)
	if err != nil {
		return result, err
	}
	if bytes+256+int64(len(state.Coverage)) > hybrid.MaxProjectionBytes {
		return result, embeddingFailure(knowl.RetrievalProjectionCapacity)
	}
	var pageCount int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM knowl_pages WHERE scope=$1 LIMIT $2) p`, scope, hybrid.MaxChunks+1).Scan(&pageCount)
	if err != nil {
		return result, err
	}
	if pageCount > hybrid.MaxChunks {
		return result, embeddingFailure(knowl.RetrievalProjectionCapacity)
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowl_pages WHERE scope=$1 AND NOT (octet_length(page_id) BETWEEN 1 AND 4096 AND octet_length(digest) BETWEEN 1 AND 256))`, scope).Scan(&invalid)
	if err != nil {
		return result, err
	}
	if invalid {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	if state.Mode == knowl.RetrievalHybrid {
		canonical, marshalErr := json.Marshal(coverage)
		if marshalErr != nil {
			return result, marshalErr
		}
		err = tx.QueryRowContext(ctx, `WITH manifest AS (
			SELECT * FROM jsonb_to_recordset($1::jsonb) AS m(page_id text,page_digest text,chunks integer)
		), grouped AS (
			SELECT page_id,COUNT(*) AS n,COUNT(DISTINCT ordinal) AS distinct_n,MIN(ordinal) AS lo,MAX(ordinal) AS hi
			FROM knowl_embedding_chunks WHERE scope=$2 GROUP BY page_id
		)
		SELECT EXISTS(SELECT 1 FROM manifest m LEFT JOIN knowl_pages p ON p.scope=$2 AND p.page_id=m.page_id
			LEFT JOIN grouped g ON g.page_id=m.page_id
			WHERE p.page_id IS NULL OR p.digest<>m.page_digest OR COALESCE(g.n,0)<>m.chunks
			OR (m.chunks>0 AND (g.distinct_n<>m.chunks OR g.lo<>0 OR g.hi<>m.chunks-1)))
		OR EXISTS(SELECT 1 FROM knowl_pages p LEFT JOIN manifest m ON m.page_id=p.page_id WHERE p.scope=$2 AND m.page_id IS NULL)
		OR EXISTS(SELECT 1 FROM knowl_embedding_chunks c LEFT JOIN manifest m ON m.page_id=c.page_id
			WHERE c.scope=$2 AND (m.page_id IS NULL OR m.page_digest<>c.page_digest))`, string(canonical), scope).Scan(&invalid)
		if err != nil {
			return result, err
		}
		if invalid {
			return result, embeddingFailure(knowl.RetrievalProjectionDrift)
		}
	}
	// Promote operands before multiplication: vector_norm's rounded square root
	// cannot preserve the existing double squared-norm boundary exactly.
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowl_embedding_chunks c WHERE c.scope=$1 AND (
		SELECT COUNT(*)<>c.dimensions OR COALESCE(abs(SUM(v::double precision*v::double precision)-1)>0.001,true)
		FROM unnest(c.vector::real[]) AS elements(v)))`, scope).Scan(&invalid)
	if err != nil {
		return result, err
	}
	if invalid {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	result.ValidatedChunks = count
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
