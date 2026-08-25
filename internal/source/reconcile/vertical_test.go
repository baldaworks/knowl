package reconcile

import (
	"context"
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
	"github.com/baldaworks/knowl/internal/source/normalize"
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
		Normalizer:    normalize.NewDefaultAdapter(),
		State:         store,
		Content:       workspace,
		SourceContent: workspace,
		Search:        store,
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

	// Initial synchronization adds every descriptor and publishes projection.
	result, err := env.service.SyncSource(ctx, env.scope, env.primary())
	requireSyncSuccess(t, result, err)
	if result.Changed != true || env.adapter.totalFetches() != 3 {
		t.Fatalf("initial = changed:%v fetches:%d; want three selective fetches", result.Changed, env.adapter.totalFetches())
	}
	headOne, headErr := env.state.DocumentState(ctx, env.scope, testEngineeringSourceID, "docs/one.md")
	if headErr != nil || headOne.Deleted || headOne.MirrorPath == "" {
		t.Fatalf("initial head = %#v, %v", headOne, headErr)
	}
	pages, searchErr := env.search.Search(ctx, env.scope, "one", knowl.ReadLimits{Pages: 5}, nil)
	if searchErr != nil || len(pages) == 0 || pages[0].Untrusted != true {
		t.Fatalf("projection search = %#v, %v", pages, searchErr)
	}
	inventory, invErr := env.service.sourceContent.SourceDigests(ctx, env.scope, testEngineeringSourceID, 16)
	if invErr != nil || len(inventory) != 3 {
		t.Fatalf("canonical inventory = %#v, %v", inventory, invErr)
	}
	digestsAfterInitial := map[string]string{}
	for _, entry := range inventory {
		digestsAfterInitial[entry.Path] = entry.Digest
	}

	// Unchanged synchronization performs zero fetches and zero canonical writes.
	beforeCalls := env.adapter.totalFetches()
	unchanged, unchangedErr := env.service.SyncSource(ctx, env.scope, env.primary())
	requireSyncSuccess(t, unchanged, unchangedErr)
	if env.adapter.totalFetches() != beforeCalls || unchanged.Changed {
		t.Fatalf("unchanged = fetches:%d changed:%v; want full convergence", env.adapter.totalFetches(), unchanged.Changed)
	}
	inventoryAgain, _ := env.service.sourceContent.SourceDigests(ctx, env.scope, testEngineeringSourceID, 16)
	for _, entry := range inventoryAgain {
		if digestsAfterInitial[entry.Path] != entry.Digest {
			t.Fatalf("unchanged run rewrote %q", entry.Path)
		}
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
	if len(inventoryAfterUpdate) != 2 {
		t.Fatalf("post-deletion inventory = %#v", inventoryAfterUpdate)
	}
	for _, entry := range inventoryAfterUpdate {
		if entry.Path == headOne.MirrorPath && entry.Digest == digestsAfterInitial[headOne.MirrorPath] {
			t.Fatal("updated mirror kept stale canonical bytes")
		}
	}
	updatedPages, searchErr := env.search.Search(ctx, env.scope, "updated", knowl.ReadLimits{Pages: 5}, nil)
	if searchErr != nil || len(updatedPages) == 0 {
		t.Fatalf("projection after update = %#v, %v", updatedPages, searchErr)
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

func TestVerticalCatalogOnlyRerenderAndEqualPaths(t *testing.T) {
	ctx := context.Background()
	env := newVerticalEnv(t)

	noteRoot := env.secondRoot
	writeVerticalFile(t, noteRoot, "notes/main.md", "# Main\n\nSee [[sibling]]\n")
	writeVerticalFile(t, noteRoot, "sibling.md", "# Sibling\n")
	obsidian := env.source("notes", noteRoot, knowl.SourceFlavorObsidian)

	first, firstErr := env.service.SyncSource(ctx, env.scope, obsidian)
	requireSyncSuccess(t, first, firstErr)
	firstPrepared, preparedErr := env.state.PreparedSync(ctx, env.scope, first.Run.ID)
	if preparedErr != nil {
		t.Fatal(preparedErr)
	}
	mainDigestBefore := ""
	for _, document := range firstPrepared.Documents {
		if document.State.DocumentID == "notes/main.md" {
			mainDigestBefore = document.State.MirrorDigest
		}
	}
	if mainDigestBefore == "" {
		t.Fatalf("main candidate missing: %#v", firstPrepared.Documents)
	}

	// Adding an unrelated descriptor shifts the catalog identity, so stored
	// raw documents rerender without any Fetch even though their revisions
	// and bodies stay byte-identical.
	writeVerticalFile(t, noteRoot, "third.md", "# Third\n")
	before := env.adapter.fetchesFor("notes/main.md")
	second, secondErr := env.service.SyncSource(ctx, env.scope, obsidian)
	requireSyncSuccess(t, second, secondErr)
	if delta := env.adapter.fetchesFor("notes/main.md"); delta != before {
		t.Fatalf("catalog-only rerender fetched main %d extra times", delta)
	}
	secondPrepared, preparedErr := env.state.PreparedSync(ctx, env.scope, second.Run.ID)
	if preparedErr != nil {
		t.Fatal(preparedErr)
	}
	if secondPrepared.Counts != (knowl.SyncCounts{Added: 1, Unchanged: 2}) {
		t.Fatalf("rerender counts = %#v", secondPrepared.Counts)
	}
	mainDigestAfter := ""
	for _, document := range secondPrepared.Documents {
		if document.State.DocumentID == "notes/main.md" {
			mainDigestAfter = document.State.MirrorDigest
		}
	}
	if mainDigestAfter == "" || mainDigestAfter == mainDigestBefore {
		t.Fatalf("catalog-only change did not alter mirror identity: %q", mainDigestAfter)
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
	for _, entry := range leftInventory {
		if entry.Path == "wiki/sources/mirror/shared/page.md" || !strings.HasPrefix(entry.Path, "wiki/sources/engineering/") {
			t.Fatalf("cross-source path leaked: %q", entry.Path)
		}
	}
	found := false
	for _, entry := range rightInventory {
		if entry.Path != "wiki/sources/mirror/shared/page.md" {
			continue
		}
		found = true
		if entry.Digest == digestOf("# Equalpathbeacon primary\n") {
			t.Fatal("mirror source reused primary bytes")
		}
	}
	if !found {
		t.Fatalf("mirror inventory missing shared page: %#v", rightInventory)
	}
	assertVerticalSourceIDs(t, env, nil, testEngineeringSourceID, "mirror")
	assertVerticalSourceIDs(t, env, []knowl.SourceID{testEngineeringSourceID}, testEngineeringSourceID)
	assertVerticalSourceIDs(t, env, []knowl.SourceID{"mirror", testEngineeringSourceID}, testEngineeringSourceID, "mirror")
	assertVerticalSourceIDs(t, env, []knowl.SourceID{"ghost"})
}

func assertVerticalSourceIDs(t *testing.T, env *verticalEnv, sources []knowl.SourceID, want ...knowl.SourceID) {
	t.Helper()
	pages, err := env.search.Search(context.Background(), env.scope, "equalpathbeacon", knowl.ReadLimits{Pages: 10}, sources)
	if err != nil {
		t.Fatalf("filtered vertical search %v: %v", sources, err)
	}
	got := make([]knowl.SourceID, 0, len(pages))
	for _, page := range pages {
		if page.SourceDocument == nil {
			t.Fatalf("filtered vertical search returned source-less page: %#v", page)
		}
		got = append(got, page.SourceDocument.SourceID)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("filtered vertical sources %v = %v, want %v", sources, got, want)
	}
}

func digestOf(body string) string {
	return sha256Hex(body)
}
