package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

const testScope = "local"
const (
	testFixtureAdapter           = "fixture"
	testResolverGitAdapter       = "git"
	testResolverDocumentID       = "resolver-page.md"
	testOtherDocumentID          = "other.md"
	testMarkdownMediaType        = "text/markdown"
	testWorkspaceSourceRef       = "fixture:source-1@1"
	testMissingSourceRef         = "fixture:missing@1"
	testWikiSourceAdapter        = "wiki-filesystem"
	testSourceRevision           = "revision-1"
	testDelimiterRevision        = "v1@v2"
	testPageOneID                = "entities/one"
	testPageOnePath              = "wiki/entities/one.md"
	testGoodPagePath             = "wiki/entities/good.md"
	testRejectedPagePath         = "wiki/entities/rejected.md"
	testBadPagePath              = "wiki/entities/bad.md"
	testParentCatalogPath        = "wiki/parent/index.md"
	testUnsafeCatalogPath        = "wiki/unsafe/index.md"
	testIndexPath                = "wiki/index.md"
	testFaultBeforeJournalName   = "before journal"
	testFaultAfterPreimagesName  = "after preimages"
	testFaultBeforeJournalPoint  = "before_journal"
	testFaultAfterPreimagesPoint = "after_preimages"
)

func TestWorkspaceInitCreatesCanonicalOKFControls(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	indexContent, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "index.md"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := okf.ParseRootIndex(indexContent, okf.DefaultLimits())
	if err != nil || index.ObservedVersion != okf.Version {
		t.Fatalf("root index = %#v, %v", index, err)
	}
	logContent, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := okf.ValidateLog("log.md", logContent, okf.DefaultLimits()); err != nil {
		t.Fatalf("root log is not OKF: %v", err)
	}
	if err := workspace.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	schemaContent, err := os.ReadFile(filepath.Join(workspace.Root(), schemaFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"schema_version: 1",
		"operator-owned Markdown policy",
		"untrusted input",
		"independently enforces OKF structure",
		"entities/",
		"concepts/",
		"syntheses/",
		"source references",
		"reachable from the root catalog",
		"contradictions",
		"superseded",
	} {
		if !strings.Contains(string(schemaContent), marker) {
			t.Errorf("starter schema missing %q", marker)
		}
	}

	custom := []byte("# Operator policy\n\nNever replace this file.\n")
	if err := os.WriteFile(filepath.Join(workspace.Root(), schemaFile), custom, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("re-initialize workspace: %v", err)
	}
	gotCustom, err := os.ReadFile(filepath.Join(workspace.Root(), schemaFile))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotCustom, custom) {
		t.Fatalf("re-initialization replaced operator schema:\n%s", gotCustom)
	}
}

func TestWorkspaceSnapshotExcludesNestedReservedDocuments(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(workspace.Root(), "wiki", "entities", "nested")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		okfIndexFilename: []byte("# Nested\n\n* [One](one.md)\n"),
		okfLogFilename:   []byte("# Nested Update Log\n"),
		"one.md":         validWorkspacePage("entities/nested/one", "One", testWorkspaceSourceRef, ""),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(directory, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := workspace.Snapshot(context.Background(), testScope)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot.Pages) != 1 || snapshot.Pages[0].ID != "entities/nested/one" {
		t.Fatalf("reserved documents entered pages: %#v", snapshot.Pages)
	}
	for _, relative := range []string{"wiki/entities/nested/index.md", "wiki/entities/nested/log.md"} {
		if snapshot.PageDigests[relative] == "" {
			t.Fatalf("reserved document digest missing for %q", relative)
		}
	}
}

