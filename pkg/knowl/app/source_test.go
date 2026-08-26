package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testSourceID     = knowl.SourceID("engineering")
	testDocumentID   = knowl.DocumentID("architecture/auth.md")
	testPageDocument = knowl.DocumentID("docs/page.md")
	testMarkdownGlob = "**/*.md"
	testSourceScope  = knowl.ScopeRef("local")
	testSyncRunID    = knowl.SyncRunID("run-1")
	testSourcePage   = "wiki/sources/engineering/page.md"

	preparedDigestMediaType    = "text/markdown"
	preparedDigestBaseRevision = "revision-1"
	preparedDigestOperationID  = "operation-1"
)

func TestSourceIdentityValidation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		sourceID   knowl.SourceID
		documentID knowl.DocumentID
		wantError  bool
	}{
		{name: "valid", sourceID: testSourceID, documentID: testDocumentID},
		{name: "source uppercase", sourceID: "Engineering", documentID: testDocumentID, wantError: true},
		{name: "source slash", sourceID: "engineering/wiki", documentID: testDocumentID, wantError: true},
		{name: "document absolute", sourceID: testSourceID, documentID: "/architecture/auth.md", wantError: true},
		{name: "document traversal", sourceID: testSourceID, documentID: "architecture/../auth.md", wantError: true},
		{name: "document backslash", sourceID: testSourceID, documentID: `architecture\auth.md`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := errors.Join(app.ValidateSourceID(test.sourceID), app.ValidateDocumentID(test.documentID))
			if test.wantError && !errors.Is(err, app.ErrSourceInvalid) {
				t.Fatalf("validation error = %v, want ErrSourceInvalid", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestSourceConfigDigestIsDeterministicAndOneWay(t *testing.T) {
	t.Parallel()
	first := fixtureSource()
	first.Config.Filesystem.Include = []string{testMarkdownGlob, "docs/*.md"}
	second := fixtureSource()
	second.Config.Filesystem.Include = []string{"docs/*.md", testMarkdownGlob}
	firstDigest, err := app.SourceConfigDigest(first)
	if err != nil {
		t.Fatalf("first digest: %v", err)
	}
	secondDigest, err := app.SourceConfigDigest(second)
	if err != nil {
		t.Fatalf("second digest: %v", err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("digests differ: %q != %q", firstDigest, secondDigest)
	}
	if len(firstDigest) != 64 || strings.Contains(firstDigest, first.Config.Filesystem.Root) {
		t.Fatalf("digest is not a redacted SHA-256 value: %q", firstDigest)
	}
}

func TestSourceValidationBounds(t *testing.T) {
	t.Parallel()
	const metadataValue = "value"
	metadata64 := make(map[string]string, 64)
	for index := range 64 {
		metadata64[string(rune('a'+index))] = metadataValue
	}
	metadata65 := make(map[string]string, 65)
	for index := range 65 {
		metadata65[string(rune(0x100+index))] = metadataValue
	}
	validRef := knowl.DocumentRef{ExternalID: testPageDocument, Revision: strings.Repeat("r", 4096), Path: string(testPageDocument), Metadata: metadata64}
	if err := app.ValidateDocumentRef(validRef); err != nil {
		t.Fatalf("boundary DocumentRef error = %v", err)
	}
	metadataBoundaryRef := knowl.DocumentRef{
		ExternalID: testPageDocument, Revision: "1", Path: string(testPageDocument),
		Metadata: map[string]string{strings.Repeat("k", 256): strings.Repeat("v", 4096)},
	}
	if err := app.ValidateDocumentRef(metadataBoundaryRef); err != nil {
		t.Fatalf("boundary metadata error = %v", err)
	}
	for _, test := range []struct {
		name string
		ref  knowl.DocumentRef
	}{
		{name: "document id", ref: knowl.DocumentRef{ExternalID: knowl.DocumentID(strings.Repeat("d", 1025)), Revision: "1", Path: string(testPageDocument)}},
		{name: "revision", ref: knowl.DocumentRef{ExternalID: testPageDocument, Revision: strings.Repeat("r", 4097), Path: string(testPageDocument)}},
		{name: "metadata entries", ref: knowl.DocumentRef{ExternalID: testPageDocument, Revision: "1", Path: string(testPageDocument), Metadata: metadata65}},
		{name: "metadata key", ref: knowl.DocumentRef{ExternalID: testPageDocument, Revision: "1", Path: string(testPageDocument), Metadata: map[string]string{strings.Repeat("k", 257): metadataValue}}},
		{name: "metadata value", ref: knowl.DocumentRef{ExternalID: testPageDocument, Revision: "1", Path: string(testPageDocument), Metadata: map[string]string{"key": strings.Repeat("v", 4097)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := app.ValidateDocumentRef(test.ref); !errors.Is(err, app.ErrSourceInvalid) {
				t.Fatalf("ValidateDocumentRef() error = %v", err)
			}
		})
	}
	if err := app.ValidateSourceID(knowl.SourceID(strings.Repeat("s", 64))); err != nil {
		t.Fatalf("boundary source id error = %v", err)
	}
	if err := app.ValidateSourceID(knowl.SourceID(strings.Repeat("s", 65))); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("oversized source id error = %v", err)
	}
	if err := app.ValidateDocumentID(knowl.DocumentID(strings.Repeat("d", 1024))); err != nil {
		t.Fatalf("boundary document id error = %v", err)
	}
	if err := app.ValidateSyncCounts(knowl.SyncCounts{Deleted: -1}); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("negative counts error = %v", err)
	}
	if got, err := app.AddSyncCounts(knowl.SyncCounts{Added: 2}, knowl.SyncCounts{Added: 3}); err != nil || got.Added != 5 {
		t.Fatalf("AddSyncCounts() = %#v, %v", got, err)
	}
	if _, err := app.AddSyncCounts(knowl.SyncCounts{Added: 1<<63 - 1}, knowl.SyncCounts{Added: 1}); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("overflow counts error = %v", err)
	}
	if _, err := app.ValidateDocumentListOptions(app.DocumentListOptions{Limit: 1000}); err != nil {
		t.Fatalf("boundary list error = %v", err)
	}
	if _, err := app.ValidateDocumentListOptions(app.DocumentListOptions{Limit: 1001}); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("oversized list error = %v", err)
	}
	run := knowl.SyncRun{
		ID:           testSyncRunID,
		Scope:        testSourceScope,
		SourceID:     testSourceID,
		ConfigDigest: strings.Repeat("a", 64),
		Status:       knowl.SyncStatusScanning,
	}
	if err := app.ValidateSyncRun(run); err != nil {
		t.Fatalf("valid run error = %v", err)
	}
	run.Cursor = strings.Repeat("c", 4096)
	if err := app.ValidateSyncRun(run); err != nil {
		t.Fatalf("boundary cursor error = %v", err)
	}
	run.Cursor += "c"
	if err := app.ValidateSyncRun(run); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("oversized cursor error = %v", err)
	}
	run.Cursor = ""
	run.Status = "invented"
	if err := app.ValidateSyncRun(run); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("unknown status error = %v", err)
	}
	run.Status = knowl.SyncStatusFailed
	run.FailureClass = "secret\nvalue"
	if err := app.ValidateSyncRun(run); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("unbounded failure class error = %v", err)
	}
}

