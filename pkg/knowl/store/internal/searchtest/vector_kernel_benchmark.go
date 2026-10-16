package searchtest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	vectorKernelNativeMode = "native-sql"
	vectorKernelScope      = "kernel-main"
)

type vectorKernelChunk struct {
	scope    string
	score    NativeScore
	vector   []float32
	encoded  []byte
	selected bool
}

// RunVectorKernelBenchmarks compares isolated native SQL page maxima with an
// isolated legacy full-transfer/dot baseline on identical disposable tables.
// BOTH omit production readiness, full-scope integrity, capacity, inference,
// lexical search, RRF and original evidence. No supported runtime claim follows.
func RunVectorKernelBenchmarks(b *testing.B, backend string, open func(*testing.B, context.Context) (*sql.DB, func(), error)) {
	b.Helper()
	for _, c := range VectorBenchmarkCases(true) {
		b.Run(c.Name(), func(b *testing.B) {
			ctx, cancel := context.WithTimeout(b.Context(), 180*time.Second)
			defer cancel()
			if estimate := VectorBenchmarkResourceEstimate(c); estimate > 512<<20 {
				EmitVectorBenchmarkObservation(b, VectorBenchmarkObservation{Mode: "isolated-kernel", Backend: backend, Case: c, Outcome: "resource_limited", Reason: fmt.Sprintf("estimated working set %d exceeds fixed 536870912-byte lab budget; no vectors/DB allocated", estimate)})
				b.Skip("bounded laboratory memory estimate")
			}
			db, closeDB, err := open(b, ctx)
			if err != nil {
				b.Fatal(err)
			}
			defer closeDB()
			chunks, queries, checksum := vectorKernelFixture(b, ctx, c)
			vectorKernelTables(b, ctx, db, backend, chunks)
			nativeSQL, args := vectorKernelQuery(b, backend, c, queries)
			want, maxError := vectorKernelOracle(b, ctx, db, backend, c, chunks, queries)
			actual, err := vectorKernelNative(ctx, db, nativeSQL, args, nil)
			if err != nil || !reflect.DeepEqual(actual, want) {
				b.Fatalf("kernel native=%v exhaustive=%v err=%v", actual, want, err)
			}
			vectorKernelExplain(b, ctx, db, backend, nativeSQL, args, c, "native")
			legacySQL, legacyArgs := vectorKernelLegacyQuery(backend)
			vectorKernelExplain(b, ctx, db, backend, legacySQL, legacyArgs, c, "legacy")
			for _, mode := range []string{vectorKernelNativeMode, "legacy-full-transfer"} {
				b.Run(mode, func(b *testing.B) {
					observation := VectorBenchmarkObservation{Mode: "isolated-kernel-" + mode, Backend: backend, Case: c, Checksum: checksum, Outcome: "supported", LogicalScoredPairs: c.Chunks * c.Windows * c.SelectedPercent / 100, NativeOracleChecked: mode == vectorKernelNativeMode, MathMaxAbsError: maxError}
					wall := make([]int64, b.N)
					var before, after runtime.MemStats
					runtime.ReadMemStats(&before)
					b.ReportAllocs()
					b.ResetTimer()
					var winners []NativeScore
					for i := range b.N {
						started := time.Now()
						if mode == vectorKernelNativeMode {
							winners, err = vectorKernelNative(ctx, db, nativeSQL, args, &observation.Transfer)
						} else {
							winners, err = vectorKernelLegacy(ctx, db, backend, c, queries, &observation.Transfer)
						}
						wall[i] = time.Since(started).Nanoseconds()
						if err != nil {
							b.Fatal(err)
						}
						if len(winners) != 20 {
							b.Fatalf("kernel distinct page winners=%d want20", len(winners))
						}
						if mode == vectorKernelNativeMode && !reflect.DeepEqual(winners, want) {
							b.Fatal("native kernel changed exact winners")
						}
					}
					b.StopTimer()
					runtime.ReadMemStats(&after)
					observation.Samples = b.N
					observation.WallNS = wall
					observation.NSPerOp = float64(b.Elapsed().Nanoseconds()) / float64(b.N)
					observation.BPerOp = float64(after.TotalAlloc-before.TotalAlloc) / float64(b.N)
					observation.AllocsPerOp = float64(after.Mallocs-before.Mallocs) / float64(b.N)
					for _, winner := range winners {
						observation.ResultIDs = append(observation.ResultIDs, winner.PageID)
					}
					// Counters accumulate actual scanned values, not fixture estimates.
					b.ReportMetric(float64(observation.Transfer.VectorBytes)/float64(b.N), "stored-vector-B/op")
					EmitVectorBenchmarkObservation(b, observation)
				})
			}
		})
	}
}

