package sqlite

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
	rows, err := store.db.QueryContext(ctx, `SELECT page_id FROM knowl_pages WHERE scope = ? ORDER BY updated_at DESC, path ASC LIMIT ?`, scope, limit)
	if err != nil {
		return nil, fmt.Errorf("select context: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var pages []knowl.PageID
	for rows.Next() {
		var id knowl.PageID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan context page: %w", err)
		}
		pages = append(pages, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate context pages: %w", err)
	}
	return pages, nil
}

// Search returns bounded, untrusted FTS references.
func (store *Store) Search(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits) ([]knowl.PageReference, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	match := ftsQuery(query)
	if match == "" {
		return nil, ErrInvalidQuery
	}
	limit := boundedLimit(limits.Pages)
	rows, err := store.db.QueryContext(ctx, `
		SELECT page_id, path, title, body, source_refs
		FROM knowl_pages_fts
		WHERE knowl_pages_fts MATCH ? AND scope = ?
		ORDER BY path ASC LIMIT ?`, match, scope, limit)
	if err != nil {
		return nil, fmt.Errorf("search pages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var references []knowl.PageReference
	for rows.Next() {
		var reference knowl.PageReference
		var body, sourceRefs string
		if err := rows.Scan(&reference.ID, &reference.Path, &reference.Title, &body, &sourceRefs); err != nil {
			return nil, fmt.Errorf("scan search page: %w", err)
		}
		if err := json.Unmarshal([]byte(sourceRefs), &reference.SourceRefs); err != nil {
			return nil, fmt.Errorf("decode page source refs: %w", err)
		}
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

func ftsQuery(raw string) string {
	fields := strings.Fields(raw)
	quoted := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.ReplaceAll(field, `"`, `""`)
		quoted = append(quoted, `"`+field+`"`)
	}
	return strings.Join(quoted, " AND ")
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
