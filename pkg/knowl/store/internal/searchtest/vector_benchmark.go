package searchtest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	vectorBenchPostgres      = "postgres"
	vectorBenchSeed          = 73
	vectorBenchSelected      = "bench-selected"
	vectorBenchQueryPrefix   = "bench-query: "
	vectorBenchPassagePrefix = "bench-passage: "
)

// VectorBenchmarkCase describes a bounded, explicitly selected laboratory case.
type VectorBenchmarkCase struct{ Chunks, Dimensions, Windows, SelectedPercent int }

// Name is stable across the actual legacy checkout and current implementation.
func (c VectorBenchmarkCase) Name() string {
	return fmt.Sprintf("chunks%d/dim%d/windows%d/selected%d", c.Chunks, c.Dimensions, c.Windows, c.SelectedPercent)
}

// VectorBenchmarkCases keeps ordinary benchmark invocations small. Larger
// fixtures require this test-only environment variable, never an app option.
func VectorBenchmarkCases(kernel bool) []VectorBenchmarkCase {
	if os.Getenv("KNOWL_VECTOR_BENCH_LAB") != "1" {
		return []VectorBenchmarkCase{{128, 8, 1, 50}, {128, 8, 4, 100}}
	}
	var cases []VectorBenchmarkCase
	for _, n := range []int{8192, 25000, 100000} {
		for _, d := range []int{2, 768, 4096} {
			for _, w := range []int{1, 4} {
				cases = append(cases, VectorBenchmarkCase{n, d, w, 50})
			}
		}
	}
	cases = append(cases, VectorBenchmarkCase{8192, 768, 1, 100})
	if kernel {
		cases = append(cases, VectorBenchmarkCase{1024, 4096, 1, 50})
	}
	return cases
}

// VectorBenchmarkResourceEstimate deliberately overestimates six simultaneous
// float32 representations plus metadata. It bounds lab work independently of
// production capacity; unsupported lab cases are observations, never timings.
func VectorBenchmarkResourceEstimate(c VectorBenchmarkCase) int64 {
	return int64(c.Chunks) * (int64(c.Dimensions)*4*6 + 512)
}

// VectorBenchmarkVector deterministically prepares finite normalized float32
// values outside retrieval timing. It is not a model-quality fixture.
func VectorBenchmarkVector(identity string, dimensions int) []float32 {
	hash := sha256.Sum256([]byte(identity))
	random := rand.New(rand.NewPCG(vectorBenchSeed, binary.LittleEndian.Uint64(hash[:8])))
	vector := make([]float32, dimensions)
	var norm float64
	for i := range vector {
		vector[i] = float32(random.Float64()*2 - 1)
		norm += float64(vector[i]) * float64(vector[i])
	}
	for i := range vector {
		vector[i] = float32(float64(vector[i]) / math.Sqrt(norm))
	}
	return vector
}

// VectorBenchmarkProvider returns copies of vectors prepared before the timer.
// Calls counts local fixture invocations; external inference calls are zero.
type VectorBenchmarkProvider struct {
	Vectors map[string][]float32
	Calls   int
	Chunks  []VectorBenchmarkChunk
	Queries [][]float32
}

// VectorBenchmarkChunk retains original fixture values only for test oracles.
type VectorBenchmarkChunk struct {
	PageID   knowl.PageID
	Ordinal  int
	Vector   []float32
	Selected bool
}

// Embed implements the controlled provider without inference or I/O.
func (p *VectorBenchmarkProvider) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.Calls++
	vectors := make([][]float32, len(inputs))
	for i, input := range inputs {
		vector, found := p.Vectors[input]
		if !found {
			return nil, &app.EmbeddingError{Code: knowl.RetrievalInvalidResponse}
		}
		vectors[i] = slices.Clone(vector)
	}
	return vectors, nil
}

