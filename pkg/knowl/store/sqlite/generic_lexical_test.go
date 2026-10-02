package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
)

const (
	testGenericAccentID = "accent"
	testGenericASCIIID  = "ascii"
)

func genericSQLiteSnapshot() knowl.WorkspaceSnapshot {
	snapshot := knowl.WorkspaceSnapshot{Scope: "generic", SchemaDigest: testSchemaDigest, CapturedAt: time.Unix(100, 0).UTC()}
	for _, data := range [][3]string{{testGenericAccentID, "Café", "original cafe\u0301 evidence"}, {testGenericASCIIID, "Cafe", "ordinary prose"}, {"question", "Question", "What is WHY"}, {"cyrillic", "ХРАНИЛИЩЕ", "technical storage"}} {
		snapshot.Pages = append(snapshot.Pages, knowl.PageSnapshot{ID: knowl.PageID(data[0]), Path: "wiki/" + data[0] + ".md", Title: data[1], Body: data[2], Content: data[2], Digest: "digest-" + data[0], SourceRefs: []string{"raw:" + data[0] + "@1"}, SourceDocuments: []knowl.SourceDocument{{SourceID: testSourceID, DocumentID: knowl.DocumentID(data[0]), Revision: testRevision}}})
	}
	return snapshot
}

func TestSQLiteGenericLiteralAndOriginalEvidence(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, t.TempDir()+"/literal.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := genericSQLiteSnapshot()
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query string
		want  knowl.PageID
	}{{"café", testGenericAccentID}, {"CAFE\u0301", testGenericAccentID}, {"cafe", testGenericASCIIID}, {"what", "question"}, {"хранилище", "cyrillic"}} {
		refs, err := store.Search(ctx, snapshot.Scope, test.query, knowl.ReadLimits{Pages: 10, Characters: 80}, nil)
		if err != nil || len(refs) != 1 || refs[0].ID != test.want {
			t.Fatalf("query %q results=%v error=%v", test.query, refs, err)
		}
		original := snapshot.Pages[slices.IndexFunc(snapshot.Pages, func(p knowl.PageSnapshot) bool { return p.ID == test.want })]
		if refs[0].Title != original.Title || !slices.Equal(refs[0].SourceRefs, original.SourceRefs) || refs[0].Snippet != original.Title+"\n\n"+original.Body {
			t.Fatalf("original evidence changed: %#v", refs[0])
		}
		ids, err := store.SelectContext(ctx, snapshot.Scope, knowl.SourceSummary{Title: test.query}, knowl.ReadLimits{Pages: 1})
		if err != nil || !slices.Equal(ids, []knowl.PageID{test.want}) {
			t.Fatalf("source %q ids=%v error=%v", test.query, ids, err)
		}
	}
	if _, err := store.Search(ctx, snapshot.Scope, "café", knowl.ReadLimits{}, []knowl.SourceID{"Invalid"}); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("direct source-filter error=%v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.Search(canceled, snapshot.Scope, "café", knowl.ReadLimits{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestSQLiteGenericProjectionFailureRollsBack(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/rollback.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := genericSQLiteSnapshot()
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	before, err := store.ProjectionStatus(t.Context(), snapshot.Scope)
	if err != nil {
		t.Fatal(err)
	}
	broken := snapshot
	broken.Pages = append(slices.Clone(snapshot.Pages), knowl.PageSnapshot{ID: "overflow", Path: "wiki/overflow.md", Title: "Overflow", Body: strings.Repeat("a ", 131073), Digest: "overflow"})
	if err := store.Rebuild(t.Context(), broken); !errors.Is(err, lexical.ErrInvalidProjection) {
		t.Fatalf("overflow error=%v", err)
	}
	after, err := store.ProjectionStatus(t.Context(), snapshot.Scope)
	if err != nil || before != after {
		t.Fatalf("partial readiness: before=%v after=%v error=%v", before, after, err)
	}
	refs, err := store.Search(t.Context(), snapshot.Scope, "cafe", knowl.ReadLimits{}, nil)
	if err != nil || len(refs) != 1 || refs[0].ID != testGenericASCIIID {
		t.Fatalf("previous index lost: %v %v", refs, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.Rebuild(canceled, broken); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled rebuild=%v", err)
	}
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatalf("rollback changed snapshot identity: %v", err)
	}
}

