package sqlite

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
	values := make([]string, len(seeds))
	arguments := make([]any, 0, len(seeds)*2+3)
	for index, seed := range seeds {
		values[index] = "(?, ?)"
		arguments = append(arguments, seed, index)
	}
	arguments = append(arguments, scope, scope, limit)
	statement := `
		WITH seeds(page_id, ordinal) AS (VALUES ` + strings.Join(values, ", ") + `)
		SELECT neighbor.page_id
		FROM seeds
		JOIN knowl_links AS link
		  ON link.scope = ?
		 AND (link.from_page = seeds.page_id OR link.to_page = seeds.page_id)
		JOIN knowl_pages AS neighbor
		  ON neighbor.scope = ?
		 AND neighbor.page_id = CASE
		       WHEN link.from_page = seeds.page_id THEN link.to_page
		       ELSE link.from_page
		     END
		WHERE neighbor.page_id <> seeds.page_id
		GROUP BY neighbor.page_id, neighbor.path
		ORDER BY MIN(seeds.ordinal) ASC, neighbor.path ASC
		LIMIT ?`
	rows, err := store.db.QueryContext(ctx, statement, arguments...)
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
	arguments := make([]any, 0, len(excluded)+2)
	arguments = append(arguments, scope)
	statement := `SELECT page_id FROM knowl_pages WHERE scope = ?`
	if len(excluded) > 0 {
		placeholders := make([]string, len(excluded))
		for index, id := range excluded {
			placeholders[index] = "?"
			arguments = append(arguments, id)
		}
		statement += ` AND page_id NOT IN (` + strings.Join(placeholders, ", ") + `)`
	}
	statement += ` ORDER BY updated_at DESC, path ASC LIMIT ?`
	arguments = append(arguments, limit)
	rows, err := store.db.QueryContext(ctx, statement, arguments...)
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

// Search returns bounded, untrusted FTS references.
func (store *Store) Search(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits) ([]knowl.PageReference, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	normalized, err := lexical.Normalize(query)
	if err != nil {
		return nil, fmt.Errorf("normalize search query: %w", ErrInvalidQuery)
	}
	limit := boundedLimit(limits.Pages)
	strict, err := store.searchPhase(ctx, scope, ftsQuery(normalized.Terms, "AND"), limit, limits.Characters, normalized.Terms)
	if err != nil {
		return nil, err
	}
	if len(strict) >= limit || len(normalized.Terms) == 1 {
		return strict, nil
	}

	relaxed, err := store.searchPhase(ctx, scope, ftsQuery(normalized.Terms, "OR"), limit, limits.Characters, normalized.Terms)
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

func (store *Store) searchPhase(ctx context.Context, scope knowl.ScopeRef, match string, limit, maxCharacters int, terms []string) ([]knowl.PageReference, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT page_id, path, title, body, source_refs,
		       snippet(knowl_pages_fts, -1, '', '', ' … ', 64)
		FROM knowl_pages_fts
		WHERE knowl_pages_fts MATCH ? AND scope = ?
		ORDER BY bm25(knowl_pages_fts, 0.0, 0.0, 0.0, 8.0, 1.0, 0.0) ASC, path ASC
		LIMIT ?`, match, scope, limit)
	if err != nil {
		return nil, fmt.Errorf("search pages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var references []knowl.PageReference
	for rows.Next() {
		var reference knowl.PageReference
		var body, sourceRefs, nativeSnippet string
		if err := rows.Scan(&reference.ID, &reference.Path, &reference.Title, &body, &sourceRefs, &nativeSnippet); err != nil {
			return nil, fmt.Errorf("scan search page: %w", err)
		}
		if err := json.Unmarshal([]byte(sourceRefs), &reference.SourceRefs); err != nil {
			return nil, fmt.Errorf("decode page source refs: %w", err)
		}
		reference.Snippet = lexical.Excerpt(nativeSnippet, reference.Title, body, terms, maxCharacters)
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
		WHERE scope = ? AND (from_page = ? OR to_page = ?)
		ORDER BY from_page, to_page, relation LIMIT ?`, scope, page, page, limit)
	if err != nil {
		return nil, fmt.Errorf("read page links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var links []knowl.LinkReference
	for rows.Next() {
		var link knowl.LinkReference
		if err := rows.Scan(&link.From, &link.To, &link.Relation); err != nil {
			return nil, fmt.Errorf("scan page link: %w", err)
		}
		link.Untrusted = true
		links = append(links, link)
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

func ftsQuery(terms []string, operator string) string {
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ReplaceAll(term, `"`, `""`)
		quoted = append(quoted, `"`+term+`"`)
	}
	return strings.Join(quoted, " "+operator+" ")
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
