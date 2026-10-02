package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// publishEmbeddings atomically publishes staged vectors if the lexical snapshot
// is still current. Preparation and inference take place before this method.
func (store *Store) publishEmbeddings(ctx context.Context, scope knowl.ScopeRef, state hybrid.ProjectionState, chunks []hybrid.Chunk) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if err := hybrid.ValidateProjection(ctx, state, chunks); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin embeddings publication: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockEmbeddingScope(ctx, tx, scope); err != nil {
		return err
	}
	var current string
	err = tx.QueryRowContext(ctx, `SELECT snapshot_digest FROM knowl_projection_state WHERE scope = $1`, scope).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return embeddingFailure(knowl.RetrievalProjectionNotReady)
	}
	if err != nil {
		return fmt.Errorf("read embeddings snapshot: %w", err)
	}
	if current != state.SnapshotDigest {
		return embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM knowl_embedding_state WHERE scope = $1`, scope); err != nil {
		return err
	}
	if state.ReadyAt.IsZero() {
		state.ReadyAt = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO knowl_embedding_state(scope,space,snapshot_digest,dimensions,mode,reason,chunk_count,omitted_chunks,omitted_runes,ready_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, scope, state.Space, state.SnapshotDigest, state.Dimensions, state.Mode, state.Reason, state.ChunkCount, state.OmittedChunks, state.OmittedRunes, state.ReadyAt.UTC())
	if err != nil {
		return fmt.Errorf("write embeddings readiness: %w", err)
	}
	for _, chunk := range chunks {
		encoded, encodeErr := hybrid.EncodeVector(chunk.Vector, state.Dimensions)
		if encodeErr != nil {
			return encodeErr
		}
		result, execErr := tx.ExecContext(ctx, `INSERT INTO knowl_embedding_chunks(scope,space,page_id,page_digest,ordinal,content_hash,dimensions,vector) SELECT $1,$2,$3,$4,$5,$6,$7,$8 WHERE EXISTS(SELECT 1 FROM knowl_pages WHERE scope = $9 AND page_id = $10 AND digest = $11)`, scope, state.Space, chunk.PageID, chunk.PageDigest, chunk.Ordinal, chunk.ContentHash, state.Dimensions, encoded, scope, chunk.PageID, chunk.PageDigest)
		if execErr != nil {
			return fmt.Errorf("write embeddings chunk: %w", execErr)
		}
		changed, countErr := result.RowsAffected()
		if countErr != nil {
			return countErr
		}
		if changed != 1 {
			return embeddingFailure(knowl.RetrievalProjectionDrift)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit embeddings publication: %w", err)
	}
	return nil
}

// embeddingProjectionTx reads and validates complete state and rows in the same
// transaction that the caller uses for lexical candidates and original evidence.
func embeddingProjectionTx(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, space string, dimensions int) (hybrid.ProjectionState, []hybrid.Chunk, error) {
	var state hybrid.ProjectionState
	if err := ctx.Err(); err != nil {
		return state, nil, err
	}
	var bounded bool
	err := tx.QueryRowContext(ctx, `SELECT
        octet_length(e.space) = 64
        AND octet_length(e.snapshot_digest) = 64
        AND octet_length(p.snapshot_digest) = 64
        AND octet_length(e.mode) <= 16
        AND octet_length(e.reason) <= 64
        AND isfinite(e.ready_at)
        FROM knowl_embedding_state e JOIN knowl_projection_state p ON p.scope=e.scope
        WHERE e.scope=$1`, scope).Scan(&bounded)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionNotReady)
	}
	if err != nil {
		return state, nil, err
	}
	if !bounded {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	var lexicalSnapshot string
	err = tx.QueryRowContext(ctx, `SELECT e.space,e.snapshot_digest,e.dimensions,e.mode,e.reason,e.chunk_count,e.omitted_chunks,e.omitted_runes,e.ready_at,p.snapshot_digest FROM knowl_embedding_state e JOIN knowl_projection_state p ON p.scope=e.scope WHERE e.scope=$1`, scope).Scan(&state.Space, &state.SnapshotDigest, &state.Dimensions, &state.Mode, &state.Reason, &state.ChunkCount, &state.OmittedChunks, &state.OmittedRunes, &state.ReadyAt, &lexicalSnapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionNotReady)
	}
	if err != nil {
		return state, nil, err
	}
	state.ReadyAt = state.ReadyAt.UTC()
	if state.Space != space || state.SnapshotDigest != lexicalSnapshot || state.Dimensions != dimensions {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	// The cap applies before joins or filters so corruption cannot hide extra rows.
	var count, bytes int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(octet_length(vector)+octet_length(page_id)+octet_length(page_digest)+octet_length(content_hash)+octet_length(space)+32),0) FROM (SELECT * FROM knowl_embedding_chunks WHERE scope=$1 LIMIT $2) AS bounded_chunks`, scope, hybrid.MaxChunks+1).Scan(&count, &bytes)
	if err != nil {
		return state, nil, err
	}
	if count > hybrid.MaxChunks || bytes+256 > hybrid.MaxProjectionBytes {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionCapacity)
	}
	if count != state.ChunkCount {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.page_id,c.page_digest,c.ordinal,c.content_hash,c.vector,c.space,c.dimensions,COALESCE(p.digest=c.page_digest,false) FROM knowl_embedding_chunks c LEFT JOIN knowl_pages p ON p.scope=c.scope AND p.page_id=c.page_id WHERE c.scope=$1 ORDER BY c.page_id,c.ordinal LIMIT $2`, scope, hybrid.MaxChunks+1)
	if err != nil {
		return state, nil, err
	}
	defer func() { _ = rows.Close() }()
	chunks := make([]hybrid.Chunk, 0, count)
	for rows.Next() {
		var chunk hybrid.Chunk
		var encoded []byte
		var rowSpace string
		var rowDimensions int
		var digestMatches bool
		if err := rows.Scan(&chunk.PageID, &chunk.PageDigest, &chunk.Ordinal, &chunk.ContentHash, &encoded, &rowSpace, &rowDimensions, &digestMatches); err != nil {
			return state, nil, err
		}
		if rowSpace != space || rowDimensions != dimensions || !digestMatches {
			return state, nil, embeddingFailure(knowl.RetrievalProjectionDrift)
		}
		chunk.Vector, err = hybrid.DecodeVector(encoded, dimensions)
		if err != nil {
			return state, nil, err
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return state, nil, err
	}
	if err := hybrid.ValidateProjection(ctx, state, chunks); err != nil {
		return state, nil, err
	}
	return state, chunks, nil
}

func embeddingFailure(code knowl.RetrievalFailure) error { return &app.EmbeddingError{Code: code} }

// lockEmbeddingScope serializes lexical replacement and vector publication across
// processes. It is held only by a short SQL transaction, never model inference.
func lockEmbeddingScope(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef) error {
	var held any
	if err := tx.QueryRowContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('knowl:projection:' || $1,0))`, scope).Scan(&held); err != nil {
		return fmt.Errorf("lock projection scope: %w", err)
	}
	return nil
}
