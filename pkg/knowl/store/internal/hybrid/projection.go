package hybrid

import (
	"context"
	"encoding/hex"
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
	OmittedChunks  int
	OmittedRunes   int
	ReadyAt        time.Time
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
	if app.ValidateRetrievalReport(report) != nil || (state.Mode != knowl.RetrievalHybrid && state.Mode != knowl.RetrievalDegraded) || (state.Mode == knowl.RetrievalDegraded && len(chunks) != 0) {
		return failure(knowl.RetrievalProjectionDrift)
	}
	ordinals := make(map[knowl.PageID]uint16)
	digests := make(map[knowl.PageID]string)
	bytes := 256
	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		bytes += len(chunk.PageID) + len(chunk.PageDigest) + len(chunk.ContentHash) + len(state.Space) + 32 + len(chunk.Vector)*4
		if bytes > MaxProjectionBytes {
			return failure(knowl.RetrievalProjectionCapacity)
		}
		if len(chunk.PageID) == 0 || len(chunk.PageID) > 4096 || len(chunk.PageDigest) == 0 || len(chunk.PageDigest) > 256 || !digest(chunk.ContentHash) || chunk.Ordinal < 0 || chunk.Ordinal >= PageChunks {
			return failure(knowl.RetrievalProjectionDrift)
		}
		if previous, ok := digests[chunk.PageID]; ok && previous != chunk.PageDigest {
			return failure(knowl.RetrievalProjectionDrift)
		}
		digests[chunk.PageID] = chunk.PageDigest
		bit := uint16(1) << chunk.Ordinal
		if ordinals[chunk.PageID]&bit != 0 {
			return failure(knowl.RetrievalProjectionDrift)
		}
		ordinals[chunk.PageID] |= bit
		if err := validateVector(chunk.Vector, state.Dimensions); err != nil {
			return err
		}
	}
	for _, mask := range ordinals {
		if mask&(mask+1) != 0 {
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
