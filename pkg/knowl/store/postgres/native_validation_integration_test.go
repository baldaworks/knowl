//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	nativeValidationDropDimensions = `ALTER TABLE knowl_embedding_chunks DROP CONSTRAINT knowl_embedding_chunks_vector_check`
	nativeValidationExtraPage      = "extra-page"
	nativeStoredVectorColumn       = "vector"
)

func TestPostgresNativeValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	container, err := pgcontainer.Run(ctx, "pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc", pgcontainer.WithDatabase("validation_tests"), pgcontainer.WithUsername("knowl"), pgcontainer.WithPassword("knowl"), pgcontainer.BasicWaitStrategies(), pgcontainer.WithSQLDriver("pgx"))
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	store := nativeOpen(t, dsn)
	nativeExec(t, store.db, `CREATE SCHEMA "vector extension"`)
	nativeExec(t, store.db, `CREATE EXTENSION vector SCHEMA "vector extension"`)
	store = nativeOpen(t, dsn, nativeOptions())
	snapshot := nativePublicationSnapshot()
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckProjection(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	degraded, err := store.ProjectionDegraded(ctx, snapshot.Scope)
	if err != nil || degraded {
		t.Fatalf("degraded=%v error=%v", degraded, err)
	}
	t.Run("whole-scope-integrity", func(t *testing.T) { runNativeValidationCorruption(t, store) })
	t.Run("native-vector-integrity", func(t *testing.T) { runNativeValidationVectors(t, store) })
	t.Run("empty-and-degraded", func(t *testing.T) { runNativeValidationModes(t, store) })
	t.Run("scope-capacity", func(t *testing.T) { runNativeValidationCapacity(t, store) })
	t.Run("logical-byte-boundary", func(t *testing.T) { runNativeValidationBytes(t, store) })
	t.Run("snapshot-and-cancellation", func(t *testing.T) { runNativeValidationSnapshot(t, store) })
	t.Run("driver-transfer", func(t *testing.T) { runNativeValidationTransfer(t, store, dsn) })
	t.Run("health-failure-policy", func(t *testing.T) { runNativeValidationHealth(t, store) })
	t.Run("actual-sql-permission-error", func(t *testing.T) { runNativeValidationPermission(t, store) })
	t.Run("blocked-read-deadline", func(t *testing.T) { runNativeValidationDeadline(t, store) })
}

