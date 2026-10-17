//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/testcontainers/testcontainers-go"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	oracleNegativeTies = "negative-close-ties"
	oracleRoundtrip    = "roundtrip"
)

// This catches premature chunk limits, omitted windows, approximate score ties,
// locale ordering, incorrect source isolation and native arithmetic drift.
func TestPostgresNativeExhaustiveOracle(t *testing.T) {
	dsn := nativeOracleContainer(t)
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			options := nativeOptions()
			store := nativeOpen(t, dsn, options)
			snapshot := nativeSearchSnapshot(searchtest.NativeOraclePages(reverse))
			if err := store.Rebuild(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			var first string
			if err := store.db.QueryRowContext(t.Context(), `SELECT id FROM (VALUES ('A'),('a')) v(id) ORDER BY id LIMIT 1`).Scan(&first); err != nil || first != "a" {
				t.Fatalf("requires non-C locale first=%s err=%v", first, err)
			}
			type key struct {
				id      knowl.PageID
				ordinal int
			}
			vectors := map[key][]float32{}
			rows, err := store.db.QueryContext(t.Context(), "SELECT page_id,ordinal FROM knowl_embedding_chunks WHERE scope=$1", snapshot.Scope)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var k key
				if err := rows.Scan(&k.id, &k.ordinal); err != nil {
					t.Fatal(err)
				}
				vectors[k] = searchtest.NativeOracleVector(k.id, k.ordinal)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			_ = rows.Close()
			if len(vectors) < 60 {
				t.Fatalf("fixture needs crowding, got %d chunks", len(vectors))
			}
			for k, v := range vectors {
				encoded, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.ExecContext(t.Context(), "UPDATE knowl_embedding_chunks SET vector=$1 WHERE scope=$2 AND page_id=$3 AND ordinal=$4", string(encoded), snapshot.Scope, k.id, k.ordinal); err != nil {
					t.Fatal(err)
				}
			}
			selected := map[knowl.PageID]bool{"crowd": true, "Z": true, "a": true, "ä": true, "o00": true, "o01": true}
			for id := range selected {
				if _, err := store.db.ExecContext(t.Context(), "INSERT INTO knowl_page_sources(scope,page_id,source_id,document_id,revision,uri) VALUES($1,$2,$3,$4,$5,$6)", snapshot.Scope, id, "oracle-selected", string(id), "1", "fixture"); err != nil {
					t.Fatal(err)
				}
			}
			// A different legitimate model belongs in a different scope.
			foreign := snapshot
			foreign.Scope = "oracle-foreign"
			original := store.embedding
			foreignEngine := *original
			foreignEngine.Space.Model = "other-model"
			foreignEngine.Fingerprint, err = hybrid.SpaceFingerprint(foreignEngine.Space)
			if err != nil {
				t.Fatal(err)
			}
			store.embedding = &foreignEngine
			if err := store.Rebuild(t.Context(), foreign); err != nil {
				t.Fatal(err)
			}
			store.embedding = original
			for _, tc := range []struct {
				name    string
				queries [][]float32
				sources []knowl.SourceID
			}{
				{"reversal", [][]float32{{1, 0}}, nil},
				{"four-windows", [][]float32{{1, 0}, {1, 0}, {1, 0}, {0, 1}}, nil},
				{"source-selection", [][]float32{{1, 0}}, []knowl.SourceID{"oracle-selected"}},
				{"no-eligible-pages", [][]float32{{1, 0}}, []knowl.SourceID{"oracle-empty"}},
				{oracleNegativeTies, [][]float32{{1, 0}}, []knowl.SourceID{"oracle-negative"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if tc.name == oracleNegativeTies {
						for _, id := range []string{"Z", "a", "ä"} {
							if _, err := store.db.ExecContext(t.Context(), "INSERT INTO knowl_page_sources(scope,page_id,source_id,document_id,revision,uri) VALUES($1,$2,$3,$4,$5,$6)", snapshot.Scope, id, "oracle-negative", id, "1", "fixture"); err != nil {
								t.Fatal(err)
							}
						}
					}
					tx, err := store.db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					validated, err := store.readDenseProjectionTx(t.Context(), tx, snapshot.Scope, store.embedding.Fingerprint, 2)
					if err != nil {
						t.Fatal(err)
					}
					if validated.PublishedChunks != len(vectors) {
						t.Fatalf("validated=%d fixture=%d", validated.PublishedChunks, len(vectors))
					}
					var scores []searchtest.NativeScore
					maxError := 0.0
					for _, query := range tc.queries {
						encoded, err := json.Marshal(query)
						if err != nil {
							t.Fatal(err)
						}
						// No GROUP BY, ORDER BY, LIMIT, production SQL builder or source join:
						// enumerate every stored chunk, then apply fixture membership in Go.
						rows, err := tx.QueryContext(t.Context(), `SELECT page_id,ordinal,1-(vector OPERATOR("vector extension".<=>) $1::"vector extension".vector) FROM knowl_embedding_chunks WHERE scope=$2`, string(encoded), snapshot.Scope)
						if err != nil {
							t.Fatal(err)
						}
						count := 0
						for rows.Next() {
							var score searchtest.NativeScore
							if err := rows.Scan(&score.PageID, &score.Ordinal, &score.Score); err != nil {
								t.Fatal(err)
							}
							count++
							maxError = math.Max(maxError, searchtest.CheckNativeCosine(t, vectors[key{score.PageID, score.Ordinal}], query, score.Score))
							eligible := len(tc.sources) == 0 || (tc.name == "source-selection" && selected[score.PageID]) || (tc.name == oracleNegativeTies && (score.PageID == "Z" || score.PageID == "a" || score.PageID == "ä"))
							if eligible {
								scores = append(scores, score)
							}
						}
						if err := rows.Err(); err != nil {
							t.Fatal(err)
						}
						_ = rows.Close()
						if count != len(vectors) {
							t.Fatalf("enumerated=%d fixture=%d", count, len(vectors))
						}
					}
					for _, limit := range []int{1, 5, 20, 100} {
						want := searchtest.NativeWinners(scores, limit)
						matches, err := store.rankDensePagesTx(t.Context(), tx, snapshot.Scope, store.embedding.Fingerprint, tc.queries, tc.sources, limit)
						if err != nil {
							t.Fatal(err)
						}
						got := make([]searchtest.NativeScore, 0, len(matches))
						for _, match := range matches {
							if match.Evidence.Vector != nil {
								t.Fatal("query returned stored vector")
							}
							got = append(got, searchtest.NativeScore{PageID: match.Evidence.PageID, Ordinal: match.Evidence.Ordinal, Score: match.Score})
						}
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("limit=%d native=%+v exhaustive=%+v", limit, got, want)
						}
					}
					winners := searchtest.NativeWinners(scores, 100)
					if tc.name == "reversal" && (winners[0].PageID != "B" || winners[1].PageID != "A") {
						t.Fatalf("true cosine reversal=%v", winners[:2])
					}
					if tc.name == "four-windows" {
						for _, winner := range winners {
							if winner.PageID == "crowd" && winner.Ordinal != 1 {
								t.Fatalf("later window ordinal=%d", winner.Ordinal)
							}
						}
					}
					if tc.name == oracleNegativeTies && (len(winners) != 3 || winners[0].PageID != "ä" || winners[1].PageID != "Z" || winners[2].PageID != "a" || winners[0].Score >= 0) {
						t.Fatalf("negative close/ties=%v", winners)
					}
					t.Logf("scalar pairs=%d dimensions=2 maximum absolute math error=%g", len(vectors)*len(tc.queries), maxError)
				})
			}
		})
	}
}

