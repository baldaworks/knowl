// Package searchtest defines the shared behavioral contract for lexical store
// adapters. It intentionally asserts observable results rather than backend
// rank values.
package searchtest

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	Scope              knowl.ScopeRef = "search-contract"
	ForeignScope       knowl.ScopeRef = "search-contract-foreign"
	decisionBadgerID   knowl.PageID   = "decision-badger"
	engineeringID      knowl.SourceID = "engineering"
	operationsID       knowl.SourceID = "operations"
	sharedSemanticID   knowl.PageID   = "shared-semantic"
	headinglessTitle                  = "Глоссарий-проекта"
	referenceType                     = "Reference"
	lifecycleTitle                    = "Lifecycle reference"
	testSourceRevision                = "revision-1"
	testSharedDocument                = "shared.md"
)

var capturedAt = time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)

// Index is the narrow adapter surface exercised by the retrieval contract.
type Index interface {
	Search(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, error)
	Rebuild(ctx context.Context, snapshot knowl.WorkspaceSnapshot) error
}

// InvalidError classifies an adapter's stable invalid-query error.
type InvalidError func(error) bool

// MeasuredQuery is one curated top-five retrieval expectation.
type MeasuredQuery struct {
	Query    string
	Expected knowl.PageID
}

// MeasuredQueries is the version-1 retrieval evaluation corpus.
var MeasuredQueries = []MeasuredQuery{
	{Query: "badger session", Expected: decisionBadgerID},
	{Query: "lease recovery", Expected: "title-weight"},
	{Query: "quorumbeacon", Expected: "middle-match"},
	{Query: "starling protocol", Expected: "title-only"},
	{Query: "ХРАНИЛИЩЕ, Баджер?", Expected: "unicode"},
	{Query: "badger", Expected: decisionBadgerID},
	{Query: "session", Expected: decisionBadgerID},
	{Query: "tieconcept", Expected: "tie-a"},
	{Query: "boundedfixture", Expected: "bounded-1"},
	{Query: "Why was Badger selected?", Expected: decisionBadgerID},
	{Query: "provenance decision", Expected: decisionBadgerID},
	{Query: "версия2", Expected: "unicode"},
}

// Snapshot returns a fresh fixed canonical projection fixture.
func Snapshot() knowl.WorkspaceSnapshot {
	pages := []knowl.PageSnapshot{
		page(decisionBadgerID, "decisions/badger.md", "Badger Session Memory", "The provenance decision records why Badger was selected for durable session memory.", "source:adr-17"),
		page("badger-only", "notes/badger.md", "Storage Note", "Badger is an embedded key value store.", "source:note-badger"),
		page("session-only", "notes/session.md", "Session Note", "Session state needs a durable lifecycle.", "source:note-session"),
		page("title-weight", "decisions/lease-recovery.md", "Lease Recovery", "An operational note with otherwise comparable wording.", "source:adr-lease"),
		page("body-weight", "notes/lease-recovery.md", "Operational Note", "Lease recovery with otherwise comparable wording.", "source:runbook-lease"),
		page("tie-a", "ties/a.md", "Tieconcept", "identical ranking material", "source:tie-a"),
		page("tie-b", "ties/b.md", "Tieconcept", "identical ranking material", "source:tie-b"),
		page("middle-match", "investigations/quorum.md", "Consensus Investigation", longText("quorumbeacon"), "source:investigation-9"),
		page("title-only", "protocols/starling.md", "Starling Protocol", "A bird-named protocol described without repeating its title terms.", "source:protocol-starling"),
		page("unicode", "decisions/unicode.md", "Хранилище Баджер", "Принято решение версия2 для проекта.", "source:adr-unicode"),
	}
	for index := 1; index <= 6; index++ {
		id := knowl.PageID(fmt.Sprintf("bounded-%d", index))
		path := fmt.Sprintf("limits/%d.md", index)
		pages = append(pages, page(id, path, "Boundedfixture", "boundedfixture result", fmt.Sprintf("source:limit-%d", index)))
	}
	pages = append(pages,
		page("source-curated", "curated/shared.md", "Sourcefilterbeacon Curated", "sourcefilterbeacon curated evidence", "source:curated"),
		sharedSemanticPage(),
		page("root-catalog", "wiki/index.md", "Sourcefilterbeacon Catalog", "sourcefilterbeacon catalog navigation", "source:catalog"),
		page("nested-catalog", "wiki/entities/index.md", "Sourcefilterbeacon Nested Catalog", "sourcefilterbeacon nested navigation", "source:catalog"),
		sourcePage("source-engineering", "wiki/sources/engineering/shared.md", "Sourcefilterbeacon Engineering", engineeringID),
		sourcePage("source-operations", "wiki/sources/operations/shared.md", "Sourcefilterbeacon Operations", operationsID),
		headinglessSourcePage(),
		metadataPage(),
	)
	digests := make(map[string]string, len(pages))
	for _, fixturePage := range pages {
		digests[fixturePage.Path] = fixturePage.Digest
	}
	return knowl.WorkspaceSnapshot{
		Scope:        Scope,
		SchemaDigest: "schema-search-contract-v1",
		PageDigests:  digests,
		Pages:        pages,
		CapturedAt:   capturedAt,
	}
}

