package hybrid

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// ProjectionState identifies one complete, disposable scoped vector projection.
type ProjectionState struct {
	Space          string
	SnapshotDigest string
	Dimensions     int
	Mode           knowl.RetrievalMode
	Reason         knowl.RetrievalFailure
	ChunkCount     int
	Coverage       string
	OmittedChunks  int
	OmittedRunes   int
	ReadyAt        time.Time
}

// PageCoverage records the expected complete vector set for one semantic page.
type PageCoverage struct {
	PageID     knowl.PageID `json:"page_id"`
	PageDigest string       `json:"page_digest"`
	Chunks     int          `json:"chunks"`
}

// EncodeCoverage preserves page order so the manifest has one stable encoding.
func EncodeCoverage(pages []PageCoverage) (string, error) {
	encoded, err := json.Marshal(pages)
	return string(encoded), err
}

// DecodeCoverage validates the bounded manifest before adapters compare it with
// the canonical lexical page set in the same transaction.
func DecodeCoverage(encoded string) ([]PageCoverage, error) {
	if encoded == "" || len(encoded) > MaxCoverageBytes {
		return nil, failure(knowl.RetrievalProjectionDrift)
	}
	var pages []PageCoverage
	if err := json.Unmarshal([]byte(encoded), &pages); err != nil || pages == nil || len(pages) > MaxChunks {
		return nil, failure(knowl.RetrievalProjectionDrift)
	}
	previous := knowl.PageID("")
	for _, page := range pages {
		if page.PageID <= previous || len(page.PageID) > 4096 || len(page.PageDigest) < 1 || len(page.PageDigest) > 256 || page.Chunks < 0 || page.Chunks > MaxChunks {
			return nil, failure(knowl.RetrievalProjectionDrift)
		}
		previous = page.PageID
	}
	return pages, nil
}

// ValidatePageSet ensures the manifest covers the current lexical page set.
func ValidatePageSet(encoded string, current []PageCoverage) error {
	expected, err := DecodeCoverage(encoded)
	if err != nil {
		return err
	}
	if len(expected) != len(current) {
		return failure(knowl.RetrievalProjectionDrift)
	}
	for i, page := range expected {
		if page.PageID != current[i].PageID || page.PageDigest != current[i].PageDigest {
			return failure(knowl.RetrievalProjectionDrift)
		}
	}
	return nil
}

// Chunk carries no source text, credentials or endpoint information.
type Chunk struct {
	PageID      knowl.PageID
	PageDigest  string
	Ordinal     int
	ContentHash string
	Vector      []float32
}

// ValidateProjection checks bounded metadata, complete ordinals and vector data.
// Adapters additionally compare the snapshot and page digests in a transaction.
func ValidateProjection(ctx context.Context, state ProjectionState, chunks []Chunk) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(chunks) > MaxChunks || state.ChunkCount > MaxChunks {
		return failure(knowl.RetrievalProjectionCapacity)
	}
	if !digest(state.Space) || !digest(state.SnapshotDigest) || state.Dimensions < 1 || state.Dimensions > 4096 || state.ChunkCount != len(chunks) {
		return failure(knowl.RetrievalProjectionDrift)
	}
	report := knowl.RetrievalReport{Requested: knowl.RetrievalHybrid, Effective: state.Mode, Reason: state.Reason, ModelSpace: state.Space[:16], IndexOmittedChunks: state.OmittedChunks, IndexOmittedRunes: state.OmittedRunes}
	if app.ValidateRetrievalReport(report) != nil || (state.Mode != knowl.RetrievalHybrid && state.Mode != knowl.RetrievalDegraded) || (state.Mode == knowl.RetrievalDegraded && (len(chunks) != 0 || state.Coverage != "")) || (state.Mode == knowl.RetrievalHybrid && (state.OmittedChunks != 0 || state.OmittedRunes != 0)) {
		return failure(knowl.RetrievalProjectionDrift)
	}
	var expected map[knowl.PageID]PageCoverage
	if state.Mode == knowl.RetrievalHybrid {
		pages, err := DecodeCoverage(state.Coverage)
		if err != nil {
			return err
		}
		expected = make(map[knowl.PageID]PageCoverage, len(pages))
		count := 0
		for _, page := range pages {
			count += page.Chunks
			expected[page.PageID] = page
		}
		if count != state.ChunkCount {
			return failure(knowl.RetrievalProjectionDrift)
		}
	}
	ordinals := make(map[knowl.PageID]map[int]struct{})
	digests := make(map[knowl.PageID]string)
	bytes := 256 + len(state.Coverage)
	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		bytes += len(chunk.PageID) + len(chunk.PageDigest) + len(chunk.ContentHash) + len(state.Space) + 32 + len(chunk.Vector)*4
		if bytes > MaxProjectionBytes {
			return failure(knowl.RetrievalProjectionCapacity)
		}
		if len(chunk.PageID) == 0 || len(chunk.PageID) > 4096 || len(chunk.PageDigest) == 0 || len(chunk.PageDigest) > 256 || !digest(chunk.ContentHash) || chunk.Ordinal < 0 || chunk.Ordinal >= MaxChunks {
			return failure(knowl.RetrievalProjectionDrift)
		}
		if previous, ok := digests[chunk.PageID]; ok && previous != chunk.PageDigest {
			return failure(knowl.RetrievalProjectionDrift)
		}
		digests[chunk.PageID] = chunk.PageDigest
		if state.Mode == knowl.RetrievalHybrid {
			page, exists := expected[chunk.PageID]
			if !exists || page.PageDigest != chunk.PageDigest || chunk.Ordinal >= page.Chunks {
				return failure(knowl.RetrievalProjectionDrift)
			}
		}
		if ordinals[chunk.PageID] == nil {
			ordinals[chunk.PageID] = make(map[int]struct{})
		}
		if _, exists := ordinals[chunk.PageID][chunk.Ordinal]; exists {
			return failure(knowl.RetrievalProjectionDrift)
		}
		ordinals[chunk.PageID][chunk.Ordinal] = struct{}{}
		if err := validateVector(chunk.Vector, state.Dimensions); err != nil {
			return err
		}
	}
	for _, positions := range ordinals {
		for ordinal := 0; ordinal < len(positions); ordinal++ {
			if _, exists := positions[ordinal]; !exists {
				return failure(knowl.RetrievalProjectionDrift)
			}
		}
	}
	for _, page := range expected {
		if len(ordinals[page.PageID]) != page.Chunks {
			return failure(knowl.RetrievalProjectionDrift)
		}
	}
	return nil
}

func digest(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