func TestWorkspaceSnapshotResolvesOnlyExistingOKFConceptLinks(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(workspace.Root(), "wiki", "sources", "engineering", "docs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	one := validWorkspacePage("sources/engineering/docs/one", "One", testWorkspaceSourceRef, "[Two](two.md) [Root](/root.md) [Broken](missing.md) [External](https://example.test/other.md) [Asset](diagram.png)")
	two := validWorkspacePage("sources/engineering/docs/two", "Two", testWorkspaceSourceRef, "")
	for name, content := range map[string][]byte{"one.md": one, "two.md": two} {
		if err := os.WriteFile(filepath.Join(directory, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rootPage := validWorkspacePage("sources/engineering/root", "Root", testWorkspaceSourceRef, "")
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "sources", "engineering", "root.md"), rootPage, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.Snapshot(context.Background(), testScope)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	want := []knowl.LinkReference{
		{From: "sources/engineering/docs/one", To: "sources/engineering/docs/two", Relation: okfLinkRelation},
		{From: "sources/engineering/docs/one", To: "sources/engineering/root", Relation: okfLinkRelation},
	}
	if !slices.Equal(snapshot.Links, want) {
		t.Fatalf("OKF links = %#v, want %#v", snapshot.Links, want)
	}
}

func TestWorkspaceSnapshotAndReadExposeDerivedOKFMetadata(t *testing.T) {
	boundary := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	now := boundary
	workspace, err := New(t.TempDir(), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	content := []byte(`---
type: Metric
description: Полезное описание.
verified: {by: human:alexey, at: 2026-08-25T00:00:00Z}
stale_after: 2026-09-23T00:00:00Z
producer_extension:
  nested: [one, {enabled: true}]
knowl:
  id: sources/engineering/Глоссарий-проекта
  source_refs: [fixture:source-1@1]
  source_document:
    source_id: engineering
    document_id: Глоссарий-проекта.md
    revision: revision-1
    uri: https://wiki.example.test/glossary
---
Полезный пользовательский текст.
`)
	directory := filepath.Join(workspace.Root(), "wiki", "sources", "engineering")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "Глоссарий-проекта.md"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.Snapshot(context.Background(), testScope)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot.Pages) != 1 {
		t.Fatalf("Snapshot() pages = %#v", snapshot.Pages)
	}
	assertDerivedOKFPage(t, snapshot.Pages[0], content)
	if snapshot.CapturedAt != boundary {
		t.Fatalf("CapturedAt = %v, want %v", snapshot.CapturedAt, boundary)
	}
	pages, err := workspace.ReadPages(context.Background(), testScope, []knowl.PageID{"sources/engineering/Глоссарий-проекта"}, knowl.ReadLimits{Pages: 1, Bytes: len(content)})
	if err != nil {
		t.Fatalf("ReadPages() error = %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("ReadPages() pages = %#v", pages)
	}
	assertDerivedOKFPage(t, pages[0], content)

	now = boundary.Add(-time.Nanosecond)
	pages, err = workspace.ReadPages(context.Background(), testScope, []knowl.PageID{"sources/engineering/Глоссарий-проекта"}, knowl.ReadLimits{Pages: 1, Bytes: len(content)})
	if err != nil {
		t.Fatalf("ReadPages(before boundary) error = %v", err)
	}
	if pages[0].OKF == nil || pages[0].OKF.Stale {
		t.Fatalf("page became stale before boundary: %#v", pages[0].OKF)
	}
}

func assertDerivedOKFPage(t *testing.T, page knowl.PageSnapshot, content []byte) {
	t.Helper()
	if page.Title != "Глоссарий-проекта" || page.Body != "Полезный пользовательский текст.\n" || page.Content != string(content) {
		t.Fatalf("title/body/content = %q / %q / %q", page.Title, page.Body, page.Content)
	}
	if page.OKF == nil || page.OKF.Type != "Metric" || page.OKF.TrustTier != okf.TrustHumanReviewed ||
		page.OKF.ResolvedStatus != okf.StatusStable || !page.OKF.Stale || page.OKF.Extensions["producer_extension"] == nil {
		t.Fatalf("OKF metadata = %#v", page.OKF)
	}
	if page.SourceDocument == nil || page.SourceDocument.SourceID != testSourceID || len(page.SourceRefs) != 1 {
		t.Fatalf("provenance = %#v / %#v", page.SourceDocument, page.SourceRefs)
	}
}

func TestAppendLogEntryProducesDeterministicOKFAudit(t *testing.T) {
	manifest := stageManifest{
		OperationID: "operation-->1", SchemaDigest: strings.Repeat("a", 64), SourceRefs: []string{"fixture:one@1"},
		Entries: []stageEntry{{Target: "wiki/entities/one.md", Digest: strings.Repeat("b", 64)}}, LogDate: "2026-08-25",
	}
	first, err := appendLogEntry([]byte(rootLogContent), manifest, strings.Repeat("c", 64))
	if err != nil {
		t.Fatalf("appendLogEntry() error = %v", err)
	}
	second, err := appendLogEntry([]byte(rootLogContent), manifest, strings.Repeat("c", 64))
	if err != nil || !slices.Equal(first, second) {
		t.Fatalf("appendLogEntry() is not deterministic: %v", err)
	}
	logDocument, err := okf.ValidateLog("log.md", first, okf.DefaultLimits())
	if err != nil {
		t.Fatalf("rendered log is not OKF: %v\n%s", err, first)
	}
	if len(logDocument.Groups) != 1 || len(logDocument.Groups[0].Entries) != 1 ||
		!strings.Contains(logDocument.Groups[0].Entries[0], "<!-- knowl:") ||
		!strings.Contains(logDocument.Groups[0].Entries[0], `"operation_id":"operation\u002d\u002d\u003e1"`) ||
		strings.Count(logDocument.Groups[0].Entries[0], "-->") != 1 {
		t.Fatalf("audit entry = %#v", logDocument)
	}
}

func TestWorkspaceInitAcceptsImmutableSourceAndReplaysIt(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	content := []byte("source content")
	digest := sha256.Sum256(content)
	envelope := knowl.SourceEnvelope{
		Scope:     testScope,
		Source:    knowl.SourceRef{Adapter: testFixtureAdapter, ID: "source-1"},
		Version:   knowl.SourceVersion{Version: "1", Digest: hex.EncodeToString(digest[:])},
		MediaType: "text/plain",
		Content:   content,
	}
	first, err := workspace.AcceptSource(context.Background(), envelope)
	if err != nil {
		t.Fatalf("accept source: %v", err)
	}
	second, err := workspace.AcceptSource(context.Background(), envelope)
	if err != nil {
		t.Fatalf("replay source: %v", err)
	}
	if first != second {
		t.Fatalf("replay result changed: %#v != %#v", first, second)
	}
	conflict := envelope
	conflict.Content = []byte("different")
	conflictDigest := sha256.Sum256(conflict.Content)
	conflict.Version.Digest = hex.EncodeToString(conflictDigest[:])
	if _, err := workspace.AcceptSource(context.Background(), conflict); !errors.Is(err, ErrSourceConflict) {
		t.Fatalf("conflicting content error = %v, want source conflict", err)
	}
}

func TestWorkspaceAcceptsEmptyImmutableSource(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	emptyDigest := sha256.Sum256(nil)
	accepted, err := workspace.AcceptSource(context.Background(), knowl.SourceEnvelope{
		Scope:     testScope,
		Source:    knowl.SourceRef{Adapter: testFixtureAdapter, ID: "empty-navigation.md"},
		Version:   knowl.SourceVersion{Version: hex.EncodeToString(emptyDigest[:]), Digest: hex.EncodeToString(emptyDigest[:])},
		MediaType: testMarkdownMediaType,
		Content:   []byte{},
	})
	if err != nil {
		t.Fatalf("accept empty source: %v", err)
	}
	content, err := workspace.ReadSource(context.Background(), accepted, knowl.ReadLimits{})
	if err != nil || len(content) != 0 {
		t.Fatalf("read empty source = %q, %v", content, err)
	}
}

func TestWorkspaceReadsAcceptedSourceWithBoundedDigest(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	content := []byte("bounded source")
	digestValue := sha256.Sum256(content)
	accepted, err := workspace.AcceptSource(context.Background(), knowl.SourceEnvelope{
		Scope:   testScope,
		Source:  knowl.SourceRef{Adapter: "fixture", ID: "read-source"},
		Version: knowl.SourceVersion{Version: "1", Digest: hex.EncodeToString(digestValue[:])},
		Content: content,
	})
	if err != nil {
		t.Fatalf("accept source: %v", err)
	}
	read, err := workspace.ReadSource(context.Background(), accepted, knowl.ReadLimits{Bytes: len(content)})
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if string(read) != string(content) {
		t.Fatalf("read source = %q, want %q", read, content)
	}
	if _, err := workspace.ReadSource(context.Background(), accepted, knowl.ReadLimits{Bytes: len(content) - 1}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("bounded source error = %v, want invalid source", err)
	}
}

func TestWorkspaceReadsAcceptedSourceAtCharacterBoundary(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	for _, test := range []struct {
		name       string
		characters int
		wantErr    bool
	}{
		{name: "reported large document", characters: 108_416},
		{name: "at limit", characters: app.DefaultReadLimits().Characters},
		{name: "over limit", characters: app.DefaultReadLimits().Characters + 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := []byte(strings.Repeat("я", test.characters))
			digestValue := sha256.Sum256(content)
			accepted, acceptErr := workspace.AcceptSource(context.Background(), knowl.SourceEnvelope{
				Scope:   testScope,
				Source:  knowl.SourceRef{Adapter: "fixture", ID: fmt.Sprintf("character-boundary-%d", test.characters)},
				Version: knowl.SourceVersion{Version: "1", Digest: hex.EncodeToString(digestValue[:])},
				Content: content,
			})
			if acceptErr != nil {
				t.Fatalf("accept source: %v", acceptErr)
			}
			_, readErr := workspace.ReadSource(context.Background(), accepted, app.DefaultReadLimits())
			if test.wantErr && !errors.Is(readErr, ErrInvalidSource) {
				t.Fatalf("over character limit error = %v, want invalid source", readErr)
			}
			if !test.wantErr && readErr != nil {
				t.Fatalf("read source at character limit: %v", readErr)
			}
		})
	}
}

func TestWorkspacePersistsConfiguredSourceProvenance(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	content := []byte("# Architecture\n")
	digestValue := sha256.Sum256(content)
	document := knowl.SourceDocument{
		SourceID: testSourceID, DocumentID: "Архитектура.md", Revision: testSourceRevision,
		URI: "https://wiki.example.test/%D0%90%D1%80%D1%85%D0%B8%D1%82%D0%B5%D0%BA%D1%82%D1%83%D1%80%D0%B0.md",
	}
	envelope := knowl.SourceEnvelope{
		Scope: testScope, Source: knowl.SourceRef{Adapter: testWikiSourceAdapter, ID: "engineering/Архитектура.md"},
		Version:   knowl.SourceVersion{Version: document.Revision, Digest: hex.EncodeToString(digestValue[:])},
		MediaType: testMarkdownMediaType, SourceDocument: document, Content: content,
	}
	accepted, err := workspace.AcceptSource(context.Background(), envelope)
	if err != nil {
		t.Fatalf("AcceptSource() error: %v", err)
	}
	if accepted.SourceDocument != document || accepted.MediaType != envelope.MediaType || accepted.Version.Digest != envelope.Version.Digest {
		t.Fatalf("accepted source = %#v, want provenance %#v", accepted, document)
	}
	replayed, err := workspace.AcceptSource(context.Background(), envelope)
	if err != nil || replayed != accepted {
		t.Fatalf("replayed source = %#v, %v; want %#v", replayed, err, accepted)
	}

	conflict := envelope
	conflict.SourceDocument.DocumentID = testOtherDocumentID
	if _, err := workspace.AcceptSource(context.Background(), conflict); !errors.Is(err, ErrSourceConflict) {
		t.Fatalf("provenance conflict error = %v", err)
	}
}

func TestWorkspaceResolvesExactAcceptedSourceAcrossAdapters(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	accepted := make([]knowl.AcceptedSource, 0, 2)
	for _, adapter := range []string{testWikiSourceAdapter, testResolverGitAdapter} {
		content := []byte(adapter + " evidence")
		digest := sha256.Sum256(content)
		envelope := knowl.SourceEnvelope{
			Scope: testScope, Source: knowl.SourceRef{Adapter: adapter, ID: "engineering/resolver-page.md"},
			Version:   knowl.SourceVersion{Version: testSourceRevision, Digest: hex.EncodeToString(digest[:])},
			MediaType: testMarkdownMediaType, Content: content,
			SourceDocument: knowl.SourceDocument{
				SourceID: testSourceID, DocumentID: testResolverDocumentID, Revision: testSourceRevision,
				URI: "https://wiki.example.test/resolver-page.md?adapter=" + adapter,
			},
		}
		source, acceptErr := workspace.AcceptSource(t.Context(), envelope)
		if acceptErr != nil {
			t.Fatal(acceptErr)
		}
		accepted = append(accepted, source)
	}
	for _, want := range accepted {
		resolved, resolveErr := workspace.ResolveAcceptedSources(t.Context(), testScope, []string{app.SourceRefKey(want)})
		got := resolved[app.SourceRefKey(want)]
		if resolveErr != nil || got != want {
			t.Fatalf("ResolveAcceptedSource(%q) = %#v, %v; want %#v", app.SourceRefKey(want), got, resolveErr, want)
		}
	}
	resolved, err := workspace.ResolveAcceptedSources(t.Context(), testScope, []string{"git:engineering/page.md@other"})
	if !errors.Is(err, app.ErrSourceNotFound) || len(resolved) != 0 {
		t.Fatalf("unknown exact revision = %#v, %v", resolved, err)
	}
}

func TestWorkspaceAcceptedSourceResolutionIgnoresUnrelatedRawState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Workspace, knowl.AcceptedSource)
	}{
		{name: "corpus above global manifest budget", mutate: func(t *testing.T, workspace *Workspace, _ knowl.AcceptedSource) {
			writeCanonicalFixture(t, workspace, "raw/unrelated/oversized/manifest.yaml", make([]byte, app.DefaultCatalogLimits().MaxSnapshotBytes+1))
		}},
		{name: "corrupt unrelated manifest", mutate: func(t *testing.T, workspace *Workspace, _ knowl.AcceptedSource) {
			writeCanonicalFixture(t, workspace, "raw/unrelated/corrupt/manifest.yaml", []byte("not: [yaml"))
		}},
		{name: "duplicate unrelated ref", mutate: func(t *testing.T, workspace *Workspace, source knowl.AcceptedSource) {
			content, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(source.ManifestRef)))
			if err != nil {
				t.Fatal(err)
			}
			writeCanonicalFixture(t, workspace, "raw/unrelated/duplicate/manifest.yaml", content)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, source := acceptedResolverFixture(t)
			test.mutate(t, workspace, source)
			ref := app.SourceRefKey(source)
			resolved, err := workspace.ResolveAcceptedSources(t.Context(), testScope, []string{ref})
			if err != nil || resolved[ref] != source || len(resolved) != 1 {
				t.Fatalf("ResolveAcceptedSources() = %#v, %v; want requested source only", resolved, err)
			}
		})
	}
}

func TestWorkspaceResolvesSameOwnerDocumentsRevisionsAndDigestsExactly(t *testing.T) {
	workspace, first := acceptedResolverFixture(t)
	accepted := []knowl.AcceptedSource{first}
	for _, candidate := range []struct {
		document knowl.DocumentID
		revision string
		content  string
	}{
		{document: testOtherDocumentID, revision: "v1", content: "other document"},
		{document: testResolverDocumentID, revision: "v2", content: "new page revision"},
	} {
		content := []byte(candidate.content)
		digest := sha256.Sum256(content)
		source, err := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{
			Scope: testScope, Source: knowl.SourceRef{Adapter: testResolverGitAdapter, ID: string(testSourceID) + "/" + string(candidate.document)},
			Version:   knowl.SourceVersion{Version: candidate.revision, Digest: hex.EncodeToString(digest[:])},
			MediaType: testMarkdownMediaType, Content: content,
			SourceDocument: knowl.SourceDocument{
				SourceID: testSourceID, DocumentID: candidate.document, Revision: candidate.revision,
				URI: "https://wiki.example.test/" + string(candidate.document) + "?revision=" + candidate.revision,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, source)
	}
	refs := make([]string, len(accepted))
	for index, source := range accepted {
		refs[index] = app.SourceRefKey(source)
	}
	resolved, err := workspace.ResolveAcceptedSources(t.Context(), testScope, refs)
	if err != nil || len(resolved) != len(accepted) {
		t.Fatalf("ResolveAcceptedSources() = %#v, %v", resolved, err)
	}
	for _, source := range accepted {
		if got := resolved[app.SourceRefKey(source)]; got != source {
			t.Fatalf("resolved %q = %#v, want %#v", app.SourceRefKey(source), got, source)
		}
	}
}

func TestWorkspaceResolvesCanonicalAndUniqueLegacyDelimiterRefs(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		document knowl.DocumentID
		revision string
		legacy   string
	}{
		{document: "docs/a@b.md", revision: "v1", legacy: "git:engineering/docs/a@b.md@v1"},
		{document: "docs/page.md", revision: testDelimiterRevision, legacy: "git:engineering/docs/page.md@v1@v2"},
	}
	for _, test := range tests {
		content := []byte(test.legacy)
		digest := sha256.Sum256(content)
		source, acceptErr := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{
			Scope: testScope, Source: knowl.SourceRef{Adapter: testResolverGitAdapter, ID: string(testSourceID) + "/" + string(test.document)},
			Version:   knowl.SourceVersion{Version: test.revision, Digest: hex.EncodeToString(digest[:])},
			MediaType: testMarkdownMediaType, Content: content,
			SourceDocument: knowl.SourceDocument{SourceID: testSourceID, DocumentID: test.document, Revision: test.revision,
				URI: "https://wiki.example.test/" + string(test.document)},
		})
		if acceptErr != nil {
			t.Fatal(acceptErr)
		}
		canonical := app.SourceRefKey(source)
		if canonical == test.legacy {
			t.Fatalf("SourceRefKey() = legacy ambiguous spelling %q", canonical)
		}
		for _, ref := range []string{canonical, test.legacy} {
			resolved, resolveErr := workspace.ResolveAcceptedSources(t.Context(), testScope, []string{ref})
			if resolveErr != nil || resolved[ref] != source || len(resolved) != 1 {
				t.Fatalf("ResolveAcceptedSources(%q) = %#v, %v; want exact source", ref, resolved, resolveErr)
			}
		}
		indexed, indexErr := workspace.operatorSources(t.Context(), testScope)
		if indexErr != nil || indexed[test.legacy] != source {
			t.Fatalf("operatorSources()[%q] = %#v, %v; want legacy-compatible source", test.legacy, indexed[test.legacy], indexErr)
		}
	}
}

