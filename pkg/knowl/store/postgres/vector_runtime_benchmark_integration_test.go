//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
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
			observation := searchtest.VectorBenchmarkObservation{Mode: "production-interface", Backend: "postgres", Case: c, Checksum: checksum, Outcome: "supported", LogicalScoredPairs: c.Chunks * c.Windows * c.SelectedPercent / 100}
			dsn := vectorRuntimePostgresDSN(b, ctx)
			store, err := Open(ctx, dsn, options)
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
			if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM knowl_embedding_chunks WHERE scope=$1", snapshot.Scope).Scan(&chunks); err != nil || chunks != c.Chunks {
				b.Fatalf("published chunks=%d want=%d err=%v", chunks, c.Chunks, err)
			}
			probe := &searchtest.VectorTransferProbe{Record: true}
			config, err := pgx.ParseConfig(dsn)
			if err != nil {
				b.Fatal(err)
			}
			observed := sql.OpenDB(searchtest.VectorProbeConnector{Connector: stdlib.GetConnector(*config), Probe: probe})
			observed.SetMaxOpenConns(1)
			observed.SetMaxIdleConns(1)
			original := store.db
			store.db = observed
			defer func() { store.db = original; _ = observed.Close() }()
			refs, report, err := store.SearchWithReport(ctx, snapshot.Scope, query, knowl.ReadLimits{Pages: 5, Characters: 80}, sources)
			if err != nil || len(refs) != 5 || report.ScannedChunks != c.Chunks {
				b.Fatalf("warmup refs=%v report=%+v err=%v", refs, report, err)
			}
			recorded := probe.Queries
			if probe.VectorRows == 0 {
				observation.MathMaxAbsError = searchtest.CheckVectorRuntimeOracle(b, ctx, original, "postgres", snapshot.Scope, c, options.Provider.(*searchtest.VectorBenchmarkProvider), refs)
				observation.NativeOracleChecked = true
			}
			plans := vectorRuntimePostgresPlans(b, ctx, original, recorded)
			encoded, err := json.Marshal(struct {
				Backend, Revision string
				Case              searchtest.VectorBenchmarkCase
				Plans             []vectorPostgresPlan
			}{"postgres", os.Getenv("KNOWL_VECTOR_BENCH_REVISION"), c, plans})
			if err != nil {
				b.Fatal(err)
			}
			b.Log(string(encoded))
			var version, extension, sharedBuffers, workMem, jit string
			if err := original.QueryRowContext(ctx, "SELECT current_setting('server_version'),extversion,current_setting('shared_buffers'),current_setting('work_mem'),current_setting('jit') FROM pg_extension WHERE extname='vector'").Scan(&version, &extension, &sharedBuffers, &workMem, &jit); err != nil {
				b.Fatal(err)
			}
			b.Logf("postgres=%s pgvector=%s shared_buffers=%s work_mem=%s jit=%s database=disposable-container-local-TCP scope=one pages=%d pool=1 total-budget=180s-including-setup", version, extension, sharedBuffers, workMem, jit, len(snapshot.Pages))
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

type vectorPostgresPlan struct {
	Category  string
	Columns   []string
	Execution []vectorPGExecution
}
type vectorPGExecution struct {
	Plan          vectorPGNode
	PlanningTime  float64 `json:"Planning Time"`
	ExecutionTime float64 `json:"Execution Time"`
}

// Expression/filter strings are deliberately omitted: EXPLAIN may inline a
// bound query vector. Typed executor work retains rows/loops/time/buffers only.
type vectorPGNode struct {
	NodeType          string  `json:"Node Type"`
	Relation          string  `json:"Relation Name"`
	ActualRows        float64 `json:"Actual Rows"`
	ActualLoops       float64 `json:"Actual Loops"`
	ActualStartupTime float64 `json:"Actual Startup Time"`
	ActualTotalTime   float64 `json:"Actual Total Time"`
	SharedHitBlocks   int64   `json:"Shared Hit Blocks"`
	SharedReadBlocks  int64   `json:"Shared Read Blocks"`
	TempReadBlocks    int64   `json:"Temp Read Blocks"`
	TempWrittenBlocks int64   `json:"Temp Written Blocks"`
	RowsRemoved       float64 `json:"Rows Removed by Filter"`
	Plans             []vectorPGNode
}

func vectorRuntimePostgresPlans(b *testing.B, ctx context.Context, db *sql.DB, queries []searchtest.VectorRecordedQuery) []vectorPostgresPlan {
	b.Helper()
	var plans []vectorPostgresPlan
	for _, query := range queries {
		category := "readiness-or-lexical-or-evidence"
		if slices.Contains(query.Columns, "score") {
			category = "native-ranking"
		}
		if slices.Contains(query.Columns, "vector") {
			category = "legacy-full-vector-transfer"
		}
		var encoded []byte
		if err := db.QueryRowContext(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query.SQL, query.Args...).Scan(&encoded); err != nil {
			b.Fatal(err)
		}
		plan := vectorPostgresPlan{Category: category, Columns: query.Columns}
		if err := json.Unmarshal(encoded, &plan.Execution); err != nil {
			b.Fatal(err)
		}
		if len(plan.Execution) != 1 || plan.Execution[0].Plan.NodeType == "" {
			b.Fatal("invalid structured executor plan")
		}
		plans = append(plans, plan)
	}
	return plans
}

func vectorRuntimePostgresDSN(b *testing.B, ctx context.Context) string {
	b.Helper()
	container, err := pgcontainer.Run(ctx, "pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc", pgcontainer.WithDatabase("vector_benchmark"), pgcontainer.WithUsername("knowl"), pgcontainer.WithPassword("knowl"), pgcontainer.BasicWaitStrategies(), pgcontainer.WithSQLDriver("pgx"))
	if err != nil {
		b.Fatal(err)
	}
	testcontainers.CleanupContainer(b, container)
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		b.Fatal(err)
	}
	operator, err := sql.Open("pgx", dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = operator.Close() }()
	if _, err := operator.ExecContext(ctx, "CREATE EXTENSION vector"); err != nil {
		b.Fatal(err)
	}
	return dsn
}