func TestFetchedDocumentAndPageBounds(t *testing.T) {
	t.Parallel()
	document := knowl.Document{
		DocumentRef: knowl.DocumentRef{ExternalID: testPageDocument, Revision: "1", Path: string(testPageDocument)},
		Title:       "Page", URI: "https://wiki.example.test/docs/page", MediaType: preparedDigestMediaType, Content: []byte("# Page\n"),
	}
	if err := app.ValidateDocument(document, len(document.Content)); err != nil {
		t.Fatalf("ValidateDocument() error: %v", err)
	}
	document.Content = []byte("oversized")
	if err := app.ValidateDocument(document, 1); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("oversized document error = %v", err)
	}
	if err := app.ValidateDocument(document, 64<<20); err != nil {
		t.Fatalf("maximum content limit error: %v", err)
	}
	if err := app.ValidateDocument(document, (64<<20)+1); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("oversized content limit error = %v", err)
	}
	page := knowl.DocumentPage{Documents: []knowl.DocumentRef{document.DocumentRef}}
	if err := app.ValidateDocumentPage(page, 1); err != nil {
		t.Fatalf("ValidateDocumentPage() error: %v", err)
	}
	if err := app.ValidateDocumentPage(page, 0); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("invalid page limit error = %v", err)
	}
	page.Documents = make([]knowl.DocumentRef, 1000)
	for index := range page.Documents {
		page.Documents[index] = document.DocumentRef
	}
	if err := app.ValidateDocumentPage(page, 1000); err != nil {
		t.Fatalf("boundary document page error = %v", err)
	}
	page.Documents = page.Documents[:1]
	page.NextPageToken = strings.Repeat("p", 4096)
	if err := app.ValidateDocumentPage(page, 1); err != nil {
		t.Fatalf("boundary page token error = %v", err)
	}
	page.NextPageToken += "p"
	if err := app.ValidateDocumentPage(page, 1); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("oversized page token error = %v", err)
	}
	secret := "postgres://operator:password@database.example/knowl?token=bearer-secret"
	document.Content = []byte(secret)
	err := app.ValidateDocument(document, 1)
	if !errors.Is(err, app.ErrSourceInvalid) || strings.Contains(err.Error(), secret) {
		t.Fatalf("secret-shaped content error = %q", err)
	}
}

