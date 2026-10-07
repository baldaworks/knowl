package sqlite

import (
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestReadyEmbeddingRejectsMissingWholePage(t *testing.T) {
	store, snapshot, state, chunks := embeddingFixture(t)
	deleted := chunks[0]
	if _, err := store.db.ExecContext(t.Context(), `DELETE FROM knowl_embedding_chunks WHERE scope=? AND page_id=?`, snapshot.Scope, deleted.PageID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_state SET chunk_count=? WHERE scope=?`, state.ChunkCount-1, snapshot.Scope); err != nil {
		t.Fatal(err)
	}
	_, _, err := readEmbeddingFixture(t.Context(), store, snapshot.Scope, state)
	assertEmbeddingFailure(t, err, knowl.RetrievalProjectionDrift)
}

func TestPersistLongPageOrdinals(t *testing.T) {
	store, snapshot, state, chunks := embeddingFixture(t)
	for ordinal := 1; ordinal <= 16; ordinal++ {
		long := chunks[0]
		long.Ordinal = ordinal
		chunks = append(chunks, long)
	}
	state.ChunkCount = len(chunks)
	coverage, err := hybrid.DecodeCoverage(state.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	for i := range coverage {
		if coverage[i].PageID == chunks[0].PageID {
			coverage[i].Chunks = 17
		}
	}
	state.Coverage, err = hybrid.EncodeCoverage(coverage)
	if err != nil {
		t.Fatal(err)
	}
	err = store.publishEmbeddings(t.Context(), snapshot.Scope, state, chunks)
	if err != nil {
		t.Fatalf("publish long page: %v", err)
	}
}
