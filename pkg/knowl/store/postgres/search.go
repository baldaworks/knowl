package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/contextpolicy"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/projectionmeta"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

type projectionReader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// SelectContext returns source-relevant pages, one-hop context, the required
// index control page, and only then deterministic recent fallback pages.
func (store *Store) selectLexicalContext(ctx context.Context, scope knowl.ScopeRef, source knowl.SourceSummary, limits knowl.ReadLimits) ([]knowl.PageID, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	limit := boundedLimit(limits.Pages)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query, queryErr := contextpolicy.SourceQuery(source)
	if queryErr != nil {
		return nil, fmt.Errorf("normalize source query: %w: %w", ErrInvalidQuery, queryErr)
	}
	candidates, err := store.contextCandidates(ctx, scope, query.Terms, contextpolicy.CandidateLimit(limit))
	if err != nil {
		return nil, err
	}
	neighbors, err := store.contextNeighbors(ctx, store.db, scope, candidates, max(0, limit-1))
	if err != nil {
		return nil, err
	}
	var recent []knowl.PageID
	if len(contextpolicy.Merge(limit, candidates, neighbors, nil)) < limit {
		excluded := append(append([]knowl.PageID(nil), candidates...), neighbors...)
		recent, err = store.recentContext(ctx, store.db, scope, excluded, limit)
		if err != nil {
			return nil, err
		}
	}
	return contextpolicy.Merge(limit, candidates, neighbors, recent), nil
}

func (store *Store) contextCandidates(ctx context.Context, scope knowl.ScopeRef, terms []string, limit int) ([]knowl.PageID, error) {
	if len(terms) == 0 || limit <= 0 {
		return nil, nil
	}
	references, err := store.search(ctx, scope, strings.Join(terms, " "), knowl.ReadLimits{Pages: limit, Characters: 1}, nil)
	if err != nil {
		return nil, fmt.Errorf("select relevant context: %w", err)
	}
	ids := make([]knowl.PageID, 0, len(references))
	for _, reference := range references {
		ids = append(ids, reference.ID)
	}
	return ids, nil
}

func (store *Store) contextNeighbors(ctx context.Context, reader projectionReader, scope knowl.ScopeRef, seeds []knowl.PageID, limit int) ([]knowl.PageID, error) {
	if len(seeds) == 0 || limit <= 0 {
		return nil, nil
	}
	seedValues := make([]string, len(seeds))
	for index, seed := range seeds {
		seedValues[index] = string(seed)
	}
	rows, err := reader.QueryContext(ctx, `
		WITH seeds(page_id, ordinal) AS (
			SELECT page_id, ordinal
			FROM unnest($2::text[]) WITH ORDINALITY AS input(page_id, ordinal)
		)
		SELECT neighbor.page_id
		FROM seeds
		JOIN knowl_links AS link
		  ON link.scope = $1
		 AND (link.from_page = seeds.page_id OR link.to_page = seeds.page_id)
		JOIN knowl_pages AS neighbor
		  ON neighbor.scope = $1
		 AND neighbor.page_id = CASE
		       WHEN link.from_page = seeds.page_id THEN link.to_page
		       ELSE link.from_page
		     END
		WHERE neighbor.page_id <> seeds.page_id
		GROUP BY neighbor.page_id, neighbor.path
		ORDER BY MIN(seeds.ordinal) ASC, neighbor.path ASC
		LIMIT $3`, scope, seedValues, limit)
	if err != nil {
		return nil, fmt.Errorf("select linked context: %w", err)
	}
	defer func() { _ = rows.Close() }()
	neighbors := make([]knowl.PageID, 0, limit)
	for rows.Next() {
		var id knowl.PageID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan linked context: %w", err)
		}
		neighbors = append(neighbors, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate linked context: %w", err)
	}
	return neighbors, nil
}

func (store *Store) recentContext(ctx context.Context, reader projectionReader, scope knowl.ScopeRef, excluded []knowl.PageID, limit int) ([]knowl.PageID, error) {
	excludedValues := make([]string, len(excluded))
	for index, id := range excluded {
		excludedValues[index] = string(id)
	}
	rows, err := reader.QueryContext(ctx, `
		SELECT page_id
		FROM knowl_pages
		WHERE scope = $1
		  AND NOT (page_id = ANY($2::text[]))
		ORDER BY updated_at DESC, path ASC
		LIMIT $3`, scope, excludedValues, limit)
	if err != nil {
		return nil, fmt.Errorf("select recent context: %w", err)
	}
	defer func() { _ = rows.Close() }()
	recent := make([]knowl.PageID, 0, limit)
	for rows.Next() {
		var id knowl.PageID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan recent context: %w", err)
		}
		recent = append(recent, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent context: %w", err)
	}
	return recent, nil
}

// Search returns bounded, untrusted PostgreSQL full-text references.
func (store *Store) Search(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, error) {
	refs, _, err := store.SearchWithReport(ctx, scope, query, limits, sources)
	return refs, err
}

func (store *Store) search(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, error) {
	return store.searchUsing(ctx, store.db, scope, query, limits, sources)
}

