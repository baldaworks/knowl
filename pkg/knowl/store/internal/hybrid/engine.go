package hybrid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const RebuildTimeout = 15 * time.Minute
const FusionRankConstant = 60

// Engine is immutable for one store lifetime. It owns no SQL transactions.
type Engine struct {
	Provider      app.EmbeddingProvider
	Space         app.EmbeddingSpace
	Fingerprint   string
	FailurePolicy app.EmbeddingFailurePolicy
}

func New(options []app.EmbeddingOptions) (*Engine, error) {
	if len(options) == 0 {
		return nil, nil
	}
	if len(options) != 1 {
		return nil, failure(knowl.RetrievalInvalidConfiguration)
	}
	option := options[0]
	if option.Provider == nil {
		if option.Space != (app.EmbeddingSpace{}) || option.FailurePolicy != "" {
			return nil, failure(knowl.RetrievalInvalidConfiguration)
		}
		return nil, nil
	}
	space, err := app.NormalizeEmbeddingSpace(option.Space)
	if err != nil {
		return nil, err
	}
	policy := option.FailurePolicy
	if policy == "" {
		policy = app.EmbeddingFallbackLexical
	}
	if policy != app.EmbeddingFallbackLexical && policy != app.EmbeddingStrict {
		return nil, failure(knowl.RetrievalInvalidConfiguration)
	}
	hash, err := SpaceFingerprint(space)
	if err != nil {
		return nil, err
	}
	return &Engine{Provider: option.Provider, Space: space, Fingerprint: hash, FailurePolicy: policy}, nil
}

// Build stages complete bounded vectors outside all SQL locks. Its caller first
// commits the lexical snapshot and later publishes this result by snapshot CAS.
func (engine *Engine) Build(ctx context.Context, snapshot knowl.WorkspaceSnapshot, digest string) (ProjectionState, []Chunk, error) {
	state := ProjectionState{Space: engine.Fingerprint, SnapshotDigest: digest, Dimensions: engine.Space.Dimensions, Mode: knowl.RetrievalHybrid, ReadyAt: time.Now().UTC()}
	buildCtx, cancel := context.WithTimeout(ctx, RebuildTimeout)
	defer cancel()
	pages := append([]knowl.PageSnapshot(nil), snapshot.Pages...)
	sort.Slice(pages, func(i, j int) bool { return pages[i].ID < pages[j].ID })
	chunks := make([]Chunk, 0)
	bytes := 256
	for _, page := range pages {
		prepared, err := PreparePage(buildCtx, page, engine.Space)
		if err != nil {
			return state, nil, err
		}
		state.OmittedChunks += prepared.OmittedChunks
		state.OmittedRunes += prepared.OmittedRunes
		if state.OmittedChunks > 2147483647 || state.OmittedRunes > 2147483647 || len(chunks)+len(prepared.Inputs) > MaxChunks {
			return state, nil, failure(knowl.RetrievalProjectionCapacity)
		}
		bytes += len(prepared.Inputs) * (len(page.ID) + len(page.Digest) + 64 + len(engine.Fingerprint) + 32 + engine.Space.Dimensions*4)
		if bytes > MaxProjectionBytes {
			return state, nil, failure(knowl.RetrievalProjectionCapacity)
		}
		if len(prepared.Inputs) == 0 {
			continue
		}
		vectors, err := engine.Provider.Embed(buildCtx, prepared.Inputs)
		if err != nil {
			return state, nil, err
		}
		if len(vectors) != len(prepared.Inputs) {
			return state, nil, failure(knowl.RetrievalInvalidResponse)
		}
		for ordinal, input := range prepared.Inputs {
			if err := validateVector(vectors[ordinal], engine.Space.Dimensions); err != nil {
				return state, nil, err
			}
			hash := sha256.Sum256([]byte(input))
			chunks = append(chunks, Chunk{PageID: page.ID, PageDigest: page.Digest, Ordinal: ordinal, ContentHash: hex.EncodeToString(hash[:]), Vector: vectors[ordinal]})
		}
	}
	state.ChunkCount = len(chunks)
	if err := ctx.Err(); err != nil {
		return state, nil, err
	}
	if err := ValidateProjection(buildCtx, state, chunks); err != nil {
		return state, nil, err
	}
	return state, chunks, nil
}

