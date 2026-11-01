package sqlite

import (
	"encoding/json"
	"testing"
)

func TestWikiPathsLegacyDefault(t *testing.T) {
	store, snapshot := nativeSearchFixture(t, nil)
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_pages(scope,page_id,path,title,body,digest,updated_at) VALUES(?,?,?,?,?,?,?)`, snapshot.Scope, "legacy-wiki-path", "legacy.md", "Legacy", "Body", "digest", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var encoded []byte
	if err := store.db.QueryRowContext(t.Context(), `SELECT wiki_paths FROM knowl_pages WHERE scope=?`, snapshot.Scope).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var paths []string
	if err := json.Unmarshal(encoded, &paths); err != nil || len(paths) != 0 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
}