func TestPostgresNativeNumericalOracleDimensions(t *testing.T) {
	dsn := nativeOracleContainer(t)
	for _, dimensions := range []int{1, 2000, 2001, 4096} {
		t.Run(fmt.Sprint(dimensions), func(t *testing.T) {
			vector := searchtest.NativeRoundtripVector(dimensions)
			provider := searchtest.NativeOracleProvider{Vector: vector}
			options := app.EmbeddingOptions{Provider: provider, FailurePolicy: app.EmbeddingStrict, Space: app.EmbeddingSpace{Model: "oracle", Revision: "1", Dimensions: dimensions}}
			store, err := Open(t.Context(), dsn, options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			snapshot := knowl.WorkspaceSnapshot{Scope: "oracle-dimensions", SchemaDigest: testSchemaDigest, Pages: []knowl.PageSnapshot{{ID: oracleRoundtrip, Path: "wiki/roundtrip.md", Digest: oracleRoundtrip, Body: "Original roundtrip evidence"}}}
			if err := store.Rebuild(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			var stored string
			if err := store.db.QueryRowContext(t.Context(), "SELECT vector::text FROM knowl_embedding_chunks WHERE scope=$1", snapshot.Scope).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			var decoded []float32
			if err := json.Unmarshal([]byte(stored), &decoded); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(decoded, vector) {
				t.Fatal("float32 publication changed fixture values")
			}
			queries := [][]float32{vector, slices.Clone(vector), slices.Clone(vector)}
			for i := range queries[1] {
				queries[1][i] = -queries[1][i]
			}
			slices.Reverse(queries[2])
			maxError := 0.0
			for _, query := range queries {
				encoded, err := json.Marshal(query)
				if err != nil {
					t.Fatal(err)
				}
				var score float64
				if err := store.db.QueryRowContext(t.Context(), `SELECT 1-(vector OPERATOR("vector extension".<=>) $1::"vector extension".vector) FROM knowl_embedding_chunks WHERE scope=$2`, string(encoded), snapshot.Scope).Scan(&score); err != nil {
					t.Fatal(err)
				}
				maxError = math.Max(maxError, searchtest.CheckNativeCosine(t, vector, query, score))
			}
			refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 1}, nil)
			if err != nil || len(refs) != 1 || refs[0].ID != oracleRoundtrip || report.ScannedChunks != 1 {
				t.Fatalf("refs=%v report=%+v err=%v", refs, report, err)
			}
			var version, extension string
			if err := store.db.QueryRowContext(t.Context(), "SELECT version(),extversion FROM pg_extension WHERE extname='vector'").Scan(&version, &extension); err != nil {
				t.Fatal(err)
			}
			t.Logf("postgres=%s pgvector=%s dimensions=%d maximum absolute math error=%g", version, extension, dimensions, maxError)
		})
	}
}

func nativeOracleContainer(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	container, err := pgcontainer.Run(ctx, "pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc", pgcontainer.WithDatabase("oracle_tests"), pgcontainer.WithUsername("knowl"), pgcontainer.WithPassword("knowl"), pgcontainer.BasicWaitStrategies(), pgcontainer.WithSQLDriver("pgx"))
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
	return dsn
}