func (engine *Engine) Report() knowl.RetrievalReport {
	if engine == nil {
		return knowl.RetrievalReport{Requested: knowl.RetrievalLexical, Effective: knowl.RetrievalLexical}
	}
	return knowl.RetrievalReport{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalHybrid, ModelSpace: engine.Fingerprint[:16]}
}

// Failure returns a safe per-call failure/degradation, preserving cancellation
// and invalid caller/configuration errors instead of masking them as success.
func (engine *Engine) Failure(ctx context.Context, report knowl.RetrievalReport, cause error) (knowl.RetrievalReport, error) {
	report.Effective = knowl.RetrievalFailed
	if err := ctx.Err(); err != nil {
		report.Reason = knowl.RetrievalDeadline
		return report, err
	}
	report.Reason = knowl.RetrievalUnavailable
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, context.Canceled) {
		report.Reason = knowl.RetrievalDeadline
	}
	var classified *app.EmbeddingError
	if errors.As(cause, &classified) && app.ValidRetrievalFailure(classified.Code) {
		report.Reason = classified.Code
	}
	if engine.FailurePolicy == app.EmbeddingFallbackLexical && report.Reason != knowl.RetrievalInvalidInput && report.Reason != knowl.RetrievalInvalidConfiguration {
		report.Effective = knowl.RetrievalDegraded
		return report, nil
	}
	return report, failure(report.Reason)
}

func CandidateLimit(limit int) int { return min(100, max(20, 4*limit)) }

// Rank aggregates maximum cosine over all query windows and page chunks. Rows
// must already be complete/compatible and scope/source-filtered by the adapter.
func Rank(ctx context.Context, queries [][]float32, chunks []Chunk, limit int) ([]knowl.PageID, error) {
	scores := make(map[knowl.PageID]float64)
	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, query := range queries {
			score, err := Cosine(query, chunk.Vector)
			if err != nil {
				return nil, err
			}
			previous, seen := scores[chunk.PageID]
			if !seen || score > previous {
				scores[chunk.PageID] = score
			}
		}
	}
	ids := make([]knowl.PageID, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] != scores[ids[j]] {
			return scores[ids[i]] > scores[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids[:min(limit, len(ids))], nil
}

// Fuse preserves channel rank positions, deduplicates pages per channel and
// uses page identity for equal reciprocal-rank scores.
func Fuse(lexical, dense []knowl.PageID, limit int) []knowl.PageID {
	scores := make(map[knowl.PageID]float64, len(lexical)+len(dense))
	for _, channel := range [][]knowl.PageID{lexical, dense} {
		seen := make(map[knowl.PageID]bool, len(channel))
		for rank, id := range channel {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			scores[id] += 1 / float64(FusionRankConstant+rank+1)
		}
	}
	ids := make([]knowl.PageID, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] != scores[ids[j]] {
			return scores[ids[i]] > scores[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids[:min(limit, len(ids))]
}

func (engine *Engine) EmbedQuery(ctx context.Context, prepared PreparedText) ([][]float32, error) {
	if len(prepared.Inputs) == 0 || len(prepared.Inputs) > QueryChunks {
		return nil, failure(knowl.RetrievalInvalidInput)
	}
	vectors, err := engine.Provider.Embed(ctx, prepared.Inputs)
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(prepared.Inputs) {
		return nil, failure(knowl.RetrievalInvalidResponse)
	}
	for _, vector := range vectors {
		if err := validateVector(vector, engine.Space.Dimensions); err != nil {
			return nil, err
		}
	}
	return vectors, nil
}

func (engine *Engine) MaintenancePolicy() *app.MaintenanceRetrievalPolicy {
	if engine == nil {
		return nil
	}
	return &app.MaintenanceRetrievalPolicy{Mode: knowl.RetrievalHybrid, ModelSpace: engine.Fingerprint, Preprocessing: PreprocessingVersion, VectorNormalization: "unit-float32-v1", Fusion: "page-max-cosine-rrf-v1", RankConstant: FusionRankConstant, MinimumCandidates: 20, MaximumCandidates: 100, CandidateMultiplier: 4, MaxChunks: MaxChunks, MaxProjectionBytes: MaxProjectionBytes, FailurePolicy: engine.FailurePolicy}
}
