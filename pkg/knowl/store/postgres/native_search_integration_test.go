//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	nativeSearchNeedle        = "unmatchedneedle"
	nativeSearchBad           = "bad"
	nativeSearchCrowd         = "crowd"
	nativeSearchChosen        = "chosen"
	nativeSearchMissing       = "absent-source"
	nativeSearchLexicalNeedle = "lexicalneedle"
	nativeSearchLexical       = "lexical"
	nativeSearchSelected      = "selected"
	nativeSearchGood          = "good"
	nativeSearchPathA         = "wiki/native-search-a.md"
)

func TestPostgresNativeSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	container, err := pgcontainer.Run(ctx, "pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc", pgcontainer.WithDatabase("search_tests"), pgcontainer.WithUsername("knowl"), pgcontainer.WithPassword("knowl"), pgcontainer.BasicWaitStrategies(), pgcontainer.WithSQLDriver("pgx"))
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	owner := nativeOpen(t, dsn)
	nativeExec(t, owner.db, `CREATE SCHEMA "vector extension"`)
	nativeExec(t, owner.db, `CREATE EXTENSION vector SCHEMA "vector extension"`)
	store := nativeOpen(t, dsn, nativeOptions())
	t.Run("page-aggregation-bounded-transfer", func(t *testing.T) { runNativeSearchCrowding(t, store, dsn) })
	t.Run("windows-ordinal-and-C-ties", func(t *testing.T) { runNativeSearchWindows(t, store) })
	t.Run("sources-scope-and-corruption", func(t *testing.T) { runNativeSearchFilters(t, store) })
	t.Run("invalid-score-outside-page-limit", func(t *testing.T) { runNativeSearchInvalidScores(t, store) })
	t.Run("attacker-search-path", func(t *testing.T) { runNativeSearchTrust(t, store, dsn) })
	t.Run("lexical-snippet-preference", func(t *testing.T) {
		snapshot := nativeSearchSnapshot([]knowl.PageSnapshot{{ID: "snippet", Path: "wiki/snippet.md", Digest: "snippet", Body: "Original lexicalneedle evidence"}})
		if err := store.Rebuild(t.Context(), snapshot); err != nil {
			t.Fatal(err)
		}
		limits := knowl.ReadLimits{Pages: 1, Characters: 100}
		lexical, err := store.search(t.Context(), snapshot.Scope, nativeSearchLexicalNeedle, limits, nil)
		if err != nil || len(lexical) != 1 {
			t.Fatalf("lexical=%v err=%v", lexical, err)
		}
		// The valid hash shape passes whole-scope validation but could not
		// reconstruct dense evidence; the lexical channel owns this snippet.
		nativeExec(t, store.db, `UPDATE knowl_embedding_chunks SET content_hash=repeat('f',64) WHERE scope=$1`, snapshot.Scope)
		refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchLexicalNeedle, limits, nil)
		if err != nil || len(refs) != 1 || refs[0].Snippet != lexical[0].Snippet || report.Effective != knowl.RetrievalHybrid || report.LexicalCandidates != 1 || report.VectorCandidates != 1 {
			t.Fatalf("refs=%v report=%+v err=%v", refs, report, err)
		}
	})
	t.Run("cosine-reversal-original-evidence", func(t *testing.T) {
		snapshot := nativeSearchSnapshot([]knowl.PageSnapshot{
			{ID: "A", Path: nativeSearchPathA, Digest: "a", Body: "Original A evidence"},
			{ID: "B", Path: "wiki/native-search-b.md", Digest: "b", Body: "Cafe\u0301 B\r\noriginal evidence"},
		})
		if err := store.Rebuild(t.Context(), snapshot); err != nil {
			t.Fatal(err)
		}
		nativeExec(t, store.db, `UPDATE knowl_embedding_chunks SET vector='[1,0.01]' WHERE page_id='A'`)
		nativeExec(t, store.db, `UPDATE knowl_embedding_chunks SET vector='[0.9998,0]' WHERE page_id='B'`)
		refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 2, Characters: 100}, nil)
		if err != nil || len(refs) != 2 || refs[0].ID != "B" || refs[1].ID != "A" || report.ScannedChunks != 2 || report.VectorCandidates != 2 {
			t.Fatalf("refs=%+v report=%+v err=%v", refs, report, err)
		}
		if refs[0].Snippet != "Cafe\u0301 B\r\noriginal evidence" {
			t.Fatalf("snippet=%q", refs[0].Snippet)
		}
		ids, report, _, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeSearchNeedle}, knowl.ReadLimits{Pages: 2})
		if err != nil || len(ids) != 2 || ids[0] != "B" || ids[1] != "index" || report.ScannedChunks != 2 {
			t.Fatalf("ids=%v report=%+v err=%v", ids, report, err)
		}
	})
	t.Run("sql-error-savepoint-recovery", func(t *testing.T) { runNativeSearchRecovery(t, store, dsn) })
}

