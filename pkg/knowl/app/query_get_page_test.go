package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	getPageTestID       = knowl.PageID("concepts/get-page")
	getPageTestScope    = knowl.ScopeRef("local")
	getPageRelatedID    = knowl.PageID("concepts/related")
	getPageRelation     = "related"
	getPageTestDigest   = "get-page-digest"
	getPageReservedPage = knowl.PageID("index")
)

type getPageContent struct {
	ContentStore
	page      knowl.PageSnapshot
	readCalls int
}

func (content *getPageContent) ReadPages(_ context.Context, scope knowl.ScopeRef, ids []knowl.PageID, _ knowl.ReadLimits) ([]knowl.PageSnapshot, error) {
	content.readCalls++
	if scope != getPageTestScope || len(ids) != 1 || ids[0] != getPageTestID {
		return nil, nil
	}
	if content.page.ID == "" {
		return nil, nil
	}
	return []knowl.PageSnapshot{content.page}, nil
}

type getPageIndex struct {
	SearchIndex
	links     []knowl.LinkReference
	linkCalls int
}

func (index *getPageIndex) Links(_ context.Context, scope knowl.ScopeRef, page knowl.PageID, _ knowl.ReadLimits) ([]knowl.LinkReference, error) {
	index.linkCalls++
	if scope != getPageTestScope || page != getPageTestID {
		return nil, errors.New("unexpected link lookup")
	}
	return index.links, nil
}

type getPageOperations struct{ OperationStore }

func newGetPageService(t *testing.T, page knowl.PageSnapshot, links []knowl.LinkReference) (*QueryService, *getPageContent, *getPageIndex) {
	t.Helper()
	content := &getPageContent{page: page}
	index := &getPageIndex{links: links}
	service, err := NewQueryService(content, getPageOperations{}, index, nil, QueryOptions{})
	if err != nil {
		t.Fatalf("NewQueryService() error = %v", err)
	}
	return service, content, index
}

func TestQueryServiceGetPageReturnsDetachedCompletePageAndDirectLinks(t *testing.T) {
	usageCount := uint64(3)
	lastModified := time.Date(2026, 11, 26, 1, 2, 3, 0, time.UTC)
	generatedAt := time.Date(2026, 11, 25, 1, 2, 3, 0, time.UTC)
	metadata := &okf.Metadata{
		Type: "concept", Title: "Get Page", Tags: []string{"navigation"},
		Sources:    []okf.Source{{Resource: "source.md", UsageCount: &usageCount, LastModified: &lastModified}},
		Generated:  &okf.Generation{By: fixtureAdapter, At: &generatedAt},
		Extensions: map[string]any{"nested": map[string]any{"value": "original", "count": int64(7)}},
	}
	source := knowl.SourceDocument{SourceID: "docs", DocumentID: "get-page.md", Revision: "revision-1", URI: "file:///docs/get-page.md"}
	page := knowl.PageSnapshot{
		ID: getPageTestID, Path: "wiki/concepts/get-page.md", Digest: "page-digest", Title: "Get Page",
		Content: "---\ntitle: Get Page\n---\n# Get Page\n\nComplete Markdown.\n", Body: "# Get Page\n\nComplete Markdown.\n",
		WikiPaths: []string{"catalogs/api"}, OKF: metadata, SourceRefs: []string{"source-ref"},
		SourceDocuments: []knowl.SourceDocument{source}, UpdatedAt: time.Date(2026, 11, 27, 1, 2, 3, 0, time.UTC),
	}
	links := []knowl.LinkReference{{From: getPageTestID, To: getPageRelatedID, Relation: getPageRelation}}
	service, content, index := newGetPageService(t, page, links)

	result, err := service.GetPage(t.Context(), GetPageRequest{Scope: getPageTestScope, PageID: getPageTestID})
	if err != nil {
		t.Fatalf("GetPage() error = %v", err)
	}
	if content.readCalls != 1 || index.linkCalls != 1 {
		t.Fatalf("read calls = %d, link calls = %d; want one each", content.readCalls, index.linkCalls)
	}
	if result.Scope != getPageTestScope || result.Page.PageID != getPageTestID || result.Page.Markdown != page.Content || result.Page.Digest != page.Digest || result.Page.Version != page.Digest || result.Version != page.Digest || !result.Page.Untrusted {
		t.Fatalf("GetPage() result = %#v", result)
	}
	if len(result.Links) != 1 || result.Links[0].To != getPageRelatedID || !result.Links[0].Untrusted {
		t.Fatalf("GetPage() links = %#v", result.Links)
	}
	if len(result.Page.SourceDocuments) != 1 || result.Page.SourceDocuments[0] != source || result.Page.OKF == nil {
		t.Fatalf("GetPage() provenance = %#v", result.Page)
	}
	if count, ok := result.Page.OKF.Extensions["nested"].(map[string]any)["count"].(int64); !ok || count != 7 {
		t.Fatalf("GetPage() changed extension type: %#v", result.Page.OKF.Extensions)
	}

	result.Page.WikiPaths[0] = mutatedFixtureValue
	result.Page.SourceRefs[0] = mutatedFixtureValue
	result.Page.SourceDocuments[0].Revision = mutatedFixtureValue
	result.Page.OKF.Tags[0] = mutatedFixtureValue
	*result.Page.OKF.Sources[0].UsageCount = 99
	*result.Page.OKF.Sources[0].LastModified = result.Page.UpdatedAt
	*result.Page.OKF.Generated.At = result.Page.UpdatedAt
	result.Page.OKF.Extensions["nested"].(map[string]any)["value"] = mutatedFixtureValue
	result.Links[0].To = mutatedFixtureValue
	if page.WikiPaths[0] != "catalogs/api" || page.SourceRefs[0] != "source-ref" || page.SourceDocuments[0].Revision != "revision-1" || metadata.Tags[0] != "navigation" || *metadata.Sources[0].UsageCount != 3 || !metadata.Sources[0].LastModified.Equal(lastModified) || !metadata.Generated.At.Equal(generatedAt) || metadata.Extensions["nested"].(map[string]any)["value"] != "original" || links[0].To != getPageRelatedID {
		t.Fatal("GetPage() exposed mutable storage or index data")
	}
}

