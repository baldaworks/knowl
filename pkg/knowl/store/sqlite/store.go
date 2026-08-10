// Package sqlite implements Knowl operational state and search projections
// using the same modernc.org/sqlite engine as Balda.
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var (
	ErrConflict           = errors.New("knowl operation conflict")
	ErrNotFound           = errors.New("knowl operation not found")
	ErrInvalidState       = errors.New("knowl operation state transition is invalid")
	ErrLeaseConflict      = errors.New("knowl operation lease is active")
	ErrInvalidQuery       = errors.New("knowl search query is invalid")
	ErrProjectionNotReady = errors.New("knowl projection is not ready")
	ErrProjectionDrift    = errors.New("knowl projection drift detected")
)

const (
	maxPageLimit     = 100
	defaultPageLimit = 20
)

// Store implements app.OperationStore and app.SearchIndex.
type Store struct {
	db   *sql.DB
	path string
	mu   sync.Mutex
}

var (
	_ app.OperationStore = (*Store)(nil)
	_ app.SearchIndex    = (*Store)(nil)
)

// Open opens or creates a Knowl SQLite operational store and runs migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if err := os.MkdirAll(filepath.Dir(trimmed), 0o700); err != nil {
		return nil, fmt.Errorf("create sqlite parent: %w", err)
	}
	db, err := sql.Open("sqlite", trimmed)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, path: trimmed}
	if err := store.configure(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the underlying database.
func (store *Store) Close() error { return store.db.Close() }

// Path returns the configured database path.
func (store *Store) Path() string { return store.path }

func (store *Store) configure(ctx context.Context) error {
	for _, statement := range []string{
		"PRAGMA foreign_keys=ON",
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			if statement == "PRAGMA journal_mode=WAL" {
				continue
			}
			return fmt.Errorf("apply sqlite pragma: %w", err)
		}
	}
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded sqlite migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, store.db, directory)
	if err != nil {
		return fmt.Errorf("create sqlite migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply sqlite migrations: %w", err)
	}
	return nil
}

// Reserve creates or returns the operation for one source revision.
func (store *Store) Reserve(ctx context.Context, key knowl.OperationKey, meta knowl.OperationMeta) (knowl.Operation, error) {
	if strings.TrimSpace(string(key.Scope)) == "" || strings.TrimSpace(key.Source.Adapter) == "" || strings.TrimSpace(key.Source.ID) == "" || strings.TrimSpace(key.Version.Version) == "" || strings.TrimSpace(key.Version.Digest) == "" {
		return knowl.Operation{}, fmt.Errorf("operation key is incomplete: %w", ErrConflict)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	var existingID, existingDigest string
	err := store.db.QueryRowContext(ctx, `
		SELECT operation_id, source_digest
		FROM knowl_operations
		WHERE scope = ? AND source_adapter = ? AND source_id = ? AND source_version = ?`,
		key.Scope, key.Source.Adapter, key.Source.ID, key.Version.Version).Scan(&existingID, &existingDigest)
	if err == nil {
		if existingDigest != key.Version.Digest {
			return knowl.Operation{}, ErrConflict
		}
		return store.Operation(ctx, key.Scope, knowl.OperationID(existingID))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return knowl.Operation{}, fmt.Errorf("inspect existing operation: %w", err)
	}
	now := meta.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	operationID := operationID(key)
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO knowl_operations (
			operation_id, scope, source_adapter, source_id, source_version, source_digest,
			schema_digest, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		operationID, key.Scope, key.Source.Adapter, key.Source.ID, key.Version.Version, key.Version.Digest,
		meta.SchemaDigest, knowl.StatusReceived, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return knowl.Operation{}, fmt.Errorf("reserve operation: %w", err)
	}
	return store.Operation(ctx, key.Scope, knowl.OperationID(operationID))
}

// SavePlan persists a redacted plan digest and advances the operation.
func (store *Store) SavePlan(ctx context.Context, id knowl.OperationID, summary knowl.PlanSummary) error {
	if strings.TrimSpace(summary.Digest) == "" {
		return fmt.Errorf("plan digest is required: %w", ErrConflict)
	}
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		if current.status == knowl.StatusPlanned {
			if current.planDigest == summary.Digest {
				return nil
			}
			return fmt.Errorf("plan digest differs: %w", ErrConflict)
		}
		if current.status != knowl.StatusReceived {
			return invalidTransition(current.status, knowl.StatusPlanned)
		}
		return updateOperationTx(ctx, tx, id, `status = ?, plan_digest = ?, updated_at = ?`, knowl.StatusPlanned, summary.Digest, nowString())
	})
}

// MarkAwaitingReview marks an operation that needs explicit apply.
func (store *Store) MarkAwaitingReview(ctx context.Context, id knowl.OperationID) error {
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		if current.status == knowl.StatusAwaitingReview {
			return nil
		}
		if current.status != knowl.StatusPlanned {
			return invalidTransition(current.status, knowl.StatusAwaitingReview)
		}
		return updateOperationTx(ctx, tx, id, `status = ?, updated_at = ?`, knowl.StatusAwaitingReview, nowString())
	})
}

// MarkApplying claims an operation with a lease.
func (store *Store) MarkApplying(ctx context.Context, id knowl.OperationID, lease knowl.Lease) error {
	if strings.TrimSpace(lease.Token) == "" || lease.ExpiresAt.IsZero() {
		return fmt.Errorf("lease token and expiry are required: %w", ErrConflict)
	}
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		now := time.Now().UTC()
		if current.status == knowl.StatusApplying {
			expiresAt, err := parseOptionalTime(current.leaseExpiresAt)
			if err != nil {
				return err
			}
			if expiresAt.After(now) {
				return ErrLeaseConflict
			}
		} else if current.status != knowl.StatusPlanned && current.status != knowl.StatusAwaitingReview {
			return invalidTransition(current.status, knowl.StatusApplying)
		}
		return updateOperationTx(ctx, tx, id, `status = ?, attempt = attempt + 1, lease_token = ?, lease_expires_at = ?, updated_at = ?`, knowl.StatusApplying, lease.Token, lease.ExpiresAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	})
}