func TestWorkspaceRejectsActuallyAmbiguousLegacySourceRef(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []struct {
		document knowl.DocumentID
		revision string
	}{
		{document: "a@b", revision: "c"},
		{document: "a", revision: "b@c"},
	} {
		content := []byte(string(candidate.document) + ":" + candidate.revision)
		digest := sha256.Sum256(content)
		_, err := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{
			Scope: testScope, Source: knowl.SourceRef{Adapter: testResolverGitAdapter, ID: string(testSourceID) + "/" + string(candidate.document)},
			Version:   knowl.SourceVersion{Version: candidate.revision, Digest: hex.EncodeToString(digest[:])},
			MediaType: testMarkdownMediaType, Content: content,
			SourceDocument: knowl.SourceDocument{SourceID: testSourceID, DocumentID: candidate.document, Revision: candidate.revision,
				URI: "https://wiki.example.test/" + string(candidate.document)},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	const legacyCollision = "git:engineering/a@b@c"
	resolved, err := workspace.ResolveAcceptedSources(t.Context(), testScope, []string{legacyCollision})
	if !errors.Is(err, app.ErrSourceRefAmbiguous) || len(resolved) != 0 {
		t.Fatalf("ResolveAcceptedSources() = %#v, %v; want typed ambiguity", resolved, err)
	}
}

func TestWorkspaceAcceptedSourceResolutionRejectsIncompleteDescriptor(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*sourceManifest)
	}{
		{name: "missing media type", mutate: func(manifest *sourceManifest) { manifest.MediaType = "" }},
		{name: "missing provenance", mutate: func(manifest *sourceManifest) { manifest.SourceDocument = knowl.SourceDocument{} }},
		{name: "uppercase digest", mutate: func(manifest *sourceManifest) { manifest.Digest = strings.ToUpper(manifest.Digest) }},
		{name: "wrong document path", mutate: func(manifest *sourceManifest) { manifest.SourceDocument.DocumentID = testOtherDocumentID }},
		{name: "wrong document revision", mutate: func(manifest *sourceManifest) { manifest.SourceDocument.Revision = "v2" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, source := acceptedResolverFixture(t)
			path := filepath.Join(workspace.Root(), filepath.FromSlash(source.ManifestRef))
			manifest, err := readManifest(path)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&manifest)
			content, err := yaml.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			writeCanonicalFixture(t, workspace, source.ManifestRef, content)
			ref := app.SourceRefKey(source)
			resolved, err := workspace.ResolveAcceptedSources(t.Context(), testScope, []string{ref})
			if !errors.Is(err, app.ErrSourceInvalid) || len(resolved) != 0 {
				t.Fatalf("ResolveAcceptedSources() = %#v, %v; want invalid descriptor", resolved, err)
			}
		})
	}
}

func TestWorkspaceAcceptedSourceResolutionFailsClosedForRequestedRef(t *testing.T) {
	tests := []struct {
		name      string
		refs      func(knowl.AcceptedSource) []string
		mutate    func(*testing.T, *Workspace, knowl.AcceptedSource)
		wantError error
	}{
		{name: "duplicate request", refs: func(source knowl.AcceptedSource) []string {
			ref := app.SourceRefKey(source)
			return []string{ref, ref}
		}, wantError: app.ErrSourceInvalid},
		{name: "corrupt requested manifest", mutate: func(t *testing.T, workspace *Workspace, source knowl.AcceptedSource) {
			writeCanonicalFixture(t, workspace, source.ManifestRef, []byte("not: [yaml"))
		}, wantError: app.ErrSourceInvalid},
		{name: "missing requested manifest", mutate: func(t *testing.T, workspace *Workspace, source knowl.AcceptedSource) {
			if err := os.Remove(filepath.Join(workspace.Root(), filepath.FromSlash(source.ManifestRef))); err != nil {
				t.Fatal(err)
			}
		}, wantError: app.ErrSourceNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, source := acceptedResolverFixture(t)
			if test.mutate != nil {
				test.mutate(t, workspace, source)
			}
			refs := []string{app.SourceRefKey(source)}
			if test.refs != nil {
				refs = test.refs(source)
			}
			if resolved, err := workspace.ResolveAcceptedSources(t.Context(), testScope, refs); !errors.Is(err, test.wantError) || len(resolved) != 0 {
				t.Fatalf("ResolveAcceptedSources() = %#v, %v; want fail closed", resolved, err)
			}
		})
	}
}

func TestWorkspaceAcceptedSourceResolutionHonorsCancellation(t *testing.T) {
	workspace, source := acceptedResolverFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	resolved, err := workspace.ResolveAcceptedSources(ctx, testScope, []string{app.SourceRefKey(source)})
	if !errors.Is(err, context.Canceled) || len(resolved) != 0 {
		t.Fatalf("ResolveAcceptedSources() = %#v, %v; want canceled", resolved, err)
	}
}

func TestWorkspaceAcceptedSourceResolutionBoundsAggregateCandidateProbes(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	refs := make([]string, 0, app.MaxAcceptedSourceResolveRefs)
	for index := 0; index < app.MaxAcceptedSourceResolveRefs; index++ {
		document := knowl.DocumentID(fmt.Sprintf("probe-%03d@active.md", index))
		content := []byte(document)
		digest := sha256.Sum256(content)
		source, acceptErr := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{
			Scope: testScope, Source: knowl.SourceRef{Adapter: testResolverGitAdapter, ID: string(testSourceID) + "/" + string(document)},
			Version:   knowl.SourceVersion{Version: "v1", Digest: hex.EncodeToString(digest[:])},
			MediaType: testMarkdownMediaType, Content: content,
			SourceDocument: knowl.SourceDocument{SourceID: testSourceID, DocumentID: document, Revision: "v1",
				URI: "https://wiki.example.test/" + string(document)},
		})
		if acceptErr != nil {
			t.Fatal(acceptErr)
		}
		refs = append(refs, source.Source.Adapter+":"+source.Source.ID+"@"+source.Version.Version)
	}
	resolved, err := workspace.ResolveAcceptedSources(t.Context(), testScope, refs)
	if err != nil || len(resolved) != len(refs) {
		t.Fatalf("exact aggregate probe cap = %d, %v; want %d", len(resolved), err, len(refs))
	}

	over := slices.Clone(refs)
	over[0] += "@overflow"
	resolved, err = workspace.ResolveAcceptedSources(t.Context(), testScope, over)
	if !errors.Is(err, app.ErrSourceRefLimit) || len(resolved) != 0 {
		t.Fatalf("aggregate probe cap +1 = %d, %v; want typed preflight limit", len(resolved), err)
	}
}

func TestWorkspaceAcceptedSourceResolutionCountsUniqueAliasPaths(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	refs := make([]string, 0, 32)
	canonical := make([]string, 0, 15)
	for index := 0; index < 15; index++ {
		document := knowl.DocumentID(fmt.Sprintf("alias-%02d%s.md", index, strings.Repeat("@part", 15)))
		content := []byte(document)
		digest := sha256.Sum256(content)
		source, acceptErr := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{
			Scope: testScope, Source: knowl.SourceRef{Adapter: testResolverGitAdapter, ID: string(testSourceID) + "/" + string(document)},
			Version:   knowl.SourceVersion{Version: "v1", Digest: hex.EncodeToString(digest[:])},
			MediaType: testMarkdownMediaType, Content: content,
			SourceDocument: knowl.SourceDocument{SourceID: testSourceID, DocumentID: document, Revision: "v1",
				URI: "https://wiki.example.test/" + string(document)},
		})
		if acceptErr != nil {
			t.Fatal(acceptErr)
		}
		aliases := app.SourceRefAliases(source)
		if len(aliases) != 2 {
			t.Fatalf("SourceRefAliases() = %#v; want canonical and legacy", aliases)
		}
		canonical = append(canonical, aliases[0])
		refs = append(refs, aliases...)
	}
	for index := 0; index < 2; index++ {
		document := knowl.DocumentID(fmt.Sprintf("simple-%02d.md", index))
		content := []byte(document)
		digest := sha256.Sum256(content)
		source, acceptErr := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{
			Scope: testScope, Source: knowl.SourceRef{Adapter: testResolverGitAdapter, ID: string(testSourceID) + "/" + string(document)},
			Version:   knowl.SourceVersion{Version: "v1", Digest: hex.EncodeToString(digest[:])},
			MediaType: testMarkdownMediaType, Content: content,
			SourceDocument: knowl.SourceDocument{SourceID: testSourceID, DocumentID: document, Revision: "v1",
				URI: "https://wiki.example.test/" + string(document)},
		})
		if acceptErr != nil {
			t.Fatal(acceptErr)
		}
		refs = append(refs, app.SourceRefKey(source))
	}

	forward, forwardErr := workspace.ResolveAcceptedSources(t.Context(), testScope, refs)
	reversedRefs := slices.Clone(refs)
	slices.Reverse(reversedRefs)
	reversed, reversedErr := workspace.ResolveAcceptedSources(t.Context(), testScope, reversedRefs)
	if !errors.Is(forwardErr, app.ErrSourceRefDuplicate) || !errors.Is(reversedErr, app.ErrSourceRefDuplicate) {
		t.Fatalf("242 unique paths = %v / %v; want stable duplicate evidence, not candidate-entry limit", forwardErr, reversedErr)
	}
	if len(forward) != 0 || len(reversed) != 0 {
		t.Fatalf("duplicate aliases exposed partial evidence: forward=%#v reverse=%#v", forward, reversed)
	}
	slices.Sort(canonical)
	for _, resolveErr := range []error{forwardErr, reversedErr} {
		var duplicate *app.SourceRefDuplicateError
		if !errors.As(resolveErr, &duplicate) || duplicate.Ref != canonical[0] {
			t.Fatalf("duplicate target = %#v; want %q", duplicate, canonical[0])
		}
	}
}

func TestWorkspaceAcceptedSourceResolutionRejectsLegacyAmplificationBeforeLock(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prefix := "git:x"
	pathological := prefix + strings.Repeat("@", (8<<10)-len(prefix)-1) + "x"
	resolved, err := workspace.ResolveAcceptedSources(t.Context(), testScope, []string{pathological})
	if !errors.Is(err, app.ErrSourceRefLimit) || len(resolved) != 0 {
		t.Fatalf("ResolveAcceptedSources() = %#v, %v; want limit before uninitialized workspace lock", resolved, err)
	}
}

