package searchtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// HybridIndex is the observable store contract for enabled retrieval.
const hybridTargetID = "semantic"

type HybridIndex interface {
	Index
	app.ReportedSearchIndex
	CheckProjection(ctx context.Context, snapshot knowl.WorkspaceSnapshot) error
	ProjectionDegraded(ctx context.Context, scope knowl.ScopeRef) (bool, error)
	ProjectWithoutInference(ctx context.Context, commit knowl.ContentCommit) error
	io.Closer
}

// HybridFactory creates a backend using the same provider and policy contract.
type HybridFactory func(*testing.T, ...app.EmbeddingOptions) HybridIndex

type controlledEmbeddings struct {
	mu      sync.Mutex
	err     error
	calls   int
	inputs  []string
	started chan<- struct{}
	release <-chan struct{}
}

func (provider *controlledEmbeddings) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	provider.mu.Lock()
	provider.calls++
	provider.inputs = append(provider.inputs, inputs...)
	failure, started, release := provider.err, provider.started, provider.release
	provider.started, provider.release = nil, nil
	provider.mu.Unlock()
	if started != nil {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, failure
	}
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}
func (provider *controlledEmbeddings) blockNext(started chan<- struct{}, release <-chan struct{}) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.started, provider.release = started, release
}

func (provider *controlledEmbeddings) fail(err error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.err = err
}
func (provider *controlledEmbeddings) count() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