// Run exercises the complete backend-neutral retrieval behavior.
func Run(t *testing.T, index Index, invalid InvalidError) {
	t.Helper()
	ctx := context.Background()
	fixture := Snapshot()
	if err := index.Rebuild(ctx, fixture); err != nil {
		t.Fatalf("Rebuild() fixture: %v", err)
	}
	foreign := fixture
	foreign.Scope = ForeignScope
	foreign.Pages = []knowl.PageSnapshot{page("foreign", "foreign.md", "Badger Session", "foreign badger session evidence", "source:foreign")}
	foreign.PageDigests = map[string]string{"foreign.md": foreign.Pages[0].Digest}
	if err := index.Rebuild(ctx, foreign); err != nil {
		t.Fatalf("Rebuild() foreign fixture: %v", err)
	}

	t.Run("strict before relaxed and deduplicated", func(t *testing.T) {
		got := search(t, index, Scope, "badger session", 3, 48)
		if len(got) != 3 || got[0].ID != decisionBadgerID {
			t.Fatalf("strict result is not the immutable prefix: %#v", got)
		}
		if !containsIDs(got[1:], "badger-only", "session-only") {
			t.Fatalf("relaxed fillers = %#v, want badger-only and session-only", got[1:])
		}
		assertUnique(t, got)
	})
	t.Run("title weight", func(t *testing.T) {
		got := search(t, index, Scope, "lease recovery", 5, 48)
		assertPrefix(t, got, "title-weight", "body-weight")
	})
	t.Run("deterministic tie", func(t *testing.T) {
		got := search(t, index, Scope, "tieconcept", 5, 48)
		assertPrefix(t, got, "tie-a", "tie-b")
	})
	t.Run("middle match excerpt", func(t *testing.T) {
		got := search(t, index, Scope, "quorumbeacon", 1, 24)
		assertIDs(t, got, "middle-match")
		assertEvidence(t, got, []string{"quorumbeacon"}, 24)
	})
	t.Run("title only excerpt", func(t *testing.T) {
		got := search(t, index, Scope, "starling protocol", 1, 24)
		assertIDs(t, got, "title-only")
		assertEvidence(t, got, []string{"starling", "protocol"}, 24)
	})
	t.Run("unicode query and excerpt", func(t *testing.T) {
		got := search(t, index, Scope, "КАК ХРАНИЛИЩЕ, Баджер?", 2, 24)
		assertIDs(t, got, "unicode")
		assertEvidence(t, got, []string{"хранилище", "баджер"}, 24)
	})
	t.Run("page limit", func(t *testing.T) {
		got := search(t, index, Scope, "boundedfixture", 3, 32)
		assertIDs(t, got, "bounded-1", "bounded-2", "bounded-3")
	})
	t.Run("scope isolation", func(t *testing.T) {
		got := search(t, index, Scope, "badger session", 10, 48)
		for _, reference := range got {
			if reference.ID == "foreign" {
				t.Fatal("Search() returned a page from another scope")
			}
		}
	})
	t.Run("source filter", func(t *testing.T) {
		assertIDs(t, searchSources(t, index, "sourcefilterbeacon", nil), "source-curated", sharedSemanticID)
		for _, sources := range [][]knowl.SourceID{{engineeringID}, {operationsID}, {operationsID, engineeringID}, {engineeringID, engineeringID}} {
			got := searchSources(t, index, "sourcefilterbeacon", sources)
			assertIDs(t, got, sharedSemanticID)
			if len(got[0].SourceDocuments) != 2 || got[0].SourceDocuments[0].SourceID != engineeringID || got[0].SourceDocuments[1].SourceID != operationsID {
				t.Fatalf("shared semantic provenance = %#v", got[0].SourceDocuments)
			}
		}
		assertIDs(t, searchSources(t, index, "sourcefilterbeacon", []knowl.SourceID{"ghost"}))
	})
	t.Run("out of vocabulary", func(t *testing.T) {
		assertIDs(t, search(t, index, Scope, "xyzzy-valera-no-such-term-92841", 20, 64))
	})
	t.Run("headingless source content", func(t *testing.T) {
		got := search(t, index, Scope, "пользовательскийглоссарий", 1, 96)
		assertIDs(t, got, "headingless-source")
		if got[0].Title != headinglessTitle {
			t.Fatalf("headingless title = %q", got[0].Title)
		}
		for _, technical := range []string{"source_refs", "source_document", "metadataonlybeacon", "type: source"} {
			if strings.Contains(got[0].Snippet, technical) {
				t.Fatalf("snippet %q contains technical metadata %q", got[0].Snippet, technical)
			}
		}
		assertIDs(t, search(t, index, Scope, "metadataonlybeacon", 10, 96))
	})
	t.Run("OKF metadata round trip and search boundaries", func(t *testing.T) {
		got := search(t, index, Scope, "lifecyclebodybeacon", 1, 96)
		assertIDs(t, got, "okf-lifecycle")
		if got[0].OKF == nil || got[0].OKF.Type != referenceType || got[0].OKF.Status != okf.StatusDeprecated || !got[0].OKF.Stale || got[0].OKF.TrustTier != okf.TrustHumanReviewed {
			t.Fatalf("OKF metadata = %#v", got[0].OKF)
		}
		got[0].OKF.Title = "mutated result"
		again := search(t, index, Scope, "lifecyclebodybeacon", 1, 96)
		if again[0].OKF == nil || again[0].OKF.Title != lifecycleTitle {
			t.Fatalf("stored OKF metadata was aliased through a result: %#v", again[0].OKF)
		}
		assertIDs(t, search(t, index, Scope, "descriptionsearchbeacon", 1, 96), "okf-lifecycle")
		assertIDs(t, search(t, index, Scope, "technicalextensionbeacon", 10, 96))
	})
	t.Run("invalid normalization", func(t *testing.T) {
		_, err := index.Search(ctx, Scope, "what is why", knowl.ReadLimits{Pages: 5, Characters: 48}, nil)
		if err == nil || invalid == nil || !invalid(err) {
			t.Fatalf("Search() error = %v, want adapter invalid-query classification", err)
		}
	})
	t.Run("curated top five", func(t *testing.T) {
		hits := 0
		for _, measured := range MeasuredQueries {
			got := search(t, index, Scope, measured.Query, 5, 64)
			if slices.ContainsFunc(got, func(reference knowl.PageReference) bool { return reference.ID == measured.Expected }) {
				hits++
			}
		}
		minimum := int(math.Ceil(0.85 * float64(len(MeasuredQueries))))
		if hits < minimum {
			t.Fatalf("top-five hits = %d/%d, want at least %d", hits, len(MeasuredQueries), minimum)
		}
	})
	t.Run("rebuild equivalence", func(t *testing.T) {
		before := search(t, index, Scope, "badger session", 5, 48)
		if err := index.Rebuild(ctx, Snapshot()); err != nil {
			t.Fatalf("Rebuild() replay: %v", err)
		}
		after := search(t, index, Scope, "badger session", 5, 48)
		if !slices.EqualFunc(before, after, equalReference) {
			t.Fatalf("search changed after rebuild:\nbefore: %#v\nafter:  %#v", before, after)
		}
	})
}

