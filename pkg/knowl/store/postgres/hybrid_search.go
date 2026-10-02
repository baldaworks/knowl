package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/contextpolicy"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/projectionmeta"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var _ app.ReportedSearchIndex = (*Store)(nil)

func (store *Store) SearchWithReport(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, knowl.RetrievalReport, error) {
	report := store.embedding.Report()
	if store.embedding != nil {
		report.Effective = knowl.RetrievalFailed
		report.Reason = knowl.RetrievalInvalidInput
	}
	if err := validateScope(scope); err != nil {
		return nil, report, err
	}
	if err := ctx.Err(); err != nil {
		if store.embedding != nil {
			report.Reason = knowl.RetrievalDeadline
		}
		return nil, report, err
	}
	sources, err := app.NormalizeSourcesFilter(sources)
	if err != nil {
		return nil, report, err
	}
	if store.embedding == nil {
		refs, err := store.search(ctx, scope, query, limits, sources)
		report.LexicalCandidates = len(refs)
		report.FusedCandidates = len(refs)
		return refs, report, err
	}
	prepared, err := hybrid.PrepareQuery(ctx, query, store.embedding.Space)
	if err != nil {
		return nil, report, fmt.Errorf("prepare query: %w: %w", ErrInvalidQuery, err)
	}
	refs, _, report, err := store.retrieveHybrid(ctx, scope, query, prepared, limits, sources, false)
	return refs, report, err
}

func (store *Store) SelectContext(ctx context.Context, scope knowl.ScopeRef, source knowl.SourceSummary, limits knowl.ReadLimits) ([]knowl.PageID, error) {
	ids, _, err := store.SelectContextWithReport(ctx, scope, source, limits)
	return ids, err
}

func (store *Store) SelectContextWithReport(ctx context.Context, scope knowl.ScopeRef, source knowl.SourceSummary, limits knowl.ReadLimits) ([]knowl.PageID, knowl.RetrievalReport, error) {
	report := store.embedding.Report()
	if store.embedding != nil {
		report.Effective = knowl.RetrievalFailed
		report.Reason = knowl.RetrievalInvalidInput
	}
	if store.embedding == nil {
		ids, err := store.selectLexicalContext(ctx, scope, source, limits)
		return ids, report, err
	}
	if err := validateScope(scope); err != nil {
		return nil, report, err
	}
	if err := ctx.Err(); err != nil {
		report.Reason = knowl.RetrievalDeadline
		return nil, report, err
	}
	query, err := contextpolicy.SourceQuery(source)
	if err != nil {
		return nil, report, fmt.Errorf("normalize source query: %w: %w", ErrInvalidQuery, err)
	}
	prepared, err := hybrid.PrepareSource(ctx, source, store.embedding.Space)
	if err != nil {
		return nil, report, err
	}
	_, ids, report, err := store.retrieveHybrid(ctx, scope, strings.Join(query.Terms, " "), prepared, limits, nil, true)
	return ids, report, err
}

