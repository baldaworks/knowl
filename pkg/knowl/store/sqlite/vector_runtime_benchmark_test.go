package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// This exact file and shared vector_benchmark.go are also executed on the
// actual pre-native checkout. It never substitutes a synthetic legacy pipeline.
func BenchmarkVectorRuntime(b *testing.B) {
	for _, c := range searchtest.VectorBenchmarkCases(false) {
		b.Run(c.Name(), func(b *testing.B) {
			ctx, cancel := context.WithTimeout(b.Context(), 180*time.Second)
			defer cancel()
			snapshot, options, query, sources, checksum, err := searchtest.VectorRuntimeFixture(ctx, c)
			if err != nil {
				b.Fatal(err)
			}
			observation := searchtest.VectorBenchmarkObservation{Mode: "production-interface", Backend: "sqlite", Case: c, Checksum: checksum, Outcome: "supported", LogicalScoredPairs: c.Chunks * c.Windows * c.SelectedPercent / 100}
			store, err := Open(ctx, b.TempDir()+"/vector-runtime.sqlite", options)
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			err = store.Rebuild(ctx, snapshot)
			var failure *app.EmbeddingError
			if errors.As(err, &failure) && failure.Code == knowl.RetrievalProjectionCapacity {
				observation.Outcome = "capacity_rejected"
				observation.Reason = string(failure.Code)
				searchtest.EmitVectorBenchmarkObservation(b, observation)
				b.Skip("unchanged production projection capacity")
			}
			if err != nil {
				b.Fatal(err)
			}
			var chunks int
			if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM knowl_embedding_chunks WHERE scope=?", snapshot.Scope).Scan(&chunks); err != nil || chunks != c.Chunks {
				b.Fatalf("published chunks=%d want=%d err=%v", chunks, c.Chunks, err)
			}
			probe := &searchtest.VectorTransferProbe{Record: true}
			databaseURL := url.URL{Scheme: "file", Path: store.path}
			q := databaseURL.Query()
			q.Set("_txlock", "immediate")
			databaseURL.RawQuery = q.Encode()
			observed := sql.OpenDB(searchtest.VectorProbeConnector{Connector: searchtest.VectorDriverConnector{DBDriver: store.db.Driver(), DSN: databaseURL.String()}, Probe: probe})
			observed.SetMaxOpenConns(1)
			observed.SetMaxIdleConns(1)
			original := store.db
			store.db = observed
			defer func() { store.db = original; _ = observed.Close() }()
			for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode = WAL", "PRAGMA busy_timeout=5000"} {
				if _, err := observed.ExecContext(ctx, pragma); err != nil {
					b.Fatal(err)
				}
			}
			refs, report, err := store.SearchWithReport(ctx, snapshot.Scope, query, knowl.ReadLimits{Pages: 5, Characters: 80}, sources)
			if err != nil || len(refs) != 5 || report.ScannedChunks != c.Chunks {
				b.Fatalf("warmup refs=%v report=%+v err=%v", refs, report, err)
			}
			recorded := probe.Queries
			if probe.VectorRows == 0 {
				observation.MathMaxAbsError = searchtest.CheckVectorRuntimeOracle(b, ctx, original, "sqlite", snapshot.Scope, c, options.Provider.(*searchtest.VectorBenchmarkProvider), refs)
				observation.NativeOracleChecked = true
			}
			plans := vectorRuntimeSQLitePlans(b, ctx, original, recorded)
			encoded, err := json.Marshal(struct {
				Backend, Revision string
				Case              searchtest.VectorBenchmarkCase
				Plans             []vectorSQLitePlan
			}{"sqlite", os.Getenv("KNOWL_VECTOR_BENCH_REVISION"), c, plans})
			if err != nil {
				b.Fatal(err)
			}
			b.Log(string(encoded))
			var version, journal, vecVersion string
			var foreignKeys, busyMS int
			if err := original.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version); err != nil {
				b.Fatal(err)
			}
			if observation.NativeOracleChecked {
				if err := original.QueryRowContext(ctx, "SELECT vec_version()").Scan(&vecVersion); err != nil {
					b.Fatal(err)
				}
			}
			if err := observed.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
				b.Fatal(err)
			}
			if err := observed.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
				b.Fatal(err)
			}
			if err := observed.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyMS); err != nil {
				b.Fatal(err)
			}
			b.Logf("sqlite=%s sqlite_vec=%s journal=%s foreign_keys=%d busy_timeout_ms=%d database=local-temporary-file scope=one pages=%d total-budget=180s-including-setup", version, vecVersion, journal, foreignKeys, busyMS, len(snapshot.Pages))
			wall := make([]int64, b.N)
			provider := options.Provider.(*searchtest.VectorBenchmarkProvider)
			provider.Calls = 0
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				*probe = searchtest.VectorTransferProbe{}
				started := time.Now()
				refs, report, err = store.SearchWithReport(ctx, snapshot.Scope, query, knowl.ReadLimits{Pages: 5, Characters: 80}, sources)
				wall[i] = time.Since(started).Nanoseconds()
				if err != nil || len(refs) != 5 || report.ScannedChunks != c.Chunks {
					b.Fatalf("iteration=%d refs=%v report=%+v err=%v", i, refs, report, err)
				}
				vectorAddTransfer(&observation.Transfer, *probe)
			}
			b.StopTimer()
			runtime.ReadMemStats(&after)
			observation.Samples = b.N
			observation.Retrieval = &report
			for _, ref := range refs {
				observation.ResultIDs = append(observation.ResultIDs, ref.ID)
			}
			observation.WallNS = wall
			observation.NSPerOp = float64(b.Elapsed().Nanoseconds()) / float64(b.N)
			observation.BPerOp = float64(after.TotalAlloc-before.TotalAlloc) / float64(b.N)
			observation.AllocsPerOp = float64(after.Mallocs-before.Mallocs) / float64(b.N)
			if provider.Calls != b.N {
				b.Fatalf("local fixed-vector provider calls=%d want=%d", provider.Calls, b.N)
			}
			b.ReportMetric(float64(observation.Transfer.VectorBytes)/float64(b.N), "stored-vector-B/op")
			b.ReportMetric(float64(observation.Transfer.CandidateRows)/float64(b.N), "candidate-rows/op")
			searchtest.EmitVectorBenchmarkObservation(b, observation)
		})
	}
}