func TestSQLiteGenericMigrationPreservesHistoryAndRebuild(t *testing.T) {
	ctx := t.Context()
	filename := t.TempDir() + "/upgrade.sqlite"
	db, err := sql.Open("sqlite", filename)
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
	if _, err := provider.UpTo(ctx, 14); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: db, path: filename}
	key, meta := executionFixture("generic", "upgrade", time.Unix(1, 0).UTC())
	operationID, err := app.SourceOperationID(key)
	if err != nil {
		t.Fatal(err)
	}
	created := meta.CreatedAt.Format(time.RFC3339Nano)
	acceptedDocument, err := encodeAcceptedSourceDocument(meta.AcceptedSource.SourceDocument)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowl_operations(operation_id,scope,source_adapter,source_id,source_version,source_digest,schema_digest,status,created_at,updated_at,work_ready_at,accepted_media_type,source_manifest_ref,accepted_source_document,schema_version,schema_snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, operationID, key.Scope, key.Source.Adapter, key.Source.ID, key.Version.Version, key.Version.Digest, meta.SchemaDigest, knowl.StatusReceived, created, created, created, meta.AcceptedSource.MediaType, meta.AcceptedSource.ManifestRef, acceptedDocument, meta.Schema.Version, meta.Schema.Content); err != nil {
		t.Fatal(err)
	}
	operationBefore := knowl.Operation{ID: operationID, Kind: knowl.WorkSourceMaintenance, Key: key, Status: knowl.StatusReceived, ReadyAt: meta.CreatedAt, UpdatedAt: meta.CreatedAt, Diagnostics: []knowl.MaintenanceDiagnostic{}}
	run := sqliteSourceRun("generic-upgrade", time.Unix(50, 0).UTC())
	state := sqliteDocumentState(run, false, time.Time{})
	finalizeSQLiteSourceRun(t, ctx, legacy, run, state, app.SyncDocumentActive, "checkpoint", "generation", run.StartedAt.Add(time.Second))
	sourceBefore, err := legacy.SourceStatus(ctx, run.Scope, run.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	documentBefore, err := legacy.DocumentState(ctx, run.Scope, run.SourceID, state.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := genericSQLiteSnapshot()
	page := snapshot.Pages[0]
	timestamp := snapshot.CapturedAt.Format(time.RFC3339Nano)
	for _, statement := range []string{
		`INSERT INTO knowl_pages (scope,page_id,path,title,body,digest,source_refs,updated_at) VALUES ('generic','accent','wiki/accent.md','Café','original café evidence','original-digest','[]','` + timestamp + `')`,
		`INSERT INTO knowl_pages_fts (scope,page_id,path,title,body,source_refs) VALUES ('generic','accent','wiki/accent.md','Café','original café evidence','[]')`,
		`INSERT INTO knowl_projection_state (scope,schema_digest,snapshot_digest,page_count,link_count,ready_at) VALUES ('generic','schema','old',1,0,'` + timestamp + `')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	if _, err := current.ProjectionStatus(ctx, snapshot.Scope); !errors.Is(err, ErrProjectionNotReady) {
		t.Fatalf("old index still ready: %v", err)
	}
	refs, err := current.Search(ctx, snapshot.Scope, "café", knowl.ReadLimits{}, nil)
	if err != nil || len(refs) != 0 {
		t.Fatalf("old tokens served before rebuild: %v %v", refs, err)
	}
	var title, body, digest string
	if err := current.db.QueryRowContext(ctx, `SELECT title,body,digest FROM knowl_pages WHERE scope=? AND page_id=?`, snapshot.Scope, page.ID).Scan(&title, &body, &digest); err != nil || title != page.Title || body != page.Body || digest != "original-digest" {
		t.Fatalf("original row changed: %q %q %q %v", title, body, digest, err)
	}
	preserved, err := current.Operation(ctx, key.Scope, operationID)
	if err != nil || !reflect.DeepEqual(preserved, operationBefore) {
		t.Fatalf("operation changed: got=%#v want=%#v err=%v", preserved, operationBefore, err)
	}
	sourceAfter, err := current.SourceStatus(ctx, run.Scope, run.SourceID)
	if err != nil || !reflect.DeepEqual(sourceAfter, sourceBefore) {
		t.Fatalf("source changed: %v", err)
	}
	documentAfter, err := current.DocumentState(ctx, run.Scope, run.SourceID, state.DocumentID)
	if err != nil || !reflect.DeepEqual(documentAfter, documentBefore) {
		t.Fatalf("document changed: %v", err)
	}
	if err := current.Rebuild(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	before, err := current.Search(ctx, snapshot.Scope, "café", knowl.ReadLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	after, err := reopened.Search(ctx, snapshot.Scope, "café", knowl.ReadLimits{}, nil)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("reopen changed results: %v", err)
	}
	if err := reopened.CheckProjection(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	provider, err = goose.NewProvider(goose.DialectSQLite3, reopened.db, directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 14); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ProjectionStatus(ctx, snapshot.Scope); !errors.Is(err, ErrProjectionNotReady) {
		t.Fatalf("down migration readiness=%v", err)
	}
	var count int
	if err := reopened.db.QueryRowContext(ctx, `SELECT count(*) FROM knowl_pages`).Scan(&count); err != nil || count != len(snapshot.Pages) {
		t.Fatal(fmt.Errorf("down migration pages=%d: %w", count, err))
	}
}