func runNativeSearchInvalidScores(t *testing.T, store *Store) {
	snapshot := nativeSearchSnapshot([]knowl.PageSnapshot{{ID: nativeSearchGood, Path: "wiki/good.md", Digest: nativeSearchGood, Body: "Good evidence"}, {ID: nativeSearchBad, Path: "wiki/bad.md", Digest: nativeSearchBad, Body: "Bad evidence"}})
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	for _, vector := range []string{"[0,0]"} {
		for _, id := range []knowl.PageID{nativeSearchGood, nativeSearchBad} {
			tx, err := store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			// Deliberately bypass validation to exercise the native score guard itself.
			nativeValidationExec(t, tx, `UPDATE knowl_embedding_chunks SET vector=$1 WHERE scope=$2 AND page_id=$3`, vector, snapshot.Scope, id)
			_, err = store.rankDensePagesTx(t.Context(), tx, snapshot.Scope, store.embedding.Fingerprint, [][]float32{{1, 0}, {0, 1}}, nil, 1)
			_ = tx.Rollback()
			assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
		}
	}
	// NULL/infinity are impossible with valid native inputs. Inject each into
	// the extension-owned scalar in this disposable container to verify that a
	// nonwinning window/chunk or page outside LIMIT cannot conceal it.
	defer nativeExec(t, store.db, `CREATE OR REPLACE FUNCTION "vector extension".cosine_distance("vector extension".vector,"vector extension".vector) RETURNS float8 AS '$libdir/vector','cosine_distance' LANGUAGE c IMMUTABLE STRICT PARALLEL SAFE`)
	for _, result := range []string{"NULL::float8", "'Infinity'::float8", "'NaN'::float8"} {
		nativeExec(t, store.db, `CREATE OR REPLACE FUNCTION "vector extension".cosine_distance("vector extension".vector,"vector extension".vector) RETURNS float8 LANGUAGE sql IMMUTABLE STRICT AS $$ SELECT CASE WHEN $1::text='[0,0]' THEN `+result+` ELSE 0::float8 END $$`)
		tx, err := store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		nativeValidationExec(t, tx, `UPDATE knowl_embedding_chunks SET vector='[0,0]' WHERE scope=$1 AND page_id='bad'`, snapshot.Scope)
		_, err = store.rankDensePagesTx(t.Context(), tx, snapshot.Scope, store.embedding.Fingerprint, [][]float32{{1, 0}, {0, 1}}, nil, 1)
		_ = tx.Rollback()
		assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
	}
}

func runNativeSearchTrust(t *testing.T, store *Store, dsn string) {
	snapshot := nativeSearchSnapshot([]knowl.PageSnapshot{{ID: "A", Path: "wiki/A.md", Digest: "A", Body: "First evidence"}, {ID: "a", Path: nativeSearchPathA, Digest: "a", Body: "Second evidence"}})
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	var localeFirst string
	if err := store.db.QueryRowContext(t.Context(), `SELECT id FROM (VALUES ('A'),('a')) v(id) ORDER BY id LIMIT 1`).Scan(&localeFirst); err != nil {
		t.Fatal(err)
	}
	if localeFirst != "a" {
		t.Fatalf("fixture requires locale ordering unlike C; first=%q", localeFirst)
	}
	nativeExec(t, store.db, `CREATE SCHEMA attacker`)
	nativeExec(t, store.db, `CREATE FUNCTION attacker.cosine_distance("vector extension".vector,"vector extension".vector) RETURNS float8 LANGUAGE sql IMMUTABLE AS 'SELECT -100::float8'`)
	nativeExec(t, store.db, `CREATE OPERATOR attacker.<=> (LEFTARG="vector extension".vector,RIGHTARG="vector extension".vector,FUNCTION=attacker.cosine_distance)`)
	nativeExec(t, store.db, `CREATE FUNCTION attacker.vector_dims("vector extension".vector) RETURNS integer LANGUAGE sql IMMUTABLE AS 'SELECT 999'`)
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = `attacker,public,"vector extension"`
	observed := sql.OpenDB(stdlib.GetConnector(*config))
	defer func() { _ = observed.Close() }()
	original := store.db
	store.db = observed
	defer func() { store.db = original }()
	refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 2}, nil)
	if err != nil || len(refs) != 2 || refs[0].ID != "A" || refs[1].ID != "a" || report.Effective != knowl.RetrievalHybrid {
		t.Fatalf("trusted refs=%v report=%+v err=%v", refs, report, err)
	}
	tx, err := observed.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	matches, err := store.rankDensePagesTx(t.Context(), tx, snapshot.Scope, store.embedding.Fingerprint, [][]float32{{1, 0}}, nil, 2)
	if err != nil || len(matches) != 2 || matches[0].Score != 1 || matches[1].Score != 1 {
		t.Fatalf("trusted native scores=%v err=%v", matches, err)
	}
}

