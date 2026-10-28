package searchtome

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var ReferenceSpace = app.EmbeddingSpace{Model: "intfloat/multilingual-e5-base", Revision: "d128750597153bb5987e10b1c3493a34e5a4502a", Dimensions: 768, QueryPrefix: "query: ", PassagePrefix: "passage: "}
var Gemma2Space = app.EmbeddingSpace{Model: "google/embeddinggemma-2", Revision: "f9972ac22e696a105c482202646979e33e4b5a1e", Dimensions: 768, QueryPrefix: "task: search result | query: ", PassagePrefix: "title: none | text: "}
var ErrProjection = errors.New("benchmark projection is not ready")

type ResultPage struct {
	ID    knowl.PageID `json:"page_id"`
	Title string       `json:"title"`
}
type Observation struct {
	ID        string                `json:"query_id"`
	Domain    string                `json:"domain"`
	Gold      string                `json:"gold_title"`
	Pages     []ResultPage          `json:"top_3"`
	Rank      int                   `json:"gold_rank"`
	LatencyNS int64                 `json:"latency_ns"`
	Report    knowl.RetrievalReport `json:"retrieval"`
	Exclusion string                `json:"exclusion,omitempty"`
	Error     string                `json:"error,omitempty"`
}
type Projection struct {
	Pages   int   `json:"pages"`
	Chunks  *int  `json:"chunks"`
	BuildNS int64 `json:"build_ns"`
}

// RunMode uses the real SQLite search implementation over a previously
// published workspace. A nil provider selects lexical-only retrieval.
func RunMode(ctx context.Context, database, domain string, published PublishedCorpus, queries []Query, provider app.EmbeddingProvider) (Projection, []Observation, error) {
	return RunModeWithSpace(ctx, database, domain, published, queries, provider, ReferenceSpace)
}

func RunModeWithSpace(ctx context.Context, database, domain string, published PublishedCorpus, queries []Query, provider app.EmbeddingProvider, space app.EmbeddingSpace) (Projection, []Observation, error) {
	mode := knowl.RetrievalLexical
	var options []app.EmbeddingOptions
	if provider != nil {
		mode = knowl.RetrievalHybrid
		options = append(options, app.EmbeddingOptions{Provider: provider, Space: space, FailurePolicy: app.EmbeddingFallbackLexical})
	}
	file, err := os.OpenFile(database, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Projection{}, nil, err
	}
	if err = file.Close(); err != nil {
		return Projection{}, nil, err
	}
	store, err := sqlite.Open(ctx, database, options...)
	if err != nil {
		return Projection{}, nil, err
	}
	defer func() { _ = store.Close() }()
	start := time.Now()
	err = store.Rebuild(ctx, published.Snapshot)
	elapsed := time.Since(start).Nanoseconds()
	if err != nil {
		return Projection{}, nil, fmt.Errorf("rebuild: %w", err)
	}
	if err = store.CheckProjection(ctx, published.Snapshot); err != nil {
		return Projection{}, nil, fmt.Errorf("check projection: %w", err)
	}
	degraded, err := store.ProjectionDegraded(ctx, published.Snapshot.Scope)
	if err != nil || degraded {
		return Projection{}, nil, ErrProjection
	}
	status, err := store.ProjectionStatus(ctx, published.Snapshot.Scope)
	if err != nil || status.PageCount != len(published.Titles) {
		return Projection{}, nil, ErrProjection
	}
	projection := Projection{Pages: status.PageCount, BuildNS: elapsed}
	out := make([]Observation, 0, len(queries))
	for _, query := range queries {
		if err := ctx.Err(); err != nil {
			return Projection{}, nil, err
		}
		o := Observation{ID: query.ID, Domain: domain, Gold: query.Gold, Exclusion: query.Exclusion}
		if o.Exclusion != "" {
			out = append(out, o)
			continue
		}
		start = time.Now()
		refs, report, searchErr := store.SearchWithReport(ctx, published.Snapshot.Scope, query.Query, knowl.ReadLimits{Pages: 3, Characters: 1024}, nil)
		o.LatencyNS = time.Since(start).Nanoseconds()
		o.Report = report
		if searchErr != nil {
			o.Error = retrievalError
			if errors.Is(searchErr, sqlite.ErrInvalidQuery) {
				o.Error = invalidQuery
			}
			o.Exclusion = o.Error
		} else if report.Requested != mode || report.Effective != mode {
			o.Exclusion = string(report.Effective)
			if o.Exclusion == "" {
				o.Exclusion = "unexpected_mode"
			}
		}
		if mode == knowl.RetrievalHybrid && report.Effective == knowl.RetrievalHybrid {
			n := report.ScannedChunks
			projection.Chunks = &n
		}
		for _, ref := range refs {
			title, ok := published.Titles[ref.ID]
			if !ok {
				o.Exclusion = "unknown_page"
				continue
			}
			o.Pages = append(o.Pages, ResultPage{ID: ref.ID, Title: title})
			if title == query.Gold {
				o.Rank = len(o.Pages)
			}
		}
		out = append(out, o)
	}
	return projection, out, nil
}