func vectorKernelFixture(b *testing.B, ctx context.Context, c VectorBenchmarkCase) ([]vectorKernelChunk, [][]float32, string) {
	b.Helper()
	queries := make([][]float32, c.Windows)
	for i := range queries {
		queries[i] = VectorBenchmarkVector(fmt.Sprintf("kernel-query-%d", i), c.Dimensions)
	}
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "kernel seed73 n%d d%d w%d selected%d pages128 foreign8\n", c.Chunks, c.Dimensions, c.Windows, c.SelectedPercent)
	chunks := make([]vectorKernelChunk, 0, c.Chunks+8)
	for i := range c.Chunks + 8 {
		if err := ctx.Err(); err != nil {
			b.Fatal(err)
		}
		id := knowl.PageID(fmt.Sprintf("p%06d", i%128))
		ordinal := i / 128
		scope := vectorKernelScope
		selected := i%128%2 == 0
		vector := VectorBenchmarkVector(fmt.Sprintf("kernel-chunk-%d", i), c.Dimensions)
		if i >= c.Chunks {
			scope = "kernel-foreign"
			id = knowl.PageID(fmt.Sprintf("foreign%02d", i-c.Chunks))
			ordinal = 0
			vector = queries[0]
			selected = true
		}
		encoded, err := hybrid.EncodeVector(vector, c.Dimensions)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = fmt.Fprintf(hash, "%s/%s/%d/%t\n", scope, id, ordinal, selected)
		hash.Write(encoded)
		chunks = append(chunks, vectorKernelChunk{scope: scope, score: NativeScore{PageID: id, Ordinal: ordinal}, vector: vector, encoded: encoded, selected: selected})
	}
	return chunks, queries, hex.EncodeToString(hash.Sum(nil))
}