// VectorRuntimeFixture constructs an exactly counted corpus using checked-in
// structured preparation:368 body runes plus fixed prefix/newline per passage.
func VectorRuntimeFixture(ctx context.Context, c VectorBenchmarkCase) (knowl.WorkspaceSnapshot, app.EmbeddingOptions, string, []knowl.SourceID, string, error) {
	space := app.EmbeddingSpace{Model: "vector-benchmark", Revision: "seed73-v1", Dimensions: c.Dimensions, QueryPrefix: vectorBenchQueryPrefix, PassagePrefix: vectorBenchPassagePrefix}
	p := &VectorBenchmarkProvider{Vectors: make(map[string][]float32)}
	options := app.EmbeddingOptions{Provider: p, Space: space, FailurePolicy: app.EmbeddingStrict}
	snapshot := knowl.WorkspaceSnapshot{Scope: "vector-runtime-benchmark", SchemaDigest: "vector-benchmark-schema", CapturedAt: time.Unix(1700000000, 0).UTC()}
	pages := min(128, c.Chunks)
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "seed=%d dimensions=%d chunks=%d windows=%d source-percent=%d generator=normalized-pcg-input-sha256-v1\n", vectorBenchSeed, c.Dimensions, c.Chunks, c.Windows, c.SelectedPercent)
	preparedCount := 0
	// Capacity rejection is reached before provider inference in Engine.Build.
	// Avoid materializing vectors for an obviously unsupported production case.
	prepareVectors := c.Chunks <= 8192 && int64(c.Chunks)*(int64(c.Dimensions)*4+512) < 64<<20
	for page := range pages {
		count := c.Chunks / pages
		if page < c.Chunks%pages {
			count++
		}
		var body strings.Builder
		for ordinal := range count {
			fmt.Fprintf(&body, "p%06d c%06d ", page, ordinal)
			body.WriteString(strings.Repeat("fixture ", 44))
		}
		id := knowl.PageID(fmt.Sprintf("p%06d", page))
		source := knowl.SourceID("bench-unselected")
		if page%2 == 0 {
			source = vectorBenchSelected
		}
		document := knowl.SourceDocument{SourceID: source, DocumentID: knowl.DocumentID(id), Revision: "1"}
		item := knowl.PageSnapshot{ID: id, Path: "wiki/" + string(id) + ".md", Digest: string(id), Body: body.String(), SourceDocuments: []knowl.SourceDocument{document}, UpdatedAt: snapshot.CapturedAt}
		prepared, err := hybrid.PreparePage(ctx, item, space)
		if err != nil {
			return snapshot, options, "", nil, "", err
		}
		if len(prepared.Inputs) != count {
			return snapshot, options, "", nil, "", fmt.Errorf("fixture preparation count: got %d want %d", len(prepared.Inputs), count)
		}
		preparedCount += len(prepared.Inputs)
		_, _ = fmt.Fprintf(hash, "page=%s source=%s\n", id, source)
		for ordinal, input := range prepared.Inputs {
			hash.Write([]byte(input))
			hash.Write([]byte{0})
			if prepareVectors {
				p.Vectors[input] = VectorBenchmarkVector(input, c.Dimensions)
				p.Chunks = append(p.Chunks, VectorBenchmarkChunk{PageID: id, Ordinal: ordinal, Vector: p.Vectors[input], Selected: page%2 == 0})
				for _, value := range p.Vectors[input] {
					var bits [4]byte
					binary.LittleEndian.PutUint32(bits[:], math.Float32bits(value))
					hash.Write(bits[:])
				}
			}
		}
		snapshot.Pages = append(snapshot.Pages, item)
	}
	if preparedCount != c.Chunks {
		return snapshot, options, "", nil, "", fmt.Errorf("fixture total: got %d want %d", preparedCount, c.Chunks)
	}
	query := "benchmarkquery"
	if c.Windows == 4 {
		var text strings.Builder
		for i := range 4 {
			fmt.Fprintf(&text, "q%06d ", i)
			text.WriteString(strings.Repeat("queryxx ", 39))
		}
		text.WriteString(strings.Repeat("queryxx ", 8))
		query = text.String()
	}
	prepared, err := hybrid.PrepareQuery(ctx, query, space)
	if err != nil {
		return snapshot, options, "", nil, "", err
	}
	if len(prepared.Inputs) != c.Windows {
		return snapshot, options, "", nil, "", fmt.Errorf("fixture query windows: got %d want %d", len(prepared.Inputs), c.Windows)
	}
	for _, input := range prepared.Inputs {
		p.Vectors[input] = VectorBenchmarkVector(input, c.Dimensions)
		p.Queries = append(p.Queries, p.Vectors[input])
		hash.Write([]byte(input))
		for _, value := range p.Vectors[input] {
			var bits [4]byte
			binary.LittleEndian.PutUint32(bits[:], math.Float32bits(value))
			hash.Write(bits[:])
		}
	}
	var sources []knowl.SourceID
	if c.SelectedPercent == 50 {
		sources = []knowl.SourceID{vectorBenchSelected}
	}
	return snapshot, options, query, sources, hex.EncodeToString(hash.Sum(nil)), nil
}