func nativeValidationTx(t *testing.T, store *Store) *sql.Tx {
	t.Helper()
	if err := store.Rebuild(t.Context(), nativePublicationSnapshot()); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func nativeValidationExec(t *testing.T, tx *sql.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func nativeValidationRead(ctx context.Context, store *Store, tx *sql.Tx) (validatedDenseProjection, error) {
	return store.validateDenseProjectionTx(ctx, tx, "publication", store.embedding.Fingerprint, store.embedding.Space.Dimensions)
}

func runNativeValidationCorruption(t *testing.T, store *Store) {
	for _, tc := range []struct {
		name, setup, query string
		want               knowl.RetrievalFailure
	}{
		{"legacy-format", "", `UPDATE knowl_embedding_state SET native_format=0`, knowl.RetrievalProjectionNotReady},
		{"absent-state", "", `DELETE FROM knowl_embedding_state`, knowl.RetrievalProjectionNotReady},
		{"invalid-json", "", `UPDATE knowl_embedding_state SET coverage='{'`, knowl.RetrievalProjectionDrift},
		{"null-json", "", `UPDATE knowl_embedding_state SET coverage='null'`, knowl.RetrievalProjectionDrift},
		{"nul-manifest-id", "", `UPDATE knowl_embedding_state SET coverage='[{"page_id":"a\u0000","page_digest":"publication-a-1","chunks":1}]'`, knowl.RetrievalProjectionDrift},
		{"oversized-coverage", `ALTER TABLE knowl_embedding_state DROP CONSTRAINT knowl_embedding_state_coverage_check`, `UPDATE knowl_embedding_state SET coverage=repeat(' ',1048577)`, knowl.RetrievalProjectionDrift},
		{"infinite-time", "", `UPDATE knowl_embedding_state SET ready_at='infinity'`, knowl.RetrievalProjectionDrift},
		{"space-hex", "", `UPDATE knowl_embedding_state SET space=repeat('G',64)`, knowl.RetrievalProjectionDrift},
		{"snapshot", "", `UPDATE knowl_embedding_state SET snapshot_digest=repeat('b',64)`, knowl.RetrievalProjectionDrift},
		{"count", "", `UPDATE knowl_embedding_state SET chunk_count=0`, knowl.RetrievalProjectionDrift},
		{"state-capacity", `ALTER TABLE knowl_embedding_state DROP CONSTRAINT knowl_embedding_state_chunk_count_check`, `UPDATE knowl_embedding_state SET chunk_count=8193`, knowl.RetrievalProjectionCapacity},
		{"omissions", "", `UPDATE knowl_embedding_state SET omitted_chunks=1`, knowl.RetrievalProjectionDrift},
		{"invalid-reason", "", `UPDATE knowl_embedding_state SET reason='unavailable'`, knowl.RetrievalProjectionDrift},
		{"foreign-space", "", `UPDATE knowl_embedding_chunks SET space=repeat('c',64)`, knowl.RetrievalProjectionDrift},
		{"dimension", nativeValidationDropDimensions, `UPDATE knowl_embedding_chunks SET dimensions=3`, knowl.RetrievalProjectionDrift},
		{"vector-dimension", nativeValidationDropDimensions, `UPDATE knowl_embedding_chunks SET vector='[1]'`, knowl.RetrievalProjectionDrift},
		{"ordinal-gap", "", `UPDATE knowl_embedding_chunks SET ordinal=1`, knowl.RetrievalProjectionDrift},
		{"negative-ordinal", `ALTER TABLE knowl_embedding_chunks DROP CONSTRAINT knowl_embedding_chunks_ordinal_check`, `UPDATE knowl_embedding_chunks SET ordinal=-1`, knowl.RetrievalProjectionDrift},
		{"uppercase-hash", "", `UPDATE knowl_embedding_chunks SET content_hash=repeat('B',64)`, knowl.RetrievalProjectionDrift},
		{"chunk-digest", "", `UPDATE knowl_embedding_chunks SET page_digest='stale'`, knowl.RetrievalProjectionDrift},
		{"page-digest", "", `UPDATE knowl_pages SET digest='changed'`, knowl.RetrievalProjectionDrift},
		{"missing-chunk", "", `DELETE FROM knowl_embedding_chunks`, knowl.RetrievalProjectionDrift},
		{"extra-chunk", "", `INSERT INTO knowl_embedding_chunks SELECT scope,space,page_id,page_digest,1,content_hash,dimensions,vector FROM knowl_embedding_chunks`, knowl.RetrievalProjectionDrift},
		{"orphan", `SET LOCAL session_replication_role='replica'`, `UPDATE knowl_embedding_chunks SET page_id='orphan'`, knowl.RetrievalProjectionDrift},
		{"duplicate-ordinal", `ALTER TABLE knowl_embedding_chunks DROP CONSTRAINT knowl_embedding_chunks_pkey`, `INSERT INTO knowl_embedding_chunks SELECT * FROM knowl_embedding_chunks; UPDATE knowl_embedding_state SET chunk_count=2,coverage='[{"page_id":"a","page_digest":"publication-a-1","chunks":2}]'`, knowl.RetrievalProjectionDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := nativeValidationTx(t, store)
			if tc.setup != "" {
				nativeValidationExec(t, tx, tc.setup)
			}
			nativeValidationExec(t, tx, tc.query)
			got, err := nativeValidationRead(t.Context(), store, tx)
			assertEmbeddingFailure(t, err, tc.want)
			if got.ValidatedChunks != 0 {
				t.Fatalf("partial validation count=%d", got.ValidatedChunks)
			}
		})
	}
}

func runNativeValidationVectors(t *testing.T, store *Store) {
	inside := float32(math.Sqrt(1.001))
	if float64(inside)*float64(inside) > 1.001 {
		inside = math.Nextafter32(inside, 0)
	}
	for _, tc := range []struct {
		name  string
		value float32
		valid bool
	}{
		{"unit", 1, true}, {"negative-unit", -1, true}, {"inside", inside, true}, {"outside", math.Nextafter32(inside, 2), false},
		{"zero", 0, false}, {"large-finite", math.MaxFloat32, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := nativeValidationTx(t, store)
			nativeValidationExec(t, tx, `UPDATE knowl_embedding_chunks SET vector=$1`, nativeVectorText([]float32{tc.value, 0}))
			got, err := nativeValidationRead(t.Context(), store, tx)
			if tc.valid {
				if err != nil || got.ValidatedChunks != 1 {
					t.Fatalf("count=%d error=%v", got.ValidatedChunks, err)
				}
			} else {
				assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
			}
		})
	}
	for _, vector := range []string{"[NaN,0]", "[Infinity,0]", "[-Infinity,0]"} {
		t.Run(vector, func(t *testing.T) {
			tx := nativeValidationTx(t, store)
			_, err := tx.ExecContext(t.Context(), `UPDATE knowl_embedding_chunks SET vector=$1`, vector)
			nativeAssertSQLState(t, err, "22000")
		})
	}
	for _, dimensions := range []int{1, 2000, 2001, 4096} {
		t.Run(nativeVectorText([]float32{float32(dimensions)}), func(t *testing.T) {
			tx := nativeValidationTx(t, store)
			v := make([]float32, dimensions)
			v[0] = 1
			nativeValidationExec(t, tx, `UPDATE knowl_embedding_state SET dimensions=$1`, dimensions)
			nativeValidationExec(t, tx, `UPDATE knowl_embedding_chunks SET dimensions=$1,vector=$2`, dimensions, nativeVectorText(v))
			got, err := store.validateDenseProjectionTx(t.Context(), tx, "publication", store.embedding.Fingerprint, dimensions)
			if err != nil || got.ValidatedChunks != 1 {
				t.Fatalf("count=%d error=%v", got.ValidatedChunks, err)
			}
		})
	}
}