// retrieveHybrid invokes the model before opening one consistent SQL snapshot.
// No query triggers a rebuild or downloads a model.
func (store *Store) retrieveHybrid(ctx context.Context, scope knowl.ScopeRef, query string, prepared hybrid.PreparedText, limits knowl.ReadLimits, sources []knowl.SourceID, contextSelection bool) (resultRefs []knowl.PageReference, resultIDs []knowl.PageID, resultReport knowl.RetrievalReport, resultErr error) {
	defer func() {
		if resultErr != nil {
			resultReport = app.FailedRetrievalReport(ctx, resultReport, resultErr)
		}
	}()
	engine := store.embedding
	report := engine.Report()
	report.QueryOmittedRunes = prepared.OmittedRunes
	vectors, err := engine.EmbedQuery(ctx, prepared)
	if err != nil {
		report, err = engine.Failure(ctx, report, err)
		if err != nil {
			return nil, nil, report, err
		}
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, nil, report, err
	}
	defer func() { _ = tx.Rollback() }()
	k := boundedLimit(limits.Pages)
	channelLimit := hybrid.CandidateLimit(k)
	lexLimits := limits
	lexLimits.Pages = channelLimit
	if contextSelection {
		lexLimits.Characters = 1
	}
	var lexicalRefs []knowl.PageReference
	if strings.TrimSpace(query) != "" {
		lexicalRefs, err = store.searchUsing(ctx, tx, scope, query, lexLimits, sources)
		if err != nil {
			return nil, nil, report, err
		}
	}
	report.LexicalCandidates = len(lexicalRefs)
	var dense []knowl.PageID
	if report.Effective == knowl.RetrievalHybrid {
		var state hybrid.ProjectionState
		var chunks []hybrid.Chunk
		state, chunks, err = embeddingProjectionTx(ctx, tx, scope, engine.Fingerprint, engine.Space.Dimensions)
		if err == nil && state.Mode == knowl.RetrievalDegraded {
			err = embeddingFailure(state.Reason)
		}
		if err != nil {
			report, err = engine.Failure(ctx, report, err)
			if err != nil {
				return nil, nil, report, err
			}
		} else {
			report.ScannedChunks = len(chunks)
			report.IndexOmittedChunks = state.OmittedChunks
			report.IndexOmittedRunes = state.OmittedRunes
			chunks, err = filterEmbeddingChunks(ctx, tx, scope, chunks, sources)
			if err != nil {
				return nil, nil, report, err
			}
			dense, err = hybrid.Rank(ctx, vectors, chunks, channelLimit)
			if err != nil {
				return nil, nil, report, err
			}
			report.VectorCandidates = len(dense)
		}
	}
	lexIDs := make([]knowl.PageID, 0, len(lexicalRefs))
	for _, ref := range lexicalRefs {
		lexIDs = append(lexIDs, ref.ID)
	}
	fused := lexIDs
	if report.Effective == knowl.RetrievalHybrid {
		fused = hybrid.Fuse(lexIDs, dense, 200)
	}
	report.FusedCandidates = len(fused)
	if contextSelection {
		seeds := fused[:min(contextpolicy.CandidateLimit(k), len(fused))]
		neighbors, err := store.contextNeighbors(ctx, tx, scope, seeds, max(0, k-1))
		if err != nil {
			return nil, nil, report, err
		}
		var recent []knowl.PageID
		if len(contextpolicy.Merge(k, seeds, neighbors, nil)) < k {
			recent, err = store.recentContext(ctx, tx, scope, append(append([]knowl.PageID(nil), seeds...), neighbors...), k)
			if err != nil {
				return nil, nil, report, err
			}
		}
		return nil, contextpolicy.Merge(k, seeds, neighbors, recent), report, ctx.Err()
	}
	ids := fused[:min(k, len(fused))]
	if report.Effective == knowl.RetrievalDegraded {
		return lexicalRefs[:min(k, len(lexicalRefs))], nil, report, ctx.Err()
	}
	refs, err := readHybridReferences(ctx, tx, scope, ids, limits.Characters)
	if err != nil {
		return nil, nil, report, err
	}
	lexicalByID := make(map[knowl.PageID]knowl.PageReference, len(lexicalRefs))
	for _, reference := range lexicalRefs {
		lexicalByID[reference.ID] = reference
	}
	for i, reference := range refs {
		if lexicalReference, ok := lexicalByID[reference.ID]; ok {
			refs[i] = lexicalReference
		}
	}
	return refs, nil, report, ctx.Err()
}

func filterEmbeddingChunks(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, chunks []hybrid.Chunk, sources []knowl.SourceID) ([]hybrid.Chunk, error) {
	if len(sources) == 0 {
		return chunks, nil
	}
	placeholders := make([]string, len(sources))
	args := []any{scope}
	for i, source := range sources {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args = append(args, source)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT c.page_id FROM knowl_embedding_chunks c WHERE c.scope=$1 AND EXISTS(SELECT 1 FROM knowl_page_sources s WHERE s.scope=c.scope AND s.page_id=c.page_id AND s.source_id IN (`+strings.Join(placeholders, ",")+`)) LIMIT 8193`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	allowed := make(map[knowl.PageID]bool)
	for rows.Next() {
		var id knowl.PageID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		allowed[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(allowed) > hybrid.MaxChunks {
		return nil, embeddingFailure(knowl.RetrievalProjectionCapacity)
	}
	filtered := make([]hybrid.Chunk, 0, len(chunks))
	for _, chunk := range chunks {
		if allowed[chunk.PageID] {
			filtered = append(filtered, chunk)
		}
	}
	return filtered, nil
}

func readHybridReferences(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, ids []knowl.PageID, characters int) ([]knowl.PageReference, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := []any{scope}
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args = append(args, id)
	}
	rows, err := tx.QueryContext(ctx, `SELECT page_id,path,title,tags,description,body,source_refs,source_document,source_documents,format,okf_metadata FROM knowl_pages WHERE scope=$1 AND page_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	found := make(map[knowl.PageID]knowl.PageReference, len(ids))
	for rows.Next() {
		ref, err := projectionmeta.Reference(rows, nil, characters)
		if err != nil {
			return nil, err
		}
		found[ref.ID] = ref
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	refs := make([]knowl.PageReference, 0, len(ids))
	for _, id := range ids {
		ref, ok := found[id]
		if !ok {
			return nil, embeddingFailure(knowl.RetrievalProjectionDrift)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}
