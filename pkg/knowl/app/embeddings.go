package app

import (
	"context"
	"errors"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// EmbeddingProvider returns normalized vectors in one configured model space.
// Text preparation/prefixes belong to its caller, not the provider.
type EmbeddingProvider interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

// EmbeddingSpace identifies the operator-declared immutable model contract.
// It intentionally cannot carry endpoints, credentials or runtime settings.
type EmbeddingSpace struct {
	Model         string `json:"model"`
	Revision      string `json:"revision"`
	Dimensions    int    `json:"dimensions"`
	QueryPrefix   string `json:"query_prefix"`
	PassagePrefix string `json:"passage_prefix"`
}

// EmbeddingFailurePolicy controls an unavailable semantic retrieval channel.
type EmbeddingFailurePolicy string

const (
	EmbeddingFallbackLexical EmbeddingFailurePolicy = "lexical"
	EmbeddingStrict          EmbeddingFailurePolicy = "strict"
)

var ErrEmbedding = errors.New("embedding failure")

// EmbeddingError contains only a stable redacted reason, never upstream text.
type EmbeddingError struct{ Code knowl.RetrievalFailure }

func (err *EmbeddingError) Error() string { return "embedding: " + string(err.Code) }
func (*EmbeddingError) Unwrap() error     { return ErrEmbedding }

// ReportedSearchIndex preserves the base port while returning per-call reports.
type ReportedSearchIndex interface {
	SearchWithReport(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, knowl.RetrievalReport, error)
	SelectContextWithReport(ctx context.Context, scope knowl.ScopeRef, source knowl.SourceSummary, limits knowl.ReadLimits) ([]knowl.PageID, knowl.RetrievalReport, error)
}

// RetrievalReportStore persists selection evidence for one execution attempt.
// Scope, current attempt and terminal state must be checked atomically.
type RetrievalReportStore interface {
	SaveRetrievalReport(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, attempt int, report knowl.RetrievalReport) error
}
