//go:build integration

package postgres

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
	genericAccentID    = "accent"
	genericASCIIID     = "ascii"
	genericSourceScope = "migration_sources"
	genericDocumentID  = "docs/page.md"
)

func genericPostgresSnapshot() knowl.WorkspaceSnapshot {
	snapshot := knowl.WorkspaceSnapshot{Scope: "generic", SchemaDigest: testSchemaDigest, CapturedAt: time.Unix(100, 0).UTC()}
	for _, data := range [][3]string{{genericAccentID, "Café", "original cafe\u0301 evidence"}, {genericASCIIID, "Cafe", "ordinary prose"}, {"question", "Question", "What is WHY"}, {"cyrillic", "ХРАНИЛИЩЕ", "technical storage"}} {
		snapshot.Pages = append(snapshot.Pages, knowl.PageSnapshot{ID: knowl.PageID(data[0]), Path: "wiki/" + data[0] + ".md", Title: data[1], Body: data[2], Content: data[2], Digest: "digest-" + data[0], SourceRefs: []string{"raw:" + data[0] + "@1"}, SourceDocuments: []knowl.SourceDocument{{SourceID: testSourceID, DocumentID: knowl.DocumentID(data[0]), Revision: "1"}}})
	}
	return snapshot
}

func runGenericPostgres(t *testing.T, dsn string) {
	t.Helper()
	t.Run("literal-evidence-and-rollback", func(t *testing.T) {
		ctx := t.Context()
		store, err := Open(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		snapshot := genericPostgresSnapshot()
		if err := store.Rebuild(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct {
			query string
			want  knowl.PageID
		}{{"café", genericAccentID}, {"CAFE\u0301", genericAccentID}, {"cafe", genericASCIIID}, {"what", "question"}, {"хранилище", "cyrillic"}} {
			t.Run(test.query, func(t *testing.T) {
				refs, err := store.Search(ctx, snapshot.Scope, test.query, knowl.ReadLimits{Pages: 10, Characters: 80}, nil)
				if err != nil || len(refs) != 1 || refs[0].ID != test.want {
					t.Fatalf("results=%v error=%v", refs, err)
				}
				original := snapshot.Pages[slices.IndexFunc(snapshot.Pages, func(p knowl.PageSnapshot) bool { return p.ID == test.want })]
				if refs[0].Title != original.Title || !slices.Equal(refs[0].SourceRefs, original.SourceRefs) || refs[0].Snippet != original.Title+"\n\n"+original.Body {
					t.Fatalf("original evidence changed: %#v", refs[0])
				}
				ids, err := store.SelectContext(ctx, snapshot.Scope, knowl.SourceSummary{Title: test.query}, knowl.ReadLimits{Pages: 1})
				if err != nil || !slices.Equal(ids, []knowl.PageID{test.want}) {
					t.Fatalf("source ids=%v error=%v", ids, err)
				}
			})
		}
		if _, err := store.Search(ctx, snapshot.Scope, "café", knowl.ReadLimits{}, []knowl.SourceID{"Invalid"}); !errors.Is(err, app.ErrSourceInvalid) {
			t.Fatalf("filter=%v", err)
		}
		before, err := store.ProjectionStatus(ctx, snapshot.Scope)
		if err != nil {
			t.Fatal(err)
		}
		broken := snapshot
		broken.Pages = append(slices.Clone(snapshot.Pages), knowl.PageSnapshot{ID: "overflow", Path: "wiki/overflow.md", Body: strings.Repeat("a ", 131073)})
		if err := store.Rebuild(ctx, broken); !errors.Is(err, lexical.ErrInvalidProjection) {
			t.Fatalf("overflow=%v", err)
		}
		after, err := store.ProjectionStatus(ctx, snapshot.Scope)
		if err != nil || before != after {
			t.Fatalf("partial readiness: %v %v %v", before, after, err)
		}
		if err := store.CheckProjection(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
		refs, err := store.Search(ctx, snapshot.Scope, "cafe", knowl.ReadLimits{}, nil)
		if err != nil || len(refs) != 1 || refs[0].ID != genericASCIIID {
			t.Fatalf("previous index lost: %v %v", refs, err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := store.Rebuild(canceled, broken); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled rebuild=%v", err)
		}
		if _, err := store.Search(canceled, snapshot.Scope, "café", knowl.ReadLimits{}, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled search=%v", err)
		}
	})
	t.Run("native-boundaries", func(t *testing.T) {
		store, err := Open(t.Context(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		long := strings.Repeat("𐐨", 256)
		words := make([]string, 8192)
		for i := range words {
			words[i] = fmt.Sprintf("word%d", i)
		}
		snapshot := knowl.WorkspaceSnapshot{Scope: "native_bounds", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: "maximum", Path: "wiki/maximum.md", Body: long}, {ID: "words", Path: "wiki/words.md", Body: strings.Join(words, " ")}, {ID: "bytes", Path: "wiki/bytes.md", Title: "ab", Body: strings.TrimSpace(strings.Repeat("a ", 131071))}}}
		if err := store.Rebuild(t.Context(), snapshot); err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{long, "word8191", "ab"} {
			refs, err := store.Search(t.Context(), snapshot.Scope, query, knowl.ReadLimits{Pages: 10}, nil)
			if err != nil || len(refs) != 1 {
				t.Fatalf("boundary query runes=%d results=%v error=%v", len([]rune(query)), refs, err)
			}
		}
		var maximumSize int
		if err := store.db.QueryRowContext(t.Context(), `SELECT max(pg_column_size(search_vector)) FROM knowl_pages WHERE scope=$1`, snapshot.Scope).Scan(&maximumSize); err != nil || maximumSize >= 1024*1024 {
			t.Fatalf("native vector size=%d error=%v", maximumSize, err)
		}
		for _, page := range []knowl.PageSnapshot{
			{ID: "too-large", Path: "wiki/too-large.md", Body: strings.Join(append(words, "extra"), " ")},
			{ID: "too-large", Path: "wiki/too-large.md", Title: "abc", Body: strings.TrimSpace(strings.Repeat("a ", 131071))},
		} {
			broken := snapshot
			broken.Pages = []knowl.PageSnapshot{page}
			if err := store.Rebuild(t.Context(), broken); !errors.Is(err, lexical.ErrInvalidProjection) {
				t.Fatalf("over boundary error=%v", err)
			}
			if err := store.CheckProjection(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("upgrade-restart-down", func(t *testing.T) { runGenericPostgresMigration(t, dsn) })
}

func runGenericPostgresMigration(t *testing.T, dsn string) {
	t.Helper()
	ctx := t.Context()
	root, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	schema := fmt.Sprintf("generic_migration_%d", time.Now().UnixNano())
	if _, err := root.db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = root.db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	migrationDSN, err := dsnWithSearchPath(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", migrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 14); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: db, dsn: migrationDSN}
	key, meta := postgresExecutionFixture("generic", "upgrade", time.Unix(1, 0).UTC())
	reservation, err := legacy.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatal(err)
	}
	operationBefore, err := legacy.Operation(ctx, key.Scope, reservation.ID)
	if err != nil {
		t.Fatal(err)
	}
	seedGenericPostgresSource(t, legacy)
	sourceBefore, err := legacy.SourceStatus(ctx, genericSourceScope, testSourceID)
	if err != nil {
		t.Fatal(err)
	}
	documentBefore, err := legacy.DocumentState(ctx, genericSourceScope, testSourceID, genericDocumentID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := genericPostgresSnapshot()
	page := snapshot.Pages[0]
	if _, err := db.ExecContext(ctx, `INSERT INTO knowl_pages (scope,page_id,path,title,body,digest,source_refs,updated_at) VALUES ($1,$2,$3,$4,$5,'original-digest','[]', $6)`, snapshot.Scope, page.ID, page.Path, page.Title, page.Body, snapshot.CapturedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowl_projection_state (scope,schema_digest,snapshot_digest,page_count,link_count,ready_at) VALUES ($1,'schema','old',1,0,$2)`, snapshot.Scope, snapshot.CapturedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(ctx, migrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	if _, err := current.ProjectionStatus(ctx, snapshot.Scope); !errors.Is(err, ErrProjectionNotReady) {
		t.Fatalf("old readiness=%v", err)
	}
	refs, err := current.Search(ctx, snapshot.Scope, "café", knowl.ReadLimits{}, nil)
	if err != nil || len(refs) != 0 {
		t.Fatalf("old tokens served: %v %v", refs, err)
	}
	var title, body, digest string
	if err := current.db.QueryRowContext(ctx, `SELECT title,body,digest FROM knowl_pages WHERE scope=$1 AND page_id=$2`, snapshot.Scope, page.ID).Scan(&title, &body, &digest); err != nil || title != page.Title || body != page.Body || digest != "original-digest" {
		t.Fatalf("original changed: %q %q %q %v", title, body, digest, err)
	}
	operationAfter, err := current.Operation(ctx, key.Scope, reservation.ID)
	if err != nil || !reflect.DeepEqual(operationBefore, operationAfter) {
		t.Fatalf("operation changed: %v", err)
	}
	sourceAfter, err := current.SourceStatus(ctx, genericSourceScope, testSourceID)
	if err != nil || !reflect.DeepEqual(sourceBefore, sourceAfter) {
		t.Fatalf("source changed: %v", err)
	}
	documentAfter, err := current.DocumentState(ctx, genericSourceScope, testSourceID, genericDocumentID)
	if err != nil || !reflect.DeepEqual(documentBefore, documentAfter) {
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
	reopened, err := Open(ctx, migrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	after, err := reopened.Search(ctx, snapshot.Scope, "café", knowl.ReadLimits{}, nil)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("restart results changed: %v", err)
	}
	if err := reopened.CheckProjection(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	provider, err = goose.NewProvider(goose.DialectPostgres, reopened.db, directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 14); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ProjectionStatus(ctx, snapshot.Scope); !errors.Is(err, ErrProjectionNotReady) {
		t.Fatalf("down readiness=%v", err)
	}
	var count int
	if err := reopened.db.QueryRowContext(ctx, `SELECT count(*) FROM knowl_pages WHERE scope=$1`, snapshot.Scope).Scan(&count); err != nil || count != len(snapshot.Pages) {
		t.Fatalf("down pages=%d error=%v", count, err)
	}
	var generated string
	if err := reopened.db.QueryRowContext(ctx, `SELECT attgenerated FROM pg_attribute WHERE attrelid='knowl_pages'::regclass AND attname='search_vector'`).Scan(&generated); err != nil || generated != "s" {
		t.Fatalf("down generated=%q error=%v", generated, err)
	}
}

func seedGenericPostgresSource(t *testing.T, store *Store) {
	t.Helper()
	ctx := t.Context()
	at := time.Unix(50, 0).UTC()
	run := knowl.SyncRun{ID: "generic-upgrade", Scope: genericSourceScope, SourceID: testSourceID, ConfigDigest: strings.Repeat("a", 64), Status: knowl.SyncStatusScanning, StartedAt: at, UpdatedAt: at}
	state := knowl.DocumentState{Scope: run.Scope, SourceID: run.SourceID, DocumentID: genericDocumentID, Revision: "1", LastSeenRunID: run.ID, AcceptedSource: knowl.AcceptedSource{Scope: run.Scope, Source: knowl.SourceRef{Adapter: "wiki-filesystem", ID: "engineering/docs/page.md"}, Version: knowl.SourceVersion{Version: "1", Digest: strings.Repeat("d", 64)}, MediaType: "text/markdown", ManifestRef: "raw/manifest.json"}}
	if _, _, err := store.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: knowl.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	prepared := app.PreparedSyncState{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, CompleteScan: true, Checkpoint: "checkpoint", Counts: knowl.SyncCounts{Added: 1}, Documents: []app.PreparedDocumentState{{Action: app.SyncDocumentActive, State: state}}, PreparedAt: at}
	digest, err := app.PreparedSyncDigest(prepared)
	if err != nil {
		t.Fatal(err)
	}
	prepared.CandidateDigest = digest
	if _, err := store.PrepareSync(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	transition := app.SyncGeneration{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, Generation: "generation", UpdatedAt: at.Add(time.Second)}
	if _, err := store.MarkContentCommitted(ctx, transition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkProjected(ctx, transition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeSync(ctx, app.SyncFinalization{RunID: run.ID, Scope: run.Scope, SourceID: run.SourceID, CandidateDigest: digest, Generation: transition.Generation, Checkpoint: prepared.Checkpoint, Counts: prepared.Counts, FinalizedAt: at.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
}