func page(id knowl.PageID, path, title, content, sourceRef string) knowl.PageSnapshot {
	return knowl.PageSnapshot{
		ID: id, Path: path, Digest: "digest-" + string(id), Title: title,
		Content: content, SourceRefs: []string{sourceRef}, Untrusted: true, UpdatedAt: capturedAt,
	}
}

func sourcePage(id knowl.PageID, path, title string, sourceID knowl.SourceID) knowl.PageSnapshot {
	fixture := page(id, path, title, "sourcefilterbeacon sourced evidence", "source:"+string(sourceID))
	fixture.SourceDocument = &knowl.SourceDocument{
		SourceID: sourceID, DocumentID: testSharedDocument, Revision: testSourceRevision, URI: "file:///" + string(sourceID) + "/shared.md",
	}
	return fixture
}

func sharedSemanticPage() knowl.PageSnapshot {
	fixture := page(sharedSemanticID, "entities/shared.md", "Sourcefilterbeacon Shared", "sourcefilterbeacon shared semantic evidence", "raw:engineering@1")
	fixture.SourceRefs = append(fixture.SourceRefs, "raw:operations@1")
	fixture.SourceDocuments = []knowl.SourceDocument{
		{SourceID: engineeringID, DocumentID: testSharedDocument, Revision: testSourceRevision, URI: "file:///engineering/shared.md"},
		{SourceID: operationsID, DocumentID: testSharedDocument, Revision: testSourceRevision, URI: "file:///operations/shared.md"},
	}
	return fixture
}