// RunHybrid checks orchestration using deterministic vectors, not model quality.
// Real multilingual recall is evaluated separately against the pinned CPU model.
func RunHybrid(t *testing.T, factory HybridFactory, projectionMismatch InvalidError) {
	t.Helper()
	space := app.EmbeddingSpace{Model: "fixture", Revision: "immutable-1", Dimensions: 2, QueryPrefix: "query: ", PassagePrefix: "passage: "}
	snapshot := func(t *testing.T) knowl.WorkspaceSnapshot {
		t.Helper()
		return knowl.WorkspaceSnapshot{Scope: knowl.ScopeRef(t.Name()), SchemaDigest: "schema", Pages: []knowl.PageSnapshot{{ID: hybridTargetID, Path: "wiki/semantic.md", Title: "Canonical target", Body: "Original enduring evidence", Digest: "page-1", SourceRefs: []string{"raw:original@1"}}}}
	}
	open := func(t *testing.T, p *controlledEmbeddings, policy app.EmbeddingFailurePolicy) HybridIndex {
		t.Helper()
		index := factory(t, app.EmbeddingOptions{Provider: p, Space: space, FailurePolicy: policy})
		t.Cleanup(func() { _ = index.Close() })
		return index
	}
	t.Run("semantic query and direct source seed", func(t *testing.T) {
		provider := &controlledEmbeddings{}
		index := open(t, provider, app.EmbeddingFallbackLexical)
		snap := snapshot(t)
		snap.Pages[0].Body = strings.Repeat("Original enduring evidence. ", 35)
		if err := index.Rebuild(t.Context(), snap); err != nil {
			t.Fatal(err)
		}
		refs, report, err := index.SearchWithReport(t.Context(), snap.Scope, "paraphrasedneedle", knowl.ReadLimits{Pages: 1, Characters: 80}, nil)
		if err != nil || len(refs) != 1 || refs[0].ID != hybridTargetID || report.Effective != knowl.RetrievalHybrid || report.VectorCandidates != 1 || report.ScannedChunks < 2 || !reflect.DeepEqual(refs[0].SourceRefs, snap.Pages[0].SourceRefs) {
			t.Fatalf("refs=%+v report=%+v err=%v", refs, report, err)
		}
		ids, report, err := index.SelectContextWithReport(t.Context(), snap.Scope, knowl.SourceSummary{Title: "Untitled", Body: "Source body without overlapping vocabulary"}, knowl.ReadLimits{Pages: 1})
		if err != nil || len(ids) != 1 || ids[0] != hybridTargetID || report.VectorCandidates != 1 {
			t.Fatalf("direct seeds=%v report=%+v err=%v", ids, report, err)
		}
		provider.mu.Lock()
		inputs := append([]string(nil), provider.inputs...)
		provider.mu.Unlock()
		found := false
		for _, input := range inputs {
			if strings.Contains(input, "Source body without overlapping vocabulary") {
				found = true
			}
		}
		if !found {
			t.Fatal("original source body did not reach embeddings")
		}
	})
	t.Run("source filter before top k and foreign scope", func(t *testing.T) {
		provider := &controlledEmbeddings{}
		index := open(t, provider, app.EmbeddingFallbackLexical)
		snap := snapshot(t)
		snap.Pages[0].ID = "z-target"
		snap.Pages[0].SourceDocuments = []knowl.SourceDocument{{SourceID: "eligible", DocumentID: "target.md", Revision: "1"}}
		for i := range 24 {
			snap.Pages = append(snap.Pages, knowl.PageSnapshot{ID: knowl.PageID(fmt.Sprintf("a-decoy-%02d", i)), Path: fmt.Sprintf("wiki/decoy-%02d.md", i), Title: "Other evidence", Body: "Other material", Digest: "page", SourceDocuments: []knowl.SourceDocument{{SourceID: "excluded", DocumentID: knowl.DocumentID(fmt.Sprintf("%d.md", i)), Revision: "1"}}})
		}
		if err := index.Rebuild(t.Context(), snap); err != nil {
			t.Fatal(err)
		}
		foreign := snapshot(t)
		foreign.Scope += "-foreign"
		foreign.Pages[0].ID = "a-foreign"
		foreign.Pages[0].SourceDocuments = snap.Pages[0].SourceDocuments
		if err := index.Rebuild(t.Context(), foreign); err != nil {
			t.Fatal(err)
		}
		refs, report, err := index.SearchWithReport(t.Context(), snap.Scope, "paraphrasedneedle", knowl.ReadLimits{Pages: 1}, []knowl.SourceID{"eligible"})
		if err != nil || len(refs) != 1 || refs[0].ID != "z-target" || report.VectorCandidates != 1 {
			t.Fatalf("filter refs=%v report=%+v err=%v", refs, report, err)
		}
		refs, _, err = index.SearchWithReport(t.Context(), snap.Scope, "paraphrasedneedle", knowl.ReadLimits{Pages: 1}, []knowl.SourceID{"missing"})
		if err != nil || len(refs) != 0 {
			t.Fatalf("unknown source refs=%v err=%v", refs, err)
		}
	})
	t.Run("model runs outside SQL and historical projection does not infer", func(t *testing.T) {
		provider := &controlledEmbeddings{}
		index := open(t, provider, app.EmbeddingFallbackLexical)
		snap := snapshot(t)
		if err := index.Rebuild(t.Context(), snap); err != nil {
			t.Fatal(err)
		}
		started, release := make(chan struct{}), make(chan struct{})
		provider.blockNext(started, release)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		type searchOutcome struct {
			report knowl.RetrievalReport
			err    error
		}
		done := make(chan searchOutcome, 1)
		go func() {
			_, report, err := index.SearchWithReport(ctx, snap.Scope, "Canonical", knowl.ReadLimits{Pages: 1}, nil)
			done <- searchOutcome{report, err}
		}()
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		writeCtx, writeCancel := context.WithTimeout(t.Context(), time.Second)
		writeErr := index.ProjectWithoutInference(writeCtx, knowl.ContentCommit{Snapshot: snap})
		writeCancel()
		close(release)
		if writeErr != nil {
			t.Fatalf("model blocked lexical publication: %v", writeErr)
		}
		outcome := <-done
		if outcome.err != nil || outcome.report.Effective != knowl.RetrievalDegraded || outcome.report.Reason != knowl.RetrievalProjectionNotReady {
			t.Fatalf("new snapshot read: %+v", outcome)
		}
		if provider.count() != 2 {
			t.Fatalf("historical projection called model: %d", provider.count())
		}
	})

	t.Run("readiness requires caller snapshot", func(t *testing.T) {
		provider := &controlledEmbeddings{}
		index := open(t, provider, app.EmbeddingFallbackLexical)
		original := snapshot(t)
		if err := index.Rebuild(t.Context(), original); err != nil {
			t.Fatal(err)
		}
		current := original
		current.Pages = append([]knowl.PageSnapshot(nil), original.Pages...)
		current.Pages[0].Digest = "changed-page"
		current.Pages[0].Body = "Changed canonical authority"
		if err := index.Rebuild(t.Context(), current); err != nil {
			t.Fatal(err)
		}
		if err := index.CheckProjection(t.Context(), original); !projectionMismatch(err) {
			t.Fatalf("stale readiness not rejected: %v", err)
		}
		if err := index.CheckProjection(t.Context(), current); err != nil {
			t.Fatal(err)
		}
	})

	for _, policy := range []app.EmbeddingFailurePolicy{app.EmbeddingFallbackLexical, app.EmbeddingStrict} {
		t.Run(string(policy)+" failure policy", func(t *testing.T) {
			provider := &controlledEmbeddings{}
			index := open(t, provider, policy)
			snap := snapshot(t)
			if err := index.Rebuild(t.Context(), snap); err != nil {
				t.Fatal(err)
			}
			provider.fail(&app.EmbeddingError{Code: knowl.RetrievalUnavailable})
			refs, report, err := index.SearchWithReport(t.Context(), snap.Scope, "Canonical target", knowl.ReadLimits{Pages: 1}, nil)
			if policy == app.EmbeddingStrict {
				if !errors.Is(err, app.ErrEmbedding) || report.Effective != knowl.RetrievalFailed || len(refs) != 0 {
					t.Fatalf("strict refs=%v report=%+v err=%v", refs, report, err)
				}
			} else if err != nil || report.Effective != knowl.RetrievalDegraded || len(refs) != 1 || refs[0].ID != hybridTargetID {
				t.Fatalf("fallback refs=%v report=%+v err=%v", refs, report, err)
			}
			calls := provider.count()
			for _, query := range []string{"", "\xff", strings.Repeat("a", 257)} {
				if _, _, err := index.SearchWithReport(t.Context(), snap.Scope, query, knowl.ReadLimits{}, nil); err == nil {
					t.Fatal("invalid query succeeded")
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, _, err := index.SearchWithReport(ctx, snap.Scope, "valid", knowl.ReadLimits{}, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation=%v", err)
			}
			if calls != provider.count() {
				t.Fatal("invalid or cancelled input invoked provider")
			}
			err = index.Rebuild(t.Context(), snap)
			if policy == app.EmbeddingStrict && !errors.Is(err, app.ErrEmbedding) || policy == app.EmbeddingFallbackLexical && err != nil {
				t.Fatalf("rebuild=%v", err)
			}
			degraded, err := index.ProjectionDegraded(t.Context(), snap.Scope)
			if err != nil || !degraded {
				t.Fatalf("failed rebuild state degraded=%v err=%v", degraded, err)
			}
			err = index.CheckProjection(t.Context(), snap)
			if policy == app.EmbeddingStrict && !errors.Is(err, app.ErrEmbedding) || policy == app.EmbeddingFallbackLexical && err != nil {
				t.Fatalf("readiness=%v", err)
			}
		})
	}
}
