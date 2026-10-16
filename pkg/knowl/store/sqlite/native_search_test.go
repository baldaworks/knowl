package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	nativeQueryNeedle   = "unmatchedneedle"
	nativeCrowd         = "crowd"
	nativeWinner        = "winner"
	nativeOther         = "other"
	nativeOtherEvidence = "Other evidence"
	nativeSelected      = "selected"
)

type nativeWindowProvider struct{ calls, windows int }

func (p *nativeWindowProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	p.calls++
	p.windows = len(inputs)
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	vectors[len(vectors)-1] = []float32{0, 1}
	return vectors, nil
}

func nativeSearchFixture(t *testing.T, pages []knowl.PageSnapshot) (*Store, knowl.WorkspaceSnapshot) {
	t.Helper()
	store, err := Open(t.Context(), t.TempDir()+"/native-search.sqlite", app.EmbeddingOptions{
		Provider: fixedEmbeddingProvider{}, FailurePolicy: app.EmbeddingStrict,
		Space: app.EmbeddingSpace{Model: "native-search", Revision: "1", Dimensions: 2, QueryPrefix: "nativequery: ", PassagePrefix: "nativepassage: "},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := knowl.WorkspaceSnapshot{Scope: "native-search", SchemaDigest: testSchemaDigest, Pages: pages}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	return store, snapshot
}

func setNativeSearchVector(t *testing.T, store *Store, scope knowl.ScopeRef, id knowl.PageID, vector []float32) {
	t.Helper()
	encoded, err := hybrid.EncodeVector(vector, len(vector))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_chunks SET vector=? WHERE scope=? AND page_id=?`, encoded, scope, id); err != nil {
		t.Fatal(err)
	}
}

// Legacy dot scoring prefers A; native cosine must prefer B despite its smaller norm.
func TestSQLiteNativeSearchCosineReversal(t *testing.T) {
	store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{
		{ID: "A", Path: projectPathA, Digest: "a", Body: "Original A evidence"},
		{ID: "B", Path: "wiki/native-b.md", Digest: "b", Body: "Cafe\u0301 B\r\noriginal evidence"},
	})
	setNativeSearchVector(t, store, snapshot.Scope, "A", []float32{1, .01})
	setNativeSearchVector(t, store, snapshot.Scope, "B", []float32{.9998, 0})
	refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 2, Characters: 100}, nil)
	if err != nil || len(refs) != 2 || refs[0].ID != "B" || refs[1].ID != "A" || report.ScannedChunks != 2 || report.VectorCandidates != 2 {
		t.Fatalf("native cosine refs=%+v report=%+v err=%v", refs, report, err)
	}
	if refs[0].Snippet != "Cafe\u0301 B\r\noriginal evidence" {
		t.Fatalf("original evidence=%q", refs[0].Snippet)
	}
	ids, report, _, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeQueryNeedle}, knowl.ReadLimits{Pages: 2})
	if err != nil || len(ids) != 2 || ids[0] != "B" || ids[1] != "index" || report.ScannedChunks != 2 {
		t.Fatalf("native cosine context=%v report=%+v err=%v", ids, report, err)
	}
}

// The observer sees real driver results, so the legacy materializer fails this assertion.
func TestSQLiteNativeSearchTransfersNoVectors(t *testing.T) {
	store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{{ID: "a", Path: projectPathA, Digest: "a", Body: "Original evidence"}})
	transfer := &validationTransfers{}
	observed := sql.OpenDB(validationConnector{driver: store.db.Driver(), path: store.path, transfer: transfer})
	t.Cleanup(func() { _ = observed.Close() })
	original := store.db
	store.db = observed
	defer func() { store.db = original }()
	refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 1}, nil)
	if err != nil || len(refs) != 1 || report.ScannedChunks != 1 || transfer.vectorBytes != 0 || transfer.denseRows != 1 {
		t.Fatalf("refs=%v report=%+v vector bytes=%d dense rows=%d err=%v", refs, report, transfer.vectorBytes, transfer.denseRows, err)
	}
}

// A chunk shortlist would let crowd consume every slot and lose distinct pages.
func TestSQLiteNativeSearchAggregatesPagesBeforeLimit(t *testing.T) {
	for _, repetitions := range []int{4000, 14000} {
		t.Run(fmt.Sprint(repetitions), func(t *testing.T) {
			pages := []knowl.PageSnapshot{{ID: nativeCrowd, Path: "wiki/crowd.md", Digest: nativeCrowd, Body: strings.Repeat("crowd ", repetitions)}}
			for i := range 24 {
				id := fmt.Sprintf("p%02d", i)
				pages = append(pages, knowl.PageSnapshot{ID: knowl.PageID(id), Path: "wiki/" + id + ".md", Digest: id, Body: nativeOtherEvidence})
			}
			store, snapshot := nativeSearchFixture(t, pages)
			for _, page := range pages[1:] {
				setNativeSearchVector(t, store, snapshot.Scope, page.ID, []float32{.6, .8})
			}
			var count int
			if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM knowl_embedding_chunks WHERE scope=?`, snapshot.Scope).Scan(&count); err != nil {
				t.Fatal(err)
			}
			transfer := &validationTransfers{}
			observed := sql.OpenDB(validationConnector{driver: store.db.Driver(), path: store.path, transfer: transfer})
			t.Cleanup(func() { _ = observed.Close() })
			original := store.db
			store.db = observed
			defer func() { store.db = original }()
			refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 5, Characters: 30}, nil)
			var ids []knowl.PageID
			for _, ref := range refs {
				ids = append(ids, ref.ID)
			}
			if err != nil || !slices.Equal(ids, []knowl.PageID{nativeCrowd, "p00", "p01", "p02", "p03"}) || report.VectorCandidates != 20 || report.ScannedChunks != count || transfer.vectorBytes != 0 || transfer.denseRows != 20 {
				t.Fatalf("ids=%v report=%+v count=%d transfer=%+v err=%v", ids, report, count, transfer, err)
			}
			ids, report, _, err = store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeQueryNeedle}, knowl.ReadLimits{Pages: 5})
			if err != nil || !slices.Equal(ids, []knowl.PageID{nativeCrowd, "p00", "p01", "index", "p02"}) || report.VectorCandidates != 20 || report.ScannedChunks != count || transfer.vectorBytes != 0 || transfer.denseRows != 40 {
				t.Fatalf("context=%v report=%+v transfer=%+v err=%v", ids, report, transfer, err)
			}
		})
	}
}

