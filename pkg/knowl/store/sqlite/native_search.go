package sqlite

import (
	"context"
	"database/sql"
	"math"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type denseMatch struct {
	Evidence hybrid.Chunk // Metadata only; Vector is never populated by retrieval.
	Score    float64
}

// rankDensePagesTx follows scalar admission of a fully validated publication
// in this snapshot.
// Materializing scalar scores makes invalid values visible before MAX can hide
// them, and ensures every eligible chunk/window participates before the page limit.
func rankDensePagesTx(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, space string, queries [][]float32, sources []knowl.SourceID, limit int) ([]denseMatch, error) {
	if len(queries) == 0 || len(queries) > hybrid.QueryChunks || limit < 1 || limit > hybrid.CandidateLimit(100) {
		return nil, embeddingFailure(knowl.RetrievalInvalidInput)
	}
	values := make([]string, len(queries))
	args := make([]any, 0, len(queries)+len(sources)+3)
	for i, vector := range queries {
		encoded, err := hybrid.EncodeVector(vector, len(queries[0]))
		if err != nil {
			return nil, err
		}
		values[i] = "(?)"
		args = append(args, encoded)
	}
	args = append(args, scope, space)
	filter := ""
	if len(sources) != 0 {
		placeholders := make([]string, len(sources))
		for i, source := range sources {
			placeholders[i] = "?"
			args = append(args, source)
		}
		filter = ` AND EXISTS(SELECT 1 FROM knowl_page_sources s WHERE s.scope=c.scope AND s.page_id=c.page_id AND s.source_id IN (` + strings.Join(placeholders, ",") + `))`
	}
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, `WITH queries(vector) AS (VALUES `+strings.Join(values, ",")+`),
		scored AS MATERIALIZED (
			SELECT c.page_id,c.page_digest,c.ordinal,c.content_hash,1-vec_distance_cosine(c.vector,q.vector) AS score
			FROM knowl_embedding_chunks c CROSS JOIN queries q WHERE c.scope=? AND c.space=?`+filter+`
		), invalid AS (
			SELECT EXISTS(SELECT 1 FROM scored WHERE score IS NULL OR abs(score)>1.7976931348623157e308) AS bad
		), chunk_best AS (
			SELECT page_id,page_digest,ordinal,content_hash,MAX(score) AS score
			FROM scored GROUP BY page_id,page_digest,ordinal,content_hash
		), page_ordered AS (
			SELECT page_id,page_digest,ordinal,content_hash,score,
			ROW_NUMBER() OVER (PARTITION BY page_id ORDER BY score DESC,ordinal ASC) AS winner FROM chunk_best
		), winners AS (
			SELECT page_id,page_digest,ordinal,content_hash,score FROM page_ordered
			WHERE winner=1 ORDER BY score DESC,page_id COLLATE BINARY ASC LIMIT ?
		)
		SELECT w.page_id,w.page_digest,w.ordinal,w.content_hash,w.score,i.bad
		FROM invalid i LEFT JOIN winners w ON i.bad=0 ORDER BY w.score DESC,w.page_id COLLATE BINARY ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var matches []denseMatch
	for rows.Next() {
		var id, digest, hash sql.NullString
		var ordinal sql.NullInt64
		var score sql.NullFloat64
		var invalid bool
		if err := rows.Scan(&id, &digest, &ordinal, &hash, &score, &invalid); err != nil {
			return nil, err
		}
		if invalid {
			return nil, embeddingFailure(knowl.RetrievalProjectionDrift)
		}
		if !id.Valid { // Empty eligibility still carries the invalid-score summary.
			continue
		}
		if !digest.Valid || !ordinal.Valid || !hash.Valid || !score.Valid || math.IsNaN(score.Float64) || math.IsInf(score.Float64, 0) {
			return nil, embeddingFailure(knowl.RetrievalProjectionDrift)
		}
		matches = append(matches, denseMatch{Evidence: hybrid.Chunk{PageID: knowl.PageID(id.String), PageDigest: digest.String, Ordinal: int(ordinal.Int64), ContentHash: hash.String}, Score: score.Float64})
	}
	return matches, rows.Err()
}
