package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type httpTailEmbeddings struct{}

func (httpTailEmbeddings) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	vectors := make([][]float32, len(inputs))
	for i, input := range inputs {
		if strings.HasPrefix(input, "query: ") || (strings.Contains(input, "beta ") && !strings.Contains(input, "alpha ")) {
			vectors[i] = []float32{1, 0}
		} else {
			vectors[i] = []float32{0, 1}
		}
	}
	return vectors, nil
}

func TestHTTPRetrieveShowsDenseTailEvidence(t *testing.T) {
	ctx := t.Context()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	space := app.EmbeddingSpace{Model: "fixture", Revision: "1", Dimensions: 2, QueryPrefix: "query: ", PassagePrefix: "passage: "}
	store, err := sqlite.Open(ctx, filepath.Join(workspace.Root(), "state.db"), app.EmbeddingOptions{Provider: httpTailEmbeddings{}, Space: space, FailurePolicy: app.EmbeddingStrict})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := domain.WorkspaceSnapshot{Scope: httpTestScope, SchemaDigest: "schema", Pages: []domain.PageSnapshot{{ID: "tail", Path: "wiki/tail.md", Digest: "tail-digest", Title: "Technical note", Body: strings.Repeat("alpha ", 1000) + strings.Repeat("beta ", 200), SourceRefs: []string{"raw:tail@1"}}}}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	ingest, err := app.NewIngestService(workspace, store, store, &httpCountingMaintainer{}, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	query, err := app.NewQueryService(workspace, store, store, ingest, app.QueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Dependencies{Scope: httpTestScope, Ingest: ingest, Query: query, Waker: &httpRecordingWaker{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/retrieve?query=paraphrasedneedle", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	var result knowlapi.RetrieveResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].PageId != "tail" || !strings.Contains(result.Evidence[0].Snippet, "beta") || !result.Evidence[0].Untrusted {
		t.Fatalf("evidence=%+v", result.Evidence)
	}
	if result.Retrieval == nil || result.Retrieval.Effective != "hybrid" {
		t.Fatalf("retrieval=%+v", result.Retrieval)
	}
}
