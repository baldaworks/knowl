package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	queryTestSourceEngineering = "engineering"
	queryTestSourceOperations  = "operations"
	queryTestSourceRevision    = "revision-1"
)

func TestNormalizeSourcesFilter(t *testing.T) {
	t.Parallel()

	got, err := NormalizeSourcesFilter([]knowl.SourceID{" " + queryTestSourceOperations + " ", "", queryTestSourceEngineering, queryTestSourceOperations, "  "})
	if err != nil {
		t.Fatalf("NormalizeSourcesFilter() error = %v", err)
	}
	want := []knowl.SourceID{queryTestSourceEngineering, queryTestSourceOperations}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeSourcesFilter() = %v, want %v", got, want)
	}
	if got, err := NormalizeSourcesFilter(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("NormalizeSourcesFilter(nil) = %#v, %v; want non-nil empty filter", got, err)
	}
	if _, err := NormalizeSourcesFilter([]knowl.SourceID{"Engineering"}); !errors.Is(err, ErrSourceInvalid) {
		t.Fatalf("invalid source error = %v, want ErrSourceInvalid", err)
	}
	secret := "postgres://operator:credential@example.test/private"
	if _, err := NormalizeSourcesFilter([]knowl.SourceID{knowl.SourceID(secret)}); !errors.Is(err, ErrSourceInvalid) || strings.Contains(err.Error(), secret) {
		t.Fatalf("secret-shaped source error = %v, want redacted ErrSourceInvalid", err)
	}
}

func TestQueryServiceNormalizesSourcesBeforeDispatch(t *testing.T) {
	t.Parallel()

	index := new(recordingSourceFilterIndex)
	service := &QueryService{index: index, limits: DefaultReadLimits()}
	if _, err := service.Search(context.Background(), "local", "query", knowl.ReadLimits{}, []knowl.SourceID{queryTestSourceOperations, " engineering ", queryTestSourceOperations}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	want := []knowl.SourceID{queryTestSourceEngineering, queryTestSourceOperations}
	if index.calls != 1 || !reflect.DeepEqual(index.sources, want) {
		t.Fatalf("index dispatch = %d, %v; want 1, %v", index.calls, index.sources, want)
	}

	overBound := make([]knowl.SourceID, maxSourcesFilter+1)
	for position := range overBound {
		overBound[position] = knowl.SourceID(fmt.Sprintf("source-%02d", position))
	}
	if _, err := service.Search(context.Background(), "local", "query", knowl.ReadLimits{}, overBound); !errors.Is(err, ErrSourceInvalid) {
		t.Fatalf("over-bound Search() error = %v, want ErrSourceInvalid", err)
	}
	if index.calls != 1 {
		t.Fatalf("over-bound Search() dispatched to index; calls = %d", index.calls)
	}
}

func TestQueryServicePreservesSourceEvidenceAndCuratedJSON(t *testing.T) {
	t.Parallel()

	document := &knowl.SourceDocument{
		SourceID: queryTestSourceEngineering, DocumentID: "docs/one.md", Revision: queryTestSourceRevision, URI: "file:///engineering/docs/one.md",
	}
	operationsDocument := knowl.SourceDocument{
		SourceID: queryTestSourceOperations, DocumentID: "runbook.md", Revision: queryTestSourceRevision, URI: "file:///operations/runbook.md",
	}
	index := &recordingSourceFilterIndex{results: []knowl.PageReference{
		{ID: "curated", Path: "wiki/curated.md", Title: "Curated", Snippet: "curated evidence", SourceRefs: []string{"source:curated"}},
		{ID: "sourced", Path: "wiki/entities/shared.md", Title: "Sourced", Snippet: "source evidence", SourceRefs: []string{"source:engineering", "source:operations"}, SourceDocument: document, SourceDocuments: []knowl.SourceDocument{operationsDocument, *document, *document}},
	}}
	service := &QueryService{index: index, limits: DefaultReadLimits()}
	references, err := service.Search(context.Background(), "local", "evidence", knowl.ReadLimits{}, nil)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(references) != 2 || references[0].SourceDocument != nil || references[1].SourceDocument == nil || *references[1].SourceDocument != *document || len(references[1].SourceDocuments) != 2 || references[1].SourceDocuments[0] != *document || references[1].SourceDocuments[1] != operationsDocument || !references[0].Untrusted || !references[1].Untrusted {
		t.Fatalf("Search() references = %#v", references)
	}
	encoded, err := json.Marshal(references[0])
	if err != nil {
		t.Fatalf("marshal curated reference: %v", err)
	}
	wantJSON := `{"id":"curated","path":"wiki/curated.md","title":"Curated","snippet":"curated evidence","source_refs":["source:curated"],"untrusted":true}`
	if string(encoded) != wantJSON {
		t.Fatalf("curated reference JSON = %s, want %s", encoded, wantJSON)
	}

	references[1].SourceDocument.Revision = mutatedFixtureValue
	references[1].SourceDocuments[0].Revision = mutatedFixtureValue
	if document.Revision != queryTestSourceRevision {
		t.Fatalf("Search() exposed mutable index provenance: %#v", document)
	}
	result, err := service.Query(context.Background(), "local", "evidence", knowl.ReadLimits{}, nil)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(result.Pages) != 2 || result.Pages[1].SourceDocument == nil || *result.Pages[1].SourceDocument != *document || len(result.Pages[1].SourceDocuments) != 2 || result.Pages[1].SourceDocuments[1] != operationsDocument {
		t.Fatalf("Query() pages = %#v", result.Pages)
	}
}

type recordingSourceFilterIndex struct {
	calls   int
	sources []knowl.SourceID
	results []knowl.PageReference
}

func (*recordingSourceFilterIndex) SelectContext(context.Context, knowl.ScopeRef, knowl.SourceSummary, knowl.ReadLimits) ([]knowl.PageID, error) {
	return nil, nil
}

func (index *recordingSourceFilterIndex) Search(_ context.Context, _ knowl.ScopeRef, _ string, _ knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, error) {
	index.calls++
	index.sources = append([]knowl.SourceID(nil), sources...)
	return append([]knowl.PageReference(nil), index.results...), nil
}

func (*recordingSourceFilterIndex) Links(context.Context, knowl.ScopeRef, knowl.PageID, knowl.ReadLimits) ([]knowl.LinkReference, error) {
	return nil, nil
}

func (*recordingSourceFilterIndex) Project(context.Context, knowl.ContentCommit) error { return nil }

func (*recordingSourceFilterIndex) Rebuild(context.Context, knowl.WorkspaceSnapshot) error {
	return nil
}
