package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const publicSecondPageID knowl.PageID = "pages/two"

func TestRetrievePublicReturnsRankedCompactEvidence(t *testing.T) {
	t.Parallel()

	index := &publicRetrieveIndex{results: []knowl.PageReference{
		{ID: "pages/one", Title: "One", Snippet: strings.Repeat("界", 900), SourceRefs: []string{"raw:z@1", "raw:a@1"}},
		{ID: publicSecondPageID, Title: "Two", Snippet: "runner-up"},
		{ID: "pages/three", Title: "Three", Snippet: "third"},
		{ID: "pages/four", Title: "Four", Snippet: "fourth"},
	}}
	service := &QueryService{index: index, limits: DefaultReadLimits()}
	result, err := service.RetrievePublic(t.Context(), PublicRetrieveRequest{Scope: fixtureScope, Query: " evidence ", Sources: []knowl.SourceID{"operations"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Query != "evidence" || len(result.Evidence) != MaxPublicRetrievePages {
		t.Fatalf("result = %#v", result)
	}
	wantIDs := []knowl.PageID{"pages/one", publicSecondPageID, "pages/three"}
	gotIDs := make([]knowl.PageID, len(result.Evidence))
	for i, evidence := range result.Evidence {
		gotIDs[i] = evidence.PageID
		if !evidence.Untrusted {
			t.Fatalf("evidence %d is trusted", i)
		}
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("page order = %v, want %v", gotIDs, wantIDs)
	}
	if utf8.RuneCountInString(result.Evidence[0].Snippet) != MaxPublicRetrieveSnippetRunes || result.Evidence[0].Citation == nil || *result.Evidence[0].Citation != "raw:a@1" {
		t.Fatalf("first evidence = %#v", result.Evidence[0])
	}
	if result.Evidence[1].Citation == nil || *result.Evidence[1].Citation != "pages/two" {
		t.Fatalf("wiki fallback citation = %#v", result.Evidence[1].Citation)
	}
	if index.limits.Pages != MaxPublicRetrievePages || index.limits.Characters != MaxPublicRetrieveSnippetRunes || !reflect.DeepEqual(index.sources, []knowl.SourceID{"operations"}) {
		t.Fatalf("search call limits=%#v sources=%v", index.limits, index.sources)
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > MaxPublicRetrieveResultBytes {
		t.Fatalf("encoded compact result len=%d err=%v body=%s", len(encoded), err, encoded)
	}
	var shape struct {
		Evidence  []map[string]json.RawMessage `json:"evidence"`
		Citations json.RawMessage              `json:"citations"`
	}
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatal(err)
	}
	if shape.Citations != nil {
		t.Fatal("compact result exposed aggregate citations")
	}
	for _, field := range []string{"source_documents", "okf"} {
		if _, present := shape.Evidence[0][field]; present {
			t.Fatalf("compact evidence exposed rich field %q", field)
		}
	}
}

func TestRetrievePublicPreservesExtractiveEvidenceKindsDeterministically(t *testing.T) {
	t.Parallel()

	want := []knowl.PageReference{
		{ID: "metadata/title", Title: "Title match", Snippet: "Title match"},
		{ID: "metadata/description", Title: "Description", Snippet: "description: matched stored description"},
		{ID: "metadata/tag", Title: "Tag", Snippet: "tag: retrieval"},
		{ID: "body/late", Title: "Late body", Snippet: "…evidence selected near the end of the stored article"},
		{ID: "body/dense", Title: "Dense", Snippet: "winning dense chunk from the original page"},
	}
	for _, reference := range want {
		index := &publicRetrieveIndex{results: []knowl.PageReference{reference}}
		service := &QueryService{index: index, limits: DefaultReadLimits()}
		first, err := service.RetrievePublic(t.Context(), PublicRetrieveRequest{Scope: fixtureScope, Query: "match"})
		if err != nil {
			t.Fatal(err)
		}
		second, err := service.RetrievePublic(t.Context(), PublicRetrieveRequest{Scope: fixtureScope, Query: "match"})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, second) || len(first.Evidence) != 1 || first.Evidence[0].Snippet != reference.Snippet || first.Evidence[0].PageID != reference.ID {
			t.Fatalf("extractive evidence for %q = %#v / %#v", reference.ID, first, second)
		}
		if index.calls != 2 {
			t.Fatalf("search calls for %q = %d, want 2 and no extra inference path", reference.ID, index.calls)
		}
	}
}

func TestRetrievePublicPropagatesCancellationWithoutPartialResult(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	index := &publicRetrieveIndex{waitForContext: true}
	service := &QueryService{index: index, limits: DefaultReadLimits()}
	result, err := service.RetrievePublic(ctx, PublicRetrieveRequest{Scope: fixtureScope, Query: "cancel"})
	if !errors.Is(err, context.Canceled) || len(result.Evidence) != 0 || index.calls != 1 {
		t.Fatalf("canceled retrieve = %#v, err=%v, calls=%d", result, err, index.calls)
	}
}

func TestFitPublicRetrieveResultHonorsSerializedBoundary(t *testing.T) {
	t.Parallel()

	result := PublicRetrieveResult{Query: "q", Evidence: []PublicEvidence{{PageID: "p", Title: "", Snippet: "", Untrusted: true}}}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	result.Evidence[0].Title = strings.Repeat("x", MaxPublicRetrieveResultBytes-len(encoded))
	fitted, err := fitPublicRetrieveResult(result)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(fitted)
	if err != nil || len(encoded) != MaxPublicRetrieveResultBytes {
		t.Fatalf("exact boundary len=%d err=%v", len(encoded), err)
	}
	result.Evidence[0].Title += "x"
	if _, err := fitPublicRetrieveResult(result); !errors.Is(err, ErrPublicRetrieveResultTooLarge) {
		t.Fatalf("oversized first card error = %v", err)
	}
	empty := PublicRetrieveResult{Query: strings.Repeat("x", MaxPublicRetrieveResultBytes), Evidence: []PublicEvidence{}}
	if _, err := fitPublicRetrieveResult(empty); !errors.Is(err, ErrPublicRetrieveResultTooLarge) {
		t.Fatalf("oversized empty envelope error = %v", err)
	}
}

func TestFitPublicRetrieveResultDropsRankingTail(t *testing.T) {
	t.Parallel()

	result := PublicRetrieveResult{Query: "q", Evidence: []PublicEvidence{
		{PageID: "rank-one", Title: strings.Repeat("a", 3000), Untrusted: true},
		{PageID: "rank-two", Title: strings.Repeat("b", 3000), Untrusted: true},
		{PageID: "third", Title: strings.Repeat("c", 3000), Untrusted: true},
	}}
	fitted, err := fitPublicRetrieveResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(fitted.Evidence) != 2 || fitted.Evidence[0].PageID != "rank-one" || fitted.Evidence[1].PageID != "rank-two" {
		t.Fatalf("tail removal = %#v", fitted.Evidence)
	}
}

func TestFitPublicRetrieveResultMeasuresJSONEscaping(t *testing.T) {
	t.Parallel()

	result := PublicRetrieveResult{Query: "escaped", Evidence: []PublicEvidence{{
		PageID: "escaped", Title: strings.Repeat("\"\\", 2500), Snippet: "line one\nline two", Untrusted: true,
	}}}
	if _, err := fitPublicRetrieveResult(result); !errors.Is(err, ErrPublicRetrieveResultTooLarge) {
		t.Fatalf("escaped oversized result error = %v", err)
	}
	result.Evidence[0].Title = "quoted \" title"
	fitted, err := fitPublicRetrieveResult(result)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(fitted)
	if err != nil {
		t.Fatal(err)
	}
	var decoded PublicRetrieveResult
	if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded, fitted) {
		t.Fatalf("escaped round trip = %#v, err=%v", decoded, err)
	}
}

type publicRetrieveIndex struct {
	results        []knowl.PageReference
	limits         knowl.ReadLimits
	sources        []knowl.SourceID
	calls          int
	waitForContext bool
}

func (*publicRetrieveIndex) SelectContext(context.Context, knowl.ScopeRef, knowl.SourceSummary, knowl.ReadLimits) ([]knowl.PageID, error) {
	return nil, nil
}

func (index *publicRetrieveIndex) Search(ctx context.Context, _ knowl.ScopeRef, _ string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, error) {
	index.calls++
	index.limits = limits
	index.sources = append([]knowl.SourceID(nil), sources...)
	if index.waitForContext {
		return nil, ctx.Err()
	}
	return append([]knowl.PageReference(nil), index.results...), nil
}

func (*publicRetrieveIndex) Links(context.Context, knowl.ScopeRef, knowl.PageID, knowl.ReadLimits) ([]knowl.LinkReference, error) {
	return nil, nil
}

func (*publicRetrieveIndex) Project(context.Context, knowl.ContentCommit) error { return nil }

func (*publicRetrieveIndex) Rebuild(context.Context, knowl.WorkspaceSnapshot) error { return nil }
