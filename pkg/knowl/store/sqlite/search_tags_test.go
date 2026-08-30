package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
)

func TestOKFSearchTagsMigrationRebuildsSQLiteProjection(t *testing.T) {
	t.Parallel()
	content, err := migrationFiles.ReadFile("migrations/00010_okf_search_tags.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"ADD COLUMN tags TEXT NOT NULL DEFAULT ''", "title,\n    tags,\n    description,\n    body", "DELETE FROM knowl_projection_state", "DROP COLUMN tags"} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("OKF tag migration missing %q", required)
		}
	}

	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/okf-tags-migration.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(90, 0).UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO knowl_pages (scope, page_id, path, title, body, digest, source_refs, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "legacy", "page", "wiki/page.md", "Page", "body", "digest", "[]", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowl_projection_state (scope, schema_digest, snapshot_digest, page_count, link_count, ready_at) VALUES (?, ?, ?, ?, ?, ?)`, "legacy", "schema", "snapshot", 1, 0, now); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var tags string
	if err := db.QueryRowContext(ctx, `SELECT tags FROM knowl_pages WHERE scope = ? AND page_id = ?`, "legacy", "page").Scan(&tags); err != nil || tags != "" {
		t.Fatalf("migrated tags = %q, %v", tags, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT scope FROM knowl_projection_state WHERE scope = ?`, "legacy").Scan(new(string)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("projection readiness after tag migration = %v, want invalidated", err)
	}
	var ftsSchema string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'knowl_pages_fts'`).Scan(&ftsSchema); err != nil || !strings.Contains(ftsSchema, "tags") {
		t.Fatalf("FTS schema = %q, %v", ftsSchema, err)
	}
	if _, err := provider.DownTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT tags FROM knowl_pages LIMIT 1`).Scan(new(string)); err == nil || !strings.Contains(strings.ToLower(err.Error()), "no such column") {
		t.Fatalf("tags column after down migration error = %v", err)
	}
}

func TestSQLiteSearchIndexesOKFTagsWithFieldPriority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/okf-tags.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const term = "prioritybeacon"
	page := func(id, title, tags, description, body string) knowl.PageSnapshot {
		return knowl.PageSnapshot{
			ID: knowl.PageID(id), Path: "wiki/" + id + ".md", Title: title, Body: body, Content: body,
			Digest: "digest-" + id, SourceRefs: []string{"raw:" + id + "@1"},
			OKF: &okf.Metadata{Type: "Reference", Title: title, Tags: []string{tags}, Description: description},
		}
	}
	snapshot := knowl.WorkspaceSnapshot{Scope: "tags", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{
		page("title", term, "other", "neutral summary", "neutral body"),
		page("tags", "Tag page", term, "neutral summary", "neutral body"),
		page("description", "Description page", "other", term, "neutral body"),
		page("body", "Body page", "other", "neutral summary", term),
	}}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	results, err := store.Search(ctx, snapshot.Scope, term, knowl.ReadLimits{Pages: 10, Characters: 64}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []knowl.PageID{"title", "tags", "description", "body"}
	if len(results) != len(want) {
		t.Fatalf("tag search results = %#v", results)
	}
	for index, id := range want {
		if results[index].ID != id {
			t.Fatalf("tag search order = %#v, want %q", results, want)
		}
	}
	if !strings.Contains(results[1].Snippet, "tag: "+term) || results[1].OKF == nil || results[1].OKF.Tags[0] != term {
		t.Fatalf("tag evidence = %#v", results[1])
	}
}
