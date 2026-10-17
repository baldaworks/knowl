package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"reflect"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
	driverSQLite "modernc.org/sqlite"
	lib "modernc.org/sqlite/lib"
)

// Removing the bundled registration must break real connections, including
// dimensions above the usual indexed-vector limits.
func TestSQLiteNativeVectorConnections(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/native.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for connection := range 2 {
		store.db.SetMaxIdleConns(0)
		var version string
		if err := store.db.QueryRowContext(t.Context(), `SELECT vec_version()`).Scan(&version); err != nil || version == "" {
			t.Fatalf("connection %d: version=%q error=%v", connection, version, err)
		}
		for _, dimensions := range []int{1, 2000, 2001, 4096} {
			t.Run(fmt.Sprintf("connection%d/dimensions%d", connection, dimensions), func(t *testing.T) {
				vector := make([]float32, dimensions)
				vector[0] = 1
				blob, err := hybrid.EncodeVector(vector, dimensions)
				if err != nil {
					t.Fatal(err)
				}
				var cosine, norm float64
				var count int
				if err := store.db.QueryRowContext(t.Context(), `SELECT 1-vec_distance_cosine(?,?),COUNT(*),SUM(value*value) FROM vec_each(?)`, blob, blob, blob).Scan(&cosine, &count, &norm); err != nil {
					t.Fatal(err)
				}
				if cosine != 1 || count != dimensions || norm != 1 {
					t.Fatalf("cosine=%v count=%d norm=%v", cosine, count, norm)
				}
			})
		}
	}
	var a, b float64
	if err := store.db.QueryRowContext(t.Context(), `SELECT 1-vec_distance_cosine('[1,0.01]','[1,0]'),1-vec_distance_cosine('[0.9998,0]','[1,0]')`).Scan(&a, &b); err != nil {
		t.Fatal(err)
	}
	if math.IsNaN(a) || !(b > a) {
		t.Fatalf("native true cosine did not reverse dot ordering: A=%v B=%v", a, b)
	}
}