// VectorTransferProbe observes actual driver values. Byte counts describe
// decoded logical result cells, not wire protocol framing or server storage.
type VectorTransferProbe struct {
	Rows, VectorRows, VectorBytes, MetadataRows, MetadataBytes, CandidateRows, EvidenceRows, ResultBytes int64
	ScalarSQLNS, RankingSQLNS, VectorSQLNS                                                               int64
	Record                                                                                               bool
	Queries                                                                                              []VectorRecordedQuery
}

// VectorRecordedQuery retains bound arguments only in test memory for EXPLAIN.
// Vector payloads and SQL are not serialized into benchmark observations.
type VectorRecordedQuery struct {
	SQL     string
	Args    []any
	Columns []string
}

// VectorProbeConnector wraps a real connector without replacing its execution.
type VectorProbeConnector struct {
	driver.Connector
	Probe *VectorTransferProbe
}

// Connect preserves the original driver's context-aware operations.
func (c VectorProbeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return vectorProbeConn{Conn: conn, probe: c.Probe}, nil
}

// VectorDriverConnector adapts the SQLite DSN with the original transaction mode.
type VectorDriverConnector struct {
	DBDriver driver.Driver
	DSN      string
}

// Connect opens the actual configured driver connection.
func (c VectorDriverConnector) Connect(context.Context) (driver.Conn, error) {
	return c.DBDriver.Open(c.DSN)
}

// Driver returns the wrapped real driver.
func (c VectorDriverConnector) Driver() driver.Driver { return c.DBDriver }

type vectorProbeConn struct {
	driver.Conn
	probe *VectorTransferProbe
}