func headinglessSourcePage() knowl.PageSnapshot {
	body := "\nПолезный пользовательскийглоссарий находится здесь без Markdown-заголовка.\n"
	return knowl.PageSnapshot{
		ID:     "headingless-source",
		Path:   "wiki/concepts/Глоссарий-проекта.md",
		Digest: "digest-headingless-source",
		Title:  headinglessTitle,
		Content: "---\nid: sources/engineering/Глоссарий-проекта\ntitle: Глоссарий-проекта\ntype: source\n" +
			"source_refs:\n  - raw:metadataonlybeacon@1\nsource_document:\n  source_id: engineering\n" +
			"  document_id: Глоссарий-проекта.md\n  revision: revision-1\n  uri: file:///metadataonlybeacon/Глоссарий-проекта.md\n" +
			"---\n" + body,
		Body: body,
		OKF: &okf.Metadata{Type: referenceType, Title: headinglessTitle, Extensions: map[string]any{
			"knowl": map[string]any{"technical": "metadataonlybeacon"},
		}},
		SourceRefs: []string{"raw:metadataonlybeacon@1"},
		SourceDocument: &knowl.SourceDocument{
			SourceID: engineeringID, DocumentID: "Глоссарий-проекта.md", Revision: testSourceRevision, URI: "file:///metadataonlybeacon/Глоссарий-проекта.md",
		},
		Untrusted: true,
		UpdatedAt: capturedAt,
	}
}

func metadataPage() knowl.PageSnapshot {
	staleAfter := capturedAt.Add(-time.Hour)
	body := "Lifecyclebodybeacon remains available for historical retrieval."
	return knowl.PageSnapshot{
		ID: "okf-lifecycle", Path: "wiki/okf-lifecycle.md", Digest: "digest-okf-lifecycle",
		Title: lifecycleTitle, Content: body, Body: body,
		SourceRefs: []string{"source:okf-lifecycle"}, Untrusted: true, UpdatedAt: capturedAt,
		OKF: &okf.Metadata{
			Type: referenceType, Title: lifecycleTitle, Description: "Descriptionsearchbeacon public summary",
			Status: okf.StatusDeprecated, StaleAfter: &staleAfter, Stale: true,
			TrustTier: okf.TrustHumanReviewed, ResolvedStatus: okf.StatusDeprecated,
			Verified:   []okf.Verification{{By: "human:reviewer", At: capturedAt.Add(-2 * time.Hour)}},
			Extensions: map[string]any{"private_note": "technicalextensionbeacon"},
		},
	}
}