func TestWorkspaceAcceptedSourceResolutionUsesOneAggregateManifestBudget(t *testing.T) {
	workspace, refs, manifestPaths := acceptedManifestBudgetFixture(t)
	resolved, err := workspace.ResolveAcceptedSources(t.Context(), testScope, refs)
	if err != nil || len(resolved) != len(refs) {
		t.Fatalf("exact aggregate budget = %d sources, %v; want %d", len(resolved), err, len(refs))
	}
	lastPath := filepath.Join(workspace.Root(), filepath.FromSlash(manifestPaths[len(manifestPaths)-1]))
	last, err := os.ReadFile(lastPath)
	if err != nil {
		t.Fatal(err)
	}
	writeCanonicalFixture(t, workspace, manifestPaths[len(manifestPaths)-1], append(last, 'x'))
	resolved, err = workspace.ResolveAcceptedSources(t.Context(), testScope, refs)
	if !errors.Is(err, app.ErrOperatorReadLimitExceeded) || len(resolved) >= len(refs) {
		t.Fatalf("aggregate budget +1 = %d sources, %v; want bounded failure", len(resolved), err)
	}
}

func TestWorkspaceAcceptedSourceResolutionChargesAliasedManifestOnce(t *testing.T) {
	workspace, refs, _ := acceptedManifestBudgetFixture(t)
	canonical, err := app.EncodeSourceRef(app.SourceRefIdentity{
		Source:   knowl.SourceRef{Adapter: testResolverGitAdapter, ID: string(testSourceID) + "/budget-064.md"},
		Revision: testDelimiterRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	refs = append(refs, canonical)
	forward, forwardErr := workspace.ResolveAcceptedSources(t.Context(), testScope, refs)
	reversedRefs := slices.Clone(refs)
	slices.Reverse(reversedRefs)
	reversed, reversedErr := workspace.ResolveAcceptedSources(t.Context(), testScope, reversedRefs)
	if !errors.Is(forwardErr, app.ErrSourceRefDuplicate) || !errors.Is(reversedErr, app.ErrSourceRefDuplicate) {
		t.Fatalf("exact 4 MiB with aliased manifest = %v / %v; want duplicate evidence after one manifest charge", forwardErr, reversedErr)
	}
	if len(forward) != 0 || len(reversed) != 0 {
		t.Fatalf("aliased exact-budget exposed partial evidence: %d / %d", len(forward), len(reversed))
	}
	for _, resolveErr := range []error{forwardErr, reversedErr} {
		var duplicate *app.SourceRefDuplicateError
		if !errors.As(resolveErr, &duplicate) || duplicate.Ref != canonical {
			t.Fatalf("exact-budget duplicate target = %#v; want %q", duplicate, canonical)
		}
	}
}

func TestWorkspaceAcceptedSourceResolutionCancelsBeforeZeroBudgetMissingProbe(t *testing.T) {
	workspace, refs, _ := acceptedManifestBudgetFixture(t)
	canonical, err := app.EncodeSourceRef(app.SourceRefIdentity{
		Source:   knowl.SourceRef{Adapter: testResolverGitAdapter, ID: string(testSourceID) + "/budget-064.md"},
		Revision: testDelimiterRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	refs = append(refs, canonical)
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The last legacy ref has two candidates and the canonical alias reuses its
	// accepted candidate path. Cancel immediately before the final unique,
	// missing candidate is probed after accepted manifests consume the exact
	// aggregate byte budget.
	cancelAt := 1 + len(refs) + (len(refs) + 1) + 2*(len(refs)-1) + 1
	ctx := &cancelOnErrorCheckContext{Context: base, cancel: cancel, cancelAt: cancelAt}
	resolved, err := workspace.ResolveAcceptedSources(ctx, testScope, refs)
	if !errors.Is(err, context.Canceled) || len(resolved) != 0 {
		t.Fatalf("ResolveAcceptedSources() = %d, %v; want cancellation before missing zero-budget probe", len(resolved), err)
	}
}

type cancelOnErrorCheckContext struct {
	context.Context
	cancel   context.CancelFunc
	cancelAt int
	checks   int
}

func (ctx *cancelOnErrorCheckContext) Err() error {
	ctx.checks++
	if ctx.checks == ctx.cancelAt {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func acceptedManifestBudgetFixture(t *testing.T) (*Workspace, []string, []string) {
	t.Helper()
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	refs := make([]string, 0, 65)
	manifestPaths := make([]string, 0, 65)
	for index := 0; index < 65; index++ {
		document := knowl.DocumentID(fmt.Sprintf("budget-%03d.md", index))
		revision := "v1"
		if index == 64 {
			revision = testDelimiterRevision
		}
		content := []byte(document)
		digest := sha256.Sum256(content)
		source, acceptErr := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{
			Scope: testScope, Source: knowl.SourceRef{Adapter: testResolverGitAdapter, ID: string(testSourceID) + "/" + string(document)},
			Version:   knowl.SourceVersion{Version: revision, Digest: hex.EncodeToString(digest[:])},
			MediaType: testMarkdownMediaType, Content: content,
			SourceDocument: knowl.SourceDocument{SourceID: testSourceID, DocumentID: document, Revision: revision,
				URI: "https://wiki.example.test/" + string(document)},
		})
		if acceptErr != nil {
			t.Fatal(acceptErr)
		}
		ref := app.SourceRefKey(source)
		if index == 64 {
			ref = source.Source.Adapter + ":" + source.Source.ID + "@" + source.Version.Version
		}
		refs = append(refs, ref)
		manifestPaths = append(manifestPaths, source.ManifestRef)
	}
	target := app.MaxAcceptedSourceResolveBytes
	remaining := target
	for _, manifestRef := range manifestPaths {
		path := filepath.Join(workspace.Root(), filepath.FromSlash(manifestRef))
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		remaining -= len(content)
	}
	if remaining <= 0 {
		t.Fatalf("fixture manifests already exceed target by %d", -remaining)
	}
	for _, manifestRef := range manifestPaths {
		if remaining == 0 {
			break
		}
		path := filepath.Join(workspace.Root(), filepath.FromSlash(manifestRef))
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		padding := min(remaining, maxStageManifestBytes-len(content))
		if padding == 1 {
			continue
		}
		if padding > 1 {
			content = append(content, '\n', '#')
			content = append(content, strings.Repeat("x", padding-2)...)
			writeCanonicalFixture(t, workspace, manifestRef, content)
			remaining -= padding
		}
	}
	if remaining != 0 {
		t.Fatalf("could not create exact aggregate fixture: %d bytes remain", remaining)
	}
	return workspace, refs, manifestPaths
}

func acceptedResolverFixture(t *testing.T) (*Workspace, knowl.AcceptedSource) {
	t.Helper()
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	content := []byte("accepted evidence")
	digest := sha256.Sum256(content)
	source, err := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{
		Scope: testScope, Source: knowl.SourceRef{Adapter: testResolverGitAdapter, ID: "engineering/resolver-page.md"},
		Version:   knowl.SourceVersion{Version: "v1", Digest: hex.EncodeToString(digest[:])},
		MediaType: testMarkdownMediaType, Content: content,
		SourceDocument: knowl.SourceDocument{
			SourceID: testSourceID, DocumentID: testResolverDocumentID, Revision: "v1", URI: "https://wiki.example.test/resolver-page.md",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return workspace, source
}

func TestWorkspaceEnrichesLegacySourceProvenance(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	content := []byte("# Legacy architecture\n")
	digestValue := sha256.Sum256(content)
	envelope := knowl.SourceEnvelope{
		Scope: testScope, Source: knowl.SourceRef{Adapter: testWikiSourceAdapter, ID: "engineering/architecture.md"},
		Version:   knowl.SourceVersion{Version: testSourceRevision, Digest: hex.EncodeToString(digestValue[:])},
		MediaType: testMarkdownMediaType, Content: content,
	}
	legacy, err := workspace.AcceptSource(context.Background(), envelope)
	if err != nil || legacy.SourceDocument != (knowl.SourceDocument{}) {
		t.Fatalf("legacy source = %#v, %v", legacy, err)
	}
	document := knowl.SourceDocument{
		SourceID: testSourceID, DocumentID: "architecture.md", Revision: envelope.Version.Version,
		URI: "https://wiki.example.test/architecture.md",
	}
	envelope.SourceDocument = document
	enriched, err := workspace.AcceptSource(context.Background(), envelope)
	if err != nil || enriched.SourceDocument != document || enriched.ManifestRef != legacy.ManifestRef {
		t.Fatalf("enriched source = %#v, %v", enriched, err)
	}
	read, err := workspace.ReadSource(context.Background(), enriched, knowl.ReadLimits{})
	if err != nil || !slices.Equal(read, content) {
		t.Fatalf("enriched source content = %q, %v", read, err)
	}
	inspection, err := workspace.Inspect(context.Background(), testScope)
	if err != nil || len(inspection.RawSources) != 1 || inspection.RawSources[0].Source.SourceDocument != document {
		t.Fatalf("enriched raw inspection = %#v, %v", inspection.RawSources, err)
	}
}

func TestWorkspaceRejectsInvalidConfiguredSourceProvenanceBeforeWrite(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	content := []byte("source")
	digestValue := sha256.Sum256(content)
	_, err = workspace.AcceptSource(context.Background(), knowl.SourceEnvelope{
		Scope: testScope, Source: knowl.SourceRef{Adapter: testWikiSourceAdapter, ID: "engineering/page.md"},
		Version: knowl.SourceVersion{Version: testSourceRevision, Digest: hex.EncodeToString(digestValue[:])},
		SourceDocument: knowl.SourceDocument{
			SourceID: "engineering", DocumentID: "page.md", Revision: "other-revision", URI: "https://wiki.example.test/page.md",
		},
		Content: content,
	})
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("AcceptSource() error = %v, want invalid source", err)
	}
	inspection, err := workspace.Inspect(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.RawSources) != 0 {
		t.Fatalf("invalid provenance wrote raw sources: %#v", inspection.RawSources)
	}
}

func TestWorkspaceRejectsUnencodableSourceRefBeforeWrite(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	content := []byte("bounded")
	digest := sha256.Sum256(content)
	_, err = workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{
		Scope: testScope, Source: knowl.SourceRef{Adapter: testResolverGitAdapter, ID: strings.Repeat("a", (8<<10)-1) + "@"},
		Version:   knowl.SourceVersion{Version: "v1", Digest: hex.EncodeToString(digest[:])},
		MediaType: testMarkdownMediaType, Content: content,
	})
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("AcceptSource() error = %v, want invalid source ref", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(workspace.Root(), workspaceRawDir))
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("invalid source wrote raw entries %#v, %v", entries, readErr)
	}
}

func TestWorkspaceRejectsUnsafePlanPath(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	_, err = workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID: "op-1",
		Edits:       []knowl.FileEdit{{Path: "wiki/../schema.md", Content: []byte("no")}},
	})
	if !errors.Is(err, ErrPathRejected) {
		t.Fatalf("unsafe plan error = %v, want path rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(workspace.Root(), "schema.md")); statErr != nil {
		t.Fatalf("schema disappeared after rejected plan: %v", statErr)
	}
}

func TestWorkspaceSnapshotIncludesMarkdownDigests(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	pagePath := filepath.Join(workspace.Root(), "wiki", "entities", "one.md")
	if err := os.WriteFile(pagePath, validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, ""), 0o600); err != nil {
		t.Fatalf("write page: %v", err)
	}
	snapshot, err := workspace.Snapshot(context.Background(), testScope)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.PageDigests[testPageOnePath] == "" {
		t.Fatalf("snapshot missing page digest: %#v", snapshot.PageDigests)
	}
	if len(snapshot.Pages) != 1 || snapshot.Pages[0].ID != testPageOneID {
		t.Fatalf("snapshot pages = %#v", snapshot.Pages)
	}
	if snapshot.Pages[0].OKF == nil || snapshot.Pages[0].OKF.Type != "entity" || snapshot.Pages[0].Body != "# One\n" {
		t.Fatalf("curated OKF snapshot = %#v", snapshot.Pages[0])
	}
}

func TestWorkspaceReadsAndSnapshotsSourceDocumentProvenance(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	relative := "wiki/sources/engineering/auth.md"
	content := []byte("---\nid: sources/engineering/auth\ntitle: Auth\ntype: source\nsource_refs:\n  - raw:auth@1\nsource_document:\n  source_id: engineering\n  document_id: architecture/auth.md\n  revision: revision-1\n  uri: https://wiki.example.test/auth\n---\n# Auth\n")
	target := filepath.Join(workspace.Root(), filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	pages, err := workspace.ReadPages(context.Background(), testScope, []knowl.PageID{"sources/engineering/auth"}, knowl.ReadLimits{Pages: 1, Bytes: len(content)})
	if err != nil || len(pages) != 1 || pages[0].SourceDocument == nil || pages[0].SourceDocument.DocumentID != "architecture/auth.md" {
		t.Fatalf("ReadPages() = %#v, %v", pages, err)
	}
	snapshot, err := workspace.Snapshot(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pages) != 1 || snapshot.Pages[0].SourceDocument == nil || snapshot.Pages[0].SourceDocument.SourceID != testSourceID {
		t.Fatalf("Snapshot() pages = %#v", snapshot.Pages)
	}
}

func TestWorkspaceRecoveryRollsBackPreparedGenerationAndCommitReplays(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	acceptWorkspaceSource(t, workspace)
	writeRootCatalogTargets(t, workspace, "entities/recovered.md")
	plan := knowl.ValidatedEditPlan{
		OperationID:  "recovery-operation",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits:        []knowl.FileEdit{{Path: "wiki/entities/recovered.md", Content: validWorkspacePage("entities/recovered", "Recovered", testWorkspaceSourceRef, "")}},
		Diagnostics:  []knowl.MaintenanceDiagnostic{{Code: knowl.DiagnosticCitationUnknownSource, Path: testRejectedPagePath}},
	}
	staged, err := workspace.StagePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("stage plan: %v", err)
	}
	if !slices.Equal(staged.Diagnostics, plan.Diagnostics) {
		t.Fatalf("staged diagnostics = %#v, want %#v", staged.Diagnostics, plan.Diagnostics)
	}
	logPath := filepath.Join(workspace.Root(), "wiki", "log.md")
	originalLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read original log: %v", err)
	}
	recoveryDir := filepath.Join(workspace.Root(), knowlDir, "recovery", token(plan.OperationID))
	if err := os.MkdirAll(recoveryDir, 0o700); err != nil {
		t.Fatalf("create recovery fixture: %v", err)
	}
	logBackup := filepath.Join(recoveryDir, "log.old")
	if err := writeAtomic(logBackup, originalLog, 0o600); err != nil {
		t.Fatalf("write log preimage: %v", err)
	}
	pagePath := filepath.Join(workspace.Root(), "wiki", "entities", "recovered.md")
	if err := os.WriteFile(pagePath, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write partial page: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("partial log"), 0o600); err != nil {
		t.Fatalf("write partial log: %v", err)
	}
	journaling := recoveryJournal{
		OperationID: plan.OperationID,
		State:       recoveryPrepared,
		Diagnostics: append([]knowl.MaintenanceDiagnostic(nil), plan.Diagnostics...),
		Entries: []recoveryEntry{
			{Target: "wiki/entities/recovered.md", HadOld: false},
			{Target: canonicalLogPath, Backup: logBackup, HadOld: true},
		},
	}
	journalPath := filepath.Join(workspace.Root(), knowlDir, "recovery", token(plan.OperationID)+".yaml")
	if err := writeJournal(journalPath, journaling); err != nil {
		t.Fatalf("write recovery journal: %v", err)
	}
	results, err := workspace.Recover(context.Background())
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(results) != 1 || results[0].Action != recoveryRolledBack {
		t.Fatalf("recovery results = %#v", results)
	}
	if _, err := os.Stat(pagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered page stat = %v, want absent", err)
	}
	restoredLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read restored log: %v", err)
	}
	if string(restoredLog) != string(originalLog) {
		t.Fatalf("restored log = %q, want %q", restoredLog, originalLog)
	}
	firstCommit, err := workspace.Commit(context.Background(), staged)
	if err != nil {
		t.Fatalf("commit after recovery: %v", err)
	}
	secondCommit, err := workspace.Commit(context.Background(), staged)
	if err != nil {
		t.Fatalf("replay commit: %v", err)
	}
	if firstCommit.Generation != secondCommit.Generation || len(secondCommit.Files) != 2 || !slices.Equal(firstCommit.Diagnostics, plan.Diagnostics) || !slices.Equal(secondCommit.Diagnostics, plan.Diagnostics) {
		t.Fatalf("replayed commits differ: %#v != %#v", firstCommit, secondCommit)
	}
}

