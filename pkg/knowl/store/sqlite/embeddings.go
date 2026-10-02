package sqlite

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
	var current string
	err = tx.QueryRowContext(ctx, `SELECT snapshot_digest FROM knowl_projection_state WHERE scope = ?`, scope).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return embeddingFailure(knowl.RetrievalProjectionNotReady)
	}
	if err != nil {
		return fmt.Errorf("read embeddings snapshot: %w", err)
	}
	if current != state.SnapshotDigest {
		return embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM knowl_embedding_state WHERE scope = ?`, scope); err != nil {
		return err
	}
	if state.ReadyAt.IsZero() {
		state.ReadyAt = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO knowl_embedding_state(scope,space,snapshot_digest,dimensions,mode,reason,chunk_count,omitted_chunks,omitted_runes,ready_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, scope, state.Space, state.SnapshotDigest, state.Dimensions, state.Mode, state.Reason, state.ChunkCount, state.OmittedChunks, state.OmittedRunes, state.ReadyAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("write embeddings readiness: %w", err)
	}
	for _, chunk := range chunks {
		encoded, encodeErr := hybrid.EncodeVector(chunk.Vector, state.Dimensions)
		if encodeErr != nil {
			return encodeErr
		}
		result, execErr := tx.ExecContext(ctx, `INSERT INTO knowl_embedding_chunks(scope,space,page_id,page_digest,ordinal,content_hash,dimensions,vector) SELECT ?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM knowl_pages WHERE scope = ? AND page_id = ? AND digest = ?)`, scope, state.Space, chunk.PageID, chunk.PageDigest, chunk.Ordinal, chunk.ContentHash, state.Dimensions, encoded, scope, chunk.PageID, chunk.PageDigest)
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
	var bounded bool
	err := tx.QueryRowContext(ctx, `SELECT
        length(CAST(e.space AS BLOB)) = 64
        AND length(CAST(e.snapshot_digest AS BLOB)) = 64
        AND length(CAST(p.snapshot_digest AS BLOB)) = 64
        AND length(CAST(e.mode AS BLOB)) <= 16
        AND length(CAST(e.reason AS BLOB)) <= 64
        AND length(CAST(e.ready_at AS BLOB)) BETWEEN 1 AND 64
        AND typeof(e.dimensions) = 'integer'
        AND typeof(e.chunk_count) = 'integer'
        AND typeof(e.omitted_chunks) = 'integer'
        AND typeof(e.omitted_runes) = 'integer'
        FROM knowl_embedding_state e JOIN knowl_projection_state p ON p.scope=e.scope
        WHERE e.scope=?`, scope).Scan(&bounded)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionNotReady)
	}
	if err != nil {
		return state, nil, err
	}
	if !bounded {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	var lexicalSnapshot, readyAt string
	err = tx.QueryRowContext(ctx, `SELECT e.space,e.snapshot_digest,e.dimensions,e.mode,e.reason,e.chunk_count,e.omitted_chunks,e.omitted_runes,e.ready_at,p.snapshot_digest FROM knowl_embedding_state e JOIN knowl_projection_state p ON p.scope=e.scope WHERE e.scope=?`, scope).Scan(&state.Space, &state.SnapshotDigest, &state.Dimensions, &state.Mode, &state.Reason, &state.ChunkCount, &state.OmittedChunks, &state.OmittedRunes, &readyAt, &lexicalSnapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionNotReady)
	}
	if err != nil {
		return state, nil, err
	}
	if state.Space != space || state.SnapshotDigest != lexicalSnapshot || state.Dimensions != dimensions {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	state.ReadyAt, err = time.Parse(time.RFC3339Nano, readyAt)
	if err != nil {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	// The cap applies before joins or filters so corruption cannot hide extra rows.
	var count, bytes int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(vector)+length(CAST(page_id AS BLOB))+length(CAST(page_digest AS BLOB))+length(CAST(content_hash AS BLOB))+length(CAST(space AS BLOB))+32),0) FROM (SELECT * FROM knowl_embedding_chunks WHERE scope=? LIMIT ?)`, scope, hybrid.MaxChunks+1).Scan(&count, &bytes)
	if err != nil {
		return state, nil, err
	}
	if count > hybrid.MaxChunks || bytes+256 > hybrid.MaxProjectionBytes {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionCapacity)
	}
	if count != state.ChunkCount {
		return state, nil, embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.page_id,c.page_digest,c.ordinal,c.content_hash,c.vector,c.space,c.dimensions,COALESCE(p.digest=c.page_digest,0) FROM knowl_embedding_chunks c LEFT JOIN knowl_pages p ON p.scope=c.scope AND p.page_id=c.page_id WHERE c.scope=? ORDER BY c.page_id,c.ordinal LIMIT ?`, scope, hybrid.MaxChunks+1)
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