func vectorKernelTables(b *testing.B, ctx context.Context, db *sql.DB, backend string, chunks []vectorKernelChunk) {
	b.Helper()
	nativeType, legacyType := "BLOB", "BLOB"
	if backend == vectorBenchPostgres {
		nativeType = "public.vector"
		legacyType = "BYTEA"
	}
	for _, table := range []struct{ name, kind string }{{"vector_benchmark_native", nativeType}, {"vector_benchmark_legacy", legacyType}} {
		if _, err := db.ExecContext(ctx, "CREATE TABLE "+table.name+"(scope TEXT,space TEXT,page_id TEXT,page_digest TEXT,ordinal INTEGER,content_hash TEXT,selected BOOLEAN,vector "+table.kind+",PRIMARY KEY(scope,space,page_id,ordinal))"); err != nil {
			b.Fatal(err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	binds := "?,?,?,?,?,?,?,?"
	if backend == vectorBenchPostgres {
		binds = "$1,$2,$3,$4,$5,$6,$7,$8"
	}
	native, err := tx.PrepareContext(ctx, "INSERT INTO vector_benchmark_native VALUES("+binds+")")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = native.Close() }()
	legacy, err := tx.PrepareContext(ctx, "INSERT INTO vector_benchmark_legacy VALUES("+binds+")")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = legacy.Close() }()
	for _, chunk := range chunks {
		var encoded any = chunk.encoded
		if backend == vectorBenchPostgres {
			text, err := json.Marshal(chunk.vector)
			if err != nil {
				b.Fatal(err)
			}
			encoded = string(text)
		}
		hash := sha256.Sum256(chunk.encoded)
		args := []any{chunk.scope, strings.Repeat("a", 64), chunk.score.PageID, string(chunk.score.PageID), chunk.score.Ordinal, hex.EncodeToString(hash[:]), chunk.selected, encoded}
		if _, err := native.ExecContext(ctx, args...); err != nil {
			b.Fatal(err)
		}
		args[7] = chunk.encoded
		if _, err := legacy.ExecContext(ctx, args...); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func vectorKernelQuery(b *testing.B, backend string, c VectorBenchmarkCase, queries [][]float32) (string, []any) {
	b.Helper()
	values := make([]string, len(queries))
	args := make([]any, 0, len(queries)+3)
	for i, query := range queries {
		if backend == vectorBenchPostgres {
			values[i] = fmt.Sprintf("($%d::public.vector)", i+1)
			encoded, err := json.Marshal(query)
			if err != nil {
				b.Fatal(err)
			}
			args = append(args, string(encoded))
		} else {
			values[i] = "(?)"
			encoded, err := hybrid.EncodeVector(query, c.Dimensions)
			if err != nil {
				b.Fatal(err)
			}
			args = append(args, encoded)
		}
	}
	score := "1-vec_distance_cosine(c.vector,q.vector)"
	scope, space, limit, collation := "?", "?", "?", "BINARY"
	if backend == vectorBenchPostgres {
		score = "1-(c.vector OPERATOR(public.<=>) q.vector)"
		scope = fmt.Sprintf("$%d", len(args)+1)
		space = fmt.Sprintf("$%d", len(args)+2)
		limit = fmt.Sprintf("$%d", len(args)+3)
		collation = `"C"`
	}
	filter := ""
	if c.SelectedPercent == 50 {
		filter = " AND c.selected"
	}
	args = append(args, vectorKernelScope, strings.Repeat("a", 64), 20)
	statement := `WITH queries(vector) AS (VALUES ` + strings.Join(values, ",") + `),scored AS MATERIALIZED (
	 SELECT c.page_id,c.page_digest,c.ordinal,c.content_hash,` + score + ` AS score FROM vector_benchmark_native c CROSS JOIN queries q WHERE c.scope=` + scope + ` AND c.space=` + space + filter + `
	),chunk_best AS (SELECT page_id,page_digest,ordinal,content_hash,MAX(score) AS score FROM scored GROUP BY page_id,page_digest,ordinal,content_hash),
	page_ordered AS (SELECT page_id,ordinal,score,ROW_NUMBER() OVER(PARTITION BY page_id ORDER BY score DESC,ordinal ASC) AS winner FROM chunk_best)
	SELECT page_id,ordinal,score FROM page_ordered WHERE winner=1 ORDER BY score DESC,page_id COLLATE ` + collation + ` ASC LIMIT ` + limit
	return statement, args
}

func vectorKernelNative(ctx context.Context, db *sql.DB, statement string, args []any, transfer *VectorTransferProbe) ([]NativeScore, error) {
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var winners []NativeScore
	for rows.Next() {
		var winner NativeScore
		if err := rows.Scan(&winner.PageID, &winner.Ordinal, &winner.Score); err != nil {
			return nil, err
		}
		winners = append(winners, winner)
		if transfer != nil {
			transfer.Rows++
			transfer.CandidateRows++
			transfer.ResultBytes += int64(len(winner.PageID) + 16)
		}
	}
	return winners, rows.Err()
}

func vectorKernelLegacyQuery(backend string) (string, []any) {
	placeholder := "?"
	if backend == vectorBenchPostgres {
		placeholder = "$1"
	}
	return "SELECT page_id,ordinal,selected,vector FROM vector_benchmark_legacy WHERE scope=" + placeholder, []any{vectorKernelScope}
}

func vectorKernelLegacy(ctx context.Context, db *sql.DB, backend string, c VectorBenchmarkCase, queries [][]float32, transfer *VectorTransferProbe) ([]NativeScore, error) {
	statement, args := vectorKernelLegacyQuery(backend)
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	// Decode the complete scope before eligibility/ranking, as the legacy
	// transfer path did. Norm, readiness and capacity checks are omitted on BOTH
	// isolated kernels; the fixture is validated outside the measurement.
	var chunks []vectorKernelChunk
	for rows.Next() {
		var chunk vectorKernelChunk
		var encoded []byte
		if err := rows.Scan(&chunk.score.PageID, &chunk.score.Ordinal, &chunk.selected, &encoded); err != nil {
			return nil, err
		}
		if len(encoded) != 4*c.Dimensions {
			return nil, fmt.Errorf("invalid isolated fixture vector length: %d", len(encoded))
		}
		if transfer != nil {
			transfer.Rows++
			transfer.VectorRows++
			transfer.VectorBytes += int64(len(encoded))
			transfer.ResultBytes += int64(len(chunk.score.PageID) + 9 + len(encoded))
		}
		chunk.vector = make([]float32, c.Dimensions)
		for i := range chunk.vector {
			chunk.vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[4*i:]))
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(chunks) != c.Chunks {
		return nil, fmt.Errorf("isolated legacy rows: got %d want%d", len(chunks), c.Chunks)
	}
	var scores []NativeScore
	for _, chunk := range chunks {
		if c.SelectedPercent == 50 && !chunk.selected {
			continue
		}
		for _, query := range queries {
			var dot float64
			for i, value := range chunk.vector {
				dot += float64(value) * float64(query[i])
			}
			scores = append(scores, NativeScore{PageID: chunk.score.PageID, Ordinal: chunk.score.Ordinal, Score: max(-1, min(1, dot))})
		}
	}
	return NativeWinners(scores, 20), nil
}

func vectorKernelOracle(b *testing.B, ctx context.Context, db *sql.DB, backend string, c VectorBenchmarkCase, chunks []vectorKernelChunk, queries [][]float32) ([]NativeScore, float64) {
	b.Helper()
	type key struct {
		id      knowl.PageID
		ordinal int
	}
	original := make(map[key]vectorKernelChunk, c.Chunks)
	for _, chunk := range chunks {
		if chunk.scope == vectorKernelScope {
			original[key{chunk.score.PageID, chunk.score.Ordinal}] = chunk
		}
	}
	var scores []NativeScore
	maxError := 0.0
	for _, query := range queries {
		nativeScores := vectorBenchmarkScalars(b, ctx, db, backend, "vector_benchmark_native", vectorKernelScope, query, c.Dimensions)
		count := 0
		for _, score := range nativeScores {
			chunk, found := original[key{score.PageID, score.Ordinal}]
			if !found {
				b.Fatal("unknown isolated native chunk")
			}
			count++
			maxError = math.Max(maxError, CheckNativeCosine(b, chunk.vector, query, score.Score))
			if c.SelectedPercent == 100 || chunk.selected {
				scores = append(scores, score)
			}
		}
		if count != c.Chunks {
			b.Fatalf("isolated native scalar count=%d want%d", count, c.Chunks)
		}
	}
	return NativeWinners(scores, 20), maxError
}

func vectorKernelExplain(b *testing.B, ctx context.Context, db *sql.DB, backend string, statement string, args []any, c VectorBenchmarkCase, mode string) {
	b.Helper()
	if backend == "sqlite" {
		rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+statement, args...)
		if err != nil {
			b.Fatal(err)
		}
		type node struct {
			ID, Parent, Unused int64
			Detail             string
		}
		var nodes []node
		for rows.Next() {
			var item node
			if err := rows.Scan(&item.ID, &item.Parent, &item.Unused, &item.Detail); err != nil {
				b.Fatal(err)
			}
			nodes = append(nodes, item)
		}
		if err := rows.Err(); err != nil {
			b.Fatal(err)
		}
		_ = rows.Close()
		encoded, err := json.Marshal(struct {
			Mode  string
			Case  VectorBenchmarkCase
			Nodes []node
		}{"isolated-kernel-" + mode + "-plan", c, nodes})
		if err != nil {
			b.Fatal(err)
		}
		b.Log(string(encoded))
		return
	}
	var data []byte
	if err := db.QueryRowContext(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+statement, args...).Scan(&data); err != nil {
		b.Fatal(err)
	}
	// Parse only numerical executor work and node kinds; expression text can
	// contain inlined bound vectors and is intentionally not published.
	type node struct {
		Kind      string  `json:"Node Type"`
		Rows      float64 `json:"Actual Rows"`
		Loops     float64 `json:"Actual Loops"`
		Time      float64 `json:"Actual Total Time"`
		Hits      int64   `json:"Shared Hit Blocks"`
		Reads     int64   `json:"Shared Read Blocks"`
		TempRead  int64   `json:"Temp Read Blocks"`
		TempWrite int64   `json:"Temp Written Blocks"`
		Plans     []json.RawMessage
	}
	var execution []struct {
		Plan      json.RawMessage
		Planning  float64 `json:"Planning Time"`
		Execution float64 `json:"Execution Time"`
	}
	if err := json.Unmarshal(data, &execution); err != nil {
		b.Fatal(err)
	}
	var flat []node
	var visit func(json.RawMessage)
	visit = func(raw json.RawMessage) {
		var item node
		if err := json.Unmarshal(raw, &item); err != nil {
			b.Fatal(err)
		}
		children := item.Plans
		item.Plans = nil
		flat = append(flat, item)
		for _, child := range children {
			visit(child)
		}
	}
	for _, item := range execution {
		visit(item.Plan)
	}
	encoded, err := json.Marshal(struct {
		Mode  string
		Case  VectorBenchmarkCase
		Nodes []node
	}{"isolated-kernel-" + mode + "-plan", c, flat})
	if err != nil {
		b.Fatal(err)
	}
	b.Log(string(encoded))
}