func TestWorkspaceStagePlanRejectsInvalidProspectiveContentWithoutCanonicalMutation(t *testing.T) {
	tests := []struct {
		name  string
		edits []knowl.FileEdit
	}{
		{name: "missing frontmatter", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("# One\n")}}},
		{name: "malformed frontmatter", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("---\nid: [\n---\n# One\n")}}},
		{name: "missing id", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("---\ntitle: One\ntype: entity\nsource_refs:\n  - " + testWorkspaceSourceRef + "\n---\n# One\n")}}},
		{name: "missing title", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("---\nid: entities/one\ntype: entity\nsource_refs:\n  - " + testWorkspaceSourceRef + "\n---\n# One\n")}}},
		{name: "missing type", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("---\nid: entities/one\ntitle: One\nsource_refs:\n  - " + testWorkspaceSourceRef + "\n---\n# One\n")}}},
		{name: "missing source refs", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("---\nid: entities/one\ntitle: One\ntype: entity\n---\n# One\n")}}},
		{name: "id mismatch", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage("entities/two", "One", testWorkspaceSourceRef, "")}}},
		{name: "unknown source ref", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage(testPageOneID, "One", testMissingSourceRef, "")}}},
		{name: "malformed link", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, "[[broken")}}},
		{name: "escaping link", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, "[[../escape]]")}}},
		{name: "missing link target", edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, "[[entities/missing]]")}}},
		{name: "broken index target", edits: []knowl.FileEdit{{Path: testIndexPath, Content: []byte(rootIndexContent + "\n* [Missing](entities/missing.md)\n")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace, err := New(t.TempDir())
			if err != nil {
				t.Fatalf("new workspace: %v", err)
			}
			if err := workspace.Init(); err != nil {
				t.Fatalf("init workspace: %v", err)
			}
			acceptWorkspaceSource(t, workspace)
			before := captureCanonicalState(t, workspace, testPageOnePath)
			schema, err := workspace.Schema(context.Background(), testScope)
			if err != nil {
				t.Fatalf("read schema: %v", err)
			}
			edits := append([]knowl.FileEdit(nil), tt.edits...)
			for index := range edits {
				if edits[index].Path != testIndexPath || edits[index].ExpectedDigest != "" {
					continue
				}
				indexContent, readErr := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "index.md"))
				if readErr != nil {
					t.Fatalf("read index: %v", readErr)
				}
				edits[index].ExpectedDigest = digestBytes(indexContent)
			}
			_, err = workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
				OperationID:  "invalid-" + token(tt.name),
				Scope:        testScope,
				SchemaDigest: schema.Digest,
				SourceRefs:   []string{testWorkspaceSourceRef},
				Edits:        edits,
			})
			if !errors.Is(err, ErrContentInvalid) {
				t.Fatalf("StagePlan() error = %v, want content invalid", err)
			}
			assertCanonicalState(t, workspace, before)
		})
	}
}

func TestWorkspaceStagePlanPreservesUnresolvedOriginalLinks(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	boundaryTarget := "entities/" + strings.Repeat("x", 2048-len("entities/")-len(".md"))
	sourceContent := []byte("[[entities/z-missing]] [[entities/a-missing]] [[" + boundaryTarget + "]]\n")
	accepted, err := workspace.AcceptSource(context.Background(), knowl.SourceEnvelope{
		Scope: testScope, Source: knowl.SourceRef{Adapter: testFixtureAdapter, ID: "original-links"},
		Version: knowl.SourceVersion{Version: "1", Digest: digestBytes(sourceContent)}, MediaType: testMarkdownMediaType, Content: sourceContent,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceRef := sourceRefKey(accepted)
	writeRootCatalogTargets(t, workspace, "entities/one.md")
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	body := "[Z](z-missing.md) [A](a-missing.md) [A](a-missing.md) [Boundary](" + strings.TrimPrefix(boundaryTarget, "entities/") + ".md)"
	plan := knowl.ValidatedEditPlan{
		OperationID: "original-links", Scope: testScope, SchemaDigest: schema.Digest,
		RequiredSourceRef: sourceRef, SourceRefs: []string{sourceRef},
		Edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage(testPageOneID, "One", sourceRef, body)}},
	}
	want := []knowl.MaintenanceDiagnostic{
		{Code: knowl.DiagnosticOriginalLinkUnresolved, Path: testPageOnePath, Target: "entities/a-missing"},
		{Code: knowl.DiagnosticOriginalLinkUnresolved, Path: testPageOnePath, Target: boundaryTarget},
		{Code: knowl.DiagnosticOriginalLinkUnresolved, Path: testPageOnePath, Target: "entities/z-missing"},
	}
	staged, err := workspace.StagePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("stage original links: %v", err)
	}
	if !slices.Equal(staged.Diagnostics, want) {
		t.Fatalf("staged diagnostics = %#v, want %#v", staged.Diagnostics, want)
	}
	replayed, err := workspace.StagePlan(context.Background(), plan)
	if err != nil || !slices.Equal(replayed.Diagnostics, want) || replayed.Digest != staged.Digest {
		t.Fatalf("replayed stage = %#v, error = %v", replayed, err)
	}
	commit, err := workspace.Commit(context.Background(), staged)
	if err != nil || !slices.Equal(commit.Diagnostics, want) {
		t.Fatalf("commit diagnostics = %#v, error = %v", commit.Diagnostics, err)
	}
}