func (store *Store) searchUsing(ctx context.Context, reader projectionReader, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sources, filterErr := app.NormalizeSourcesFilter(sources)
	if filterErr != nil {
		return nil, filterErr
	}
	normalized, err := lexical.Normalize(query)
	if err != nil {
		return nil, fmt.Errorf("normalize search query: %w", ErrInvalidQuery)
	}
	limit := boundedLimit(limits.Pages)
	encodedTerms := normalized.IndexTerms()
	strict, err := store.searchPhase(ctx, reader, scope, tsQuery(encodedTerms, "&"), limit, limits.Characters, normalized.Terms, sources)
	if err != nil {
		return nil, err
	}
	if len(strict) >= limit || len(normalized.Terms) == 1 {
		return strict, nil
	}

	relaxed, err := store.searchPhase(ctx, reader, scope, tsQuery(encodedTerms, "|"), limit, limits.Characters, normalized.Terms, sources)
	if err != nil {
		return nil, err
	}
	references := make([]knowl.PageReference, 0, limit)
	references = append(references, strict...)
	seen := make(map[knowl.PageID]struct{}, len(strict))
	for _, reference := range strict {
		seen[reference.ID] = struct{}{}
	}
	for _, reference := range relaxed {
		if _, duplicate := seen[reference.ID]; duplicate {
			continue
		}
		references = append(references, reference)
		seen[reference.ID] = struct{}{}
		if len(references) == limit {
			break
		}
	}
	return references, nil
}

func (store *Store) searchPhase(ctx context.Context, reader projectionReader, scope knowl.ScopeRef, query string, limit, maxCharacters int, terms []string, sources []knowl.SourceID) ([]knowl.PageReference, error) {
	statement := `
		WITH lexical_query AS (
			SELECT to_tsquery('simple'::regconfig, $2) AS query
		)
		SELECT p.page_id, p.path, p.title, p.tags, p.description, p.body, p.source_refs, p.source_document, p.source_documents, p.format, p.okf_metadata
		FROM knowl_pages AS p
		CROSS JOIN lexical_query
		WHERE p.scope = $1
		  AND p.search_vector @@ lexical_query.query`
	arguments := make([]any, 0, len(sources)+3)
	arguments = append(arguments, scope, query)
	if len(sources) > 0 {
		placeholders := make([]string, len(sources))
		for index, source := range sources {
			placeholders[index] = fmt.Sprintf("$%d", index+3)
			arguments = append(arguments, source)
		}
		statement += ` AND EXISTS (
			SELECT 1 FROM knowl_page_sources AS source
			WHERE source.scope = p.scope AND source.page_id = p.page_id
			  AND source.source_id IN (` + strings.Join(placeholders, ", ") + `)
		)`
	}
	limitPlaceholder := fmt.Sprintf("$%d", len(arguments)+1)
	statement += `
		ORDER BY ts_rank_cd(ARRAY[0.1, 0.4, 0.7, 1.0]::real[], p.search_vector, lexical_query.query) DESC,
		         p.path ASC
		LIMIT ` + limitPlaceholder
	arguments = append(arguments, maxPageLimit)
	rows, err := reader.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, fmt.Errorf("search pages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var references []knowl.PageReference
	for rows.Next() {
		reference, err := projectionmeta.Reference(rows, terms, maxCharacters)
		if err != nil {
			return nil, fmt.Errorf("decode search reference: %w", err)
		}
		references = append(references, reference)
		if len(references) == limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search pages: %w", err)
	}
	return references, nil
}

// Links returns bounded, untrusted graph references.
func (store *Store) Links(ctx context.Context, scope knowl.ScopeRef, page knowl.PageID, limits knowl.ReadLimits) ([]knowl.LinkReference, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	limit := boundedLimit(limits.Pages)
	rows, err := store.db.QueryContext(ctx, `
		SELECT from_page, to_page, relation
		FROM knowl_links
		WHERE scope = $1 AND (from_page = $2 OR to_page = $2)
		ORDER BY from_page, to_page, relation
		LIMIT $3`, scope, page, limit)
	if err != nil {
		return nil, fmt.Errorf("read page links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var links []knowl.LinkReference
	for rows.Next() {
		var fromPage, toPage, relation string
		if err := rows.Scan(&fromPage, &toPage, &relation); err != nil {
			return nil, fmt.Errorf("scan page link: %w", err)
		}
		links = append(links, knowl.LinkReference{
			From: knowl.PageID(fromPage), To: knowl.PageID(toPage), Relation: relation, Untrusted: true,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate page links: %w", err)
	}
	return links, nil
}

func boundedLimit(limit int) int {
	if limit <= 0 {
		return defaultPageLimit
	}
	if limit > maxPageLimit {
		return maxPageLimit
	}
	return limit
}

func tsQuery(terms []string, operator string) string {
	return strings.Join(terms, " "+operator+" ")
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func validateScope(scope knowl.ScopeRef) error {
	if strings.TrimSpace(string(scope)) == "" {
		return fmt.Errorf("scope is required: %w", ErrConflict)
	}
	return nil
}