func runNativeValidationModes(t *testing.T, store *Store) {
	for _, mode := range []string{"empty", "zero-chunk-page", nativeValidationExtraPage, "degraded"} {
		t.Run(mode, func(t *testing.T) {
			tx := nativeValidationTx(t, store)
			if mode == nativeValidationExtraPage {
				nativeValidationExec(t, tx, `INSERT INTO knowl_pages(scope,page_id,path,title,body,digest,updated_at,search_vector,format,okf_metadata) SELECT scope,'z','z.md',title,body,digest,updated_at,search_vector,format,okf_metadata FROM knowl_pages`)
			} else {
				nativeValidationExec(t, tx, `DELETE FROM knowl_embedding_chunks`)
				nativeValidationExec(t, tx, `UPDATE knowl_embedding_state SET chunk_count=0,coverage='[]'`)
				if mode == "empty" {
					nativeValidationExec(t, tx, `DELETE FROM knowl_pages`)
				}
				if mode == "zero-chunk-page" {
					nativeValidationExec(t, tx, `UPDATE knowl_embedding_state SET coverage='[{"page_id":"a","page_digest":"publication-a-1","chunks":0}]'`)
				}
				if mode == "degraded" {
					nativeValidationExec(t, tx, `UPDATE knowl_embedding_state SET mode='degraded',reason='unavailable',coverage=''`)
				}
			}
			got, err := nativeValidationRead(t.Context(), store, tx)
			if mode == nativeValidationExtraPage {
				assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
			} else if err != nil || got.ValidatedChunks != 0 {
				t.Fatalf("count=%d error=%v", got.ValidatedChunks, err)
			}
		})
	}
}

func runNativeValidationCapacity(t *testing.T, store *Store) {
	tx := nativeValidationTx(t, store)
	nativeValidationExec(t, tx, `INSERT INTO knowl_embedding_chunks SELECT scope,space,page_id,page_digest,n,content_hash,dimensions,vector FROM knowl_embedding_chunks CROSS JOIN generate_series(1,8191) n`)
	nativeValidationExec(t, tx, `UPDATE knowl_embedding_state SET chunk_count=8192,coverage='[{"page_id":"a","page_digest":"publication-a-1","chunks":8192}]'`)
	got, err := nativeValidationRead(t.Context(), store, tx)
	if err != nil || got.ValidatedChunks != 8192 {
		t.Fatalf("count=%d error=%v", got.ValidatedChunks, err)
	}
	// Overflow belongs to a foreign space; prefilter counting must still see it.
	nativeValidationExec(t, tx, `INSERT INTO knowl_embedding_chunks SELECT scope,repeat('f',64),page_id,page_digest,ordinal,content_hash,dimensions,vector FROM knowl_embedding_chunks WHERE ordinal=0`)
	_, err = nativeValidationRead(t.Context(), store, tx)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionCapacity)
}