func TestNormalizeSourceMutationPlan(t *testing.T) {
	t.Parallel()
	digest := strings.Repeat("a", 64)
	content := []byte("# Page\n")
	plan := knowl.SourceMutationPlan{
		RunID: testSyncRunID, Scope: testSourceScope, SourceID: testSourceID,
		Mutations: []knowl.SourceMutation{
			{Action: knowl.SourceMutationDelete, Path: "wiki/sources/engineering/old.md", ExpectedDigest: digest},
			{Action: knowl.SourceMutationWrite, Path: "wiki/sources/engineering/assets/logo.png", Content: []byte{0x89, 0x50}},
			{Action: knowl.SourceMutationWrite, Path: testSourcePage, ExpectedDigest: digest, Content: content},
		},
	}
	normalized, err := app.NormalizeSourceMutationPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{normalized.Mutations[0].Path, normalized.Mutations[1].Path, normalized.Mutations[2].Path}; strings.Join(got, ",") != "wiki/sources/engineering/assets/logo.png,wiki/sources/engineering/old.md,wiki/sources/engineering/page.md" {
		t.Fatalf("normalized paths = %v", got)
	}
	content[0] = 'x'
	if normalized.Mutations[2].Content[0] != '#' {
		t.Fatal("normalized content aliases caller bytes")
	}
	repeated, err := app.NormalizeSourceMutationPlan(plan)
	if err != nil || repeated.Mutations[0].Path != normalized.Mutations[0].Path {
		t.Fatalf("repeated normalization = %#v, %v", repeated, err)
	}
}

