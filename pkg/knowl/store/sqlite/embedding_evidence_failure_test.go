package sqlite

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestStaleDenseEvidenceFollowsFailurePolicy(t *testing.T) {
	for _, policy := range []app.EmbeddingFailurePolicy{app.EmbeddingStrict, app.EmbeddingFallbackLexical} {
		t.Run(string(policy), func(t *testing.T) {
			space := app.EmbeddingSpace{Model: "fixture", Revision: "1", Dimensions: 2, QueryPrefix: "query: ", PassagePrefix: "passage: "}
			store, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), app.EmbeddingOptions{Provider: fixedEmbeddingProvider{}, Space: space, FailurePolicy: policy})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			snap := knowl.WorkspaceSnapshot{Scope: testLocalScope, SchemaDigest: "schema", Pages: []knowl.PageSnapshot{
				{ID: "a-dense", Path: "wiki/a-dense.md", Digest: "a-digest", Title: "Dense", Body: "Original evidence"},
				{ID: "z-lexical", Path: "wiki/z-lexical.md", Digest: "z-digest", Title: "Lexical", Body: "paraphrasedneedle exists here"},
			}}
			if err := store.Rebuild(t.Context(), snap); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_chunks SET content_hash=? WHERE scope=? AND page_id=?`, strings.Repeat("f", 64), snap.Scope, "a-dense"); err != nil {
				t.Fatal(err)
			}
			refs, report, err := store.SearchWithReport(t.Context(), snap.Scope, "paraphrasedneedle", knowl.ReadLimits{Pages: 2, Characters: 80}, nil)
			if policy == app.EmbeddingStrict {
				if !errors.Is(err, app.ErrEmbedding) || report.Effective != knowl.RetrievalFailed || report.Reason != knowl.RetrievalProjectionDrift || len(refs) != 0 {
					t.Fatalf("strict refs=%+v report=%+v err=%v", refs, report, err)
				}
			} else if err != nil || report.Effective != knowl.RetrievalDegraded || report.Reason != knowl.RetrievalProjectionDrift || len(refs) != 1 || refs[0].ID != "z-lexical" {
				t.Fatalf("fallback refs=%+v report=%+v err=%v", refs, report, err)
			}
		})
	}
}
