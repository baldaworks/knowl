package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	setNativeFormat            = `UPDATE knowl_embedding_state SET native_format=? WHERE scope=?`
	setNativeCoverage          = `UPDATE knowl_embedding_state SET coverage=? WHERE scope=?`
	setQuestionVector          = `UPDATE knowl_embedding_chunks SET vector=? WHERE scope=? AND page_id='question'`
	nativeEvidenceDigestColumn = "digest"
	nativeEvidenceTagsColumn   = "tags"
)

func readNativeFixture(ctx context.Context, store *Store, scope knowl.ScopeRef, state hybrid.ProjectionState) (validatedDenseProjection, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return validatedDenseProjection{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return validateDenseProjectionTx(ctx, tx, scope, state.Space, state.Dimensions)
}

func TestNativeProjectionWholeScopeIntegrity(t *testing.T) {
	for _, tc := range []struct {
		name, statement string
		value           any
		want            knowl.RetrievalFailure
	}{
		{"legacy readiness", setNativeFormat, 0, knowl.RetrievalProjectionNotReady},
		{"format type", setNativeFormat, []byte("1"), knowl.RetrievalProjectionNotReady},
		{"bad coverage", setNativeCoverage, "{", knowl.RetrievalProjectionDrift},
		{"null coverage", setNativeCoverage, "null", knowl.RetrievalProjectionDrift},
		{"oversized coverage", setNativeCoverage, strings.Repeat(" ", hybrid.MaxCoverageBytes+1), knowl.RetrievalProjectionDrift},
		{"bad timestamp", `UPDATE knowl_embedding_state SET ready_at=? WHERE scope=?`, "invalid", knowl.RetrievalProjectionDrift},
		{"bad space hex", `UPDATE knowl_embedding_state SET space=? WHERE scope=?`, strings.Repeat("G", 64), knowl.RetrievalProjectionDrift},
		{"snapshot", `UPDATE knowl_embedding_state SET snapshot_digest=? WHERE scope=?`, strings.Repeat("b", 64), knowl.RetrievalProjectionDrift},
		{"state count", `UPDATE knowl_embedding_state SET chunk_count=? WHERE scope=?`, 0, knowl.RetrievalProjectionDrift},
		{"state capacity", `UPDATE knowl_embedding_state SET chunk_count=? WHERE scope=?`, hybrid.MaxChunks + 1, knowl.RetrievalProjectionCapacity},
		{"omissions", `UPDATE knowl_embedding_state SET omitted_chunks=? WHERE scope=?`, 1, knowl.RetrievalProjectionDrift},
		{"wrong reason", `UPDATE knowl_embedding_state SET reason=? WHERE scope=?`, knowl.RetrievalUnavailable, knowl.RetrievalProjectionDrift},
		{"foreign model", `UPDATE knowl_embedding_chunks SET space=? WHERE scope=? AND page_id='question'`, strings.Repeat("c", 64), knowl.RetrievalProjectionDrift},
		{"wrong dimension", `UPDATE knowl_embedding_chunks SET dimensions=? WHERE scope=? AND page_id='question'`, 3, knowl.RetrievalProjectionDrift},
		{"real ordinal", `UPDATE knowl_embedding_chunks SET ordinal=? WHERE scope=? AND page_id='question'`, 0.5, knowl.RetrievalProjectionDrift},
		{"gap", `UPDATE knowl_embedding_chunks SET ordinal=? WHERE scope=? AND page_id='question'`, 1, knowl.RetrievalProjectionDrift},
		{"uppercase hash", `UPDATE knowl_embedding_chunks SET content_hash=? WHERE scope=? AND page_id='question'`, strings.Repeat("B", 64), knowl.RetrievalProjectionDrift},
		{"nul hash", `UPDATE knowl_embedding_chunks SET content_hash=? WHERE scope=? AND page_id='question'`, strings.Repeat("b", 63) + "\x00", knowl.RetrievalProjectionDrift},
		{"page digest", `UPDATE knowl_embedding_chunks SET page_digest=? WHERE scope=? AND page_id='question'`, "stale", knowl.RetrievalProjectionDrift},
		{"current digest", `UPDATE knowl_pages SET digest=? WHERE scope=? AND page_id='question'`, "changed", knowl.RetrievalProjectionDrift},
		{"short blob", setQuestionVector, []byte{0}, knowl.RetrievalProjectionDrift},
		{"text vector", setQuestionVector, "[1,0]   ", knowl.RetrievalProjectionDrift},
		{"zero", setQuestionVector, make([]byte, 8), knowl.RetrievalProjectionDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, snapshot, state, _ := embeddingFixture(t)
			if _, err := store.db.ExecContext(t.Context(), `PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(t.Context(), tc.statement, tc.value, snapshot.Scope); err != nil {
				t.Fatal(err)
			}
			got, err := readNativeFixture(t.Context(), store, snapshot.Scope, state)
			assertEmbeddingFailure(t, err, tc.want)
			if got.ValidatedChunks != 0 {
				t.Fatalf("partial validation reported %d chunks", got.ValidatedChunks)
			}
		})
	}
}

func TestNativeProjectionVectorReduction(t *testing.T) {
	// Adjacent float32 values straddle the squared-norm tolerance; rounding the
	// norm to float32 or checking a rounded square root would move this boundary.
	inside := float32(math.Sqrt(1.001))
	if float64(inside)*float64(inside) > 1.001 {
		inside = math.Nextafter32(inside, 0)
	}
	for _, tc := range []struct {
		name string
		v    float32
		good bool
	}{
		{"unit", 1, true}, {"negative unit", -1, true}, {"inside", inside, true},
		{"outside", math.Nextafter32(inside, 2), false},
		{"nan", float32(math.NaN()), false}, {"infinity", float32(math.Inf(1)), false},
		{"negative infinity", float32(math.Inf(-1)), false}, {"finite overflow norm", math.MaxFloat32, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, snapshot, state, _ := embeddingFixture(t)
			encoded := make([]byte, 8)
			binary.LittleEndian.PutUint32(encoded, math.Float32bits(tc.v))
			if _, err := store.db.ExecContext(t.Context(), setQuestionVector, encoded, snapshot.Scope); err != nil {
				t.Fatal(err)
			}
			got, err := readNativeFixture(t.Context(), store, snapshot.Scope, state)
			if !tc.good {
				assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
			} else if err != nil || got.ValidatedChunks != 4 || got.State != state {
				t.Fatalf("valid projection=%+v err=%v", got, err)
			}
		})
	}
}

func TestNativeProjectionCoverageIncludesZeroChunkPages(t *testing.T) {
	store, snapshot, state, chunks := embeddingFixture(t)
	coverage, err := hybrid.DecodeCoverage(state.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	coverage[0].Chunks = 0
	state.ChunkCount--
	state.Coverage, err = hybrid.EncodeCoverage(coverage)
	if err != nil {
		t.Fatal(err)
	}
	chunks = slices.DeleteFunc(chunks, func(c hybrid.Chunk) bool { return c.PageID == coverage[0].PageID })
	if err := store.publishEmbeddings(t.Context(), snapshot.Scope, state, chunks); err != nil {
		t.Fatal(err)
	}
	got, err := readNativeFixture(t.Context(), store, snapshot.Scope, state)
	if err != nil || got.ValidatedChunks != 3 {
		t.Fatalf("zero-chunk page=%+v err=%v", got, err)
	}
	coverage = coverage[1:]
	encoded, err := hybrid.EncodeCoverage(coverage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), setNativeCoverage, encoded, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	_, err = readNativeFixture(t.Context(), store, snapshot.Scope, state)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
}

func TestNativeProjectionCancellation(t *testing.T) {
	store, snapshot, state, _ := embeddingFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := readNativeFixture(ctx, store, snapshot.Scope, state)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled validation=%v", err)
	}
}

func TestNativeProjectionSupportedDimensions(t *testing.T) {
	for _, dimensions := range []int{1, 2000, 2001, 4096} {
		t.Run(strconv.Itoa(dimensions), func(t *testing.T) {
			store, snapshot, state, chunks := embeddingFixture(t)
			state.Dimensions = dimensions
			for i := range chunks {
				chunks[i].Vector = make([]float32, dimensions)
				chunks[i].Vector[0] = 1
			}
			if err := store.publishEmbeddings(t.Context(), snapshot.Scope, state, chunks); err != nil {
				t.Fatal(err)
			}
			got, err := readNativeFixture(t.Context(), store, snapshot.Scope, state)
			if err != nil || got.ValidatedChunks != 4 {
				t.Fatalf("dimensions %d projection=%+v err=%v", dimensions, got, err)
			}
		})
	}
}

func TestNativeProjectionUsesValidatedCanonicalCoverage(t *testing.T) {
	store, snapshot, state, _ := embeddingFixture(t)
	// encoding/json uses the last duplicate field, SQLite json_extract uses the
	// first. Only expanding the canonical validated encoding preserves Go's
	// existing manifest contract when persisted JSON contains duplicate fields.
	state.Coverage = `[{"page_id":"foreign","page_id":"accent","page_digest":"digest-accent","chunks":0,"chunks":1},` +
		`{"page_id":"ascii","page_digest":"digest-ascii","chunks":1},` +
		`{"page_id":"cyrillic","page_digest":"digest-cyrillic","chunks":1},` +
		`{"page_id":"question","page_digest":"digest-question","chunks":1}]`
	if _, err := store.db.ExecContext(t.Context(), setNativeCoverage, state.Coverage, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	got, err := readNativeFixture(t.Context(), store, snapshot.Scope, state)
	if err != nil || got.ValidatedChunks != 4 {
		t.Fatalf("canonical coverage=%+v err=%v", got, err)
	}
}

func TestNativeProjectionDetectsDuplicateOrdinalsWithoutConstraints(t *testing.T) {
	store, snapshot, state, chunks := embeddingFixture(t)
	// Rebuild just this disposable test table without its PK/FK guards so the
	// read-side relational validation is exercised independently of insertion.
	for _, statement := range []string{
		`ALTER TABLE knowl_embedding_chunks RENAME TO original_embedding_chunks`,
		`CREATE TABLE knowl_embedding_chunks AS SELECT scope,space,page_id,page_digest,ordinal,content_hash,dimensions,vector FROM original_embedding_chunks`,
		`DROP TABLE original_embedding_chunks`,
	} {
		if _, err := store.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	coverage, err := hybrid.DecodeCoverage(state.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	for i := range coverage {
		if coverage[i].PageID == chunks[0].PageID {
			coverage[i].Chunks++
		}
	}
	state.Coverage, err = hybrid.EncodeCoverage(coverage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_state SET chunk_count=5,coverage=? WHERE scope=?`, state.Coverage, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_embedding_chunks SELECT scope,space,page_id,page_digest,ordinal,content_hash,dimensions,vector FROM knowl_embedding_chunks WHERE scope=? AND page_id=?`, snapshot.Scope, chunks[0].PageID); err != nil {
		t.Fatal(err)
	}
	_, err = readNativeFixture(t.Context(), store, snapshot.Scope, state)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
}

func TestNativeProjectionExactChunkCapacity(t *testing.T) {
	store, snapshot, state, chunks := embeddingFixture(t)
	coverage, err := hybrid.DecodeCoverage(state.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	for i := range coverage {
		coverage[i].Chunks = 0
		if coverage[i].PageID == chunks[0].PageID {
			coverage[i].Chunks = hybrid.MaxChunks
		}
	}
	state.Coverage, err = hybrid.EncodeCoverage(coverage)
	if err != nil {
		t.Fatal(err)
	}
	state.ChunkCount = hybrid.MaxChunks
	if _, err := store.db.ExecContext(t.Context(), `DELETE FROM knowl_embedding_chunks WHERE scope=?`, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_state SET chunk_count=8192,coverage=? WHERE scope=?`, state.Coverage, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `WITH RECURSIVE n(i) AS (VALUES(0) UNION ALL SELECT i+1 FROM n WHERE i<8191)
		INSERT INTO knowl_embedding_chunks(scope,space,page_id,page_digest,ordinal,content_hash,dimensions,vector)
		SELECT ?,?,?,?,i,?,2,X'0000803F00000000' FROM n`, snapshot.Scope, state.Space, chunks[0].PageID, chunks[0].PageDigest, chunks[0].ContentHash); err != nil {
		t.Fatal(err)
	}
	got, err := readNativeFixture(t.Context(), store, snapshot.Scope, state)
	if err != nil || got.ValidatedChunks != 8192 {
		t.Fatalf("exact chunk cap=%+v err=%v", got, err)
	}
}

func TestNativeProjectionCountsForeignRowsBeforeValidation(t *testing.T) {
	store, snapshot, state, chunks := embeddingFixture(t)
	if _, err := store.db.ExecContext(t.Context(), `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	// This inserts foreign-space, malformed vectors. The capacity predicate must
	// observe them before any join/filter/native decode can hide or reject them.
	if _, err := store.db.ExecContext(t.Context(), `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<8192)
		INSERT INTO knowl_embedding_chunks(scope,space,page_id,page_digest,ordinal,content_hash,dimensions,vector)
		SELECT ?,printf('foreign-%d',i),?,?,0,?,2,X'00' FROM n`, snapshot.Scope, chunks[0].PageID, chunks[0].PageDigest, chunks[0].ContentHash); err != nil {
		t.Fatal(err)
	}
	_, err := readNativeFixture(t.Context(), store, snapshot.Scope, state)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionCapacity)
}

func TestNativeProjectionExactLogicalByteCapacity(t *testing.T) {
	store, snapshot, state, chunks := embeddingFixture(t)
	// 4000 full-dimensional vectors leave less than 1 MiB for the coverage
	// field. Valid JSON whitespace lets the fixture exercise the exact byte cap.
	state.Dimensions = 4096
	state.ChunkCount = 4000
	coverage, err := hybrid.DecodeCoverage(state.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	for i := range coverage {
		coverage[i].Chunks = 0
		if coverage[i].PageID == chunks[0].PageID {
			coverage[i].Chunks = 4000
		}
	}
	state.Coverage, err = hybrid.EncodeCoverage(coverage)
	if err != nil {
		t.Fatal(err)
	}
	rowBytes := 4*4096 + len(chunks[0].PageID) + len(chunks[0].PageDigest) + 64 + 64 + 32
	coverageBytes := hybrid.MaxProjectionBytes - 256 - 4000*rowBytes
	if coverageBytes < len(state.Coverage) || coverageBytes >= hybrid.MaxCoverageBytes {
		t.Fatalf("fixture coverage budget=%d", coverageBytes)
	}
	state.Coverage += strings.Repeat(" ", coverageBytes-len(state.Coverage))
	vector := make([]byte, 4*4096)
	binary.LittleEndian.PutUint32(vector, math.Float32bits(1))
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `DELETE FROM knowl_embedding_chunks WHERE scope=?`, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE knowl_embedding_state SET dimensions=4096,chunk_count=4000,coverage=? WHERE scope=?`, state.Coverage, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `WITH RECURSIVE n(i) AS (VALUES(0) UNION ALL SELECT i+1 FROM n WHERE i<3999)
		INSERT INTO knowl_embedding_chunks(scope,space,page_id,page_digest,ordinal,content_hash,dimensions,vector)
		SELECT ?,?,?,?,i,?,4096,? FROM n`, snapshot.Scope, state.Space, chunks[0].PageID, chunks[0].PageDigest, chunks[0].ContentHash, vector); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := readNativeFixture(t.Context(), store, snapshot.Scope, state)
	if err != nil || got.ValidatedChunks != 4000 {
		t.Fatalf("exact 64 MiB projection=%+v err=%v", got, err)
	}
	deadline, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = readNativeFixture(deadline, store, snapshot.Scope, state)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("large native scan deadline=%v", err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_state SET coverage=coverage||' ' WHERE scope=?`, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	_, err = readNativeFixture(t.Context(), store, snapshot.Scope, state)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionCapacity)
}

// These wrappers observe values returned by the real SQLite driver. They do
// not inspect SQL text or replace query execution with a fake result set.
type validationTransfers struct {
	rows          int
	blobBytes     int
	vectorBytes   int
	denseRows     int
	coverageBytes int
	metadataRows  int
	evidenceRows  int
}

type validationConnector struct {
	driver   driver.Driver
	path     string
	transfer *validationTransfers
}

func (c validationConnector) Driver() driver.Driver { return c.driver }
func (c validationConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.path)
	if err != nil {
		return nil, err
	}
	return validationConnection{Conn: conn, transfer: c.transfer}, nil
}

type validationConnection struct {
	driver.Conn
	transfer *validationTransfers
}

func (c validationConnection) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c validationConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return validationRows{Rows: rows, transfer: c.transfer}, nil
}

type validationRows struct {
	driver.Rows
	transfer *validationTransfers
}

func (r validationRows) Next(values []driver.Value) error {
	err := r.Rows.Next(values)
	if err == nil {
		r.transfer.rows++
		columns := r.Columns()
		if slices.Equal(columns, []string{nativeEvidenceDigestColumn, "title", nativeEvidenceTagsColumn, "description", "body", "format", "okf_metadata"}) {
			r.transfer.evidenceRows++
		}
		for i, value := range values {
			if columns[i] == "coverage" {
				r.transfer.metadataRows++
				if text, ok := value.(string); ok {
					r.transfer.coverageBytes += len(text)
				}
			}
			if bytes, ok := value.([]byte); ok {
				r.transfer.blobBytes += len(bytes)
				if r.Columns()[i] == "vector" {
					r.transfer.vectorBytes += len(bytes)
				}
			}
			if r.Columns()[i] == "score" && value != nil {
				r.transfer.denseRows++
			}
		}
	}
	return err
}

func TestNativeProjectionHealthChecksTransferNoVectors(t *testing.T) {
	store, snapshot, state, chunks := embeddingFixture(t)
	for ordinal := 1; ordinal < 64; ordinal++ {
		chunk := chunks[0]
		chunk.Ordinal = ordinal
		chunks = append(chunks, chunk)
	}
	coverage, err := hybrid.DecodeCoverage(state.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	for i := range coverage {
		if coverage[i].PageID == chunks[0].PageID {
			coverage[i].Chunks = 64
		}
	}
	state.ChunkCount = len(chunks)
	state.Coverage, err = hybrid.EncodeCoverage(coverage)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.publishEmbeddings(t.Context(), snapshot.Scope, state, chunks); err != nil {
		t.Fatal(err)
	}
	transfer := &validationTransfers{}
	observed := sql.OpenDB(validationConnector{driver: store.db.Driver(), path: store.path, transfer: transfer})
	t.Cleanup(func() { _ = observed.Close() })
	original := store.db
	store.db = observed
	defer func() { store.db = original }()
	store.embedding = &hybrid.Engine{Fingerprint: state.Space}
	store.embedding.Space.Dimensions = state.Dimensions
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	degraded, err := store.ProjectionDegraded(t.Context(), snapshot.Scope)
	if err != nil || degraded {
		t.Fatalf("health degraded=%v err=%v", degraded, err)
	}
	if transfer.blobBytes != 0 || transfer.rows >= len(chunks) {
		t.Fatalf("health transferred %d BLOB bytes and %d rows for %d chunks", transfer.blobBytes, transfer.rows, len(chunks))
	}
	// Maintenance deliberately materializes vectors, proving the instrumentation
	// would detect accidentally routing either health check through that path.
	if _, _, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state); err != nil {
		t.Fatal(err)
	}
	if transfer.blobBytes != 8*len(chunks) {
		t.Fatalf("maintenance vector bytes=%d; want %d", transfer.blobBytes, 8*len(chunks))
	}
}

// SQLite affinity permits BLOB metadata with valid bytes. Readiness must reject
// these persisted types rather than accepting database/sql's string conversion.
func TestNativeProjectionRejectsDynamicMetadataTypes(t *testing.T) {
	for _, statement := range []string{
		`UPDATE knowl_embedding_state SET space=CAST(space AS BLOB) WHERE scope=?`,
		`UPDATE knowl_embedding_state SET coverage=CAST(coverage AS BLOB) WHERE scope=?`,
		`UPDATE knowl_embedding_chunks SET content_hash=CAST(content_hash AS BLOB) WHERE scope=?`,
		`UPDATE knowl_embedding_chunks SET page_id=CAST(page_id AS BLOB) WHERE scope=?`,
	} {
		t.Run(statement, func(t *testing.T) {
			store, snapshot, state, _ := embeddingFixture(t)
			store.embedding = &hybrid.Engine{Fingerprint: state.Space}
			store.embedding.Space.Dimensions = state.Dimensions
			if _, err := store.db.ExecContext(t.Context(), `PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(t.Context(), statement, snapshot.Scope); err != nil {
				t.Fatal(err)
			}
			assertEmbeddingFailure(t, store.CheckProjection(t.Context(), snapshot), knowl.RetrievalProjectionDrift)
			_, err := store.ProjectionDegraded(t.Context(), snapshot.Scope)
			assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
		})
	}
}