func runNativeSearchCrowding(t *testing.T, store *Store, dsn string) {
	for _, repetitions := range []int{4000, 14000} {
		pages := []knowl.PageSnapshot{{ID: nativeSearchCrowd, Path: "wiki/crowd.md", Digest: nativeSearchCrowd, Body: strings.Repeat("crowd ", repetitions)}}
		for i := range 24 {
			id := fmt.Sprintf("p%02d", i)
			pages = append(pages, knowl.PageSnapshot{ID: knowl.PageID(id), Path: "wiki/" + id + ".md", Digest: id, Body: "Other evidence"})
		}
		snapshot := nativeSearchSnapshot(pages)
		if err := store.Rebuild(t.Context(), snapshot); err != nil {
			t.Fatal(err)
		}
		nativeExec(t, store.db, `UPDATE knowl_embedding_chunks SET vector='[0.6,0.8]' WHERE scope=$1 AND page_id<>'crowd'`, snapshot.Scope)
		var count int
		if err := store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM knowl_embedding_chunks WHERE scope=$1`, snapshot.Scope).Scan(&count); err != nil {
			t.Fatal(err)
		}
		config, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		counts := &nativeTransferCounts{}
		observed := sql.OpenDB(nativeObservedConnector{Connector: stdlib.GetConnector(*config), counts: counts})
		original := store.db
		store.db = observed
		refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 5, Characters: 30}, nil)
		var ids []knowl.PageID
		for _, ref := range refs {
			ids = append(ids, ref.ID)
		}
		if err != nil || !slices.Equal(ids, []knowl.PageID{nativeSearchCrowd, "p00", "p01", "p02", "p03"}) || report.VectorCandidates != 20 || report.ScannedChunks != count || counts.vectorBytes != 0 || counts.denseRows != 20 {
			t.Errorf("ids=%v report=%+v count=%d transfer=%+v err=%v", ids, report, count, counts, err)
		}
		ids, report, _, err = store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeSearchNeedle}, knowl.ReadLimits{Pages: 5})
		store.db = original
		_ = observed.Close()
		if err != nil || !slices.Equal(ids, []knowl.PageID{nativeSearchCrowd, "p00", "p01", "index", "p02"}) || report.VectorCandidates != 20 || report.ScannedChunks != count || counts.vectorBytes != 0 || counts.denseRows != 40 {
			t.Fatalf("context=%v report=%+v transfer=%+v err=%v", ids, report, counts, err)
		}
	}
}

type nativeSearchWindowProvider struct{ calls, windows int }

func (p *nativeSearchWindowProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	p.calls++
	p.windows = len(inputs)
	v := make([][]float32, len(inputs))
	for i := range v {
		v[i] = []float32{1, 0}
	}
	v[len(v)-1] = []float32{0, 1}
	return v, nil
}

func runNativeSearchWindows(t *testing.T, store *Store) {
	snapshot := nativeSearchSnapshot([]knowl.PageSnapshot{
		{ID: "z", Path: "wiki/z.md", Digest: "z", Body: strings.Repeat("W", 384) + "\r\n\r\n" + strings.Repeat("Z", 384)},
		{ID: "a", Path: nativeSearchPathA, Digest: "a", Body: "Second evidence"},
		{ID: "A", Path: "wiki/A.md", Digest: "A", Body: "First evidence"},
	})
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, store.db, `UPDATE knowl_embedding_chunks SET vector='[0,1]' WHERE scope=$1`, snapshot.Scope)
	p := &nativeSearchWindowProvider{}
	original := store.embedding.Provider
	store.embedding.Provider = p
	defer func() { store.embedding.Provider = original }()
	query := strings.Repeat(nativeSearchNeedle+" ", 200)
	refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, query, knowl.ReadLimits{Pages: 3, Characters: 100}, nil)
	if err != nil || len(refs) != 3 || refs[0].ID != "A" || refs[1].ID != "a" || refs[2].ID != "z" || refs[2].Snippet != strings.Repeat("W", 100) || p.calls != 1 || p.windows != 4 || report.QueryOmittedRunes == 0 {
		t.Fatalf("refs=%+v report=%+v provider=%+v err=%v", refs, report, p, err)
	}
	ids, report, _, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Body: query}, knowl.ReadLimits{Pages: 2})
	if err != nil || len(ids) != 2 || ids[0] != "A" || p.calls != 2 || p.windows != 4 || report.VectorCandidates != 3 {
		t.Fatalf("ids=%v report=%+v err=%v", ids, report, err)
	}
	// Native close unequal negative scores must keep their exact native order.
	store.embedding.Provider = original
	nativeExec(t, store.db, `UPDATE knowl_embedding_chunks SET vector=CASE page_id WHEN 'A' THEN '[-0.6,0.8]'::"vector extension".vector WHEN 'a' THEN '[-0.60001,0.7999925]'::"vector extension".vector ELSE '[-1,0]'::"vector extension".vector END WHERE scope=$1`, snapshot.Scope)
	refs, _, err = store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 3}, nil)
	if err != nil || len(refs) != 3 || refs[0].ID != "A" || refs[1].ID != "a" || refs[2].ID != "z" {
		t.Fatalf("negative close refs=%v err=%v", refs, err)
	}
}

func runNativeSearchFilters(t *testing.T, store *Store) {
	snapshot := nativeSearchSnapshot([]knowl.PageSnapshot{
		{ID: nativeSearchSelected, Path: "wiki/selected.md", Digest: nativeSearchSelected, Body: "lexicalneedle selected evidence", SourceDocuments: []knowl.SourceDocument{{SourceID: nativeSearchChosen, DocumentID: "one", Revision: "1"}, {SourceID: nativeSearchChosen, DocumentID: "two", Revision: "1"}}},
		{ID: "excluded", Path: "wiki/excluded.md", Digest: "excluded", Body: "Excluded evidence", SourceDocuments: []knowl.SourceDocument{{SourceID: "other", DocumentID: "one", Revision: "1"}}},
	})
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, store.db, `UPDATE knowl_embedding_chunks SET vector='[-1,0]' WHERE scope=$1 AND page_id='selected'`, snapshot.Scope)
	foreign := snapshot
	foreign.Scope = "foreign-native-search"
	if err := store.Rebuild(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	for _, source := range []knowl.SourceID{nativeSearchChosen, nativeSearchMissing} {
		refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 1}, []knowl.SourceID{source})
		want := 1
		if source == nativeSearchMissing {
			want = 0
		}
		if err != nil || len(refs) != want || report.ScannedChunks != 2 || report.VectorCandidates != want || (want == 1 && refs[0].ID != nativeSearchSelected) {
			t.Fatalf("source=%s refs=%v report=%+v err=%v", source, refs, report, err)
		}
	}
	for _, mutation := range []string{`UPDATE knowl_embedding_state SET snapshot_digest=repeat('f',64) WHERE scope='native-search'`} {
		if err := store.Rebuild(t.Context(), snapshot); err != nil {
			t.Fatal(err)
		}
		nativeExec(t, store.db, mutation)
		_, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 1}, []knowl.SourceID{nativeSearchChosen})
		assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
		if report.ScannedChunks != 0 {
			t.Fatalf("partial report=%+v", report)
		}
		store.embedding.FailurePolicy = app.EmbeddingFallbackLexical
		ids, report, metadata, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeSearchLexicalNeedle}, knowl.ReadLimits{Pages: 2})
		store.embedding.FailurePolicy = app.EmbeddingStrict
		if err != nil || len(ids) != 2 || ids[0] != nativeSearchSelected || report.Effective != knowl.RetrievalDegraded || metadata.VectorProjection.State != knowl.VectorInvalid {
			t.Fatalf("fallback ids=%v report=%+v metadata=%+v err=%v", ids, report, metadata, err)
		}
	}
	if err := store.Rebuild(t.Context(), nativeSearchSnapshot(nil)); err != nil {
		t.Fatal(err)
	}
	refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 1}, nil)
	if err != nil || len(refs) != 0 || report.Effective != knowl.RetrievalHybrid || report.ScannedChunks != 0 {
		t.Fatalf("empty refs=%v report=%+v err=%v", refs, report, err)
	}
}

func runNativeSearchRecovery(t *testing.T, owner *Store, dsn string) {
	snapshot := nativeSearchSnapshot([]knowl.PageSnapshot{{ID: nativeSearchLexical, Path: "wiki/lexical.md", Digest: nativeSearchLexical, Body: "lexicalneedle original"}, {ID: "recent", Path: "wiki/recent.md", Digest: "recent", Body: "Recent evidence"}})
	if err := owner.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, owner.db, `CREATE ROLE search_runtime LOGIN PASSWORD 'fixture-search'`)
	nativeExec(t, owner.db, `GRANT USAGE ON SCHEMA public,"vector extension" TO search_runtime`)
	nativeExec(t, owner.db, `GRANT SELECT ON ALL TABLES IN SCHEMA public TO search_runtime`)
	runtimeURL, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	runtimeURL.User = url.UserPassword("search_runtime", "fixture-search")
	params := runtimeURL.Query()
	params.Set("application_name", "native-query-cancel")
	runtimeURL.RawQuery = params.Encode()
	options := nativeOptions()
	options.FailurePolicy = app.EmbeddingFallbackLexical
	store := nativeOpen(t, runtimeURL.String(), options)
	if store.nativeErr != nil {
		t.Fatalf("preflight=%v", store.nativeErr)
	}
	// Revoke only after Open: the statement really aborts the query transaction.
	nativeExec(t, owner.db, `REVOKE SELECT ON knowl_embedding_state FROM search_runtime`)
	defer nativeExec(t, owner.db, `GRANT SELECT ON knowl_embedding_state TO search_runtime`)
	for _, strict := range []bool{false, true} {
		store.embedding.FailurePolicy = app.EmbeddingFallbackLexical
		if strict {
			store.embedding.FailurePolicy = app.EmbeddingStrict
		}
		refs, report, searchErr := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchLexicalNeedle, knowl.ReadLimits{Pages: 2}, nil)
		ids, contextReport, _, contextErr := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeSearchLexicalNeedle}, knowl.ReadLimits{Pages: 3})
		if strict {
			assertEmbeddingFailure(t, searchErr, knowl.RetrievalUnavailable)
			assertEmbeddingFailure(t, contextErr, knowl.RetrievalUnavailable)
		} else if searchErr != nil || contextErr != nil || len(refs) != 1 || len(ids) != 3 || ids[0] != nativeSearchLexical || !slices.Contains(ids, knowl.PageID("recent")) || report.Effective != knowl.RetrievalDegraded || contextReport.Reason != knowl.RetrievalUnavailable {
			t.Fatalf("refs=%v ids=%v report=%+v context=%+v errors=%v/%v", refs, ids, report, contextReport, searchErr, contextErr)
		}
	}
	nativeExec(t, owner.db, `GRANT SELECT ON knowl_embedding_state TO search_runtime`)
	// Use the real extension-owned operator function with a disposable fault body.
	nativeExec(t, owner.db, `CREATE OR REPLACE FUNCTION "vector extension".cosine_distance("vector extension".vector,"vector extension".vector) RETURNS float8 LANGUAGE plpgsql IMMUTABLE STRICT AS $$ BEGIN RAISE EXCEPTION 'secret DSN vector SQL' USING ERRCODE='P0001'; END $$`)
	store.embedding.FailurePolicy = app.EmbeddingFallbackLexical
	ids, report, _, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeSearchLexicalNeedle}, knowl.ReadLimits{Pages: 3})
	if err != nil || len(ids) != 3 || report.Effective != knowl.RetrievalDegraded || report.Reason != knowl.RetrievalUnavailable || report.ScannedChunks != 2 {
		t.Fatalf("unknown operational ids=%v report=%+v err=%v", ids, report, err)
	}
	store.embedding.FailurePolicy = app.EmbeddingStrict
	_, _, err = store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchLexicalNeedle, knowl.ReadLimits{Pages: 2}, nil)
	assertEmbeddingFailure(t, err, knowl.RetrievalUnavailable)
	var upstream *pgconn.PgError
	if errors.As(err, &upstream) {
		t.Fatalf("raw upstream error escaped: code=%s", upstream.Code)
	}
	store.embedding.FailurePolicy = app.EmbeddingFallbackLexical
	nativeExec(t, owner.db, `CREATE OR REPLACE FUNCTION "vector extension".cosine_distance("vector extension".vector,"vector extension".vector) RETURNS float8 LANGUAGE plpgsql IMMUTABLE STRICT AS $$ BEGIN PERFORM pg_sleep(60); RETURN 0; END $$`)
	cancelCtx, cancelQuery := context.WithCancel(t.Context())
	done := make(chan nativeSearchResult, 1)
	go func() {
		_, r, _, e := store.SelectContextWithDiagnostics(cancelCtx, snapshot.Scope, knowl.SourceSummary{Title: nativeSearchLexicalNeedle}, knowl.ReadLimits{Pages: 3})
		done <- nativeSearchResult{r, e}
	}()
	waitNativeSearchSleeping(t, owner, done, cancelQuery)
	cancelQuery()
	canceled := <-done
	if !errors.Is(canceled.err, context.Canceled) || canceled.report.Effective != knowl.RetrievalFailed || canceled.report.Reason != knowl.RetrievalDeadline {
		t.Fatalf("canceled report=%+v err=%v", canceled.report, canceled.err)
	}
	// Terminate only this fixture's active ranking backend while the caller's
	// context is still live. A broken transaction cannot become lexical success.
	brokenCtx, cancelBroken := context.WithCancel(t.Context())
	defer cancelBroken()
	go func() {
		_, r, _, e := store.SelectContextWithDiagnostics(brokenCtx, snapshot.Scope, knowl.SourceSummary{Title: nativeSearchLexicalNeedle}, knowl.ReadLimits{Pages: 3})
		done <- nativeSearchResult{r, e}
	}()
	waitNativeSearchSleeping(t, owner, done, cancelBroken)
	nativeExec(t, owner.db, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='native-query-cancel' AND wait_event='PgSleep'`)
	broken := <-done
	var terminated *pgconn.PgError
	fatal := errors.Is(broken.err, sql.ErrTxDone) || errors.Is(broken.err, driver.ErrBadConn) || (errors.As(broken.err, &terminated) && terminated.Code == "57P01")
	if !fatal || broken.report.Effective != knowl.RetrievalFailed || broken.report.Reason != knowl.RetrievalUnavailable {
		t.Fatalf("broken report=%+v err=%v", broken.report, broken.err)
	}
	// Cancel a real blocked validator statement; cached lexical rows cannot succeed.
	tx, err := owner.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	nativeValidationExec(t, tx, `LOCK TABLE knowl_embedding_chunks IN ACCESS EXCLUSIVE MODE`)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, report, _, err = store.SelectContextWithDiagnostics(ctx, snapshot.Scope, knowl.SourceSummary{Title: nativeSearchLexicalNeedle}, knowl.ReadLimits{Pages: 3})
	if !errors.Is(err, context.DeadlineExceeded) || report.Effective != knowl.RetrievalFailed || report.Reason != knowl.RetrievalDeadline {
		t.Fatalf("deadline report=%+v err=%v", report, err)
	}
}

type nativeSearchResult struct {
	report knowl.RetrievalReport
	err    error
}

func waitNativeSearchSleeping(t *testing.T, owner *Store, done <-chan nativeSearchResult, cancel context.CancelFunc) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waiting := false
	for !waiting {
		select {
		case <-deadline.C:
			cancel()
			t.Fatal("ranking never reached server wait")
		case result := <-done:
			cancel()
			t.Fatalf("ranking returned early: %+v", result)
		case <-ticker.C:
			if err := owner.db.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name='native-query-cancel' AND wait_event='PgSleep')`).Scan(&waiting); err != nil {
				cancel()
				t.Fatal(err)
			}
		}
	}
}

func nativeSearchSnapshot(pages []knowl.PageSnapshot) knowl.WorkspaceSnapshot {
	return knowl.WorkspaceSnapshot{Scope: "native-search", SchemaDigest: "native-search-schema", Pages: pages}
}