func TestWorkspaceStagePlanIsolatesInvalidDocument(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	acceptWorkspaceSource(t, workspace)
	writeRootCatalogTargets(t, workspace, "entities/good.md", "entities/rejected.md")
	goodBefore := validWorkspacePage("entities/good", "Good", testWorkspaceSourceRef, "old good")
	rejectedBefore := validWorkspacePage("entities/rejected", "Rejected", testWorkspaceSourceRef, "preserved canonical content")
	for target, content := range map[string][]byte{
		testGoodPagePath: goodBefore, testRejectedPagePath: rejectedBefore,
	} {
		if err := os.WriteFile(filepath.Join(workspace.Root(), filepath.FromSlash(target)), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	plan := knowl.ValidatedEditPlan{
		OperationID: "mixed-documents", Scope: testScope, SchemaDigest: schema.Digest,
		RequiredSourceRef: testWorkspaceSourceRef, SourceRefs: []string{testWorkspaceSourceRef, testMissingSourceRef},
		Edits: []knowl.FileEdit{
			{Path: testRejectedPagePath, ExpectedDigest: digestBytes(rejectedBefore), Content: validWorkspacePage("entities/rejected", "Rejected", testMissingSourceRef, "must not publish")},
			{Path: testGoodPagePath, ExpectedDigest: digestBytes(goodBefore), Content: validWorkspacePage("entities/good", "Good", testWorkspaceSourceRef, "new good")},
		},
	}
	staged, err := workspace.StagePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("stage mixed plan: %v", err)
	}
	if want := []string{testGoodPagePath}; !slices.Equal(staged.Files, want) {
		t.Fatalf("staged files = %v, want %v", staged.Files, want)
	}
	wantDiagnostics := []knowl.MaintenanceDiagnostic{{Code: knowl.DiagnosticCitationUnknownSource, Path: testRejectedPagePath}}
	if !slices.Equal(staged.Diagnostics, wantDiagnostics) {
		t.Fatalf("staged diagnostics = %#v, want %#v", staged.Diagnostics, wantDiagnostics)
	}
	replayed, err := workspace.StagePlan(context.Background(), plan)
	if err != nil || replayed.Digest != staged.Digest || !slices.Equal(replayed.Files, staged.Files) || !slices.Equal(replayed.Diagnostics, staged.Diagnostics) {
		t.Fatalf("replayed mixed stage = %#v, error = %v", replayed, err)
	}
	commit, err := workspace.Commit(context.Background(), staged)
	if err != nil || !slices.Equal(commit.Diagnostics, wantDiagnostics) {
		t.Fatalf("commit = %#v, error = %v", commit, err)
	}
	goodAfter, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "entities", "good.md"))
	if err != nil || !strings.Contains(string(goodAfter), "new good") {
		t.Fatalf("good content = %q, error = %v", goodAfter, err)
	}
	rejectedAfter, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "entities", "rejected.md"))
	if err != nil || !slices.Equal(rejectedAfter, rejectedBefore) {
		t.Fatalf("rejected content = %q, error = %v", rejectedAfter, err)
	}
}

func TestWorkspaceStagePlanReportsDiagnosticsWhenEveryEditIsRejected(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	acceptWorkspaceSource(t, workspace)
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	plan := knowl.ValidatedEditPlan{
		OperationID: "rejected-plan", Scope: testScope, SchemaDigest: schema.Digest,
		RequiredSourceRef: testWorkspaceSourceRef, SourceRefs: []string{testWorkspaceSourceRef, testMissingSourceRef},
		Edits: []knowl.FileEdit{
			{Path: testRejectedPagePath, Content: validWorkspacePage("entities/rejected", "Rejected", testMissingSourceRef, "must not publish")},
			{Path: "wiki/rejected/index.md", Content: []byte("# Rejected\n\n* [Rejected](../entities/rejected.md)\n")},
		},
	}
	want := []knowl.MaintenanceDiagnostic{
		{Code: knowl.DiagnosticCitationUnknownSource, Path: testRejectedPagePath},
		{Code: knowl.DiagnosticCatalogDependencyReject, Path: "wiki/rejected/index.md", Target: "entities/rejected.md"},
	}

	staged, err := workspace.StagePlan(context.Background(), plan)
	if !errors.Is(err, ErrContentInvalid) {
		t.Fatalf("StagePlan() error = %v, want content invalid", err)
	}
	if !slices.Equal(staged.Diagnostics, want) {
		t.Fatalf("StagePlan() diagnostics = %#v, want %#v", staged.Diagnostics, want)
	}
	if staged.OperationID != plan.OperationID || len(staged.Files) != 0 || staged.Digest != "" {
		t.Fatalf("StagePlan() rejected result = %#v", staged)
	}
	if _, err := workspace.LoadStage(context.Background(), testScope, knowl.OperationID(plan.OperationID)); !errors.Is(err, app.ErrStageNotFound) {
		t.Fatalf("LoadStage() error = %v, want stage not found", err)
	}
}

func TestWorkspaceStagePlanWithholdsRejectedCatalogClosure(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	acceptWorkspaceSource(t, workspace)
	writeRootCatalogTargets(t, workspace, "entities/good.md")
	goodBefore := validWorkspacePage("entities/good", "Good", testWorkspaceSourceRef, "old")
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "entities", "good.md"), goodBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	rootBefore, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "index.md"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	plan := knowl.ValidatedEditPlan{
		OperationID: "catalog-closure", Scope: testScope, SchemaDigest: schema.Digest,
		RequiredSourceRef: testWorkspaceSourceRef, SourceRefs: []string{testWorkspaceSourceRef, testMissingSourceRef},
		Edits: []knowl.FileEdit{
			{Path: testBadPagePath, Content: validWorkspacePage("entities/bad", "Bad", testMissingSourceRef, "rejected")},
			{Path: testGoodPagePath, ExpectedDigest: digestBytes(goodBefore), Content: validWorkspacePage("entities/good", "Good", testWorkspaceSourceRef, "updated")},
			{Path: testIndexPath, ExpectedDigest: digestBytes(rootBefore), Content: []byte(rootIndexContent + "\n* [Good](entities/good.md)\n* [Safe](safe/index.md)\n* [Parent](parent/index.md)\n")},
			{Path: testParentCatalogPath, Content: []byte("# Parent\n\n* [Unsafe](../unsafe/index.md)\n")},
			{Path: "wiki/safe/index.md", Content: []byte("# Safe\n\n* [Good](../entities/good.md)\n")},
			{Path: testUnsafeCatalogPath, Content: []byte("# Unsafe\n\n* [Bad](../entities/bad.md)\n")},
		},
	}
	staged, err := workspace.StagePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("stage catalog closure: %v", err)
	}
	wantFiles := []string{testGoodPagePath, "wiki/safe/index.md"}
	if !slices.Equal(staged.Files, wantFiles) {
		t.Fatalf("staged files = %v, want %v", staged.Files, wantFiles)
	}
	wantDiagnostics := []knowl.MaintenanceDiagnostic{
		{Code: knowl.DiagnosticCitationUnknownSource, Path: testBadPagePath},
		{Code: knowl.DiagnosticCatalogDependencyReject, Path: testIndexPath, Target: "parent/index.md"},
		{Code: knowl.DiagnosticCatalogDependencyReject, Path: testParentCatalogPath, Target: "unsafe/index.md"},
		{Code: knowl.DiagnosticCatalogDependencyReject, Path: testUnsafeCatalogPath, Target: "entities/bad.md"},
	}
	if !slices.Equal(staged.Diagnostics, wantDiagnostics) {
		t.Fatalf("staged diagnostics = %#v, want %#v", staged.Diagnostics, wantDiagnostics)
	}
	commit, err := workspace.Commit(context.Background(), staged)
	if err != nil || !slices.Equal(commit.Diagnostics, wantDiagnostics) {
		t.Fatalf("commit catalog closure = %#v, error = %v", commit, err)
	}
	rootAfter, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "index.md"))
	if err != nil || !slices.Equal(rootAfter, rootBefore) {
		t.Fatalf("root catalog changed = %q, error = %v", rootAfter, err)
	}
	for _, withheld := range []string{testBadPagePath, testParentCatalogPath, testUnsafeCatalogPath} {
		if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(withheld))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("withheld path %q stat = %v", withheld, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki", "safe", "index.md")); err != nil {
		t.Fatalf("safe sibling missing: %v", err)
	}
}

func TestWorkspaceStagePlanAllowsExistingAndSamePlanTargets(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	acceptWorkspaceSource(t, workspace)
	writeRootCatalogTargets(t, workspace, "entities/one.md", "entities/two.md")
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "entities", "two.md"), validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, ""), 0o600); err != nil {
		t.Fatalf("write existing target: %v", err)
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "existing-target",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits:        []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, "[Two](two.md)")}},
	}); err != nil {
		t.Fatalf("stage existing target: %v", err)
	}
	if _, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "same-plan-target",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits: []knowl.FileEdit{
			{Path: testPageOnePath, Content: validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, "[Two](two.md)")},
			{Path: "wiki/entities/two.md", ExpectedDigest: digestBytes(validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, "")), Content: validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, "")},
		},
	}); err != nil {
		t.Fatalf("stage same-plan target: %v", err)
	}
}

func TestWorkspaceStagePlanAllowsIndexTargetsWithoutFrontmatter(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	acceptWorkspaceSource(t, workspace)
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "entities", "two.md"), validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, ""), 0o600); err != nil {
		t.Fatalf("write existing target: %v", err)
	}
	indexPath := filepath.Join(workspace.Root(), "wiki", "index.md")
	indexContent, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "index-existing-target",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits: []knowl.FileEdit{{
			Path:           testIndexPath,
			ExpectedDigest: digestBytes(indexContent),
			Content:        []byte(rootIndexContent + "\n* [Two](entities/two.md)\n"),
		}},
	}); err != nil {
		t.Fatalf("stage index existing target: %v", err)
	}
	if _, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "index-same-plan-target",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits: []knowl.FileEdit{
			{
				Path:           testIndexPath,
				ExpectedDigest: digestBytes(indexContent),
				Content:        []byte(rootIndexContent + "\n* [Two](entities/two.md)\n* [Three](entities/three.md)\n"),
			},
			{
				Path:    "wiki/entities/three.md",
				Content: validWorkspacePage("entities/three", "Three", testWorkspaceSourceRef, ""),
			},
		},
	}); err != nil {
		t.Fatalf("stage index same-plan target: %v", err)
	}
}