// CommitOutcome marks a canonical content commit as complete.
func (store *Store) CommitOutcome(ctx context.Context, id knowl.OperationID, commit knowl.ContentCommit) error {
	if strings.TrimSpace(commit.Generation) == "" {
		return fmt.Errorf("commit generation is required: %w", ErrConflict)
	}
	if commit.OperationID != "" && knowl.OperationID(commit.OperationID) != id {
		return fmt.Errorf("commit belongs to %q, want %q: %w", commit.OperationID, id, ErrConflict)
	}
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		if current.status == knowl.StatusCommitted {
			if current.commitGeneration == commit.Generation {
				return nil
			}
			return fmt.Errorf("commit generation differs: %w", ErrConflict)
		}
		if current.status != knowl.StatusApplying {
			return invalidTransition(current.status, knowl.StatusCommitted)
		}
		return updateOperationTx(ctx, tx, id, `status = ?, commit_generation = ?, lease_token = '', lease_expires_at = '', updated_at = ?`, knowl.StatusCommitted, commit.Generation, nowString())
	})
}

// Fail records a stable redacted failure class.
func (store *Store) Fail(ctx context.Context, id knowl.OperationID, failure knowl.Failure) error {
	if strings.TrimSpace(failure.Class) == "" {
		return fmt.Errorf("failure class is required: %w", ErrConflict)
	}
	return store.transition(ctx, id, func(tx *sql.Tx, current operationRow) error {
		if current.status == knowl.StatusFailed {
			if current.failureClass == failure.Class {
				return nil
			}
			return fmt.Errorf("failure class differs: %w", ErrConflict)
		}
		if current.status == knowl.StatusCommitted {
			return invalidTransition(current.status, knowl.StatusFailed)
		}
		return updateOperationTx(ctx, tx, id, `status = ?, failure_class = ?, lease_token = '', lease_expires_at = '', updated_at = ?`, knowl.StatusFailed, failure.Class, nowString())
	})
}

// Operation reads one operation within its scope.
func (store *Store) Operation(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.Operation, error) {
	var operation knowl.Operation
	var sourceAdapter, sourceID, sourceVersion, sourceDigest, schemaDigest string
	var status, failureClass, updatedAt string
	var attempt int
	err := store.db.QueryRowContext(ctx, `
		SELECT operation_id, source_adapter, source_id, source_version, source_digest,
		       schema_digest, status, attempt, failure_class, updated_at
		FROM knowl_operations WHERE scope = ? AND operation_id = ?`, scope, id).
		Scan(&operation.ID, &sourceAdapter, &sourceID, &sourceVersion, &sourceDigest, &schemaDigest, &status, &attempt, &failureClass, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return knowl.Operation{}, ErrNotFound
	}
	if err != nil {
		return knowl.Operation{}, fmt.Errorf("read operation: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return knowl.Operation{}, fmt.Errorf("parse operation time: %w", err)
	}
	operation.Key = knowl.OperationKey{Scope: scope, Source: knowl.SourceRef{Adapter: sourceAdapter, ID: sourceID}, Version: knowl.SourceVersion{Version: sourceVersion, Digest: sourceDigest}}
	operation.Status = knowl.OperationStatus(status)
	operation.Attempt = attempt
	operation.UpdatedAt = parsed
	if failureClass != "" {
		operation.Failure = &knowl.Failure{Class: failureClass, OperationID: string(operation.ID)}
	}
	_ = schemaDigest
	return operation, nil
}

type operationRow struct {
	status           knowl.OperationStatus
	planDigest       string
	commitGeneration string
	failureClass     string
	leaseExpiresAt   string
}

func (store *Store) transition(ctx context.Context, id knowl.OperationID, update func(*sql.Tx, operationRow) error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin operation transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var current operationRow
	var status string
	if err := tx.QueryRowContext(ctx, `
		SELECT status, plan_digest, commit_generation, failure_class, lease_expires_at
		FROM knowl_operations WHERE operation_id = ?`, id).
		Scan(&status, &current.planDigest, &current.commitGeneration, &current.failureClass, &current.leaseExpiresAt); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("read operation transition: %w", err)
	}
	current.status = knowl.OperationStatus(status)
	if err := update(tx, current); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit operation transition: %w", err)
	}
	return nil
}

func updateOperationTx(ctx context.Context, tx *sql.Tx, id knowl.OperationID, assignments string, args ...any) error {
	result, err := tx.ExecContext(ctx, `UPDATE knowl_operations SET `+assignments+` WHERE operation_id = ?`, append(args, id)...)
	if err != nil {
		return fmt.Errorf("update operation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect operation update: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func invalidTransition(from, to knowl.OperationStatus) error {
	return fmt.Errorf("cannot transition operation from %q to %q: %w", from, to, ErrInvalidState)
}

func parseOptionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse lease expiry: %w", err)
	}
	return parsed, nil
}

func nowString() string { return time.Now().UTC().Format(time.RFC3339Nano) }

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

func operationID(key knowl.OperationKey) string {
	return fmt.Sprintf("%s:%s:%s@%s#%s", key.Scope, key.Source.Adapter, key.Source.ID, key.Version.Version, key.Version.Digest[:minInt(len(key.Version.Digest), 16)])
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