func TestSourceMutationValidationRejectsInvalidAndBoundedInputs(t *testing.T) {
	t.Parallel()
	digest := strings.Repeat("a", 64)
	valid := knowl.SourceMutationPlan{
		RunID: testSyncRunID, Scope: testSourceScope, SourceID: testSourceID,
		Mutations: []knowl.SourceMutation{{Action: knowl.SourceMutationWrite, Path: testSourcePage, Content: []byte{}}},
	}
	tests := []struct {
		name string
		edit func(*knowl.SourceMutationPlan)
		want error
	}{
		{name: "empty scope", edit: func(plan *knowl.SourceMutationPlan) { plan.Scope = "" }, want: app.ErrSourceMutationInvalid},
		{name: "invalid source", edit: func(plan *knowl.SourceMutationPlan) { plan.SourceID = "Engineering" }, want: app.ErrSourceMutationInvalid},
		{name: "empty run", edit: func(plan *knowl.SourceMutationPlan) { plan.RunID = "" }, want: app.ErrSourceMutationInvalid},
		{name: "empty mutations", edit: func(plan *knowl.SourceMutationPlan) { plan.Mutations = nil }, want: app.ErrSourceMutationInvalid},
		{name: "other source", edit: func(plan *knowl.SourceMutationPlan) { plan.Mutations[0].Path = "wiki/sources/operations/page.md" }, want: app.ErrSourceMutationInvalid},
		{name: "namespace root", edit: func(plan *knowl.SourceMutationPlan) { plan.Mutations[0].Path = "wiki/sources/engineering/" }, want: app.ErrSourceMutationInvalid},
		{name: "traversal", edit: func(plan *knowl.SourceMutationPlan) {
			plan.Mutations[0].Path = "wiki/sources/engineering/../operations/page.md"
		}, want: app.ErrSourceMutationInvalid},
		{name: "backslash", edit: func(plan *knowl.SourceMutationPlan) { plan.Mutations[0].Path = `wiki/sources/engineering\page.md` }, want: app.ErrSourceMutationInvalid},
		{name: "absolute", edit: func(plan *knowl.SourceMutationPlan) { plan.Mutations[0].Path = "/wiki/sources/engineering/page.md" }, want: app.ErrSourceMutationInvalid},
		{name: "oversized path", edit: func(plan *knowl.SourceMutationPlan) {
			plan.Mutations[0].Path = "wiki/sources/engineering/" + strings.Repeat("p", 2025)
		}, want: app.ErrSourceMutationInvalid},
		{name: "unknown action", edit: func(plan *knowl.SourceMutationPlan) { plan.Mutations[0].Action = "rename" }, want: app.ErrSourceMutationInvalid},
		{name: "nil write", edit: func(plan *knowl.SourceMutationPlan) { plan.Mutations[0].Content = nil }, want: app.ErrSourceMutationInvalid},
		{name: "delete without digest", edit: func(plan *knowl.SourceMutationPlan) {
			plan.Mutations[0] = knowl.SourceMutation{Action: knowl.SourceMutationDelete, Path: testSourcePage}
		}, want: app.ErrSourceMutationInvalid},
		{name: "delete with content", edit: func(plan *knowl.SourceMutationPlan) {
			plan.Mutations[0] = knowl.SourceMutation{Action: knowl.SourceMutationDelete, Path: testSourcePage, ExpectedDigest: digest, Content: []byte{1}}
		}, want: app.ErrSourceMutationInvalid},
		{name: "invalid digest", edit: func(plan *knowl.SourceMutationPlan) { plan.Mutations[0].ExpectedDigest = "not-a-digest" }, want: app.ErrSourceMutationInvalid},
		{name: "duplicate target", edit: func(plan *knowl.SourceMutationPlan) { plan.Mutations = append(plan.Mutations, plan.Mutations[0]) }, want: app.ErrSourceMutationInvalid},
		{name: "mutation count", edit: func(plan *knowl.SourceMutationPlan) {
			plan.Mutations = make([]knowl.SourceMutation, 2049)
			for index := range plan.Mutations {
				plan.Mutations[index] = knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: fmt.Sprintf("wiki/sources/engineering/%04d.md", index), Content: []byte{}}
			}
		}, want: app.ErrSourceMutationLimit},
		{name: "oversized file", edit: func(plan *knowl.SourceMutationPlan) { plan.Mutations[0].Content = make([]byte, (64<<20)+1) }, want: app.ErrSourceMutationLimit},
		{name: "oversized total", edit: func(plan *knowl.SourceMutationPlan) {
			shared := make([]byte, 64<<20)
			plan.Mutations = make([]knowl.SourceMutation, 9)
			for index := range plan.Mutations {
				plan.Mutations[index] = knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: fmt.Sprintf("wiki/sources/engineering/total-%d.bin", index), Content: shared}
			}
		}, want: app.ErrSourceMutationLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := valid
			plan.Mutations = append([]knowl.SourceMutation(nil), valid.Mutations...)
			test.edit(&plan)
			if _, err := app.NormalizeSourceMutationPlan(plan); !errors.Is(err, test.want) {
				t.Fatalf("NormalizeSourceMutationPlan() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSourceDocumentValidation(t *testing.T) {
	t.Parallel()
	valid := knowl.SourceDocument{SourceID: testSourceID, DocumentID: testDocumentID, Revision: preparedDigestBaseRevision, URI: "https://wiki.example.test/auth"}
	if err := app.ValidateSourceDocument(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.URI = strings.Repeat("u", 8193)
	if err := app.ValidateSourceDocument(invalid); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("ValidateSourceDocument() error = %v", err)
	}
	invalid = valid
	invalid.DocumentID = ""
	if err := app.ValidateSourceDocument(invalid); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("partial SourceDocument error = %v", err)
	}
	invalid = valid
	invalid.URI = "https://operator:secret@wiki.example.test/auth"
	if err := app.ValidateSourceDocument(invalid); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("credential URI error = %v", err)
	}
	invalid = valid
	invalid.URI = "https://wiki.example.test/auth page"
	if err := app.ValidateSourceDocument(invalid); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("non-canonical URI error = %v", err)
	}
	if err := app.ValidateOwnedSourceDocument("operations", valid); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("owner mismatch error = %v", err)
	}
}