func TestQueryServiceGetPageRejectsMismatchedStoredIdentity(t *testing.T) {
	page := knowl.PageSnapshot{ID: "concepts/other", Content: "# Other\n"}
	service, _, index := newGetPageService(t, page, nil)
	if _, err := service.GetPage(t.Context(), GetPageRequest{Scope: getPageTestScope, PageID: getPageTestID}); !errors.Is(err, ErrOperatorWorkspaceUnavailable) {
		t.Fatalf("GetPage() error = %v, want ErrOperatorWorkspaceUnavailable", err)
	}
	if index.linkCalls != 0 {
		t.Fatalf("identity mismatch read links %d times", index.linkCalls)
	}
}

func TestQueryServiceGetPageValidatesIdentityAndMissingPage(t *testing.T) {
	service, content, index := newGetPageService(t, knowl.PageSnapshot{}, nil)
	for _, request := range []GetPageRequest{
		{},
		{Scope: getPageTestScope, PageID: "../escape"},
		{Scope: getPageTestScope, PageID: getPageReservedPage},
		{Scope: getPageTestScope, PageID: "concepts/" + getPageReservedPage},
	} {
		if _, err := service.GetPage(t.Context(), request); !errors.Is(err, ErrQueryInvalid) {
			t.Errorf("GetPage(%#v) error = %v, want ErrQueryInvalid", request, err)
		}
	}
	if content.readCalls != 0 || index.linkCalls != 0 {
		t.Fatalf("invalid calls read storage: content=%d links=%d", content.readCalls, index.linkCalls)
	}

	if _, err := service.GetPage(t.Context(), GetPageRequest{Scope: getPageTestScope, PageID: getPageTestID}); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("missing GetPage() error = %v, want ErrPageNotFound", err)
	}
	if content.readCalls != 1 || index.linkCalls != 0 {
		t.Fatalf("missing page calls: content=%d links=%d", content.readCalls, index.linkCalls)
	}
}

func TestQueryServiceGetPageEnforcesEncodedResultLimitWithoutTruncation(t *testing.T) {
	page := knowl.PageSnapshot{ID: getPageTestID, Digest: getPageTestDigest, Title: "Page", Content: "# Page\n", SourceRefs: []string{"x"}}
	service, _, _ := newGetPageService(t, page, nil)
	one, err := service.GetPage(t.Context(), GetPageRequest{Scope: getPageTestScope, PageID: getPageTestID})
	if err != nil {
		t.Fatalf("GetPage(one) error = %v", err)
	}
	encoded, err := json.Marshal(one)
	if err != nil {
		t.Fatal(err)
	}
	remaining := MaxGetPageResultBytes - len(encoded)
	if remaining <= 0 {
		t.Fatalf("fixture encoded length = %d", len(encoded))
	}

	page.SourceRefs[0] = strings.Repeat("x", remaining+1)
	service, _, _ = newGetPageService(t, page, nil)
	atLimit, err := service.GetPage(t.Context(), GetPageRequest{Scope: getPageTestScope, PageID: getPageTestID})
	if err != nil {
		t.Fatalf("GetPage(at limit) error = %v", err)
	}
	encoded, err = json.Marshal(atLimit)
	if err != nil || len(encoded) != MaxGetPageResultBytes {
		t.Fatalf("encoded at limit = %d, %v; want %d", len(encoded), err, MaxGetPageResultBytes)
	}

	page.SourceRefs[0] += "x"
	service, _, _ = newGetPageService(t, page, nil)
	if result, err := service.GetPage(t.Context(), GetPageRequest{Scope: getPageTestScope, PageID: getPageTestID}); !errors.Is(err, ErrPageResultTooLarge) || result.Scope != "" || result.Page.PageID != "" || len(result.Links) != 0 {
		t.Fatalf("GetPage(over limit) = %#v, %v; want empty ErrPageResultTooLarge", result, err)
	}
}