func TestWorkspaceStagePlanValidatesProspectiveCatalogGraph(t *testing.T) {
	const (
		entitiesCatalogLink = "* [Entities](entities/index.md)\n"
		entitiesCatalogPath = "entities/index.md"
	)
	tests := []struct {
		name      string
		rootLinks string
		nested    map[string]string
		wantError bool
	}{
		{
			name: "nested hierarchy", rootLinks: entitiesCatalogLink,
			nested: map[string]string{entitiesCatalogPath: "# Entities\n\n* [One](one.md)\n"},
		},
		{
			name: "missing nested target", rootLinks: entitiesCatalogLink,
			nested: map[string]string{entitiesCatalogPath: "# Entities\n\n* [Missing](missing.md)\n"}, wantError: true,
		},
		{
			name: "catalog escape", rootLinks: entitiesCatalogLink,
			nested: map[string]string{entitiesCatalogPath: "# Entities\n\n* [Escape](../../../outside.md)\n"}, wantError: true,
		},
		{
			name: "catalog cycle", rootLinks: entitiesCatalogLink,
			nested: map[string]string{entitiesCatalogPath: "# Entities\n\n* [Root](../index.md)\n* [One](one.md)\n"}, wantError: true,
		},
		{
			name: "unreachable page", rootLinks: "",
			nested: map[string]string{entitiesCatalogPath: "# Entities\n\n* [One](one.md)\n"}, wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := workspace.Init(); err != nil {
				t.Fatal(err)
			}
			acceptWorkspaceSource(t, workspace)
			schema, err := workspace.Schema(context.Background(), testScope)
			if err != nil {
				t.Fatal(err)
			}
			rootPath := filepath.Join(workspace.Root(), "wiki", "index.md")
			rootBefore, err := os.ReadFile(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			edits := []knowl.FileEdit{
				{Path: testPageOnePath, Content: validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, "")},
				{Path: testIndexPath, ExpectedDigest: digestBytes(rootBefore), Content: append(append([]byte(nil), rootBefore...), []byte("\n"+test.rootLinks)...)},
			}
			for relative, content := range test.nested {
				edits = append(edits, knowl.FileEdit{Path: "wiki/" + relative, Content: []byte(content)})
			}
			_, err = workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
				OperationID: "catalog-" + token(test.name), Scope: testScope, SchemaDigest: schema.Digest,
				SourceRefs: []string{testWorkspaceSourceRef}, Edits: edits,
			})
			if test.wantError {
				if !errors.Is(err, ErrContentInvalid) {
					t.Fatalf("StagePlan() error = %v, want content invalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("StagePlan() error = %v", err)
			}
		})
	}
}

func TestWorkspaceStagePlanRequiresExistingOrphansToBeReconciled(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	acceptWorkspaceSource(t, workspace)
	pageOne := validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, "before")
	pageTwo := validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, "orphan")
	for target, content := range map[string][]byte{
		testPageOnePath:        pageOne,
		"wiki/entities/two.md": pageTwo,
	} {
		absolute := filepath.Join(workspace.Root(), filepath.FromSlash(target))
		if err := os.WriteFile(absolute, content, 0o600); err != nil {
			t.Fatalf("write %s: %v", target, err)
		}
	}
	writeRootCatalogTargets(t, workspace, "entities/one.md")
	if _, err := workspace.Snapshot(context.Background(), testScope); err != nil {
		t.Fatalf("read-compatible workspace snapshot: %v", err)
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	pageOneAfter := validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, "after")
	_, err = workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "existing-orphan",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits: []knowl.FileEdit{{
			Path: testPageOnePath, ExpectedDigest: digestBytes(pageOne), Content: pageOneAfter,
		}},
	})
	if !errors.Is(err, ErrContentInvalid) || !strings.Contains(err.Error(), `wiki/entities/two.md`) || !strings.Contains(err.Error(), "catalog.reconciliation_required") {
		t.Fatalf("StagePlan() error = %v, want path-specific reconciliation requirement", err)
	}

	rootPath := filepath.Join(workspace.Root(), "wiki", "index.md")
	rootBefore, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	rootAfter := append(append([]byte(nil), rootBefore...), []byte("\n* [Two](entities/two.md)\n")...)
	if _, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "reconcile-existing-orphan",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits: []knowl.FileEdit{
			{Path: testPageOnePath, ExpectedDigest: digestBytes(pageOne), Content: pageOneAfter},
			{Path: testIndexPath, ExpectedDigest: digestBytes(rootBefore), Content: rootAfter},
		},
	}); err != nil {
		t.Fatalf("StagePlan() with complete reconciliation error = %v", err)
	}
}

func TestWorkspaceInspectionIncludesDeterministicCatalogs(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	for relative, content := range map[string]string{
		"wiki/zeta/index.md":  "# Zeta\n",
		"wiki/alpha/index.md": "# Alpha\n",
	} {
		target := filepath.Join(workspace.Root(), filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inspection, err := workspace.Inspect(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(inspection.Catalogs))
	for _, catalog := range inspection.Catalogs {
		paths = append(paths, catalog.Path)
		if !catalog.Untrusted || catalog.Digest == "" || catalog.Content == "" {
			t.Fatalf("catalog snapshot = %#v", catalog)
		}
	}
	want := []string{testIndexPath, "wiki/alpha/index.md", "wiki/zeta/index.md"}
	if !slices.Equal(paths, want) {
		t.Fatalf("catalog paths = %v, want %v", paths, want)
	}
}

func TestWorkspaceStagePlanPreservesSourceLineage(t *testing.T) {
	tests := []struct {
		name      string
		refs      func(oldRef, currentRef, unrelatedRef string) []string
		planRefs  func(currentRef, unrelatedRef string) []string
		wantError bool
	}{
		{
			name:     "same lineage replacement",
			refs:     func(_, currentRef, unrelatedRef string) []string { return []string{currentRef, unrelatedRef} },
			planRefs: func(currentRef, unrelatedRef string) []string { return []string{currentRef, unrelatedRef} },
		},
		{
			name:      "unrelated lineage removal",
			refs:      func(_, currentRef, _ string) []string { return []string{currentRef} },
			planRefs:  func(currentRef, _ string) []string { return []string{currentRef} },
			wantError: true,
		},
		{
			name:      "current source missing",
			refs:      func(oldRef, _, unrelatedRef string) []string { return []string{oldRef, unrelatedRef} },
			planRefs:  func(currentRef, unrelatedRef string) []string { return []string{currentRef, unrelatedRef} },
			wantError: true,
		},
		{
			name:      "page ref absent from plan",
			refs:      func(_, currentRef, unrelatedRef string) []string { return []string{currentRef, unrelatedRef} },
			planRefs:  func(currentRef, _ string) []string { return []string{currentRef} },
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := workspace.Init(); err != nil {
				t.Fatal(err)
			}
			oldRef := acceptStructuredWorkspaceSource(t, workspace, "engineering", "page.md", testSourceRevision)
			currentRef := acceptStructuredWorkspaceSource(t, workspace, "engineering", "page.md", "revision-2")
			unrelatedRef := acceptStructuredWorkspaceSource(t, workspace, "operations", "runbook.md", testSourceRevision)
			writeRootCatalogTargets(t, workspace, "entities/shared.md")
			existing := validWorkspacePageRefs("entities/shared", "Shared", []string{oldRef, unrelatedRef}, "Existing facts")
			target := filepath.Join(workspace.Root(), "wiki", "entities", "shared.md")
			if err := os.WriteFile(target, existing, 0o600); err != nil {
				t.Fatal(err)
			}
			schema, err := workspace.Schema(context.Background(), testScope)
			if err != nil {
				t.Fatal(err)
			}
			refs := test.refs(oldRef, currentRef, unrelatedRef)
			_, err = workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
				OperationID: "lineage-" + token(test.name), Scope: testScope, SchemaDigest: schema.Digest,
				RequiredSourceRef: currentRef, SourceRefs: test.planRefs(currentRef, unrelatedRef),
				Edits: []knowl.FileEdit{{
					Path: "wiki/entities/shared.md", ExpectedDigest: digestBytes(existing),
					Content: validWorkspacePageRefs("entities/shared", "Shared", refs, "Updated facts"),
				}},
			})
			if test.wantError {
				if !errors.Is(err, ErrContentInvalid) {
					t.Fatalf("StagePlan() error = %v, want content invalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("StagePlan() error = %v", err)
			}
		})
	}
}

func TestWorkspaceSnapshotResolvesSortedSourceDocuments(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	operationsRef := acceptStructuredWorkspaceSource(t, workspace, "operations", "runbook.md", testSourceRevision)
	engineeringRef := acceptStructuredWorkspaceSource(t, workspace, "engineering", "architecture.md", testSourceRevision)
	writeRootCatalogTargets(t, workspace, "entities/shared.md")
	content := validWorkspacePageRefs("entities/shared", "Shared", []string{operationsRef, engineeringRef}, "Shared facts")
	target := filepath.Join(workspace.Root(), "wiki", "entities", "shared.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := workspace.Snapshot(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pages) != 1 || len(snapshot.Pages[0].SourceDocuments) != 2 {
		t.Fatalf("snapshot provenance = %#v", snapshot.Pages)
	}
	documents := snapshot.Pages[0].SourceDocuments
	if documents[0].SourceID != "engineering" || documents[1].SourceID != "operations" || snapshot.Pages[0].SourceDocument == nil || *snapshot.Pages[0].SourceDocument != documents[0] {
		t.Fatalf("sorted source documents = %#v, singular = %#v", documents, snapshot.Pages[0].SourceDocument)
	}
	read, err := workspace.ReadPages(context.Background(), testScope, []knowl.PageID{"entities/shared"}, knowl.ReadLimits{Pages: 1, Bytes: len(content)})
	if err != nil || len(read) != 1 || !slices.Equal(read[0].SourceDocuments, documents) {
		t.Fatalf("ReadPages() provenance = %#v, %v", read, err)
	}
}

func TestWorkspaceStagePlanRejectsInvalidExistingStageReplay(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	acceptWorkspaceSource(t, workspace)
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	plan := knowl.ValidatedEditPlan{
		OperationID:  "replay-invalid",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits:        []knowl.FileEdit{{Path: testPageOnePath, Content: []byte("# One\n")}},
	}
	stageDir := filepath.Join(workspace.Root(), knowlDir, "staging", token(plan.OperationID))
	if err := os.MkdirAll(filepath.Dir(filepath.Join(stageDir, filepath.FromSlash(plan.Edits[0].Path))), 0o700); err != nil {
		t.Fatalf("create staged parent: %v", err)
	}
	if err := writeAtomic(filepath.Join(stageDir, filepath.FromSlash(plan.Edits[0].Path)), plan.Edits[0].Content, 0o600); err != nil {
		t.Fatalf("write staged page: %v", err)
	}
	manifest := stageManifest{
		OperationID:  plan.OperationID,
		Scope:        string(plan.Scope),
		SchemaDigest: plan.SchemaDigest,
		SourceRefs:   append([]string(nil), plan.SourceRefs...),
		Entries: []stageEntry{{
			Target: plan.Edits[0].Path,
			Digest: digestBytes(plan.Edits[0].Content),
		}},
	}
	core, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal core manifest: %v", err)
	}
	logBefore, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logAfter, err := appendLogEntry(logBefore, manifest, digestBytes(core))
	if err != nil {
		t.Fatalf("append log entry: %v", err)
	}
	manifest.LogExpectedDigest = digestBytes(logBefore)
	manifest.LogDigest = digestBytes(logAfter)
	metadata, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := writeAtomic(filepath.Join(stageDir, "manifest.yaml"), metadata, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	_, err = workspace.StagePlan(context.Background(), plan)
	if !errors.Is(err, ErrContentInvalid) {
		t.Fatalf("StagePlan() replay error = %v, want content invalid", err)
	}
}

func TestWorkspaceStagePlanRejectsProtectedSourceNamespace(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	for index, target := range []string{
		"wiki/sources/engineering/page.md",
		"wiki/entities/../sources/engineering/page.md",
		`wiki\sources\engineering\page.md`,
	} {
		plan := knowl.ValidatedEditPlan{
			OperationID: fmt.Sprintf("protected-source-%d", index), Scope: testScope, SchemaDigest: schema.Digest,
			SourceRefs: []string{"fixture:source@1"}, Edits: []knowl.FileEdit{{Path: target, Content: []byte("protected")}},
		}
		if _, err := workspace.StagePlan(context.Background(), plan); !errors.Is(err, ErrPathRejected) {
			t.Fatalf("StagePlan(%q) error = %v, want ErrPathRejected", target, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki", "sources", "engineering", "page.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("protected canonical target stat = %v", err)
	}
}

func TestWorkspaceLoadStageSurvivesReopen(t *testing.T) {
	workspace, staged, _ := stageLoadFixture(t)
	reopened, err := New(workspace.Root())
	if err != nil {
		t.Fatalf("reopen workspace: %v", err)
	}
	loaded, err := reopened.LoadStage(context.Background(), testScope, knowl.OperationID(staged.OperationID))
	if err != nil {
		t.Fatalf("load staged artifact: %v", err)
	}
	if loaded.OperationID != staged.OperationID || loaded.Digest != staged.Digest || !slices.Equal(loaded.Files, staged.Files) {
		t.Fatalf("loaded stage = %#v, want %#v", loaded, staged)
	}
}

func TestWorkspaceLoadStageFailsClosed(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		workspace, err := New(t.TempDir())
		if err != nil {
			t.Fatalf("new workspace: %v", err)
		}
		if err := workspace.Init(); err != nil {
			t.Fatalf("init workspace: %v", err)
		}
		_, err = workspace.LoadStage(context.Background(), testScope, "absent")
		if !errors.Is(err, app.ErrStageNotFound) {
			t.Fatalf("absent stage error = %v", err)
		}
	})

	tests := []struct {
		name   string
		load   func(knowl.StagedChange) (knowl.ScopeRef, knowl.OperationID)
		mutate func(t *testing.T, workspace *Workspace, staged knowl.StagedChange, stageDir string)
		want   error
	}{
		{
			name: "missing manifest",
			mutate: func(t *testing.T, _ *Workspace, _ knowl.StagedChange, stageDir string) {
				t.Helper()
				if err := os.Remove(filepath.Join(stageDir, "manifest.yaml")); err != nil {
					t.Fatalf("remove manifest: %v", err)
				}
			},
			want: ErrPlanConflict,
		},
		{
			name: "cross scope",
			load: func(staged knowl.StagedChange) (knowl.ScopeRef, knowl.OperationID) {
				return "other", knowl.OperationID(staged.OperationID)
			},
			want: ErrPlanConflict,
		},
		{
			name: "content digest mismatch",
			mutate: func(t *testing.T, _ *Workspace, _ knowl.StagedChange, stageDir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(stageDir, filepath.FromSlash(testPageOnePath)), []byte("corrupt"), 0o600); err != nil {
					t.Fatalf("corrupt staged file: %v", err)
				}
			},
			want: ErrPlanConflict,
		},
		{
			name: "oversized staged content",
			mutate: func(t *testing.T, _ *Workspace, _ knowl.StagedChange, stageDir string) {
				t.Helper()
				oversized := make([]byte, app.DefaultPlanLimits().MaxFileBytes+1)
				if err := os.WriteFile(filepath.Join(stageDir, filepath.FromSlash(testPageOnePath)), oversized, 0o600); err != nil {
					t.Fatalf("write oversized staged file: %v", err)
				}
			},
			want: ErrPlanConflict,
		},
		{
			name: "stale schema",
			mutate: func(t *testing.T, workspace *Workspace, _ knowl.StagedChange, _ string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(workspace.Root(), schemaFile), []byte("# Changed schema\n"), 0o600); err != nil {
					t.Fatalf("change schema: %v", err)
				}
			},
			want: ErrPrecondition,
		},
		{
			name: "symlinked content",
			mutate: func(t *testing.T, _ *Workspace, _ knowl.StagedChange, stageDir string) {
				t.Helper()
				path := filepath.Join(stageDir, filepath.FromSlash(testPageOnePath))
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove staged file: %v", err)
				}
				if err := os.Symlink(filepath.Join(stageDir, "manifest.yaml"), path); err != nil {
					t.Skipf("create symlink: %v", err)
				}
			},
			want: ErrPathRejected,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, staged, stageDir := stageLoadFixture(t)
			if test.mutate != nil {
				test.mutate(t, workspace, staged, stageDir)
			}
			scope, id := knowl.ScopeRef(testScope), knowl.OperationID(staged.OperationID)
			if test.load != nil {
				scope, id = test.load(staged)
			}
			_, err := workspace.LoadStage(context.Background(), scope, id)
			if !errors.Is(err, test.want) {
				t.Fatalf("LoadStage() error = %v, want %v", err, test.want)
			}
		})
	}
}

