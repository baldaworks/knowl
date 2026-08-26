package reconcile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"

	filesystem "github.com/baldaworks/knowl/internal/source/filesystem"
	"github.com/baldaworks/knowl/pkg/knowl/content/fs"
	sqlitestore "github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
)

const testEngineeringSourceID knowl.SourceID = "engineering"

// countingAdapter wraps the production filesystem adapter with fetch counters.
type countingAdapter struct {
	inner   app.SourceAdapter
	mutex   sync.Mutex
	fetches int
	fetched map[knowl.DocumentID]int
}

func newCountingAdapter(inner app.SourceAdapter) *countingAdapter {
	return &countingAdapter{inner: inner, fetched: make(map[knowl.DocumentID]int)}
}

func (a *countingAdapter) List(ctx context.Context, source knowl.Source, token string) (knowl.DocumentPage, error) {
	return a.inner.List(ctx, source, token)
}

func (a *countingAdapter) Fetch(ctx context.Context, source knowl.Source, ref knowl.DocumentRef) (knowl.Document, error) {
	a.mutex.Lock()
	a.fetches++
	a.fetched[ref.ExternalID]++
	a.mutex.Unlock()
	return a.inner.Fetch(ctx, source, ref)
}

func (a *countingAdapter) totalFetches() int {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	return a.fetches
}

func (a *countingAdapter) fetchesFor(id knowl.DocumentID) int {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	return a.fetched[id]
}

type verticalEnv struct {
	service     *Service
	adapter     *countingAdapter
	state       app.SourceStateStore
	search      app.SearchIndex
	workspace   *fs.Workspace
	storePath   string
	scope       knowl.ScopeRef
	sourceRoot  string
	secondRoot  string
	runSequence int
	clockStep   int64
	mutex       sync.Mutex
	queue       *recordingMaintenanceQueue
}

