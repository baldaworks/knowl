//go:build integration

package postgres

import (
	"encoding/json"
	"errors"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"slices"
	"testing"
)

const wikiPathOne = "Wiki / One"

func runPostgresWikiPathStorage(t *testing.T, store *Store) {
	oldProvider := store.embedding.Provider
	defer func() { store.embedding.Provider = oldProvider }()
	provider := &recordingProjectProvider{}
	store.embedding.Provider = provider

	snapshot := knowl.WorkspaceSnapshot{Scope: "wiki-paths", SchemaDigest: "wiki-path-schema", Pages: []knowl.PageSnapshot{
		{ID: "a", Path: "wiki/a.md", Title: "A", Digest: "a", Body: "Original alpha evidence", WikiPaths: []string{wikiPathOne, "Wiki / Two"}},
		{ID: "b", Path: "wiki/wiki-path-b.md", Title: "B", Digest: "b", Body: "Original beta evidence", WikiPaths: []string{"Wiki / Other"}},
	}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	if len(provider.inputs) != 2 {
		t.Fatalf("initial inputs=%v", provider.inputs)
	}
	var encoded []byte
	if err := store.db.QueryRowContext(t.Context(), `SELECT wiki_paths FROM knowl_pages WHERE scope=$1 AND page_id='a'`, snapshot.Scope).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var paths []string
	if err := json.Unmarshal(encoded, &paths); err != nil || !slices.Equal(paths, snapshot.Pages[0].WikiPaths) {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	// SQL reconstruction must authenticate the same input without citing wiki metadata.
	refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, "unmatchedneedle", knowl.ReadLimits{Pages: 2, Characters: 200}, nil)
	if err != nil || report.Effective != knowl.RetrievalHybrid || len(refs) != 2 {
		t.Fatalf("refs=%v report=%v err=%v", refs, report, err)
	}
	if refs[0].Snippet != "A\n\nOriginal alpha evidence" {
		t.Fatalf("original evidence=%q", refs[0].Snippet)
	}
	initial := len(provider.inputs)
	snapshot.Pages[0].WikiPaths = []string{"Wiki / Two", wikiPathOne, wikiPathOne}
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatalf("equivalent path order changed snapshot: %v", err)
	}
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if len(provider.inputs) != initial {
		t.Fatalf("equivalent paths reembedded: %v", provider.inputs[initial:])
	}
	snapshot.Pages[0].WikiPaths = []string{"Wiki / Moved"}
	if err := store.CheckProjection(t.Context(), snapshot); !errors.Is(err, ErrProjectionDrift) {
		t.Fatalf("path-only change readiness=%v", err)
	}
	if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if len(provider.inputs)-initial != 1 {
		t.Fatalf("path-only change embedded %d inputs", len(provider.inputs)-initial)
	}
	if provider.inputs[initial] != "Wiki / Moved\nA\nOriginal alpha evidence" {
		t.Fatalf("changed input=%q", provider.inputs[initial])
	}
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	refs, report, err = store.SearchWithReport(t.Context(), snapshot.Scope, "unmatchedneedle", knowl.ReadLimits{Pages: 2, Characters: 200}, nil)
	if err != nil || report.Effective != knowl.RetrievalHybrid || len(refs) != 2 || refs[0].Snippet != "A\n\nOriginal alpha evidence" {
		t.Fatalf("refs=%v report=%v err=%v", refs, report, err)
	}
	// A corrupt persisted context must not authenticate otherwise valid vectors.
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_pages SET wiki_paths='{"bad":1}' WHERE scope=$1 AND page_id='a'`, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.SearchWithReport(t.Context(), snapshot.Scope, "unmatchedneedle", knowl.ReadLimits{Pages: 2, Characters: 200}, nil)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
}