func longText(term string) string {
	return "This deliberately long investigation starts with unrelated consensus background. " +
		"Several alternatives and operational constraints were evaluated before the decisive " + term +
		" evidence appeared near the end of the document after enough prefix text to defeat prefix snippets."
}

func search(t *testing.T, index Index, scope knowl.ScopeRef, query string, pages, characters int) []knowl.PageReference {
	t.Helper()
	got, err := index.Search(context.Background(), scope, query, knowl.ReadLimits{Pages: pages, Characters: characters}, nil)
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	if len(got) > pages {
		t.Fatalf("Search(%q) returned %d pages, limit %d", query, len(got), pages)
	}
	normalized, err := lexical.Normalize(query)
	if err != nil {
		t.Fatalf("Normalize(%q): %v", query, err)
	}
	assertEvidence(t, got, normalized.Terms, characters)
	return got
}

func searchSources(t *testing.T, index Index, query string, sources []knowl.SourceID) []knowl.PageReference {
	t.Helper()
	got, err := index.Search(context.Background(), Scope, query, knowl.ReadLimits{Pages: 10, Characters: 64}, sources)
	if err != nil {
		t.Fatalf("Search(%q, %v): %v", query, sources, err)
	}
	return got
}

func assertEvidence(t *testing.T, references []knowl.PageReference, terms []string, characters int) {
	t.Helper()
	for _, reference := range references {
		if reference.ID == "" || reference.Path == "" || reference.Title == "" {
			t.Fatalf("incomplete page identity: %#v", reference)
		}
		if !reference.Untrusted {
			t.Fatalf("reference %q is not marked untrusted", reference.ID)
		}
		if len(reference.SourceRefs) == 0 || reference.SourceRefs[0] == "" {
			t.Fatalf("reference %q lost provenance: %#v", reference.ID, reference.SourceRefs)
		}
		if !utf8.ValidString(reference.Snippet) || utf8.RuneCountInString(reference.Snippet) > characters {
			t.Fatalf("reference %q has invalid or oversized snippet %q", reference.ID, reference.Snippet)
		}
		if !lexical.ContainsTerm(reference.Snippet, terms) {
			t.Fatalf("reference %q snippet %q contains none of %q", reference.ID, reference.Snippet, terms)
		}
	}
}

func assertIDs(t *testing.T, references []knowl.PageReference, want ...knowl.PageID) {
	t.Helper()
	got := make([]knowl.PageID, len(references))
	for index, reference := range references {
		got[index] = reference.ID
	}
	if !slices.Equal(got, want) {
		t.Fatalf("result IDs = %q, want %q", got, want)
	}
}

func assertPrefix(t *testing.T, references []knowl.PageReference, want ...knowl.PageID) {
	t.Helper()
	if len(references) < len(want) {
		t.Fatalf("result count = %d, want prefix %q", len(references), want)
	}
	assertIDs(t, references[:len(want)], want...)
}

func assertUnique(t *testing.T, references []knowl.PageReference) {
	t.Helper()
	seen := make(map[knowl.PageID]struct{}, len(references))
	for _, reference := range references {
		if _, duplicate := seen[reference.ID]; duplicate {
			t.Fatalf("duplicate result ID %q", reference.ID)
		}
		seen[reference.ID] = struct{}{}
	}
}

func containsIDs(references []knowl.PageReference, want ...knowl.PageID) bool {
	got := make([]knowl.PageID, len(references))
	for index, reference := range references {
		got[index] = reference.ID
	}
	for _, id := range want {
		if !slices.Contains(got, id) {
			return false
		}
	}
	return true
}

func equalReference(left, right knowl.PageReference) bool {
	return left.ID == right.ID && left.Path == right.Path && left.Title == right.Title &&
		left.Snippet == right.Snippet && left.Untrusted == right.Untrusted &&
		slices.Equal(left.SourceRefs, right.SourceRefs) && reflect.DeepEqual(left.SourceDocument, right.SourceDocument) &&
		slices.Equal(left.SourceDocuments, right.SourceDocuments) && reflect.DeepEqual(left.OKF, right.OKF)
}
