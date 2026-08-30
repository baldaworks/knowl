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

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/projectionmeta"
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
	for _, statement := range []string{"DELETE FROM knowl_pages_fts WHERE scope = ?", "DELETE FROM knowl_links WHERE scope = ?", "DELETE FROM knowl_page_sources WHERE scope = ?", "DELETE FROM knowl_pages WHERE scope = ?", "DELETE FROM knowl_projection_state WHERE scope = ?"} {
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
	semanticPages := make([]knowl.PageSnapshot, 0, len(snapshot.Pages))
	excludedPageIDs := make(map[knowl.PageID]struct{})
	for _, page := range snapshot.Pages {
		if !projectionmeta.SemanticPage(page) {
			excludedPageIDs[page.ID] = struct{}{}
			continue
		}
		semanticPages = append(semanticPages, page)
	}
	for _, page := range semanticPages {
		values, valuesErr := projectionmeta.ValuesForPage(page)
		if valuesErr != nil {
			return fmt.Errorf("project page %q: %w", page.Path, valuesErr)
		}
		updatedAt := page.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = now
		}
		sourceRefs, marshalErr := json.Marshal(page.SourceRefs)
		if marshalErr != nil {
			return fmt.Errorf("encode page source refs: %w", marshalErr)
		}
		documents := projectionmeta.SourceDocuments(page)
		encodedDocuments, encodeErr := json.Marshal(documents)
		if encodeErr != nil {
			return fmt.Errorf("encode page source documents: %w", encodeErr)
		}
		var sourceID any
		var sourceDocument any
		legacyDocument := page.SourceDocument
		if legacyDocument == nil && len(documents) > 0 {
			legacyDocument = &documents[0]
		}
		if legacyDocument != nil {
			sourceID = string(legacyDocument.SourceID)
			encodedDocument, encodeErr := json.Marshal(legacyDocument)
			if encodeErr != nil {
				return fmt.Errorf("encode page source document: %w", encodeErr)
			}
			sourceDocument = string(encodedDocument)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowl_pages (page_id, scope, path, title, tags, description, body, digest, source_refs, source_id, source_document, source_documents, format, okf_metadata, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(scope, path) DO UPDATE SET page_id=excluded.page_id, title=excluded.title, tags=excluded.tags, description=excluded.description, body=excluded.body, digest=excluded.digest, source_refs=excluded.source_refs, source_id=excluded.source_id, source_document=excluded.source_document, source_documents=excluded.source_documents, format=excluded.format, okf_metadata=excluded.okf_metadata, updated_at=excluded.updated_at`,
			page.ID, snapshot.Scope, page.Path, page.Title, values.Tags, values.Description, values.Body, page.Digest, sourceRefs, sourceID, sourceDocument, encodedDocuments, values.Format, nullableJSON(values.Metadata), updatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("project page %q: %w", page.Path, err)
		}
		for _, document := range documents {
			if _, err := tx.ExecContext(ctx, `INSERT INTO knowl_page_sources (scope, page_id, source_id, document_id, revision, uri) VALUES (?, ?, ?, ?, ?, ?)`, snapshot.Scope, page.ID, document.SourceID, document.DocumentID, document.Revision, document.URI); err != nil {
				return fmt.Errorf("project page source %q: %w", page.Path, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowl_pages_fts (page_id, scope, path, title, tags, description, body, source_refs) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, page.ID, snapshot.Scope, page.Path, page.Title, values.Tags, values.Description, values.Body, sourceRefs); err != nil {
			return fmt.Errorf("project page search %q: %w", page.Path, err)
		}
	}
	projectedLinks := 0
	for _, link := range snapshot.Links {
		if _, excluded := excludedPageIDs[link.From]; excluded {
			continue
		}
		if _, excluded := excludedPageIDs[link.To]; excluded {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowl_links (scope, from_page, to_page, relation) VALUES (?, ?, ?, ?)`, snapshot.Scope, link.From, link.To, link.Relation); err != nil {
			return fmt.Errorf("project link: %w", err)
		}
		projectedLinks++
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO knowl_projection_state (scope, schema_digest, snapshot_digest, page_count, link_count, ready_at)
		VALUES (?, ?, ?, ?, ?, ?)`, snapshot.Scope, snapshot.SchemaDigest, snapshotDigest(snapshot), len(semanticPages), projectedLinks, now.Format(time.RFC3339Nano)); err != nil {
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
		ID              knowl.PageID
		Path            string
		Digest          string
		Title           string
		Format          string
		Tags            string
		Description     string
		Body            string
		OKF             json.RawMessage
		SourceRefs      []string
		SourceDocument  *knowl.SourceDocument
		SourceDocuments []knowl.SourceDocument
		UpdatedAt       time.Time
	}
	type digestLink struct {
		From     knowl.PageID
		To       knowl.PageID
		Relation string
	}
	pages := make([]digestPage, 0, len(snapshot.Pages))
	for _, page := range snapshot.Pages {
		values, err := projectionmeta.ValuesForPage(page)
		if err != nil {
			return ""
		}
		sourceRefs := append([]string(nil), page.SourceRefs...)
		sort.Strings(sourceRefs)
		pages = append(pages, digestPage{
			ID: page.ID, Path: page.Path, Digest: page.Digest, Title: page.Title,
			Format: values.Format, Tags: values.Tags, Description: values.Description, Body: values.Body, OKF: values.Metadata,
			SourceRefs: sourceRefs, SourceDocument: page.SourceDocument,
			SourceDocuments: projectionmeta.SourceDocuments(page),
			UpdatedAt:       page.UpdatedAt.UTC(),
		})
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

func nullableJSON(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}