func runNativeValidationSnapshot(t *testing.T, store *Store) {
	tx := nativeValidationTx(t, store)
	if _, err := nativeValidationRead(t.Context(), store, tx); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, store.db, `UPDATE knowl_embedding_chunks SET vector='[0,0]'`)
	if _, err := nativeValidationRead(t.Context(), store, tx); err != nil {
		t.Fatalf("snapshot changed: %v", err)
	}
	_, err := store.ProjectionDegraded(t.Context(), "publication")
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.ProjectionDegraded(ctx, "publication")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

type nativeTransferCounts struct{ rows, vectorBytes, denseRows int }
type nativeObservedConnector struct {
	driver.Connector
	counts *nativeTransferCounts
}

func (c nativeObservedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return nativeObservedConnection{Conn: conn, counts: c.counts}, nil
}

type nativeObservedConnection struct {
	driver.Conn
	counts *nativeTransferCounts
}

func (c nativeObservedConnection) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c nativeObservedConnection) CheckNamedValue(value *driver.NamedValue) error {
	return c.Conn.(driver.NamedValueChecker).CheckNamedValue(value)
}
func (c nativeObservedConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return nativeObservedRows{Rows: rows, counts: c.counts}, nil
}

type nativeObservedRows struct {
	driver.Rows
	counts *nativeTransferCounts
}

func (r nativeObservedRows) Next(values []driver.Value) error {
	err := r.Rows.Next(values)
	if err == nil {
		r.counts.rows++
		for i, column := range r.Columns() {
			if column == "score" && values[i] != nil {
				r.counts.denseRows++
			}
			if column == nativeStoredVectorColumn {
				switch value := values[i].(type) {
				case string:
					r.counts.vectorBytes += len(value)
				case []byte:
					r.counts.vectorBytes += len(value)
				}
			}
		}
	}
	return err
}
func runNativeValidationTransfer(t *testing.T, store *Store, dsn string) {
	if err := store.Rebuild(t.Context(), nativePublicationSnapshot()); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	counts := &nativeTransferCounts{}
	observed := sql.OpenDB(nativeObservedConnector{Connector: stdlib.GetConnector(*config), counts: counts})
	t.Cleanup(func() { _ = observed.Close() })
	original := store.db
	store.db = observed
	defer func() { store.db = original }()
	for _, n := range []int{64, 256} {
		nativeExec(t, original, `DELETE FROM knowl_embedding_chunks WHERE ordinal>0`)
		nativeExec(t, original, `INSERT INTO knowl_embedding_chunks SELECT scope,space,page_id,page_digest,n,content_hash,dimensions,vector FROM knowl_embedding_chunks CROSS JOIN generate_series(1,$1::integer-1) n`, n)
		nativeExec(t, original, `UPDATE knowl_embedding_state SET chunk_count=$1,coverage=$2`, n, `[{"page_id":"a","page_digest":"publication-a-1","chunks":`+strings.Trim(nativeVectorText([]float32{float32(n)}), "[]")+`}]`)
		*counts = nativeTransferCounts{}
		if err := store.CheckProjection(t.Context(), nativePublicationSnapshot()); err != nil {
			t.Fatal(err)
		}
		degraded, err := store.ProjectionDegraded(t.Context(), "publication")
		if err != nil || degraded {
			t.Fatalf("degraded=%v error=%v", degraded, err)
		}
		if counts.vectorBytes != 0 || counts.rows >= n {
			t.Fatalf("chunks=%d transfer=%+v", n, counts)
		}
	}
	tx, err := observed.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, chunks, err := maintenanceEmbeddingProjectionTx(t.Context(), tx, "publication", store.embedding.Fingerprint, 2)
	if err != nil || len(chunks) != 256 || counts.vectorBytes == 0 {
		t.Fatalf("maintenance chunks=%d transfer=%+v error=%v", len(chunks), counts, err)
	}
}

