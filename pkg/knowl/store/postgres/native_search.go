package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type denseMatch struct {
	Evidence hybrid.Chunk // Metadata only; query results never populate Vector.
	Score    float64
}

const nativeVectorTypeName = "vector"

// rankDensePagesTx requires successful whole-scope validation in this snapshot.
// Materialized scalar scores retain invalid results before MAX or LIMIT can
// hide them. PostgreSQL sorts NaN above infinity, so the finite range predicate
// rejects it as well as either infinity.
func (store *Store) rankDensePagesTx(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, space string, queries [][]float32, sources []knowl.SourceID, limit int) ([]denseMatch, error) {
	if len(queries) == 0 || len(queries) > hybrid.QueryChunks || limit < 1 || limit > hybrid.CandidateLimit(100) {
		return nil, embeddingFailure(knowl.RetrievalInvalidInput)
	}
	vectorType := pgx.Identifier{store.native.schema, nativeVectorTypeName}.Sanitize()
	operator := "OPERATOR(" + pgx.Identifier{store.native.schema}.Sanitize() + ".<=>)"
	values := make([]string, len(queries))
	args := make([]any, 0, len(queries)+len(sources)+3)
	for i, vector := range queries {
		if _, err := hybrid.EncodeVector(vector, len(queries[0])); err != nil {
			return nil, err
		}
		values[i] = fmt.Sprintf("($%d::%s)", i+1, vectorType)
		args = append(args, nativeVectorText(vector))
	}
	args = append(args, scope, space)
	filter := fmt.Sprintf("c.scope=$%d AND c.space=$%d", len(args)-1, len(args))
	if len(sources) != 0 {
		placeholders := make([]string, len(sources))
		for i, source := range sources {
			args = append(args, source)
			placeholders[i] = fmt.Sprintf("$%d", len(args))
		}
		filter += ` AND EXISTS(SELECT 1 FROM knowl_page_sources s WHERE s.scope=c.scope AND s.page_id=c.page_id AND s.source_id IN (` + strings.Join(placeholders, ",") + `))`
	}
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, `WITH queries(vector) AS (VALUES `+strings.Join(values, ",")+`),
		scored AS MATERIALIZED (
			SELECT c.page_id,c.page_digest,c.ordinal,c.content_hash,1-(c.vector `+operator+` q.vector) AS score
			FROM knowl_embedding_chunks c CROSS JOIN queries q WHERE `+filter+`
		), invalid AS (
			SELECT EXISTS(SELECT 1 FROM scored WHERE score IS NULL OR NOT (score>'-Infinity'::float8 AND score<'Infinity'::float8)) AS bad
		), chunk_best AS (
			SELECT page_id,page_digest,ordinal,content_hash,MAX(score) AS score
			FROM scored GROUP BY page_id,page_digest,ordinal,content_hash
		), page_ordered AS (
			SELECT page_id,page_digest,ordinal,content_hash,score,
			ROW_NUMBER() OVER (PARTITION BY page_id ORDER BY score DESC,ordinal ASC) AS winner FROM chunk_best
		), winners AS (
			SELECT page_id,page_digest,ordinal,content_hash,score FROM page_ordered WHERE winner=1
			ORDER BY score DESC,page_id COLLATE "C" ASC LIMIT $`+fmt.Sprint(len(args))+`
		)
		SELECT w.page_id,w.page_digest,w.ordinal,w.content_hash,w.score,i.bad
		FROM invalid i LEFT JOIN winners w ON NOT i.bad ORDER BY w.score DESC,w.page_id COLLATE "C" ASC`, args...)
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
		if !id.Valid {
			continue
		}
		if !digest.Valid || !ordinal.Valid || !hash.Valid || !score.Valid || math.IsNaN(score.Float64) || math.IsInf(score.Float64, 0) {
			return nil, embeddingFailure(knowl.RetrievalProjectionDrift)
		}
		matches = append(matches, denseMatch{Evidence: hybrid.Chunk{PageID: knowl.PageID(id.String), PageDigest: digest.String, Ordinal: int(ordinal.Int64), ContentHash: hash.String}, Score: score.Float64})
	}
	return matches, rows.Err()
}

// densePagesTx owns the private savepoint. Even a statement permission error
// must be rolled back before context neighbors/recent pages can use the snapshot.
func (store *Store) densePagesTx(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, queries [][]float32, sources []knowl.SourceID, limit int) (validatedDenseProjection, []denseMatch, error) {
	var validated validatedDenseProjection
	if _, err := tx.ExecContext(ctx, `SAVEPOINT knowl_dense_read`); err != nil {
		return validated, nil, errors.Join(sql.ErrTxDone, redactDenseError(ctx, err))
	}
	validated, err := store.validateDenseProjectionTx(ctx, tx, scope, store.embedding.Fingerprint, store.embedding.Space.Dimensions)
	var matches []denseMatch
	if err == nil && validated.State.Mode == knowl.RetrievalDegraded {
		err = embeddingFailure(validated.State.Reason)
	}
	if err == nil {
		matches, err = store.rankDensePagesTx(ctx, tx, scope, store.embedding.Fingerprint, queries, sources, limit)
	}
	if err != nil {
		if denseTransactionFailed(ctx, err) {
			return validated, nil, redactDenseError(ctx, err)
		}
		if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT knowl_dense_read`); rollbackErr != nil {
			return validated, nil, errors.Join(sql.ErrTxDone, redactDenseError(ctx, rollbackErr))
		}
	}
	if _, releaseErr := tx.ExecContext(ctx, `RELEASE SAVEPOINT knowl_dense_read`); releaseErr != nil {
		return validated, nil, errors.Join(sql.ErrTxDone, redactDenseError(ctx, releaseErr))
	}
	return validated, matches, redactDenseError(ctx, denseValidationError(ctx, err))
}

func denseTransactionFailed(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrTxDone) {
		return true
	}
	var failure *pgconn.PgError
	return errors.As(err, &failure) && (strings.HasPrefix(failure.Code, "08") || failure.Code == "25P02" || failure.Code == "57P01" || failure.Code == "57P02" || failure.Code == "57P03" || failure.Code == "57014")
}

func redactDenseError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, driver.ErrBadConn) {
		return driver.ErrBadConn
	}
	if errors.Is(err, sql.ErrTxDone) {
		return sql.ErrTxDone
	}
	var embedding *app.EmbeddingError
	if errors.As(err, &embedding) {
		return embeddingFailure(embedding.Code)
	}
	var failure *pgconn.PgError
	if errors.As(err, &failure) {
		return &pgconn.PgError{Code: failure.Code, Message: "dense retrieval failed"}
	}
	return errors.New("postgres dense retrieval failed")
}
