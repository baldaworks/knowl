package sqlite

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// A structurally valid projection for another model must never replace the
// current complete publication, even if an internal caller supplies it.
func TestNativeTrustPublication(t *testing.T) {
	for _, field := range []string{"fingerprint", "dimensions"} {
		t.Run(field, func(t *testing.T) {
			store, snapshot, state, chunks := embeddingFixture(t)
			store.embedding = &hybrid.Engine{Fingerprint: state.Space}
			store.embedding.Space.Dimensions = state.Dimensions
			wrong := state
			if field == "fingerprint" {
				wrong.Space = strings.Repeat("c", 64)
			} else {
				wrong.Dimensions = 1
				for i := range chunks {
					chunks[i].Vector = []float32{1}
				}
			}
			assertEmbeddingFailure(t, store.publishEmbeddings(t.Context(), snapshot.Scope, wrong, chunks), knowl.RetrievalProjectionDrift)
			actual, _, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
			if err != nil || actual != state {
				t.Fatalf("prior publication=%+v err=%v", actual, err)
			}
		})
	}
}

func TestNativeTrustCoveragePublicationBoundary(t *testing.T) {
	store, snapshot, state, chunks := embeddingFixture(t)
	state.Coverage += strings.Repeat(" ", hybrid.MaxCoverageBytes-len(state.Coverage))
	if err := store.publishEmbeddings(t.Context(), snapshot.Scope, state, chunks); err != nil {
		t.Fatal(err)
	}
	actual, _, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
	if err != nil || len(actual.Coverage) != 1048576 {
		t.Fatalf("exact manifest bytes=%d err=%v", len(actual.Coverage), err)
	}
	state.Coverage += " "
	assertEmbeddingFailure(t, store.publishEmbeddings(t.Context(), snapshot.Scope, state, chunks), knowl.RetrievalProjectionDrift)
	actual, _, err = readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
	if err != nil || len(actual.Coverage) != 1048576 {
		t.Fatalf("prior manifest changed: bytes=%d err=%v", len(actual.Coverage), err)
	}
}

// Inspect actual executed bytecode and catalog roots: a hidden correlated
// population audit fails even when it transfers just one scalar result row.
func TestNativeTrustReadPlans(t *testing.T) {
	for _, count := range []int{1, 64} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			pages := make([]knowl.PageSnapshot, count)
			for i := range pages {
				id := fmt.Sprintf("page-%03d", i)
				pages[i] = knowl.PageSnapshot{ID: knowl.PageID(id), Path: id + ".md", Digest: id, Body: "Original trust evidence"}
			}
			store, snapshot := nativeSearchFixture(t, pages)
			probe := &searchtest.VectorTransferProbe{Record: true}
			original := store.db
			observed := sql.OpenDB(searchtest.VectorProbeConnector{Connector: searchtest.VectorDriverConnector{DBDriver: original.Driver(), DSN: store.path}, Probe: probe})
			observed.SetMaxOpenConns(1)
			store.db = observed
			defer func() { store.db = original; _ = observed.Close() }()
			calls := []func() error{
				func() error {
					_, _, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 1}, nil)
					return err
				},
				func() error {
					_, _, _, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeQueryNeedle}, knowl.ReadLimits{Pages: 1})
					return err
				},
				func() error { return store.CheckProjection(t.Context(), snapshot) },
				func() error { _, err := store.ProjectionDegraded(t.Context(), snapshot.Scope); return err },
			}
			for _, call := range calls {
				*probe = searchtest.VectorTransferProbe{Record: true}
				if err := call(); err != nil {
					t.Fatal(err)
				}
				if probe.MetadataBytes != 0 || probe.VectorBytes != 0 || probe.CandidateRows > 20 || probe.EvidenceRows > 20 {
					t.Fatalf("transfer=%+v", probe)
				}
				assertSQLiteAdmissionPlans(t, original, probe.Queries)
			}
		})
	}
}

