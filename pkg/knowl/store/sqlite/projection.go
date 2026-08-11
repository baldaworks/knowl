package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// Project indexes a canonical content commit snapshot.
func (store *Store) Project(ctx context.Context, commit knowl.ContentCommit) error {
	return store.Rebuild(ctx, commit.Snapshot)
}

// Rebuild recreates all projections from canonical Markdown snapshots.
func (store *Store) Rebuild(ctx context.Context, snapshot knowl.WorkspaceSnapshot) error {
	if err := validateScope(snapshot.Scope); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin projection rebuild: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	defer rollback()
	for _, statement := range []string{"DELETE FROM knowl_pages_fts WHERE scope = ?", "DELETE FROM knowl_links WHERE scope = ?", "DELETE FROM knowl_pages WHERE scope = ?", "DELETE FROM knowl_projection_state WHERE scope = ?"} {
		var execErr error
		_, execErr = tx.ExecContext(ctx, statement, snapshot.Scope)
		if execErr != nil {
			return fmt.Errorf("clear projection: %w", execErr)
		}
	}
	now := snapshot.CapturedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	for _, page := range snapshot.Pages {
		sourceRefs, marshalErr := json.Marshal(page.SourceRefs)
		if marshalErr != nil {
			return fmt.Errorf("encode page source refs: %w", marshalErr)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowl_pages (page_id, scope, path, title, body, digest, source_refs, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(scope, path) DO UPDATE SET page_id=excluded.page_id, title=excluded.title, body=excluded.body, digest=excluded.digest, source_refs=excluded.source_refs, updated_at=excluded.updated_at`,
			page.ID, snapshot.Scope, page.Path, page.Title, page.Content, page.Digest, sourceRefs, now.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("project page %q: %w", page.Path, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowl_pages_fts (page_id, scope, path, title, body, source_refs) VALUES (?, ?, ?, ?, ?, ?)`, page.ID, snapshot.Scope, page.Path, page.Title, page.Content, sourceRefs); err != nil {
			return fmt.Errorf("project page search %q: %w", page.Path, err)
		}
	}
	for _, link := range snapshot.Links {
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowl_links (scope, from_page, to_page, relation) VALUES (?, ?, ?, ?)`, snapshot.Scope, link.From, link.To, link.Relation); err != nil {
			return fmt.Errorf("project link: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO knowl_projection_state (scope, schema_digest, snapshot_digest, page_count, link_count, ready_at)
		VALUES (?, ?, ?, ?, ?, ?)`, snapshot.Scope, snapshot.SchemaDigest, snapshotDigest(snapshot), len(snapshot.Pages), len(snapshot.Links), now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record projection readiness: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit projection rebuild: %w", err)
	}
	return nil
}

// ProjectionState describes the last canonical snapshot projected for a scope.
type ProjectionState struct {
	Scope          knowl.ScopeRef
	SchemaDigest   string
	SnapshotDigest string
	PageCount      int
	LinkCount      int
	ReadyAt        time.Time
}

// ProjectionStatus returns readiness metadata for a scope.
func (store *Store) ProjectionStatus(ctx context.Context, scope knowl.ScopeRef) (ProjectionState, error) {
	if err := validateScope(scope); err != nil {
		return ProjectionState{}, err
	}
	var state ProjectionState
	var readyAt string
	err := store.db.QueryRowContext(ctx, `
		SELECT schema_digest, snapshot_digest, page_count, link_count, ready_at
		FROM knowl_projection_state WHERE scope = ?`, scope).
		Scan(&state.SchemaDigest, &state.SnapshotDigest, &state.PageCount, &state.LinkCount, &readyAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectionState{}, ErrProjectionNotReady
	}
	if err != nil {
		return ProjectionState{}, fmt.Errorf("read projection status: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, readyAt)
	if err != nil {
		return ProjectionState{}, fmt.Errorf("parse projection readiness: %w", err)
	}
	state.Scope = scope
	state.ReadyAt = parsed
	return state, nil
}

// CheckProjection verifies that a projection represents the supplied snapshot.
func (store *Store) CheckProjection(ctx context.Context, snapshot knowl.WorkspaceSnapshot) error {
	state, err := store.ProjectionStatus(ctx, snapshot.Scope)
	if err != nil {
		return err
	}
	if state.SchemaDigest != snapshot.SchemaDigest || state.SnapshotDigest != snapshotDigest(snapshot) {
		return ErrProjectionDrift
	}
	return nil
}

func snapshotDigest(snapshot knowl.WorkspaceSnapshot) string {
	type digestPage struct {
		ID         knowl.PageID
		Path       string
		Digest     string
		Title      string
		Content    string
		SourceRefs []string
	}
	type digestLink struct {
		From     knowl.PageID
		To       knowl.PageID
		Relation string
	}
	pages := make([]digestPage, 0, len(snapshot.Pages))
	for _, page := range snapshot.Pages {
		sourceRefs := append([]string(nil), page.SourceRefs...)
		sort.Strings(sourceRefs)
		pages = append(pages, digestPage{ID: page.ID, Path: page.Path, Digest: page.Digest, Title: page.Title, Content: page.Content, SourceRefs: sourceRefs})
	}
	sort.Slice(pages, func(left, right int) bool {
		if pages[left].Path == pages[right].Path {
			return pages[left].ID < pages[right].ID
		}
		return pages[left].Path < pages[right].Path
	})
	links := make([]digestLink, 0, len(snapshot.Links))
	for _, link := range snapshot.Links {
		links = append(links, digestLink{From: link.From, To: link.To, Relation: link.Relation})
	}
	sort.Slice(links, func(left, right int) bool {
		if links[left].From == links[right].From {
			if links[left].To == links[right].To {
				return links[left].Relation < links[right].Relation
			}
			return links[left].To < links[right].To
		}
		return links[left].From < links[right].From
	})
	payload := struct {
		Scope        knowl.ScopeRef
		SchemaDigest string
		PageDigests  map[string]string
		Pages        []digestPage
		Links        []digestLink
	}{Scope: snapshot.Scope, SchemaDigest: snapshot.SchemaDigest, PageDigests: snapshot.PageDigests, Pages: pages, Links: links}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
