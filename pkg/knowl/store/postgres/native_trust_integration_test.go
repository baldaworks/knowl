//go:build integration

package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresNativeTrustReadPlans(t *testing.T) {
	dsn := nativeOracleContainer(t)
	store := nativeOpen(t, dsn, nativeOptions())
	for _, count := range []int{1, 64} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			pages := make([]knowl.PageSnapshot, count)
			for i := range pages {
				id := fmt.Sprintf("page-%03d", i)
				pages[i] = knowl.PageSnapshot{ID: knowl.PageID(id), Path: id + ".md", Digest: id, Body: "Original trust evidence"}
			}
			snapshot := nativeSearchSnapshot(pages)
			if err := store.Rebuild(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			config, err := pgx.ParseConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			probe := &searchtest.VectorTransferProbe{Record: true}
			original := store.db
			observed := sql.OpenDB(searchtest.VectorProbeConnector{Connector: stdlib.GetConnector(*config), Probe: probe})
			store.db = observed
			defer func() { store.db = original; _ = observed.Close() }()
			calls := []func() error{
				func() error {
					_, _, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeSearchNeedle, knowl.ReadLimits{Pages: 1}, nil)
					return err
				},
				func() error {
					_, _, _, err := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeSearchNeedle}, knowl.ReadLimits{Pages: 1})
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
				for _, query := range probe.Queries {
					if slices.Contains(query.Columns, "coverage") || slices.Contains(query.Columns, "vector") {
						t.Fatalf("unbounded read columns=%v", query.Columns)
					}
					if slices.Contains(query.Columns, "score") {
						continue
					}
					var encoded []byte
					if err := original.QueryRowContext(t.Context(), "EXPLAIN (FORMAT JSON) "+query.SQL, query.Args...).Scan(&encoded); err != nil {
						t.Fatal(err)
					}
					var execution []vectorPGExecution
					if err := json.Unmarshal(encoded, &execution); err != nil {
						t.Fatal(err)
					}
					var inspect func(vectorPGNode)
					inspect = func(node vectorPGNode) {
						if node.Relation == "knowl_embedding_chunks" || (len(query.Columns) <= 2 && node.Relation == "knowl_pages") {
							t.Fatalf("nonranking population audit relation=%s columns=%v", node.Relation, query.Columns)
						}
						for _, child := range node.Plans {
							inspect(child)
						}
					}
					for _, item := range execution {
						inspect(item.Plan)
					}
				}
			}
		})
	}
}