func (c vectorProbeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c vectorProbeConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}
func (c vectorProbeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if executor, ok := c.Conn.(driver.ExecerContext); ok {
		return executor.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}
func (c vectorProbeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	started := time.Now()
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	columns := rows.Columns()
	if c.probe.Record {
		record := VectorRecordedQuery{SQL: query, Columns: slices.Clone(columns)}
		for _, arg := range args {
			record.Args = append(record.Args, arg.Value)
		}
		c.probe.Queries = append(c.probe.Queries, record)
	}
	return &vectorProbeRows{Rows: rows, probe: c.probe, started: started}, nil
}

type vectorProbeRows struct {
	driver.Rows
	probe   *VectorTransferProbe
	started time.Time
	closed  bool
}

func (r *vectorProbeRows) Close() error {
	if !r.closed {
		elapsed := time.Since(r.started).Nanoseconds()
		columns := r.Columns()
		switch {
		case slices.Contains(columns, "score"):
			r.probe.RankingSQLNS += elapsed
		case slices.Contains(columns, "vector"):
			r.probe.VectorSQLNS += elapsed
		case len(columns) <= 2:
			r.probe.ScalarSQLNS += elapsed
		}
		r.closed = true
	}
	return r.Rows.Close()
}
func (r *vectorProbeRows) Next(values []driver.Value) error {
	if err := r.Rows.Next(values); err != nil {
		return err
	}
	columns := r.Columns()
	r.probe.Rows++
	if len(columns) == 7 && columns[0] == "digest" && columns[6] == "okf_metadata" {
		r.probe.EvidenceRows++
	}
	for i, value := range values {
		bytes := int64(0)
		switch v := value.(type) {
		case string:
			bytes = int64(len(v))
		case []byte:
			bytes = int64(len(v))
		case int64, float64:
			bytes = 8
		case bool:
			bytes = 1
		case time.Time:
			bytes = int64(len(v.UTC().Format(time.RFC3339Nano)))
		}
		r.probe.ResultBytes += bytes
		switch columns[i] {
		case "vector":
			r.probe.VectorRows++
			r.probe.VectorBytes += bytes
		case "coverage":
			r.probe.MetadataRows++
			r.probe.MetadataBytes += bytes
		case "score":
			if value != nil {
				r.probe.CandidateRows++
			}
		}
	}
	return nil
}

// VectorBenchmarkObservation is emitted as JSON through native Go test events.
type VectorBenchmarkObservation struct {
	Mode, Implementation, Backend, Outcome, Reason, Checksum, GoVersion, Provider string
	Case                                                                          VectorBenchmarkCase
	Samples                                                                       int
	WallNS                                                                        []int64
	MedianNS, P95NS, MaxNS, NSPerOp, BPerOp, AllocsPerOp                          float64
	Transfer                                                                      VectorTransferProbe
	NativeVectorHeaderBytes                                                       int
	LogicalScoredPairs                                                            int
	Retrieval                                                                     *knowl.RetrievalReport
	ResultIDs                                                                     []knowl.PageID
	NativeOracleChecked                                                           bool
	MathMaxAbsError                                                               float64
}

// CheckVectorRuntimeOracle enumerates native scalars outside the timer and
// applies fixture source membership/independent page aggregation in Go. The
// legacy checkout retains its actual old pipeline and does not call this SQL.
func CheckVectorRuntimeOracle(b *testing.B, ctx context.Context, db *sql.DB, backend string, scope knowl.ScopeRef, c VectorBenchmarkCase, p *VectorBenchmarkProvider, refs []knowl.PageReference) float64 {
	b.Helper()
	type key struct {
		id      knowl.PageID
		ordinal int
	}
	chunks := make(map[key]VectorBenchmarkChunk, len(p.Chunks))
	for _, chunk := range p.Chunks {
		chunks[key{chunk.PageID, chunk.Ordinal}] = chunk
	}
	var scores []NativeScore
	maxError := 0.0
	for _, query := range p.Queries {
		nativeScores := vectorBenchmarkScalars(b, ctx, db, backend, "knowl_embedding_chunks", scope, query, c.Dimensions)
		count := 0
		for _, score := range nativeScores {
			chunk, found := chunks[key{score.PageID, score.Ordinal}]
			if !found {
				b.Fatal("native oracle encountered unknown fixture chunk")
			}
			count++
			maxError = math.Max(maxError, CheckNativeCosine(b, chunk.Vector, query, score.Score))
			if c.SelectedPercent == 100 || chunk.Selected {
				scores = append(scores, score)
			}
		}
		if count != c.Chunks {
			b.Fatalf("oracle native scalar count=%d want=%d", count, c.Chunks)
		}
	}
	winners := NativeWinners(scores, 5)
	if len(refs) != len(winners) {
		b.Fatalf("native runtime winners=%d exhaustive=%d", len(refs), len(winners))
	}
	for i, winner := range winners {
		if refs[i].ID != winner.PageID {
			b.Fatalf("native runtime position%d page=%s exhaustive=%s", i, refs[i].ID, winner.PageID)
		}
	}
	return maxError
}

// vectorBenchmarkScalars only enumerates scalars: it deliberately performs no
// filtering, grouping or ordering, leaving each independent Go oracle intact.
func vectorBenchmarkScalars(b *testing.B, ctx context.Context, db *sql.DB, backend, table string, scope knowl.ScopeRef, query []float32, dimensions int) []NativeScore {
	b.Helper()
	statement := "SELECT page_id,ordinal,1-vec_distance_cosine(vector,?) FROM " + table + " WHERE scope=?"
	var encoded any
	if backend == vectorBenchPostgres {
		statement = "SELECT page_id,ordinal,1-(vector OPERATOR(public.<=>) $1::public.vector) FROM " + table + " WHERE scope=$2"
		value, err := json.Marshal(query)
		if err != nil {
			b.Fatal(err)
		}
		encoded = string(value)
	} else {
		value, err := hybrid.EncodeVector(query, dimensions)
		if err != nil {
			b.Fatal(err)
		}
		encoded = value
	}
	rows, err := db.QueryContext(ctx, statement, encoded, scope)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var scores []NativeScore
	for rows.Next() {
		var score NativeScore
		if err := rows.Scan(&score.PageID, &score.Ordinal, &score.Score); err != nil {
			b.Fatal(err)
		}
		scores = append(scores, score)
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
	return scores
}

// EmitVectorBenchmarkObservation records actual measurements, excluding setup.
func EmitVectorBenchmarkObservation(b *testing.B, observation VectorBenchmarkObservation) {
	b.Helper()
	observation.Implementation = os.Getenv("KNOWL_VECTOR_BENCH_REVISION")
	observation.GoVersion = runtime.Version()
	observation.Provider = "fixed precomputed normalized float32; external inference calls=0"
	observation.Transfer.Record = false
	observation.Transfer.Queries = nil
	if len(observation.WallNS) > 0 {
		ordered := slices.Clone(observation.WallNS)
		slices.Sort(ordered)
		observation.MedianNS = float64(ordered[len(ordered)/2])
		observation.P95NS = float64(ordered[(95*len(ordered)+99)/100-1])
		observation.MaxNS = float64(ordered[len(ordered)-1])
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		b.Fatal(err)
	}
	b.Log(string(encoded))
}