// Up and Down must discard only disposable dense state. A failed Up after
// ALTER must roll back both schema and history, leaving legacy rows intact.
func TestSQLiteNativeMigration(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprintf("interrupted=%v", interrupted), func(t *testing.T) {
			store, snapshot, state, chunks := embeddingFixture(t)
			provider := nativeMigrationProvider(t, store.db)
			if _, err := provider.DownTo(t.Context(), 25); err != nil {
				t.Fatal(err)
			}
			insertLegacyNativeFixture(t, store, snapshot.Scope, state, chunks)
			_, err := store.Reserve(t.Context(), knowl.OperationKey{Scope: snapshot.Scope, Source: knowl.SourceRef{Adapter: testFixture, ID: "native-migration"}, Version: knowl.SourceVersion{Version: "1", Digest: embeddingTestDigest}}, knowl.OperationMeta{SchemaDigest: testSchemaDigest})
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range []string{
				`INSERT INTO knowl_sources(scope,source_id,source_type,config_digest,created_at,updated_at) VALUES('generic','native-source','fixture','config','before','before')`,
				`INSERT INTO knowl_source_documents(scope,source_id,document_id,revision,accepted_source,last_seen_run_id,created_at,updated_at) VALUES('generic','native-source','native-document','1','{"raw":"retained"}','run','before','before')`,
			} {
				if _, err := store.db.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			before := nativeAuthoritativeRows(t, store.db)
			if interrupted {
				if _, err := store.db.ExecContext(t.Context(), `CREATE TRIGGER interrupt_native_migration BEFORE DELETE ON knowl_embedding_state BEGIN SELECT RAISE(ABORT,'test interruption'); END`); err != nil {
					t.Fatal(err)
				}
				_, err := provider.Up(t.Context())
				var classified *driverSQLite.Error
				if !errors.As(err, &classified) || classified.Code()&255 != lib.SQLITE_CONSTRAINT {
					t.Fatalf("interrupted migration error=%v", err)
				}
				assertNativeMigrationState(t, provider, store.db, 25, 1, len(chunks), 0)
				if _, err := store.db.ExecContext(t.Context(), `DROP TRIGGER interrupt_native_migration`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := provider.Up(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertNativeMigrationState(t, provider, store.db, 26, 0, 0, 1)
			if !reflect.DeepEqual(before, nativeAuthoritativeRows(t, store.db)) {
				t.Fatal("Up changed authoritative rows")
			}
			if err := store.CheckProjection(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			refs, err := store.Search(t.Context(), snapshot.Scope, "café", knowl.ReadLimits{}, nil)
			if err != nil || len(refs) != 1 || refs[0].ID != testGenericAccentID {
				t.Fatalf("Up lexical results=%v error=%v", refs, err)
			}
			// Legacy INSERT deliberately omits native_format: its default cannot
			// turn old readiness into valid current readiness.
			insertLegacyNativeFixture(t, store, snapshot.Scope, state, chunks)
			_, _, err = readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
			assertEmbeddingFailure(t, err, knowl.RetrievalProjectionNotReady)
			if err := store.publishEmbeddings(t.Context(), snapshot.Scope, state, chunks); err != nil {
				t.Fatal(err)
			}
			if _, _, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.DownTo(t.Context(), 25); err != nil {
				t.Fatal(err)
			}
			assertNativeMigrationState(t, provider, store.db, 25, 0, 0, 0)
			if !reflect.DeepEqual(before, nativeAuthoritativeRows(t, store.db)) {
				t.Fatal("Down changed authoritative rows")
			}
			refs, err = store.Search(t.Context(), snapshot.Scope, "café", knowl.ReadLimits{}, nil)
			if err != nil || len(refs) != 1 {
				t.Fatalf("Down lexical results=%v error=%v", refs, err)
			}
			if _, err := provider.Up(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertNativeMigrationState(t, provider, store.db, 26, 0, 0, 1)
		})
	}
}

func insertLegacyNativeFixture(t *testing.T, store *Store, scope knowl.ScopeRef, state hybrid.ProjectionState, chunks []hybrid.Chunk) {
	t.Helper()
	if _, err := store.db.ExecContext(t.Context(), `DELETE FROM knowl_embedding_state WHERE scope=?`, scope); err != nil {
		t.Fatal(err)
	}
	_, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_embedding_state(scope,space,snapshot_digest,dimensions,mode,reason,chunk_count,omitted_chunks,omitted_runes,ready_at,coverage) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, scope, state.Space, state.SnapshotDigest, state.Dimensions, state.Mode, state.Reason, state.ChunkCount, state.OmittedChunks, state.OmittedRunes, "1970-01-01T00:01:40Z", state.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range chunks {
		blob, err := hybrid.EncodeVector(chunk.Vector, state.Dimensions)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_embedding_chunks(scope,space,page_id,page_digest,ordinal,content_hash,dimensions,vector) VALUES(?,?,?,?,?,?,?,?)`, scope, state.Space, chunk.PageID, chunk.PageDigest, chunk.Ordinal, chunk.ContentHash, state.Dimensions, blob); err != nil {
			t.Fatal(err)
		}
	}
}

func assertNativeMigrationState(t *testing.T, provider *goose.Provider, db *sql.DB, version int64, states, chunks, formatColumns int) {
	t.Helper()
	gotVersion, err := provider.GetDBVersion(t.Context())
	if err != nil || gotVersion != version {
		t.Fatalf("migration version=%d want=%d error=%v", gotVersion, version, err)
	}
	var gotStates, gotChunks, gotColumns int
	if err := db.QueryRowContext(t.Context(), `SELECT (SELECT COUNT(*) FROM knowl_embedding_state),(SELECT COUNT(*) FROM knowl_embedding_chunks),(SELECT COUNT(*) FROM pragma_table_info('knowl_embedding_state') WHERE name='native_format')`).Scan(&gotStates, &gotChunks, &gotColumns); err != nil {
		t.Fatal(err)
	}
	if gotStates != states || gotChunks != chunks || gotColumns != formatColumns {
		t.Fatalf("states/chunks/format columns=%d/%d/%d want=%d/%d/%d", gotStates, gotChunks, gotColumns, states, chunks, formatColumns)
	}
}

func nativeAuthoritativeRows(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	result := map[string][][]any{}
	for table, query := range map[string]string{
		"operations":    `SELECT operation_id,scope,source_adapter,source_id,source_version,source_digest,schema_digest,status,attempt,plan_digest,created_at,updated_at FROM knowl_operations ORDER BY operation_id`,
		"pages":         `SELECT scope,page_id,path,title,body,digest,source_refs,updated_at FROM knowl_pages ORDER BY scope,page_id`,
		"links":         `SELECT scope,from_page,to_page,relation FROM knowl_links ORDER BY scope,from_page,to_page,relation`,
		"projection":    `SELECT scope,schema_digest,snapshot_digest,page_count,link_count,ready_at FROM knowl_projection_state ORDER BY scope`,
		"page_sources":  `SELECT scope,page_id,source_id FROM knowl_page_sources ORDER BY scope,page_id,source_id`,
		"sources":       `SELECT scope,source_id,source_type,config_digest,checkpoint,status,created_at,updated_at FROM knowl_sources ORDER BY scope,source_id`,
		"raw_documents": `SELECT scope,source_id,document_id,revision,accepted_source,mirror_path,mirror_digest,last_seen_run_id,deleted,deleted_at,created_at,updated_at FROM knowl_source_documents ORDER BY scope,source_id,document_id`,
	} {
		rows, err := db.QueryContext(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			result[table] = append(result[table], values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func nativeMigrationProvider(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, directory, goose.WithGoMigrations(operatorOperationsMigration()))
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

// An old writer that leaves the default marker must not regain readiness.
func TestSQLiteNativeFormatAdmission(t *testing.T) {
	store, snapshot, state, _ := embeddingFixture(t)
	var format int
	if err := store.db.QueryRowContext(t.Context(), `SELECT native_format FROM knowl_embedding_state WHERE scope=?`, snapshot.Scope).Scan(&format); err != nil || format != 1 {
		t.Fatalf("new publication format=%d error=%v", format, err)
	}
	for _, format := range []int{0, 2, -1} {
		if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_state SET native_format=? WHERE scope=?`, format, snapshot.Scope); err != nil {
			t.Fatal(err)
		}
		_, _, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
		assertEmbeddingFailure(t, err, knowl.RetrievalProjectionNotReady)
	}
}

func TestSQLiteLegacyFormatFailurePolicy(t *testing.T) {
	for _, policy := range []app.EmbeddingFailurePolicy{app.EmbeddingStrict, app.EmbeddingFallbackLexical} {
		t.Run(string(policy), func(t *testing.T) {
			store, err := Open(t.Context(), t.TempDir()+"/policy.sqlite", app.EmbeddingOptions{Provider: fixedEmbeddingProvider{}, Space: app.EmbeddingSpace{Model: testFixture, Revision: "1", Dimensions: 2, QueryPrefix: "query: format ", PassagePrefix: "passage: format "}, FailurePolicy: policy})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			snapshot := genericSQLiteSnapshot()
			if err := store.Rebuild(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_state SET native_format=0 WHERE scope=?`, snapshot.Scope); err != nil {
				t.Fatal(err)
			}
			refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, "café", knowl.ReadLimits{}, nil)
			if policy == app.EmbeddingStrict {
				assertEmbeddingFailure(t, err, knowl.RetrievalProjectionNotReady)
				if report.Effective != knowl.RetrievalFailed || report.Reason != knowl.RetrievalProjectionNotReady || len(refs) != 0 {
					t.Fatalf("strict refs=%v report=%+v", refs, report)
				}
			} else if err != nil || report.Effective != knowl.RetrievalDegraded || report.Reason != knowl.RetrievalProjectionNotReady || len(refs) != 1 || refs[0].ID != testGenericAccentID {
				t.Fatalf("fallback refs=%v report=%+v error=%v", refs, report, err)
			}
			ids, contextReport, _, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: "café"}, knowl.ReadLimits{Pages: 1})
			if policy == app.EmbeddingStrict {
				assertEmbeddingFailure(t, err, knowl.RetrievalProjectionNotReady)
				if len(ids) != 0 {
					t.Fatalf("strict context ids=%v", ids)
				}
			} else if err != nil || contextReport.Reason != knowl.RetrievalProjectionNotReady || contextReport.Effective != knowl.RetrievalDegraded || len(ids) != 1 || ids[0] != testGenericAccentID {
				t.Fatalf("fallback context ids=%v report=%+v error=%v", ids, contextReport, err)
			}
		})
	}
}