func runNativeValidationBytes(t *testing.T, store *Store) {
	tx := nativeValidationTx(t, store)
	id, digest := strings.Repeat("p", 4096), strings.Repeat("d", 256)
	dimensions := 4096
	vector := make([]float32, dimensions)
	vector[0] = 1
	// One page; padding in the valid bounded manifest lands exactly on 64 MiB.
	const rowBytes = 4096*4 + 4096 + 256 + 64 + 64 + 32
	const chunks = (hybrid.MaxProjectionBytes - 256) / rowBytes
	coverage := fmt.Sprintf(`[{"page_id":%q,"page_digest":%q,"chunks":%d}]`, id, digest, chunks)
	padding := hybrid.MaxProjectionBytes - 256 - chunks*rowBytes - len(coverage)
	if padding < 0 {
		t.Fatal("fixture does not fit")
	}
	coverage += strings.Repeat(" ", padding)
	nativeValidationExec(t, tx, `SET LOCAL session_replication_role='replica'`)
	nativeValidationExec(t, tx, `UPDATE knowl_pages SET page_id=$1,digest=$2`, id, digest)
	nativeValidationExec(t, tx, `UPDATE knowl_embedding_chunks SET page_id=$1,page_digest=$2,dimensions=$3,vector=$4`, id, digest, dimensions, nativeVectorText(vector))
	nativeValidationExec(t, tx, `INSERT INTO knowl_embedding_chunks SELECT scope,space,page_id,page_digest,n,content_hash,dimensions,vector FROM knowl_embedding_chunks CROSS JOIN generate_series(1,$1::integer-1) n`, chunks)
	nativeValidationExec(t, tx, `UPDATE knowl_embedding_state SET dimensions=$1,chunk_count=$2,coverage=$3`, dimensions, chunks, coverage)
	got, err := store.validateDenseProjectionTx(t.Context(), tx, "publication", store.embedding.Fingerprint, dimensions)
	if err != nil || got.ValidatedChunks != chunks {
		t.Fatalf("exact boundary count=%d error=%v", got.ValidatedChunks, err)
	}
	nativeValidationExec(t, tx, `UPDATE knowl_embedding_state SET coverage=coverage||' '`)
	_, err = store.validateDenseProjectionTx(t.Context(), tx, "publication", store.embedding.Fingerprint, dimensions)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionCapacity)
}

func runNativeValidationHealth(t *testing.T, store *Store) {
	snapshot := nativePublicationSnapshot()
	snapshot.Pages[0].SourceRefs = []string{"selected"}
	snapshot.Pages = append(snapshot.Pages, knowl.PageSnapshot{ID: "b", Path: "b.md", Digest: "b1", Title: "B", Body: "Unselected page", SourceRefs: []string{"unselected"}})
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, store.db, `UPDATE knowl_embedding_chunks SET vector='[0,0]' WHERE page_id='b'`)
	assertEmbeddingFailure(t, store.CheckProjection(t.Context(), snapshot), knowl.RetrievalProjectionDrift)
	_, err := store.ProjectionDegraded(t.Context(), snapshot.Scope)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	prior, err := store.previousProjection(t.Context(), snapshot.Scope)
	if err != nil {
		t.Fatal(err)
	}
	state := *prior.state
	state.Mode, state.Reason, state.ChunkCount, state.Coverage = knowl.RetrievalDegraded, knowl.RetrievalUnavailable, 0, ""
	if err := store.publishEmbeddings(t.Context(), snapshot.Scope, state, nil); err != nil {
		t.Fatal(err)
	}
	assertEmbeddingFailure(t, store.CheckProjection(t.Context(), snapshot), knowl.RetrievalUnavailable)
	store.embedding.FailurePolicy = app.EmbeddingFallbackLexical
	defer func() { store.embedding.FailurePolicy = app.EmbeddingStrict }()
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	degraded, err := store.ProjectionDegraded(t.Context(), snapshot.Scope)
	if err != nil || !degraded {
		t.Fatalf("degraded=%v error=%v", degraded, err)
	}
}

func runNativeValidationPermission(t *testing.T, store *Store) {
	tx := nativeValidationTx(t, store)
	nativeValidationExec(t, tx, `CREATE ROLE validation_runtime`)
	nativeValidationExec(t, tx, `GRANT USAGE ON SCHEMA public,"vector extension" TO validation_runtime`)
	nativeValidationExec(t, tx, `GRANT SELECT ON ALL TABLES IN SCHEMA public TO validation_runtime`)
	nativeValidationExec(t, tx, `REVOKE EXECUTE ON FUNCTION "vector extension".vector_dims("vector extension".vector) FROM PUBLIC`)
	nativeValidationExec(t, tx, `SET LOCAL ROLE validation_runtime`)
	_, err := nativeValidationRead(t.Context(), store, tx)
	assertEmbeddingFailure(t, err, knowl.RetrievalUnavailable)
}

func runNativeValidationDeadline(t *testing.T, store *Store) {
	if err := store.Rebuild(t.Context(), nativePublicationSnapshot()); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	nativeValidationExec(t, tx, `LOCK TABLE knowl_embedding_chunks IN ACCESS EXCLUSIVE MODE`)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err = store.ProjectionDegraded(ctx, "publication")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked read error=%v", err)
	}
}