func TestResolveSourceDocumentSupportsLegacyFallback(t *testing.T) {
	t.Parallel()
	document := knowl.SourceDocument{
		SourceID: testSourceID, DocumentID: testDocumentID, Revision: preparedDigestBaseRevision, URI: "https://wiki.example.test/auth",
	}
	accepted := knowl.AcceptedSource{Version: knowl.SourceVersion{Version: document.Revision}, SourceDocument: document}
	resolved, err := app.ResolveSourceDocument(testSourceID, accepted, knowl.SourceDocument{})
	if err != nil || resolved != document {
		t.Fatalf("persisted resolution = %#v, %v", resolved, err)
	}

	legacy := accepted
	legacy.SourceDocument = knowl.SourceDocument{}
	resolved, err = app.ResolveSourceDocument(testSourceID, legacy, document)
	if err != nil || resolved != document {
		t.Fatalf("legacy resolution = %#v, %v", resolved, err)
	}
	if _, err := app.ResolveSourceDocument(testSourceID, legacy, knowl.SourceDocument{}); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("missing lineage error = %v", err)
	}
	mismatch := document
	mismatch.Revision = "other"
	if _, err := app.ResolveSourceDocument(testSourceID, legacy, mismatch); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("mismatched lineage error = %v", err)
	}
}