// The fourth query window wins; identical chunk scores must choose ordinal zero.
func TestSQLiteNativeSearchAllWindowsAndOrdinalTies(t *testing.T) {
	store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{
		{ID: nativeWinner, Path: "wiki/winner.md", Digest: nativeWinner, Body: strings.Repeat("W", 384) + "\r\n\r\n" + strings.Repeat("Z", 384)},
		{ID: nativeOther, Path: "wiki/other.md", Digest: nativeOther, Body: nativeOtherEvidence},
	})
	setNativeSearchVector(t, store, snapshot.Scope, nativeWinner, []float32{0, 1})
	setNativeSearchVector(t, store, snapshot.Scope, nativeOther, []float32{.6, .8})
	p := &nativeWindowProvider{}
	store.embedding.Provider = p
	query := strings.Repeat("unmatchedneedle ", 200)
	refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, query, knowl.ReadLimits{Pages: 2, Characters: 100}, nil)
	if err != nil || len(refs) != 2 || refs[0].ID != nativeWinner || refs[0].Snippet != strings.Repeat("W", 100) || p.calls != 1 || p.windows != 4 || report.QueryOmittedRunes == 0 {
		t.Fatalf("refs=%+v report=%+v provider=%+v err=%v", refs, report, p, err)
	}
	ids, report, _, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Body: query}, knowl.ReadLimits{Pages: 2})
	if err != nil || len(ids) != 2 || ids[0] != nativeWinner || p.calls != 2 || p.windows != 4 || report.VectorCandidates != 2 {
		t.Fatalf("context=%v report=%+v provider=%+v err=%v", ids, report, p, err)
	}
}

func TestSQLiteNativeSearchNegativeCloseAndBinaryTies(t *testing.T) {
	store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{
		{ID: "ä", Path: "wiki/umlaut.md", Digest: "u", Body: "U evidence"},
		{ID: "a", Path: projectPathA, Digest: "a", Body: "A evidence"},
		{ID: "Z", Path: "wiki/z.md", Digest: "z", Body: "Z evidence"},
	})
	for _, page := range snapshot.Pages {
		setNativeSearchVector(t, store, snapshot.Scope, page.ID, []float32{-.6, .8})
	}
	check := func(want []knowl.PageID) {
		t.Helper()
		refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 3}, nil)
		var ids []knowl.PageID
		for _, ref := range refs {
			ids = append(ids, ref.ID)
		}
		if err != nil || !slices.Equal(ids, want) || report.VectorCandidates != 3 {
			t.Fatalf("ids=%v report=%+v err=%v", ids, report, err)
		}
	}
	check([]knowl.PageID{"Z", "a", "ä"})
	setNativeSearchVector(t, store, snapshot.Scope, "ä", []float32{-.599999, .80000075})
	check([]knowl.PageID{"ä", "Z", "a"})
}