func newVerticalEnv(t *testing.T) *verticalEnv {
	t.Helper()
	ctx := context.Background()
	env := &verticalEnv{
		scope:      "vertical_contract",
		sourceRoot: t.TempDir(),
		secondRoot: t.TempDir(),
		storePath:  filepath.Join(t.TempDir(), "vertical.sqlite"),
	}
	store, err := sqlitestore.Open(ctx, env.storePath)
	if err != nil {
		t.Fatalf("open vertical store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	env.state, env.search = store, store
	workspace, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatalf("open vertical workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init vertical workspace: %v", err)
	}
	env.workspace = workspace
	env.adapter = newCountingAdapter(filesystem.NewDefault())
	env.queue = newRecordingMaintenanceQueue()

	var runMutex sync.Mutex
	options := Options{}
	options.Clock = func() time.Time {
		env.mutex.Lock()
		defer env.mutex.Unlock()
		env.clockStep++
		return time.Unix(10_000, 0).UTC().Add(time.Duration(env.clockStep) * time.Second)
	}
	options.NewRunID = func() knowl.SyncRunID {
		runMutex.Lock()
		defer runMutex.Unlock()
		env.runSequence++
		return knowl.SyncRunID(fmt.Sprintf("vertical-%03d", env.runSequence))
	}
	dependencies := Dependencies{
		Adapters:      map[knowl.SourceType]app.SourceAdapter{knowl.SourceTypeFilesystem: env.adapter},
		State:         store,
		Content:       workspace,
		SourceContent: workspace,
		Search:        store,
		Maintenance:   env.queue,
	}
	service, err := NewService(dependencies, options)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	env.service = service
	writeVerticalFile(t, env.sourceRoot, "docs/one.md", "# One\n\nBody one\n")
	writeVerticalFile(t, env.sourceRoot, "docs/two.md", "# Two\n")
	writeVerticalFile(t, env.sourceRoot, "assets/logo.bin", "\x00\x01logo")
	return env
}

func writeVerticalFile(t *testing.T, root, relative, body string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func removeVerticalFile(t *testing.T, root, relative string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
		t.Fatal(err)
	}
}

func (env *verticalEnv) source(id knowl.SourceID, root, flavor string) knowl.Source {
	return knowl.Source{
		ID: id, Type: knowl.SourceTypeFilesystem, Enabled: true,
		Config: knowl.SourceConfig{Filesystem: &knowl.FilesystemSourceConfig{
			Root: root, Include: []string{"**/*"}, Flavor: flavor,
			URIBase: "https://wiki.example.test",
		}},
	}
}

func (env *verticalEnv) primary() knowl.Source {
	return env.source(testEngineeringSourceID, env.sourceRoot, knowl.SourceFlavorMarkdown)
}

func TestVerticalReconciliationSliceEndToEnd(t *testing.T) {
	ctx := context.Background()
	env := newVerticalEnv(t)

	// Initial synchronization accepts immutable raw revisions and reserves text.
	result, err := env.service.SyncSource(ctx, env.scope, env.primary())
	requireSyncSuccess(t, result, err)
	if result.Changed != true || env.adapter.totalFetches() != 3 {
		t.Fatalf("initial = changed:%v fetches:%d; want three selective fetches", result.Changed, env.adapter.totalFetches())
	}
	headOne, headErr := env.state.DocumentState(ctx, env.scope, testEngineeringSourceID, "docs/one.md")
	if headErr != nil || headOne.Deleted || headOne.MirrorPath != "" || headOne.MirrorDigest != "" ||
		headOne.MaintenanceRevision != headOne.Revision || headOne.MaintenanceOperationID == "" {
		t.Fatalf("initial head = %#v, %v", headOne, headErr)
	}
	if document := headOne.AcceptedSource.SourceDocument; app.ValidateOwnedSourceDocument(testEngineeringSourceID, document) != nil ||
		document.DocumentID != "docs/one.md" || document.Revision != headOne.Revision {
		t.Fatalf("raw source provenance = %#v", document)
	}
	pages, searchErr := env.search.Search(ctx, env.scope, "one", knowl.ReadLimits{Pages: 5}, nil)
	if searchErr != nil || len(pages) != 0 {
		t.Fatalf("raw source leaked into semantic search = %#v, %v", pages, searchErr)
	}
	inventory, invErr := env.service.sourceContent.SourceDigests(ctx, env.scope, testEngineeringSourceID, 16)
	if invErr != nil || len(inventory) != 0 {
		t.Fatalf("canonical source inventory = %#v, %v; want no mirrors", inventory, invErr)
	}
	if len(env.queue.requests) != 2 {
		t.Fatalf("maintenance reservations = %d, want two Markdown documents", len(env.queue.requests))
	}

	// Unchanged synchronization performs zero fetches and zero canonical writes.
	beforeCalls := env.adapter.totalFetches()
	unchanged, unchangedErr := env.service.SyncSource(ctx, env.scope, env.primary())
	requireSyncSuccess(t, unchanged, unchangedErr)
	if env.adapter.totalFetches() != beforeCalls || unchanged.Changed {
		t.Fatalf("unchanged = fetches:%d changed:%v; want full convergence", env.adapter.totalFetches(), unchanged.Changed)
	}
	if len(env.queue.requests) != 2 {
		t.Fatalf("unchanged run reserved again: %d requests", len(env.queue.requests))
	}

	// Update one file selectively while another is deleted safely.
	writeVerticalFile(t, env.sourceRoot, "docs/one.md", "# One updated\n")
	removeVerticalFile(t, env.sourceRoot, "docs/two.md")
	retainedRaw, rawErr := env.service.content.ReadSource(ctx, headOne.AcceptedSource, knowl.ReadLimits{})
	fetchTwoBefore := env.adapter.fetchesFor("docs/two.md")
	updateBase := env.adapter.totalFetches()
	updatedResult, updateErr := env.service.SyncSource(ctx, env.scope, env.primary())
	requireSyncSuccess(t, updatedResult, updateErr)
	if delta := env.adapter.totalFetches() - updateBase; delta != 1 {
		t.Fatalf("update fetch delta = %d; want exactly one", delta)
	}
	if env.adapter.fetchesFor("docs/two.md") != fetchTwoBefore {
		t.Fatal("deleted document was fetched")
	}
	tombstone, tombstoneErr := env.state.DocumentState(ctx, env.scope, testEngineeringSourceID, "docs/two.md")
	if tombstoneErr != nil || !tombstone.Deleted || tombstone.DeletedAt.IsZero() {
		t.Fatalf("tombstone head = %#v, %v", tombstone, tombstoneErr)
	}
	if rawErr != nil || len(retainedRaw) == 0 {
		t.Fatalf("raw history of deleted document lost: %v", rawErr)
	}
	for _, sources := range [][]knowl.SourceID{nil, {testEngineeringSourceID}, {"ghost"}} {
		deletedPages, deletedSearchErr := env.search.Search(ctx, env.scope, "two", knowl.ReadLimits{Pages: 5}, sources)
		if deletedSearchErr != nil || len(deletedPages) != 0 {
			t.Fatalf("tombstoned search with sources %v = %#v, %v", sources, deletedPages, deletedSearchErr)
		}
	}
	inventoryAfterUpdate, _ := env.service.sourceContent.SourceDigests(ctx, env.scope, testEngineeringSourceID, 16)
	if len(inventoryAfterUpdate) != 0 {
		t.Fatalf("post-deletion inventory = %#v", inventoryAfterUpdate)
	}
	updatedPages, searchErr := env.search.Search(ctx, env.scope, "updated", knowl.ReadLimits{Pages: 5}, nil)
	if searchErr != nil || len(updatedPages) != 0 {
		t.Fatalf("updated raw source leaked into semantic search = %#v, %v", updatedPages, searchErr)
	}

	// Interrupted scan fails safely and leaves prior heads active.
	if err := os.RemoveAll(env.sourceRoot); err != nil {
		t.Fatal(err)
	}
	interrupted, interruptErr := env.service.SyncSource(ctx, env.scope, env.primary())
	if !strings.Contains(classOf(interruptErr), classAdapter) {
		t.Fatalf("interrupted scan error = %v, want adapter class", interruptErr)
	}
	interruptedRow, rowErr := env.state.SyncRun(ctx, env.scope, interrupted.Run.ID)
	if rowErr != nil || interruptedRow.Status != knowl.SyncStatusFailed || interruptedRow.FailureClass != classAdapter {
		t.Fatalf("interrupted run = %#v, %v; want failed/adapter", interruptedRow, rowErr)
	}
	stillActive, stillErr := env.state.DocumentState(ctx, env.scope, testEngineeringSourceID, "docs/one.md")
	if stillErr != nil || stillActive.Deleted {
		t.Fatalf("interrupted scan touched active head = %#v, %v", stillActive, stillErr)
	}
	resumableAfterInterrupt, _ := env.state.ResumableSyncRuns(ctx, env.scope, 10)
	if len(resumableAfterInterrupt) != 0 {
		t.Fatalf("failed scans must not stay resumable: %#v", resumableAfterInterrupt)
	}
}

func TestVerticalOKFFlavorPreservesControlsMetadataAndConceptLinks(t *testing.T) {
	ctx := context.Background()
	env := newVerticalEnv(t)
	if err := os.RemoveAll(env.sourceRoot); err != nil {
		t.Fatal(err)
	}
	writeVerticalFile(t, env.sourceRoot, "index.md", "---\nokf_version: \"0.9\"\n---\n# Catalog\n\n* [One](docs/one.md)\n")
	writeVerticalFile(t, env.sourceRoot, "log.md", "# Catalog Log\n\n## 2026-08-26\n* Published catalog\n")
	writeVerticalFile(t, env.sourceRoot, "docs/one.md", "---\ntype: Metric\ntitle: One metric\ndescription: Semantic description\nstatus: deprecated\nvendor: retained\n---\nMetricverticalbeacon [Two](two.md) [Broken](missing.md).\n")
	writeVerticalFile(t, env.sourceRoot, "docs/two.md", "---\ntype: Reference\ntitle: Two\n---\nSecond concept.\n")

	result, err := env.service.SyncSource(ctx, env.scope, env.source(testEngineeringSourceID, env.sourceRoot, knowl.SourceFlavorOKF))
	requireSyncSuccess(t, result, err)
	if len(result.Diagnostics) != 0 {
		t.Fatalf("raw-only sync emitted normalization diagnostics: %#v", result.Diagnostics)
	}
	inventory, err := env.workspace.SourceDigests(ctx, env.scope, testEngineeringSourceID, 16)
	if err != nil || len(inventory) != 0 {
		t.Fatalf("OKF source mirrors = %#v, %v", inventory, err)
	}
	state, err := env.state.DocumentState(ctx, env.scope, testEngineeringSourceID, "docs/one.md")
	if err != nil || state.MaintenanceRevision != state.Revision || state.MaintenanceOperationID == "" {
		t.Fatalf("OKF raw state = %#v, %v", state, err)
	}
	raw, err := env.workspace.ReadSource(ctx, state.AcceptedSource, knowl.ReadLimits{})
	if err != nil || !strings.Contains(string(raw), "vendor: retained") || !strings.Contains(string(raw), "Metricverticalbeacon") {
		t.Fatalf("OKF raw bytes = %q, %v", raw, err)
	}
	if len(env.queue.requests) != 4 {
		t.Fatalf("OKF maintenance reservations = %d, want all four textual documents", len(env.queue.requests))
	}
}

func TestVerticalCatalogOnlyRerenderAndEqualPaths(t *testing.T) {
	ctx := context.Background()
	env := newVerticalEnv(t)

	noteRoot := env.secondRoot
	writeVerticalFile(t, noteRoot, "notes/main.md", "# Main\n\nSee [[sibling]]\n")
	writeVerticalFile(t, noteRoot, "sibling.md", "# Sibling\n")
	obsidian := env.source("notes", noteRoot, knowl.SourceFlavorObsidian)

	first, firstErr := env.service.SyncSource(ctx, env.scope, obsidian)
	requireSyncSuccess(t, first, firstErr)
	if len(env.queue.requests) != 2 {
		t.Fatalf("initial reservations = %d", len(env.queue.requests))
	}

	// Adding an unrelated descriptor accepts and reserves only that document;
	// unchanged raw documents are neither fetched nor re-reserved.
	writeVerticalFile(t, noteRoot, "third.md", "# Third\n")
	before := env.adapter.fetchesFor("notes/main.md")
	second, secondErr := env.service.SyncSource(ctx, env.scope, obsidian)
	requireSyncSuccess(t, second, secondErr)
	if delta := env.adapter.fetchesFor("notes/main.md"); delta != before {
		t.Fatalf("catalog addition fetched unchanged main %d extra times", delta)
	}
	secondPrepared, preparedErr := env.state.PreparedSync(ctx, env.scope, second.Run.ID)
	if preparedErr != nil {
		t.Fatal(preparedErr)
	}
	if secondPrepared.Counts != (knowl.SyncCounts{Added: 1, Unchanged: 2}) {
		t.Fatalf("rerender counts = %#v", secondPrepared.Counts)
	}
	if len(env.queue.requests) != 3 {
		t.Fatalf("catalog addition reservations = %d, want one new request", len(env.queue.requests))
	}

	// Equal paths under two sources stay isolated end to end.
	writeVerticalFile(t, env.sourceRoot, "shared/page.md", "# Equalpathbeacon primary\n")
	otherRoot := t.TempDir()
	writeVerticalFile(t, otherRoot, "shared/page.md", "# Equalpathbeacon secondary\n")
	other := env.source("mirror", otherRoot, knowl.SourceFlavorMarkdown)
	primaryResult, primaryErr := env.service.SyncSource(ctx, env.scope, env.primary())
	requireSyncSuccess(t, primaryResult, primaryErr)
	otherResult, otherErr := env.service.SyncSource(ctx, env.scope, other)
	requireSyncSuccess(t, otherResult, otherErr)
	leftInventory, leftErr := env.service.sourceContent.SourceDigests(ctx, env.scope, testEngineeringSourceID, 16)
	rightInventory, rightErr2 := env.service.sourceContent.SourceDigests(ctx, env.scope, "mirror", 16)
	if leftErr != nil || rightErr2 != nil {
		t.Fatalf("inventories = %#v / %#v, %v / %v", leftInventory, rightInventory, leftErr, rightErr2)
	}
	if len(leftInventory) != 0 || len(rightInventory) != 0 {
		t.Fatalf("source mirrors remain: %#v / %#v", leftInventory, rightInventory)
	}
	leftState, leftErr := env.state.DocumentState(ctx, env.scope, testEngineeringSourceID, "shared/page.md")
	rightState, rightErr := env.state.DocumentState(ctx, env.scope, "mirror", "shared/page.md")
	if leftErr != nil || rightErr != nil || leftState.AcceptedSource.SourceDocument.SourceID != testEngineeringSourceID ||
		rightState.AcceptedSource.SourceDocument.SourceID != "mirror" || leftState.AcceptedSource.ManifestRef == rightState.AcceptedSource.ManifestRef {
		t.Fatalf("equal-path raw lineage = %#v / %#v, %v / %v", leftState, rightState, leftErr, rightErr)
	}
}

func TestVerticalAzureWikiEmptyNavigationAndUnicodePaths(t *testing.T) {
	ctx := context.Background()
	env := newVerticalEnv(t)
	root := t.TempDir()
	writeVerticalFile(t, root, "Архитектура.md", "")
	writeVerticalFile(t, root, "Архитектура/Обзор.md", "# Обзор\n\n[[_TOC_]]\n[[_TOSP_]]\nСм. [[Архитектура]].\n")
	source := env.source("azure-wiki", root, knowl.SourceFlavorObsidian)

	result, err := env.service.SyncSource(ctx, env.scope, source)
	requireSyncSuccess(t, result, err)
	if !result.Changed {
		t.Fatal("initial Azure Wiki synchronization reported no change")
	}
	placeholder, err := env.state.DocumentState(ctx, env.scope, source.ID, "Архитектура.md")
	if err != nil {
		t.Fatalf("empty navigation state: %v", err)
	}
	raw, err := env.workspace.ReadSource(ctx, placeholder.AcceptedSource, knowl.ReadLimits{})
	if err != nil || len(raw) != 0 {
		t.Fatalf("empty navigation raw source = %q, %v", raw, err)
	}
	overview, err := env.state.DocumentState(ctx, env.scope, source.ID, "Архитектура/Обзор.md")
	if err != nil || overview.AcceptedSource.SourceDocument.DocumentID != "Архитектура/Обзор.md" {
		t.Fatalf("Unicode raw state = %#v, %v", overview, err)
	}
	overviewRaw, err := env.workspace.ReadSource(ctx, overview.AcceptedSource, knowl.ReadLimits{})
	if err != nil || !strings.Contains(string(overviewRaw), "[[_TOC_]]") || !strings.Contains(string(overviewRaw), "[[_TOSP_]]") ||
		!strings.Contains(string(overviewRaw), "[[Архитектура]]") {
		t.Fatalf("Azure raw directives = %q, %v", overviewRaw, err)
	}
	inventory, err := env.workspace.SourceDigests(ctx, env.scope, source.ID, 16)
	if err != nil || len(inventory) != 0 {
		t.Fatalf("Azure source mirrors = %#v, %v", inventory, err)
	}
}

func TestVerticalLegacyMirrorCleanupPreservesSemanticWiki(t *testing.T) {
	env := newVerticalEnv(t)
	root := t.TempDir()
	writeVerticalFile(t, root, "Раздел/Страница.md", "# Страница\n\n[[Отсутствует]]\n")
	source := env.source("azure-wiki", root, knowl.SourceFlavorMarkdown)
	legacy := filepath.Join(env.workspace.Root(), "wiki", "sources", "azure-wiki", "Раздел", "Страница.md")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("legacy mirror"), 0o600); err != nil {
		t.Fatal(err)
	}
	semantic := filepath.Join(env.workspace.Root(), "wiki", "index.md")
	before, err := os.ReadFile(semantic)
	if err != nil {
		t.Fatal(err)
	}
	result, err := env.service.SyncSource(context.Background(), env.scope, source)
	requireSyncSuccess(t, result, err)
	if _, statErr := os.Stat(legacy); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("legacy mirror remains: %v", statErr)
	}
	curated, err := os.ReadFile(semantic)
	if err != nil || string(curated) != string(before) {
		t.Fatalf("semantic page changed = %q, %v", curated, err)
	}
}