func stageLoadFixture(t *testing.T) (*Workspace, knowl.StagedChange, string) {
	t.Helper()
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	acceptWorkspaceSource(t, workspace)
	writeRootCatalogTargets(t, workspace, "entities/one.md")
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	staged, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "load-stage",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits: []knowl.FileEdit{{
			Path:    testPageOnePath,
			Content: validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, ""),
		}},
	})
	if err != nil {
		t.Fatalf("stage fixture: %v", err)
	}
	return workspace, staged, filepath.Join(workspace.Root(), knowlDir, "staging", token(staged.OperationID))
}

func TestWorkspaceCommitRejectsBrokenProspectiveStateBeforeJournal(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	acceptWorkspaceSource(t, workspace)
	writeRootCatalogTargets(t, workspace, "entities/one.md", "entities/two.md")
	targetPath := filepath.Join(workspace.Root(), "wiki", "entities", "two.md")
	if err := os.WriteFile(targetPath, validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, ""), 0o600); err != nil {
		t.Fatalf("write target page: %v", err)
	}
	schema, err := workspace.Schema(context.Background(), testScope)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	staged, err := workspace.StagePlan(context.Background(), knowl.ValidatedEditPlan{
		OperationID:  "commit-invalid",
		Scope:        testScope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testWorkspaceSourceRef},
		Edits:        []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, "[Two](two.md)")}},
	})
	if err != nil {
		t.Fatalf("stage plan: %v", err)
	}
	logBefore, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove target page: %v", err)
	}
	_, err = workspace.Commit(context.Background(), staged)
	if !errors.Is(err, ErrContentInvalid) {
		t.Fatalf("Commit() error = %v, want content invalid", err)
	}
	if _, statErr := os.Stat(filepath.Join(workspace.Root(), "wiki", "entities", "one.md")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("committed page stat = %v, want absent", statErr)
	}
	logAfter, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read log after failed commit: %v", err)
	}
	if string(logAfter) != string(logBefore) {
		t.Fatalf("log changed after failed commit: %q != %q", logAfter, logBefore)
	}
	if _, statErr := os.Stat(filepath.Join(workspace.Root(), knowlDir, "recovery", token(staged.OperationID)+".yaml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("recovery journal stat = %v, want absent", statErr)
	}
}

func writeRootCatalogTargets(t *testing.T, workspace *Workspace, targets ...string) {
	t.Helper()
	indexPath := filepath.Join(workspace.Root(), "wiki", "index.md")
	content, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read root catalog: %v", err)
	}
	for _, target := range targets {
		content = append(content, []byte("\n* [Target]("+target+")\n")...)
	}
	if err := os.WriteFile(indexPath, content, 0o600); err != nil {
		t.Fatalf("write root catalog: %v", err)
	}
}

func acceptWorkspaceSource(t *testing.T, workspace *Workspace) {
	t.Helper()
	content := []byte("source content")
	digest := sha256.Sum256(content)
	if _, err := workspace.AcceptSource(context.Background(), knowl.SourceEnvelope{
		Scope:     testScope,
		Source:    knowl.SourceRef{Adapter: testFixtureAdapter, ID: "source-1"},
		Version:   knowl.SourceVersion{Version: "1", Digest: hex.EncodeToString(digest[:])},
		MediaType: "text/plain",
		Content:   content,
	}); err != nil {
		t.Fatalf("accept source: %v", err)
	}
}

func validWorkspacePage(id, title, sourceRef, body string) []byte {
	return validWorkspacePageRefs(id, title, []string{sourceRef}, body)
}

func validWorkspacePageRefs(id, title string, sourceRefs []string, body string) []byte {
	content := "---\nid: " + id + "\ntitle: " + title + "\ntype: entity\nsource_refs:\n"
	for _, sourceRef := range sourceRefs {
		content += "  - " + sourceRef + "\n"
	}
	content += "---\n# " + title + "\n"
	if body != "" {
		content += "\n" + body + "\n"
	}
	return []byte(content)
}

func acceptStructuredWorkspaceSource(t *testing.T, workspace *Workspace, sourceID knowl.SourceID, documentID knowl.DocumentID, revision string) string {
	t.Helper()
	content := []byte(string(sourceID) + "/" + string(documentID) + "@" + revision)
	accepted, err := workspace.AcceptSource(context.Background(), knowl.SourceEnvelope{
		Scope: testScope, Source: knowl.SourceRef{Adapter: testWikiSourceAdapter, ID: string(sourceID) + "/" + string(documentID)},
		Version: knowl.SourceVersion{Version: revision, Digest: digestBytes(content)}, MediaType: testMarkdownMediaType,
		SourceDocument: knowl.SourceDocument{
			SourceID: sourceID, DocumentID: documentID, Revision: revision,
			URI: "https://wiki.example.test/" + string(documentID),
		},
		Content: content,
	})
	if err != nil {
		t.Fatalf("accept structured source: %v", err)
	}
	return sourceRefKey(accepted)
}

type canonicalState struct {
	content map[string][]byte
	missing map[string]struct{}
}

func captureCanonicalState(t *testing.T, workspace *Workspace, extraPaths ...string) canonicalState {
	t.Helper()
	state := canonicalState{
		content: make(map[string][]byte),
		missing: make(map[string]struct{}),
	}
	for _, relative := range append([]string{testIndexPath, canonicalLogPath}, extraPaths...) {
		path := filepath.Join(workspace.Root(), filepath.FromSlash(relative))
		content, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			state.missing[relative] = struct{}{}
			continue
		}
		if err != nil {
			t.Fatalf("read canonical state %q: %v", relative, err)
		}
		state.content[relative] = append([]byte(nil), content...)
	}
	return state
}

func assertCanonicalState(t *testing.T, workspace *Workspace, state canonicalState) {
	t.Helper()
	for relative, before := range state.content {
		after, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read canonical state %q: %v", relative, err)
		}
		if string(after) != string(before) {
			t.Fatalf("canonical state %q changed: %q != %q", relative, after, before)
		}
	}
	for relative := range state.missing {
		if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canonical state %q stat = %v, want absent", relative, err)
		}
	}
}
