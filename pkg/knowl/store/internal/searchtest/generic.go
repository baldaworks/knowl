package searchtest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// runGeneric uses independently authored originals and expectations, not the
// index encoding. Numeric rank values remain backend-specific.
func runGeneric(t *testing.T, index Index, invalid InvalidError) {
	t.Helper()
	const scope knowl.ScopeRef = "generic-shared"
	const accentID knowl.PageID = "accent"
	const mixedID knowl.PageID = "mixed"
	snapshot := knowl.WorkspaceSnapshot{Scope: scope, SchemaDigest: "generic-shared-v1", CapturedAt: capturedAt, Pages: []knowl.PageSnapshot{
		page(accentID, "wiki/accent.md", "Café", "canonical evidence", "raw:accent@1"),
		page("ascii", "wiki/ascii.md", "Cafe", "ordinary evidence", "raw:ascii@1"),
		page("span", "wiki/span.md", "Original span", "prefix e\u0301 suffix", "raw:span@1"),
		page(mixedID, "wiki/mixed.md", "SDKхранилище2", "mixed technical token", "raw:mixed@1"),
		page("question", "wiki/question.md", "Question", "What is WHY", "raw:question@1"),
		page("maxword", "wiki/maxword.md", "Word boundary", strings.Repeat("z", 256), "raw:word@1"),
		page("snippets", "wiki/snippets.md", "Excerpt boundary", strings.Repeat("_", 270000)+" snippetbeacon suffix", "raw:snippet@1"),
	}}
	snapshot.Pages[0].SourceDocuments = []knowl.SourceDocument{{SourceID: engineeringID, DocumentID: "accent.md", Revision: testSourceRevision}}
	if err := index.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query string
		want  knowl.PageID
	}{{"CAFE\u0301", accentID}, {"café", accentID}, {"cafe", "ascii"}, {"sdkХРАНИЛИЩЕ2", mixedID}, {"what", "question"}, {"WHY", "question"}, {strings.Repeat("z", 256), "maxword"}} {
		refs, err := index.Search(t.Context(), scope, test.query, knowl.ReadLimits{Pages: 10, Characters: 300}, nil)
		if err != nil || len(refs) != 1 || refs[0].ID != test.want {
			t.Fatalf("query %q results=%v error=%v", test.query, refs, err)
		}
		original := snapshot.Pages[slices.IndexFunc(snapshot.Pages, func(p knowl.PageSnapshot) bool { return p.ID == test.want })]
		if refs[0].Title != original.Title || !slices.Equal(refs[0].SourceRefs, original.SourceRefs) || refs[0].Snippet != original.Title+"\n\n"+original.Content {
			t.Fatalf("original evidence=%#v", refs[0])
		}
	}
	for _, query := range []string{"sdkхранилище", "cafeteria"} {
		refs, err := index.Search(t.Context(), scope, query, knowl.ReadLimits{}, nil)
		if err != nil || len(refs) != 0 {
			t.Fatalf("unrelated word %q results=%v error=%v", query, refs, err)
		}
	}
	span, err := index.Search(t.Context(), scope, "É", knowl.ReadLimits{Pages: 1, Characters: 2}, nil)
	if err != nil || len(span) != 1 || span[0].ID != "span" || span[0].Snippet != "e\u0301" {
		t.Fatalf("original combining span=%v error=%v", span, err)
	}
	for _, requested := range []int{0, -1, 262145} {
		bound := 4096
		if requested > 0 {
			bound = 262144
		}
		refs, err := index.Search(t.Context(), scope, "snippetbeacon", knowl.ReadLimits{Pages: 1, Characters: requested}, nil)
		if err != nil || len(refs) != 1 || utf8.RuneCountInString(refs[0].Snippet) > bound || !strings.Contains(refs[0].Snippet, "snippetbeacon") {
			t.Fatalf("snippet bound %d results=%v error=%v", requested, refs, err)
		}
	}
	exact := strings.Repeat(" ", 64*1024-len("café")) + "café"
	if refs, err := index.Search(t.Context(), scope, exact, knowl.ReadLimits{}, nil); err != nil || len(refs) != 1 || refs[0].ID != accentID {
		t.Fatalf("raw query boundary results=%v error=%v", refs, err)
	}
	terms := make([]string, 32)
	for i := range terms {
		terms[i] = fmt.Sprintf("term%d", i)
	}
	if _, err := index.Search(t.Context(), scope, strings.Join(terms, " "), knowl.ReadLimits{}, nil); err != nil {
		t.Fatalf("32 terms rejected: %v", err)
	}
	for _, query := range []string{"café" + strings.Repeat(" ", 64*1024), strings.Repeat("z", 257), strings.Join(append(terms, "extra"), " "), "café\xff"} {
		if _, err := index.Search(t.Context(), scope, query, knowl.ReadLimits{}, nil); err == nil || invalid == nil || !invalid(err) {
			t.Fatalf("invalid query not classified: %v", err)
		}
	}
	for _, count := range []int{256, 257} {
		filters := make([]knowl.SourceID, count)
		for i := range filters {
			filters[i] = engineeringID
		}
		refs, err := index.Search(t.Context(), scope, "café", knowl.ReadLimits{}, filters)
		if count == 257 {
			if !errors.Is(err, app.ErrSourceInvalid) {
				t.Fatalf("raw filter overflow=%v", err)
			}
		} else if err != nil || len(refs) != 1 || refs[0].ID != accentID {
			t.Fatalf("raw filter boundary=%v %v", refs, err)
		}
	}
	tooManySources := make([]knowl.SourceID, 17)
	for i := range tooManySources {
		tooManySources[i] = knowl.SourceID(fmt.Sprintf("source%d", i))
	}
	if _, err := index.Search(t.Context(), scope, "café", knowl.ReadLimits{}, tooManySources[:16]); err != nil {
		t.Fatalf("16 unique sources rejected: %v", err)
	}
	for _, filters := range [][]knowl.SourceID{{"Invalid"}, tooManySources} {
		if _, err := index.Search(t.Context(), scope, "café", knowl.ReadLimits{}, filters); !errors.Is(err, app.ErrSourceInvalid) {
			t.Fatalf("source filter error=%v", err)
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := index.Search(canceled, scope, "café", knowl.ReadLimits{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	before, err := index.Search(t.Context(), scope, "CAFE\u0301", knowl.ReadLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, err := index.Search(t.Context(), scope, "CAFE\u0301", knowl.ReadLimits{}, nil)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("generic replay changed evidence: %v", err)
	}
}