func vectorAddTransfer(total *searchtest.VectorTransferProbe, p searchtest.VectorTransferProbe) {
	total.Rows += p.Rows
	total.VectorRows += p.VectorRows
	total.VectorBytes += p.VectorBytes
	total.MetadataRows += p.MetadataRows
	total.MetadataBytes += p.MetadataBytes
	total.CandidateRows += p.CandidateRows
	total.EvidenceRows += p.EvidenceRows
	total.ResultBytes += p.ResultBytes
	total.ScalarSQLNS += p.ScalarSQLNS
	total.RankingSQLNS += p.RankingSQLNS
	total.VectorSQLNS += p.VectorSQLNS
}

type vectorSQLitePlan struct {
	Category string
	Columns  []string
	Nodes    []vectorSQLitePlanNode
}
type vectorSQLitePlanNode struct {
	ID, Parent, Unused int64
	Detail             string
}

func vectorRuntimeSQLitePlans(b *testing.B, ctx context.Context, db *sql.DB, queries []searchtest.VectorRecordedQuery) []vectorSQLitePlan {
	b.Helper()
	var plans []vectorSQLitePlan
	for _, query := range queries {
		category := "readiness-or-lexical-or-evidence"
		if slices.Contains(query.Columns, "score") {
			category = "native-ranking"
		}
		if slices.Contains(query.Columns, "vector") {
			category = "legacy-full-vector-transfer"
		}
		rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query.SQL, query.Args...)
		if err != nil {
			b.Fatal(err)
		}
		plan := vectorSQLitePlan{Category: category, Columns: query.Columns}
		for rows.Next() {
			var node vectorSQLitePlanNode
			if err := rows.Scan(&node.ID, &node.Parent, &node.Unused, &node.Detail); err != nil {
				b.Fatal(err)
			}
			plan.Nodes = append(plan.Nodes, node)
		}
		if err := rows.Err(); err != nil {
			b.Fatal(err)
		}
		_ = rows.Close()
		plans = append(plans, plan)
	}
	return plans
}