func TestSQLiteNativeSearchSourceEligibilityAndWholeScopeValidation(t *testing.T) {
	store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{
		{ID: "excluded", Path: "wiki/excluded.md", Digest: "x", Body: "Excluded evidence"},
		{ID: "included", Path: "wiki/included.md", Digest: "i", Body: "Included evidence"},
	})
	setNativeSearchVector(t, store, snapshot.Scope, "included", []float32{0, 1})
	for _, doc := range []string{"one", "two"} {
		if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_page_sources(scope,page_id,source_id,document_id,revision,uri) VALUES(?,?,?,?,?,?)`, snapshot.Scope, "included", nativeSelected, doc, "1", "uri"); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		source knowl.SourceID
		want   int
	}{{nativeSelected, 1}, {"native-missing", 0}} {
		refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 2}, []knowl.SourceID{tc.source})
		if err != nil || len(refs) != tc.want || (len(refs) > 0 && refs[0].ID != "included") || report.ScannedChunks != 2 || report.VectorCandidates != tc.want {
			t.Fatalf("source=%s refs=%v report=%+v err=%v", tc.source, refs, report, err)
		}
	}
	// Foreign scopes cannot affect selection or full-scope validation.
	foreign := snapshot
	foreign.Scope = "native-foreign"
	if err := store.Rebuild(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_chunks SET vector=zeroblob(8) WHERE scope=?`, foreign.Scope); err != nil {
		t.Fatal(err)
	}
	refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 2}, []knowl.SourceID{nativeSelected})
	if err != nil || len(refs) != 1 || report.ScannedChunks != 2 {
		t.Fatalf("foreign scope refs=%v report=%+v err=%v", refs, report, err)
	}
	// Corruption on an unselected page must fail before eligibility or LIMIT.
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_chunks SET vector=zeroblob(8) WHERE scope=? AND page_id=?`, snapshot.Scope, "excluded"); err != nil {
		t.Fatal(err)
	}
	refs, report, err = store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 1}, []knowl.SourceID{nativeSelected})
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
	if len(refs) != 0 || report.ScannedChunks != 0 || report.Effective != knowl.RetrievalFailed {
		t.Fatalf("corruption refs=%v report=%+v", refs, report)
	}
	_, report, _, err = store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeQueryNeedle}, knowl.ReadLimits{Pages: 1})
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
	if report.ScannedChunks != 0 {
		t.Fatalf("corrupt context report=%+v", report)
	}
}

func TestSQLiteNativeSearchEmptyReadyProjection(t *testing.T) {
	store, snapshot := nativeSearchFixture(t, nil)
	refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeQueryNeedle, knowl.ReadLimits{Pages: 1}, nil)
	if err != nil || len(refs) != 0 || report.Effective != knowl.RetrievalHybrid || report.VectorCandidates != 0 || report.ScannedChunks != 0 {
		t.Fatalf("empty refs=%v report=%+v err=%v", refs, report, err)
	}
}

func TestSQLiteNativeSearchCorruptNonwinnerPolicies(t *testing.T) {
	for _, policy := range []app.EmbeddingFailurePolicy{app.EmbeddingStrict, app.EmbeddingFallbackLexical} {
		for _, tc := range []struct {
			name, sql string
			arg       any
		}{
			{"foreign model", `UPDATE knowl_embedding_chunks SET space=? WHERE scope=? AND page_id='nonwinner'`, strings.Repeat("f", 64)},
			{"snapshot", `UPDATE knowl_embedding_state SET snapshot_digest=? WHERE scope=?`, strings.Repeat("f", 64)},
			{"old native format", `UPDATE knowl_embedding_state SET native_format=? WHERE scope=?`, 0},
			{"nonwinner dimension", `UPDATE knowl_embedding_chunks SET dimensions=? WHERE scope=? AND page_id='nonwinner'`, 3},
		} {
			t.Run(string(policy)+"/"+tc.name, func(t *testing.T) {
				store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{
					{ID: nativeWinner, Path: "wiki/winner.md", Digest: nativeWinner, Body: "lexicalneedle winner"},
					{ID: "nonwinner", Path: "wiki/nonwinner.md", Digest: "nonwinner", Body: nativeOtherEvidence},
				})
				store.embedding.FailurePolicy = policy
				setNativeSearchVector(t, store, snapshot.Scope, "nonwinner", []float32{-1, 0})
				if _, err := store.db.ExecContext(t.Context(), `PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.ExecContext(t.Context(), tc.sql, tc.arg, snapshot.Scope); err != nil {
					t.Fatal(err)
				}
				want := knowl.RetrievalProjectionDrift
				if tc.name == "old native format" {
					want = knowl.RetrievalProjectionNotReady
				}
				refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, "lexicalneedle", knowl.ReadLimits{Pages: 1}, nil)
				if policy == app.EmbeddingStrict {
					assertEmbeddingFailure(t, err, want)
					if len(refs) != 0 || report.Effective != knowl.RetrievalFailed {
						t.Fatalf("strict refs=%v report=%+v", refs, report)
					}
				} else if err != nil || len(refs) != 1 || refs[0].ID != nativeWinner || report.Effective != knowl.RetrievalDegraded {
					t.Fatalf("fallback refs=%v report=%+v err=%v", refs, report, err)
				}
				if report.Reason != want || report.ScannedChunks != 0 || report.VectorCandidates != 0 {
					t.Fatalf("invalid projection report=%+v", report)
				}
				ids, contextReport, metadata, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: "lexicalneedle"}, knowl.ReadLimits{Pages: 1})
				if policy == app.EmbeddingStrict {
					assertEmbeddingFailure(t, err, want)
					if len(ids) != 0 {
						t.Fatalf("strict context=%v", ids)
					}
				} else if err != nil || len(ids) != 1 || ids[0] != nativeWinner || contextReport.Effective != knowl.RetrievalDegraded {
					t.Fatalf("fallback context=%v report=%+v err=%v", ids, contextReport, err)
				}
				if metadata.VectorProjection.State != knowl.VectorInvalid || metadata.VectorProjection.Reason != want || contextReport.ScannedChunks != 0 {
					t.Fatalf("context report=%+v metadata=%+v", contextReport, metadata)
				}
			})
		}
	}
}

