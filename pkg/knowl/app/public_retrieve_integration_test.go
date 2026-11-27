package app_test

import (
	"context"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	publicIntegrationScope domain.ScopeRef = "public-retrieve-integration"
	publicTagQuery                         = "tagbeacon"
	publicDensePageID      domain.PageID   = "dense"
	publicDenseToken                       = "densewinner"
	publicDenseBody                        = publicDenseToken + " original semantic passage"
	publicHybridControlID                  = "hybrid-control"
	publicQueryPrefix                      = "public query: "
)

type publicCountingMaintainer struct{ calls atomic.Int64 }

func (maintainer *publicCountingMaintainer) Plan(context.Context, domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	maintainer.calls.Add(1)
	return domain.ModelEditPlan{}, nil
}

type publicEmbeddingProvider struct{ calls atomic.Int64 }

func (provider *publicEmbeddingProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	provider.calls.Add(1)
	vectors := make([][]float32, len(inputs))
	for index, input := range inputs {
		if strings.HasPrefix(input, publicQueryPrefix) || strings.Contains(input, publicDenseToken) {
			vectors[index] = []float32{1, 0}
		} else {
			vectors[index] = []float32{0, 1}
		}
	}
	return vectors, nil
}

func TestRetrievePublicUsesRealSQLiteLexicalEvidenceDeterministically(t *testing.T) {
	t.Parallel()

	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(t.Context(), t.TempDir()+"/lexical.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	page := func(id, title string, tags []string, description, body string) domain.PageSnapshot {
		return domain.PageSnapshot{
			ID: domain.PageID(id), Path: "wiki/" + id + ".md", Title: title, Body: body, Content: body,
			Digest: "digest-" + id, SourceRefs: []string{"raw:" + id + "@1"},
			OKF: &okf.Metadata{Type: "Reference", Title: title, Tags: tags, Description: description},
		}
	}
	snapshot := domain.WorkspaceSnapshot{Scope: publicIntegrationScope, SchemaDigest: strings.Repeat("a", 64), Pages: []domain.PageSnapshot{
		page("title", "titlebeacon", []string{"title-neutral-tag"}, "title neutral description", "title neutral body"),
		page("tag", "Tag page", []string{publicTagQuery}, "tag neutral description", "tag neutral body"),
		page("description", "Description page", []string{"description-neutral-tag"}, "descriptionbeacon", "description neutral body"),
		page("late", "Late body", []string{"late-neutral-tag"}, "late neutral description", strings.Repeat("unrelated prefix ", 200)+"latebeacon final evidence"),
	}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	maintainer := &publicCountingMaintainer{}
	ingest, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	query, err := app.NewQueryService(workspace, store, store, ingest, app.QueryOptions{})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		query  string
		pageID domain.PageID
		marker string
	}{
		{query: "titlebeacon", pageID: "title", marker: "titlebeacon"},
		{query: publicTagQuery, pageID: "tag", marker: publicTagQuery},
		{query: "descriptionbeacon", pageID: "description", marker: "descriptionbeacon"},
		{query: "latebeacon", pageID: "late", marker: "latebeacon"},
	}
	for _, test := range tests {
		first, err := query.RetrievePublic(t.Context(), app.PublicRetrieveRequest{Scope: publicIntegrationScope, Query: test.query})
		if err != nil {
			t.Fatal(err)
		}
		second, err := query.RetrievePublic(t.Context(), app.PublicRetrieveRequest{Scope: publicIntegrationScope, Query: test.query})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, second) || len(first.Evidence) == 0 || first.Evidence[0].PageID != test.pageID || !hasFoldedToken(first.Evidence[0].Snippet, test.marker) {
			t.Fatalf("lexical retrieve %q = %#v / %#v", test.query, first, second)
		}
		if !utf8.ValidString(first.Evidence[0].Snippet) || utf8.RuneCountInString(first.Evidence[0].Snippet) > app.MaxPublicRetrieveSnippetRunes {
			t.Fatalf("invalid bounded snippet %q", first.Evidence[0].Snippet)
		}
	}
	if calls := maintainer.calls.Load(); calls != 0 {
		t.Fatalf("public retrieve invoked generation maintainer %d times", calls)
	}
}

func TestRetrievePublicUsesRealSQLiteHybridEvidenceWithoutExtraInference(t *testing.T) {
	t.Parallel()

	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	embeddings := &publicEmbeddingProvider{}
	space := app.EmbeddingSpace{Model: "public-retrieve-model", Revision: "1", Dimensions: 2, QueryPrefix: publicQueryPrefix, PassagePrefix: "public passage: "}
	store, err := sqlite.Open(t.Context(), t.TempDir()+"/hybrid.sqlite", app.EmbeddingOptions{Provider: embeddings, Space: space, FailurePolicy: app.EmbeddingStrict})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := domain.WorkspaceSnapshot{Scope: publicIntegrationScope, SchemaDigest: strings.Repeat("b", 64), Pages: []domain.PageSnapshot{
		{ID: publicDensePageID, Path: "wiki/dense.md", Title: "Dense evidence", Body: publicDenseBody, Content: publicDenseBody, Digest: string(publicDensePageID), SourceRefs: []string{"raw:dense@1"}},
		{ID: domain.PageID(publicHybridControlID), Path: "wiki/" + publicHybridControlID + ".md", Title: "Hybrid control", Body: "unrelated content", Content: "unrelated content", Digest: publicHybridControlID, SourceRefs: []string{"raw:" + publicHybridControlID + "@1"}},
	}}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	embeddings.calls.Store(0)
	maintainer := &publicCountingMaintainer{}
	ingest, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	query, err := app.NewQueryService(workspace, store, store, ingest, app.QueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	request := app.PublicRetrieveRequest{Scope: publicIntegrationScope, Query: "paraphrasedneedle"}
	first, err := query.RetrievePublic(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := query.RetrievePublic(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || len(first.Evidence) == 0 || first.Evidence[0].PageID != publicDensePageID || !hasFoldedToken(first.Evidence[0].Snippet, publicDenseToken) {
		t.Fatalf("hybrid retrieve = %#v / %#v", first, second)
	}
	if first.Retrieval == nil || first.Retrieval.Effective != domain.RetrievalHybrid {
		t.Fatalf("hybrid retrieval status = %#v", first.Retrieval)
	}
	if calls := embeddings.calls.Load(); calls != 2 {
		t.Fatalf("query embedding calls = %d, want exactly one existing search inference per retrieve", calls)
	}
	if calls := maintainer.calls.Load(); calls != 0 {
		t.Fatalf("public retrieve invoked generation maintainer %d times", calls)
	}
}

func hasFoldedToken(text, wanted string) bool {
	for _, field := range strings.Fields(text) {
		if strings.EqualFold(strings.Trim(field, " :.,;!?…\n\r\t"), wanted) {
			return true
		}
	}
	return false
}
