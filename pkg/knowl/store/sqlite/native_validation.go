package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type validatedDenseProjection struct {
	State           hybrid.ProjectionState
	ValidatedChunks int
}

// validateDenseProjectionTx transfers only bounded readiness metadata and scalar
// summaries. Each stage finishes before the next statement can decode vectors.
// The caller retains this snapshot for ranking and original-page evidence.
func validateDenseProjectionTx(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, space string, dimensions int) (validatedDenseProjection, error) {
	var result validatedDenseProjection
	state := &result.State
	var bounded, currentFormat bool
	err := tx.QueryRowContext(ctx, `SELECT
		typeof(e.space)='text' AND length(CAST(e.space AS BLOB))=64
		AND typeof(e.snapshot_digest)='text' AND length(CAST(e.snapshot_digest AS BLOB))=64
		AND typeof(p.snapshot_digest)='text' AND length(CAST(p.snapshot_digest AS BLOB))=64
		AND typeof(e.mode)='text' AND length(CAST(e.mode AS BLOB))<=16
		AND typeof(e.reason)='text' AND length(CAST(e.reason AS BLOB))<=64
		AND typeof(e.ready_at)='text' AND length(CAST(e.ready_at AS BLOB)) BETWEEN 1 AND 64
		AND typeof(e.coverage)='text' AND length(CAST(e.coverage AS BLOB))<=1048576
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
	err = tx.QueryRowContext(ctx, `SELECT e.space,e.snapshot_digest,e.dimensions,e.mode,e.reason,e.chunk_count,e.omitted_chunks,e.omitted_runes,e.ready_at,e.coverage,p.snapshot_digest
		FROM knowl_embedding_state e JOIN knowl_projection_state p ON p.scope=e.scope WHERE e.scope=?`, scope).Scan(&state.Space, &state.SnapshotDigest, &state.Dimensions, &state.Mode, &state.Reason, &state.ChunkCount, &state.OmittedChunks, &state.OmittedRunes, &readyAt, &state.Coverage, &lexicalSnapshot)
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
	coverage, err := hybrid.ValidateProjectionMetadata(ctx, *state)
	if err != nil {
		return result, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM knowl_embedding_chunks WHERE scope=? LIMIT ?)`, scope, hybrid.MaxChunks+1).Scan(&count); err != nil {
		return result, err
	}
	if count > hybrid.MaxChunks {
		return result, embeddingFailure(knowl.RetrievalProjectionCapacity)
	}
	if count != state.ChunkCount {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	// Do not let affinity coercion, NULL aggregate semantics, or an unsafe native
	// function hide a malformed row. This statement never decodes vector bytes.
	var invalid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowl_embedding_chunks WHERE scope=? AND NOT (
		typeof(space)='text' AND space=?
		AND typeof(page_id)='text' AND length(CAST(page_id AS BLOB)) BETWEEN 1 AND 4096
		AND typeof(page_digest)='text' AND length(CAST(page_digest AS BLOB)) BETWEEN 1 AND 256
		AND typeof(content_hash)='text' AND length(CAST(content_hash AS BLOB))=64 AND length(content_hash)=64 AND content_hash NOT GLOB '*[^0-9a-f]*'
		AND typeof(ordinal)='integer' AND ordinal BETWEEN 0 AND 8191
		AND typeof(dimensions)='integer' AND dimensions BETWEEN 1 AND 4096 AND dimensions=?
		AND typeof(vector)='blob' AND length(vector)=4*dimensions))`, scope, space, dimensions).Scan(&invalid)
	if err != nil {
		return result, err
	}
	if invalid {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	var bytes int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(4*dimensions+length(CAST(page_id AS BLOB))+length(CAST(page_digest AS BLOB))+length(CAST(content_hash AS BLOB))+length(CAST(space AS BLOB))+32),0) FROM knowl_embedding_chunks WHERE scope=?`, scope).Scan(&bytes)
	if err != nil {
		return result, err
	}
	if bytes+256+int64(len(state.Coverage)) > hybrid.MaxProjectionBytes {
		return result, embeddingFailure(knowl.RetrievalProjectionCapacity)
	}
	var pageCount int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM knowl_pages WHERE scope=? LIMIT ?)`, scope, hybrid.MaxChunks+1).Scan(&pageCount)
	if err != nil {
		return result, err
	}
	if pageCount > hybrid.MaxChunks {
		return result, embeddingFailure(knowl.RetrievalProjectionCapacity)
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowl_pages WHERE scope=? AND NOT (
		typeof(page_id)='text' AND length(CAST(page_id AS BLOB)) BETWEEN 1 AND 4096
		AND typeof(digest)='text' AND length(CAST(digest AS BLOB)) BETWEEN 1 AND 256))`, scope).Scan(&invalid)
	if err != nil {
		return result, err
	}
	if invalid {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	if state.Mode == knowl.RetrievalHybrid {
		// Expand only the canonical encoding of the already validated Go manifest.
		canonical, marshalErr := json.Marshal(coverage)
		if marshalErr != nil {
			return result, marshalErr
		}
		err = tx.QueryRowContext(ctx, `WITH manifest AS (
			SELECT json_extract(value,'$.page_id') AS page_id, json_extract(value,'$.page_digest') AS digest,
			json_extract(value,'$.chunks') AS chunks FROM json_each(?)
		), grouped AS (
			SELECT page_id,COUNT(*) AS n,COUNT(DISTINCT ordinal) AS distinct_n,MIN(ordinal) AS lo,MAX(ordinal) AS hi
			FROM knowl_embedding_chunks WHERE scope=? GROUP BY page_id
		)
		SELECT EXISTS(SELECT 1 FROM manifest m LEFT JOIN knowl_pages p ON p.scope=? AND p.page_id=m.page_id
			LEFT JOIN grouped g ON g.page_id=m.page_id
			WHERE p.page_id IS NULL OR p.digest<>m.digest OR COALESCE(g.n,0)<>m.chunks
			OR (m.chunks>0 AND (g.distinct_n<>m.chunks OR g.lo<>0 OR g.hi<>m.chunks-1)))
		OR EXISTS(SELECT 1 FROM knowl_pages p LEFT JOIN manifest m ON m.page_id=p.page_id WHERE p.scope=? AND m.page_id IS NULL)
		OR EXISTS(SELECT 1 FROM knowl_embedding_chunks c LEFT JOIN manifest m ON m.page_id=c.page_id
			WHERE c.scope=? AND (m.page_id IS NULL OR m.digest<>c.page_digest))`, string(canonical), scope, scope, scope, scope).Scan(&invalid)
		if err != nil {
			return result, err
		}
		if invalid {
			return result, embeddingFailure(knowl.RetrievalProjectionDrift)
		}
	}
	// Shape is proven for every row by an earlier completed statement. REAL
	// operands preserve the existing double-precision squared-norm threshold.
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowl_embedding_chunks c WHERE c.scope=? AND (
		SELECT COUNT(*)<>c.dimensions
		OR SUM(CASE WHEN value IS NULL OR abs(value)>3.4028234663852886e38 THEN 1 ELSE 0 END)>0
		OR COALESCE(abs(SUM(CAST(value AS REAL)*CAST(value AS REAL))-1)>0.001,1)
		FROM vec_each(c.vector)))`, scope).Scan(&invalid)
	if err != nil {
		return result, err
	}
	if invalid {
		return result, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	result.ValidatedChunks = count
	return result, nil
}
