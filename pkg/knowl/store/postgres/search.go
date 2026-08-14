package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/contextpolicy"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// SelectContext returns source-relevant pages, one-hop context, the required
// index control page, and only then deterministic recent fallback pages.
func (store *Store) SelectContext(ctx context.Context, scope knowl.ScopeRef, source knowl.SourceSummary, limits knowl.ReadLimits) ([]knowl.PageID, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	limit := boundedLimit(limits.Pages)
	query := lexical.Summarize(source.Title, source.Source.ID, source.Source.Adapter)
	candidates, err := store.contextCandidates(ctx, scope, query.Terms, contextpolicy.CandidateLimit(limit))
	if err != nil {
		return nil, err
	}
	neighbors, err := store.contextNeighbors(ctx, scope, candidates, max(0, limit-1))
	if err != nil {
		return nil, err
	}
	var recent []knowl.PageID
	if len(contextpolicy.Merge(limit, candidates, neighbors, nil)) < limit {
		excluded := append(append([]knowl.PageID(nil), candidates...), neighbors...)
		recent, err = store.recentContext(ctx, scope, excluded, limit)
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
	references, err := store.Search(ctx, scope, strings.Join(terms, " "), knowl.ReadLimits{Pages: limit, Characters: 1})
	if err != nil {
		return nil, fmt.Errorf("select relevant context: %w", err)
	}
	ids := make([]knowl.PageID, 0, len(references))
	for _, reference := range references {
		ids = append(ids, reference.ID)
	}
	return ids, nil
}

func (store *Store) contextNeighbors(ctx context.Context, scope knowl.ScopeRef, seeds []knowl.PageID, limit int) ([]knowl.PageID, error) {
	if len(seeds) == 0 || limit <= 0 {
		return nil, nil
	}
	seedValues := make([]string, len(seeds))
	for index, seed := range seeds {
		seedValues[index] = string(seed)
	}
	rows, err := store.db.QueryContext(ctx, `
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

func (store *Store) recentContext(ctx context.Context, scope knowl.ScopeRef, excluded []knowl.PageID, limit int) ([]knowl.PageID, error) {
	excludedValues := make([]string, len(excluded))
	for index, id := range excluded {
		excludedValues[index] = string(id)
	}
	rows, err := store.db.QueryContext(ctx, `
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
func (store *Store) Search(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits) ([]knowl.PageReference, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	normalized, err := lexical.Normalize(query)
	if err != nil {
		return nil, fmt.Errorf("normalize search query: %w", ErrInvalidQuery)
	}
	limit := boundedLimit(limits.Pages)
	strict, err := store.searchPhase(ctx, scope, tsQuery(normalized.Terms, "&"), limit, limits.Characters, normalized.Terms)
	if err != nil {
		return nil, err
	}
	if len(strict) >= limit || len(normalized.Terms) == 1 {
		return strict, nil
	}

	relaxed, err := store.searchPhase(ctx, scope, tsQuery(normalized.Terms, "|"), limit, limits.Characters, normalized.Terms)
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

func (store *Store) searchPhase(ctx context.Context, scope knowl.ScopeRef, query string, limit, maxCharacters int, terms []string) ([]knowl.PageReference, error) {
	rows, err := store.db.QueryContext(ctx, `
		WITH lexical_query AS (
			SELECT to_tsquery('simple'::regconfig, $2) AS query
		)
		SELECT page_id, path, title, body, source_refs,
		       ts_headline(
		           'simple'::regconfig,
		           title || E'\n' || body,
		           lexical_query.query,
		           'StartSel=, StopSel=, MaxFragments=1, MinWords=1, MaxWords=64, FragmentDelimiter= … '
		       )
		FROM knowl_pages
		CROSS JOIN lexical_query
		WHERE scope = $1
		  AND search_vector @@ lexical_query.query
		ORDER BY ts_rank_cd(ARRAY[0.01, 0.05, 0.125, 1.0]::real[], search_vector, lexical_query.query) DESC,
		         path ASC
		LIMIT $3`, scope, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search pages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var references []knowl.PageReference
	for rows.Next() {
		var reference knowl.PageReference
		var pageID, path, title, body, nativeSnippet string
		var sourceRefs []byte
		if err := rows.Scan(&pageID, &path, &title, &body, &sourceRefs, &nativeSnippet); err != nil {
			return nil, fmt.Errorf("scan search page: %w", err)
		}
		if err := json.Unmarshal(sourceRefs, &reference.SourceRefs); err != nil {
			return nil, fmt.Errorf("decode page source refs: %w", err)
		}
		reference.ID = knowl.PageID(pageID)
		reference.Path = path
		reference.Title = title
		reference.Snippet = lexical.Excerpt(nativeSnippet, title, body, terms, maxCharacters)
		reference.Untrusted = true
		references = append(references, reference)
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