func TestSourceAdapterContractCarriesContextAndExplicitSource(t *testing.T) {
	t.Parallel()
	adapter := fixtureAdapter{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.List(ctx, fixtureSource(), ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("List error = %v, want context canceled", err)
	}
	if _, err := adapter.Fetch(context.Background(), fixtureSource(), knowl.DocumentRef{ExternalID: testPageDocument, Revision: "1", Path: string(testPageDocument)}); err != nil {
		t.Fatalf("Fetch error = %v", err)
	}
}

type fixtureAdapter struct{}

func (fixtureAdapter) List(ctx context.Context, _ knowl.Source, _ string) (knowl.DocumentPage, error) {
	if err := ctx.Err(); err != nil {
		return knowl.DocumentPage{}, err
	}
	return knowl.DocumentPage{Documents: []knowl.DocumentRef{{ExternalID: testPageDocument, Revision: "1", Path: string(testPageDocument)}}}, nil
}

func (fixtureAdapter) Fetch(ctx context.Context, _ knowl.Source, ref knowl.DocumentRef) (knowl.Document, error) {
	if err := ctx.Err(); err != nil {
		return knowl.Document{}, err
	}
	return knowl.Document{DocumentRef: ref, Title: "Page", URI: "file:///docs/page.md", MediaType: "text/markdown", Content: []byte("# Page\n")}, nil
}

func fixtureSource() knowl.Source {
	return knowl.Source{
		ID:      testSourceID,
		Type:    knowl.SourceTypeFilesystem,
		Enabled: true,
		Config: knowl.SourceConfig{Filesystem: &knowl.FilesystemSourceConfig{
			Root:    "/sources/engineering",
			Include: []string{testMarkdownGlob},
		}},
		Sync: knowl.SourceSyncPolicy{Interval: time.Minute},
	}
}

var _ app.SourceAdapter = fixtureAdapter{}

type fixtureSourceStateStore struct{}

type fixtureSourceContentStore struct{}

func (fixtureSourceContentStore) SourceDigests(context.Context, knowl.ScopeRef, knowl.SourceID, int) ([]app.SourceDigestEntry, error) {
	return nil, nil
}

func (fixtureSourceContentStore) StageSourcePlan(context.Context, knowl.SourceMutationPlan) (knowl.StagedSourceMutation, error) {
	return knowl.StagedSourceMutation{}, nil
}

func (fixtureSourceContentStore) LoadSourceStage(context.Context, knowl.ScopeRef, knowl.SourceID, knowl.SyncRunID) (knowl.StagedSourceMutation, error) {
	return knowl.StagedSourceMutation{}, nil
}

func (fixtureSourceContentStore) CommitSource(context.Context, knowl.StagedSourceMutation) (knowl.ContentCommit, error) {
	return knowl.ContentCommit{}, nil
}

var _ app.SourceContentStore = fixtureSourceContentStore{}

func (fixtureSourceStateStore) BeginSync(context.Context, app.BeginSyncRequest) (knowl.SyncRun, bool, error) {
	return knowl.SyncRun{}, false, nil
}

func (fixtureSourceStateStore) SyncRun(context.Context, knowl.ScopeRef, knowl.SyncRunID) (knowl.SyncRun, error) {
	return knowl.SyncRun{}, nil
}

func (fixtureSourceStateStore) ScanDocuments(context.Context, knowl.ScopeRef, knowl.SyncRunID, int) ([]knowl.DocumentRef, error) {
	return nil, nil
}

func (fixtureSourceStateStore) RecordScanPage(context.Context, app.ScanPageRecord) (knowl.SyncRun, error) {
	return knowl.SyncRun{}, nil
}

func (fixtureSourceStateStore) PrepareSync(context.Context, app.PreparedSyncState) (knowl.SyncRun, error) {
	return knowl.SyncRun{}, nil
}

func (fixtureSourceStateStore) PreparedSync(context.Context, knowl.ScopeRef, knowl.SyncRunID) (app.PreparedSyncRead, error) {
	return app.PreparedSyncRead{}, nil
}

func (fixtureSourceStateStore) MarkContentCommitted(context.Context, app.SyncGeneration) (knowl.SyncRun, error) {
	return knowl.SyncRun{}, nil
}

func (fixtureSourceStateStore) MarkProjected(context.Context, app.SyncGeneration) (knowl.SyncRun, error) {
	return knowl.SyncRun{}, nil
}

func (fixtureSourceStateStore) FinalizeSync(context.Context, app.SyncFinalization) (knowl.SyncRun, error) {
	return knowl.SyncRun{}, nil
}

func (fixtureSourceStateStore) FailSync(context.Context, knowl.ScopeRef, knowl.SyncRunID, string, time.Time) (knowl.SyncRun, error) {
	return knowl.SyncRun{}, nil
}

func (fixtureSourceStateStore) DocumentState(context.Context, knowl.ScopeRef, knowl.SourceID, knowl.DocumentID) (knowl.DocumentState, error) {
	return knowl.DocumentState{}, nil
}

func (fixtureSourceStateStore) DocumentStates(context.Context, knowl.ScopeRef, knowl.SourceID, app.DocumentListOptions) ([]knowl.DocumentState, error) {
	return nil, nil
}

func (fixtureSourceStateStore) SourceStatus(context.Context, knowl.ScopeRef, knowl.SourceID) (knowl.SourceStatus, error) {
	return knowl.SourceStatus{}, nil
}

func (fixtureSourceStateStore) ResumableSyncRuns(context.Context, knowl.ScopeRef, int) ([]knowl.SyncRun, error) {
	return nil, nil
}

var _ app.SourceStateStore = fixtureSourceStateStore{}

func preparedDigestFixture() app.PreparedSyncState {
	return app.PreparedSyncState{
		RunID: testSyncRunID, Scope: testSourceScope, SourceID: testSourceID, CompleteScan: true,
		Checkpoint: "checkpoint-7", Counts: knowl.SyncCounts{Added: 2, Unchanged: 1},
		Documents: []app.PreparedDocumentState{
			{Action: app.SyncDocumentActive, State: preparedDigestState("docs/b.md", false)},
			{Action: app.SyncDocumentActive, State: preparedDigestState("docs/a.md", false)},
			{Action: app.SyncDocumentTombstone, State: preparedDigestState("docs/c.md", true)},
		},
		PreparedAt: time.Unix(80, 0).UTC(),
	}
}

func preparedDigestState(documentID string, deleted bool) knowl.DocumentState {
	state := knowl.DocumentState{
		Scope: testSourceScope, SourceID: testSourceID, DocumentID: knowl.DocumentID(documentID), Revision: preparedDigestBaseRevision,
		AcceptedSource: knowl.AcceptedSource{Scope: testSourceScope, Source: knowl.SourceRef{Adapter: "wiki-filesystem", ID: string(testSourceID) + "/" + documentID}, Version: knowl.SourceVersion{Version: preparedDigestBaseRevision, Digest: strings.Repeat("d", 64)}, MediaType: preparedDigestMediaType, ManifestRef: "raw/manifest.json"},
		MirrorPath:     "wiki/sources/" + string(testSourceID) + "/" + documentID, MirrorDigest: strings.Repeat("e", 64), LastSeenRunID: testSyncRunID,
	}
	if deleted {
		state.Deleted = true
		state.DeletedAt = time.Unix(90, 0).UTC()
		state.MirrorPath = ""
		state.MirrorDigest = ""
	}
	return state
}

func TestPreparedSyncDigestIsCanonicalAndDeterministic(t *testing.T) {
	t.Parallel()
	base := preparedDigestFixture()
	first, err := app.PreparedSyncDigest(base)
	if err != nil {
		t.Fatalf("PreparedSyncDigest() error = %v", err)
	}
	if len(first) != 64 || first != strings.ToLower(first) {
		t.Fatalf("PreparedSyncDigest() = %q, want lowercase sha256", first)
	}
	reordered := base
	reordered.Documents = []app.PreparedDocumentState{base.Documents[2], base.Documents[0], base.Documents[1]}
	second, err := app.PreparedSyncDigest(reordered)
	if err != nil {
		t.Fatalf("PreparedSyncDigest() reordered error = %v", err)
	}
	if second != first {
		t.Fatalf("document order changed digest = %q, want %q", second, first)
	}
	retimed := base
	retimed.PreparedAt = time.Unix(81, 0).UTC().Add(7 * time.Microsecond)
	third, err := app.PreparedSyncDigest(retimed)
	if err != nil {
		t.Fatalf("PreparedSyncDigest() retimed error = %v", err)
	}
	if third != first {
		t.Fatalf("prepared time changed digest = %q, want %q", third, first)
	}
	for name, mutate := range map[string]func(app.PreparedSyncState) app.PreparedSyncState{
		"checkpoint": func(in app.PreparedSyncState) app.PreparedSyncState { in.Checkpoint = "checkpoint-8"; return in },
		"counts":     func(in app.PreparedSyncState) app.PreparedSyncState { in.Counts.Updated++; return in },
		"revision": func(in app.PreparedSyncState) app.PreparedSyncState {
			in.Documents[0].State.Revision = "revision-2"
			return in
		},
		"maintenance operation": func(in app.PreparedSyncState) app.PreparedSyncState {
			in.Documents[0].State.MaintenanceRevision = in.Documents[0].State.Revision
			in.Documents[0].State.MaintenanceOperationID = preparedDigestOperationID
			return in
		},
		"deleted time": func(in app.PreparedSyncState) app.PreparedSyncState {
			in.Documents[2].State.DeletedAt = time.Unix(91, 0).UTC()
			return in
		},
	} {
		changed := base
		changed.Documents = append([]app.PreparedDocumentState(nil), base.Documents...)
		changed = mutate(changed)
		digest, err := app.PreparedSyncDigest(changed)
		if err != nil {
			t.Fatalf("%s digest error = %v", name, err)
		}
		if digest == first {
			t.Fatalf("%s mutation kept digest %q", name, first)
		}
	}
}

func TestPreparedSyncDigestRejectsInvalidPayloads(t *testing.T) {
	t.Parallel()
	valid := preparedDigestFixture()
	if _, err := app.PreparedSyncDigest(valid); err != nil {
		t.Fatalf("fixture digest error = %v", err)
	}
	for _, test := range []struct {
		name    string
		mutator func(app.PreparedSyncState) app.PreparedSyncState
	}{
		{"incomplete scan", func(in app.PreparedSyncState) app.PreparedSyncState { in.CompleteScan = false; return in }},
		{"empty scope", func(in app.PreparedSyncState) app.PreparedSyncState { in.Scope = "  "; return in }},
		{"invalid run id", func(in app.PreparedSyncState) app.PreparedSyncState { in.RunID = "bad\x00run"; return in }},
		{"invalid source id", func(in app.PreparedSyncState) app.PreparedSyncState { in.SourceID = "Bad"; return in }},
		{"negative counts", func(in app.PreparedSyncState) app.PreparedSyncState { in.Counts.Failed = -1; return in }},
		{"duplicate documents", func(in app.PreparedSyncState) app.PreparedSyncState {
			in.Documents = []app.PreparedDocumentState{in.Documents[0], in.Documents[0]}
			return in
		}},
		{"tombstone malformed mirror digest", func(in app.PreparedSyncState) app.PreparedSyncState {
			tombstone := preparedDigestState("docs/c.md", true)
			tombstone.MirrorDigest = "not-a-digest"
			in.Documents = []app.PreparedDocumentState{{Action: app.SyncDocumentTombstone, State: tombstone}}
			return in
		}},
		{"active claims deletion", func(in app.PreparedSyncState) app.PreparedSyncState {
			in.Documents = []app.PreparedDocumentState{{Action: app.SyncDocumentActive, State: preparedDigestState("docs/c.md", true)}}
			return in
		}},
		{"foreign accepted scope", func(in app.PreparedSyncState) app.PreparedSyncState {
			in.Documents[0].State.AcceptedSource.Scope = "other"
			return in
		}},
		{"foreign accepted provenance", func(in app.PreparedSyncState) app.PreparedSyncState {
			in.Documents[0].State.AcceptedSource.SourceDocument = knowl.SourceDocument{
				SourceID: "operations", DocumentID: "docs/a.md", Revision: "revision-1", URI: "https://wiki.example.test/docs/a.md",
			}
			return in
		}},
		{"mismatched accepted provenance document", func(in app.PreparedSyncState) app.PreparedSyncState {
			in.Documents[0].State.AcceptedSource.SourceDocument = knowl.SourceDocument{
				SourceID: testSourceID, DocumentID: "docs/other.md", Revision: in.Documents[0].State.Revision, URI: "https://wiki.example.test/docs/other.md",
			}
			return in
		}},
		{"maintenance operation without revision", func(in app.PreparedSyncState) app.PreparedSyncState {
			in.Documents[0].State.MaintenanceOperationID = preparedDigestOperationID
			return in
		}},
		{"maintenance revision mismatch", func(in app.PreparedSyncState) app.PreparedSyncState {
			in.Documents[0].State.MaintenanceRevision = "other-revision"
			in.Documents[0].State.MaintenanceOperationID = preparedDigestOperationID
			return in
		}},
		{"wrong last seen run", func(in app.PreparedSyncState) app.PreparedSyncState {
			in.Documents[0].State.LastSeenRunID = "other-run"
			return in
		}},
		{"deleted time beyond microsecond precision", func(in app.PreparedSyncState) app.PreparedSyncState {
			in.Documents[2].State.DeletedAt = time.Unix(90, 123456789).UTC()
			return in
		}},
		{"over limit documents", func(in app.PreparedSyncState) app.PreparedSyncState {
			documents := make([]app.PreparedDocumentState, 1001)
			for index := range documents {
				documents[index] = app.PreparedDocumentState{Action: app.SyncDocumentActive, State: preparedDigestState(fmt.Sprintf("docs/%04d.md", index), false)}
			}
			in.Documents = documents
			return in
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := valid
			fixture.Documents = append([]app.PreparedDocumentState(nil), valid.Documents...)
			if _, err := app.PreparedSyncDigest(test.mutator(fixture)); !errors.Is(err, app.ErrSourceInvalid) {
				t.Fatalf("PreparedSyncDigest() error = %v, want ErrSourceInvalid", err)
			}
		})
	}
}

func TestNormalizePreparedDocumentsOrdersAndValidates(t *testing.T) {
	t.Parallel()
	base := preparedDigestFixture()
	normalized, err := app.NormalizePreparedDocuments(base.Scope, base.SourceID, base.RunID, base.Documents)
	if err != nil {
		t.Fatalf("NormalizePreparedDocuments() error = %v", err)
	}
	order := ""
	for index, document := range normalized {
		order += string(document.State.DocumentID)
		if index > 0 && normalized[index-1].State.DocumentID >= document.State.DocumentID {
			t.Fatalf("normalized order = %s at %d", order, index)
		}
	}
	if order != "docs/a.md"+"docs/b.md"+"docs/c.md" {
		t.Fatalf("normalized order = %s", order)
	}
	if normalized[0].State.DocumentID != "docs/a.md" || base.Documents[0].State.DocumentID != "docs/b.md" {
		t.Fatalf("normalization must copy, input mutated: first=%s", base.Documents[0].State.DocumentID)
	}
	if _, err := app.NormalizePreparedDocuments(base.Scope, base.SourceID, "", base.Documents); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("missing run identity error = %v", err)
	}
	if _, err := app.NormalizePreparedDocuments(base.Scope, base.SourceID, base.RunID, nil); err != nil {
		t.Fatalf("empty candidate set error = %v", err)
	}
	if err := app.ValidateScanDocumentLimit(0); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("zero limit error = %v", err)
	}
	if err := app.ValidateScanDocumentLimit(1001); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("over-limit error = %v", err)
	}
	if err := app.ValidateScanDocumentLimit(1000); err != nil {
		t.Fatalf("boundary limit error = %v", err)
	}
}