// A lexical hit keeps the lexical snippet even when dense evidence would fail.
func TestSQLiteNativeSearchPrefersLexicalSnippet(t *testing.T) {
	store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{{ID: "a", Path: projectPathA, Digest: "a", Body: "Original lexicalneedle evidence"}})
	limits := knowl.ReadLimits{Pages: 1, Characters: 100}
	lexical, err := store.search(t.Context(), snapshot.Scope, "lexicalneedle", limits, nil)
	if err != nil || len(lexical) != 1 {
		t.Fatalf("lexical fixture=%v err=%v", lexical, err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_chunks SET content_hash=? WHERE scope=?`, strings.Repeat("f", 64), snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, "lexicalneedle", limits, nil)
	if err != nil || len(refs) != 1 || refs[0].Snippet != lexical[0].Snippet || report.Effective != knowl.RetrievalHybrid || report.LexicalCandidates != 1 || report.VectorCandidates != 1 {
		t.Fatalf("lexical preference refs=%v report=%+v err=%v", refs, report, err)
	}
}

// Normal retrieval rejects these vectors during validation. Exercise the native
// scoring guard independently so neither MAX nor the final limit can hide an
// unexpected NULL/NaN score from a later chunk on a page outside top-k.
func TestSQLiteNativeScoresRejectInvalidNonwinningChunk(t *testing.T) {
	for _, value := range []float32{0, float32(math.Inf(1)), float32(math.NaN())} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			store, snapshot := nativeSearchFixture(t, []knowl.PageSnapshot{
				{ID: "a", Path: projectPathA, Digest: "a", Body: "Valid winner"},
				{ID: "z", Path: "wiki/native-invalid.md", Digest: "z", Body: strings.Repeat("x ", 500)},
			})
			encoded := make([]byte, 8)
			binary.LittleEndian.PutUint32(encoded, math.Float32bits(value))
			result, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_chunks SET vector=? WHERE scope=? AND page_id=? AND ordinal=?`, encoded, snapshot.Scope, "z", 1)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := result.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("invalid chunk fixture rows=%d err=%v", n, err)
			}
			tx, err := store.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			matches, err := rankDensePagesTx(t.Context(), tx, snapshot.Scope, store.embedding.Fingerprint, [][]float32{{1, 0}, {0, 1}}, nil, 1)
			assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
			if len(matches) != 0 {
				t.Fatalf("invalid native score returned matches=%v", matches)
			}
		})
	}
}