func assertSQLiteAdmissionPlans(t *testing.T, db *sql.DB, queries []searchtest.VectorRecordedQuery) {
	t.Helper()
	roots := map[int]string{}
	rows, err := db.QueryContext(t.Context(), "SELECT rootpage,tbl_name FROM sqlite_schema WHERE rootpage>0")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var root int
		var table string
		if err := rows.Scan(&root, &table); err != nil {
			t.Fatal(err)
		}
		roots[root] = table
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	coverageColumn := sqliteCoverageColumn(t, db)
	for _, query := range queries {
		if slices.Contains(query.Columns, "coverage") || slices.Contains(query.Columns, "vector") {
			t.Fatalf("unbounded read columns=%v", query.Columns)
		}
		if slices.Contains(query.Columns, "score") {
			continue
		}
		rows, err := db.QueryContext(t.Context(), "EXPLAIN "+query.SQL, query.Args...)
		if err != nil {
			t.Fatal(err)
		}
		cursors := map[int]string{}
		for rows.Next() {
			var addr, p1, p2, p3, p5 int
			var opcode string
			var p4, comment any
			if err := rows.Scan(&addr, &opcode, &p1, &p2, &p3, &p4, &p5, &comment); err != nil {
				t.Fatal(err)
			}
			if opcode == "OpenRead" && (roots[p2] == "knowl_embedding_chunks" || (len(query.Columns) <= 2 && roots[p2] == "knowl_pages")) {
				t.Fatalf("nonranking read audits population table=%s columns=%v", roots[p2], query.Columns)
			}
			if opcode == "OpenRead" {
				cursors[p1] = roots[p2]
			}
			if opcode == "Column" && cursors[p1] == "knowl_embedding_state" && p2 == coverageColumn {
				t.Fatal("ordinary read accesses coverage manifest")
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
	}
}

func sqliteCoverageColumn(t *testing.T, db *sql.DB) int {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "PRAGMA table_info(knowl_embedding_state)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err := rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "coverage" {
			return cid
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("coverage column missing")
	return -1
}

// Ordinary health admission trusts the complete publication and transfers no
// coverage manifest or stored vector. Full maintenance reads still do both.
func TestNativeTrustReadAdmission(t *testing.T) {
	store, snapshot, state, _ := embeddingFixture(t)
	store.embedding = &hybrid.Engine{Fingerprint: state.Space}
	store.embedding.Space.Dimensions = state.Dimensions
	transfer := &validationTransfers{}
	observed := sql.OpenDB(validationConnector{driver: store.db.Driver(), path: store.path, transfer: transfer})
	original := store.db
	store.db = observed
	defer func() { store.db = original; _ = observed.Close() }()
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	degraded, err := store.ProjectionDegraded(t.Context(), snapshot.Scope)
	if err != nil || degraded {
		t.Fatalf("degraded=%v err=%v", degraded, err)
	}
	if transfer.coverageBytes != 0 || transfer.vectorBytes != 0 {
		t.Fatalf("read admission transfer=%+v", transfer)
	}
}

func TestNativeTrustReadScalarFailures(t *testing.T) {
	for _, tc := range []struct {
		name, statement string
		want            knowl.RetrievalFailure
	}{
		{"absent", `DELETE FROM knowl_embedding_state`, knowl.RetrievalProjectionNotReady},
		{"format", `UPDATE knowl_embedding_state SET native_format=0`, knowl.RetrievalProjectionNotReady},
		{"dynamic type", `UPDATE knowl_embedding_state SET space=CAST(space AS BLOB)`, knowl.RetrievalProjectionDrift},
		{"snapshot mismatch", `UPDATE knowl_embedding_state SET snapshot_digest=printf('%064d',1)`, knowl.RetrievalProjectionDrift},
		{"model", `UPDATE knowl_embedding_state SET space=printf('%064d',1)`, knowl.RetrievalProjectionDrift},
		{"dimensions", `UPDATE knowl_embedding_state SET dimensions=1`, knowl.RetrievalProjectionDrift},
		{"timestamp", `UPDATE knowl_embedding_state SET ready_at='invalid'`, knowl.RetrievalProjectionDrift},
		{"mode", `UPDATE knowl_embedding_state SET mode='lexical'`, knowl.RetrievalProjectionDrift},
		{"reason", `UPDATE knowl_embedding_state SET reason='unavailable'`, knowl.RetrievalProjectionDrift},
		{"omission", `UPDATE knowl_embedding_state SET omitted_chunks=1`, knowl.RetrievalProjectionDrift},
		{"negative count", `UPDATE knowl_embedding_state SET chunk_count=-1`, knowl.RetrievalProjectionDrift},
		{"capacity", `UPDATE knowl_embedding_state SET chunk_count=8193`, knowl.RetrievalProjectionCapacity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{{ID: "a", Path: "a.md", Digest: "a", Body: "Original scalar admission evidence"}})
			if _, err := store.db.ExecContext(t.Context(), "PRAGMA ignore_check_constraints=ON"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(t.Context(), tc.statement); err != nil {
				t.Fatal(err)
			}
			assertEmbeddingFailure(t, store.CheckProjection(t.Context(), snapshot), tc.want)
			_, err := store.ProjectionDegraded(t.Context(), snapshot.Scope)
			assertEmbeddingFailure(t, err, tc.want)
			assertSQLitePublicBoundFailure(t, store, snapshot.Scope, tc.want)
		})
	}
}
