package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// SelectContext returns deterministic recent page IDs for maintainer context.
func (store *Store) SelectContext(ctx context.Context, scope knowl.ScopeRef, _ knowl.SourceSummary, limits knowl.ReadLimits) ([]knowl.PageID, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	limit := boundedLimit(limits.Pages)
	rows, err := store.db.QueryContext(ctx, `
		SELECT page_id
		FROM knowl_pages
		WHERE scope = $1
		ORDER BY updated_at DESC, path ASC
		LIMIT $2`, scope, limit)
	if err != nil {
		return nil, fmt.Errorf("select context: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var pages []knowl.PageID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan context page: %w", err)
		}
		pages = append(pages, knowl.PageID(id))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate context pages: %w", err)
	}
	return pages, nil
}

// Search returns bounded, untrusted PostgreSQL full-text references.
func (store *Store) Search(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits) ([]knowl.PageReference, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return nil, ErrInvalidQuery
	}
	limit := boundedLimit(limits.Pages)
	rows, err := store.db.QueryContext(ctx, `
		SELECT page_id, path, title, body, source_refs
		FROM knowl_pages
		WHERE scope = $1
		  AND search_vector @@ plainto_tsquery('simple'::regconfig, $2)
		ORDER BY path ASC
		LIMIT $3`, scope, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search pages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var references []knowl.PageReference
	for rows.Next() {
		var reference knowl.PageReference
		var pageID, path, title, body string
		var sourceRefs []byte
		if err := rows.Scan(&pageID, &path, &title, &body, &sourceRefs); err != nil {
			return nil, fmt.Errorf("scan search page: %w", err)
		}
		if err := json.Unmarshal(sourceRefs, &reference.SourceRefs); err != nil {
			return nil, fmt.Errorf("decode page source refs: %w", err)
		}
		reference.ID = knowl.PageID(pageID)
		reference.Path = path
		reference.Title = title
		reference.Snippet = snippet(body, limits.Characters)
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

func snippet(body string, maxCharacters int) string {
	if maxCharacters <= 0 {
		return body
	}
	characters := []rune(body)
	if len(characters) <= maxCharacters {
		return body
	}
	return string(characters[:maxCharacters])
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
