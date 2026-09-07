package knowl_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sourcegit "github.com/baldaworks/knowl/internal/source/git"
	knowl "github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	sqlitestore "github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
)

const (
	gitTestRemoteURL = "https://github.com/org/integration-repo.git"
	gitTestDocIntro  = "docs/intro.md"
	gitTestDocGuide  = "docs/guide.md"
	gitTestDocNew    = "docs/new.md"
	gitTestBranchRef = "refs/heads/main"
	gitTestMarkdown  = "**/*.md"
)

type stubGitRemoteLister struct {
	mu   sync.Mutex
	refs []*plumbing.Reference
	err  error
}

func (s *stubGitRemoteLister) setRef(name string, hash plumbing.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = nil
	s.refs = []*plumbing.Reference{plumbing.NewReferenceFromStrings(name, hash.String())}
}

func (s *stubGitRemoteLister) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *stubGitRemoteLister) ListRemoteRefs(_ context.Context, _ domain.GitSourceConfig) ([]*plumbing.Reference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.refs, nil
}

type stubGitRepoOpener struct {
	mu   sync.Mutex
	repo *gogit.Repository
	err  error
}

func (s *stubGitRepoOpener) OpenOrClone(_ context.Context, _ domain.Source) (*gogit.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.repo, nil
}

func storeMemBlob(t *testing.T, storer storage.Storer, content []byte) plumbing.Hash {
	t.Helper()
	obj := storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		t.Fatalf("blob writer: %v", err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatalf("blob write: %v", err)
	}
	_ = w.Close()
	hash, err := storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store blob: %v", err)
	}
	return hash
}

func storeMemTree(t *testing.T, storer storage.Storer, tree *object.Tree) plumbing.Hash {
	t.Helper()
	sort.Sort(object.TreeEntrySorter(tree.Entries))
	obj := storer.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		t.Fatalf("encode tree: %v", err)
	}
	hash, err := storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store tree: %v", err)
	}
	return hash
}

func storeMemCommit(t *testing.T, storer storage.Storer, treeHash plumbing.Hash, parents ...plumbing.Hash) plumbing.Hash {
	t.Helper()
	commit := &object.Commit{
		Author: object.Signature{
			Name:  "Test Author",
			Email: "author@example.com",
			When:  time.Now(),
		},
		Committer: object.Signature{
			Name:  "Test Committer",
			Email: "committer@example.com",
			When:  time.Now(),
		},
		Message:      "test commit",
		TreeHash:     treeHash,
		ParentHashes: parents,
	}
	obj := storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	hash, err := storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store commit: %v", err)
	}
	return hash
}

func TestGitSourceRuntime_EndToEndReconciliation(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}

	storer := memory.NewStorage()
	repo, err := gogit.Init(storer, nil)
	if err != nil {
		t.Fatalf("gogit init: %v", err)
	}

	// Commit 1 (S1): docs/intro.md and docs/guide.md
	blobIntroV1 := storeMemBlob(t, storer, []byte("# Intro\n\nWelcome to Knowl git source."))
	blobGuideV1 := storeMemBlob(t, storer, []byte("# Guide\n\nStep by step documentation."))
	docsTreeV1Hash := storeMemTree(t, storer, &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "guide.md", Mode: filemode.Regular, Hash: blobGuideV1},
			{Name: "intro.md", Mode: filemode.Regular, Hash: blobIntroV1},
		},
	})
	rootTreeV1Hash := storeMemTree(t, storer, &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "docs", Mode: filemode.Dir, Hash: docsTreeV1Hash},
		},
	})
	commit1Hash := storeMemCommit(t, storer, rootTreeV1Hash)
	snapshot1SHA := commit1Hash.String()

	// Setup remote ref lister and opener
	lister := &stubGitRemoteLister{}
	lister.setRef(gitTestBranchRef, commit1Hash)
	opener := &stubGitRepoOpener{repo: repo}

	// Build adapter
	resolver := sourcegit.NewRefResolver(lister)
	adapter, err := sourcegit.NewAdapter(sourcegit.DefaultLimits(), resolver, opener)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}

	sourceID := domain.SourceID("git-team")
	gitSource := domain.Source{
		ID:      sourceID,
		Type:    domain.SourceTypeGit,
		Enabled: true,
		Config: domain.SourceConfig{
			Git: &domain.GitSourceConfig{
				Remote:  gitTestRemoteURL,
				Ref:     "main",
				RefKind: domain.GitRefKindBranch,
				Include: []string{gitTestMarkdown},
			},
		},
		Sync: domain.SourceSyncPolicy{OnStart: false},
	}

	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.Sources = []domain.Source{gitSource}

	host, err := knowl.New(ctx, knowl.Options{
		Config:     config,
		Maintainer: provider.Fixture{},
		SourceAdapters: map[domain.SourceType]app.SourceAdapter{
			domain.SourceTypeGit: adapter,
		},
	})
	if err != nil {
		t.Fatalf("knowl.New: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = host.Stop(stopCtx)
	}()

	// 0. Verify Agent Business Surface: REQ-API-001 (no git-specific tools)
	mcpTools := host.MCP().ListTools()
	for _, tool := range mcpTools {
		if strings.Contains(strings.ToLower(tool.Name), "git") {
			t.Fatalf("agent MCP surface leaks git-specific business tool: %s", tool.Name)
		}
	}

	// 1. Initial Sync: additions and checkpoint S1
	result1, err := host.SyncSource(ctx, sourceID)
	if err != nil {
		t.Fatalf("Initial SyncSource error: %v", err)
	}
	if !result1.Changed {
		t.Errorf("initial sync Changed = false, want true")
	}
	if result1.Run.Status != domain.SyncStatusSucceeded {
		t.Fatalf("initial sync status = %s, want succeeded", result1.Run.Status)
	}
	if result1.Run.Checkpoint != snapshot1SHA {
		t.Errorf("initial sync checkpoint = %s, want %s", result1.Run.Checkpoint, snapshot1SHA)
	}
	if result1.Run.Counts.Added != 2 {
		t.Errorf("initial sync counts.Added = %d, want 2", result1.Run.Counts.Added)
	}

	// Verify durable status
	status1, err := host.SourceStatus(ctx, sourceID)
	if err != nil {
		t.Fatalf("SourceStatus error: %v", err)
	}
	if status1.Checkpoint != snapshot1SHA {
		t.Errorf("status checkpoint = %s, want %s", status1.Checkpoint, snapshot1SHA)
	}
	if status1.Status != domain.SyncStatusSucceeded {
		t.Errorf("status = %s, want succeeded", status1.Status)
	}

	// Verify raw document content is written and readable
	rawIntro, ok := runtimeRawDocumentContent(t, host, config.Scope, sourceID, gitTestDocIntro)
	if !ok || !strings.Contains(string(rawIntro), "Welcome to Knowl git source.") {
		t.Errorf("raw content for intro doc: %q, ok=%v", rawIntro, ok)
	}
	rawGuide, ok := runtimeRawDocumentContent(t, host, config.Scope, sourceID, gitTestDocGuide)
	if !ok || !strings.Contains(string(rawGuide), "Step by step documentation.") {
		t.Errorf("raw content for guide doc: %q, ok=%v", rawGuide, ok)
	}

	// 2. Idempotent Sync: zero fetches, unchanged counts, same checkpoint
	result2, err := host.SyncSource(ctx, sourceID)
	if err != nil {
		t.Fatalf("Second SyncSource error: %v", err)
	}
	if result2.Changed {
		t.Errorf("idempotent sync Changed = true, want false")
	}
	if result2.Run.Counts.Unchanged != 2 || result2.Run.Counts.Added != 0 || result2.Run.Counts.Deleted != 0 {
		t.Errorf("idempotent sync unexpected counts: %+v", result2.Run.Counts)
	}
	if result2.Run.Checkpoint != snapshot1SHA {
		t.Errorf("idempotent sync checkpoint changed: %s", result2.Run.Checkpoint)
	}

	// 3. Incremental Sync: add doc, update doc, delete doc in commit S2
	blobIntroV2 := storeMemBlob(t, storer, []byte("# Intro V2\n\nUpdated welcome."))
	blobNewV1 := storeMemBlob(t, storer, []byte("# New Feature\n\nNewly added document."))
	docsTreeV2Hash := storeMemTree(t, storer, &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "intro.md", Mode: filemode.Regular, Hash: blobIntroV2},
			{Name: "new.md", Mode: filemode.Regular, Hash: blobNewV1},
		},
	})
	rootTreeV2Hash := storeMemTree(t, storer, &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "docs", Mode: filemode.Dir, Hash: docsTreeV2Hash},
		},
	})
	commit2Hash := storeMemCommit(t, storer, rootTreeV2Hash, commit1Hash)
	snapshot2SHA := commit2Hash.String()

	lister.setRef(gitTestBranchRef, commit2Hash)

	result3, err := host.SyncSource(ctx, sourceID)
	if err != nil {
		t.Fatalf("Incremental SyncSource error: %v", err)
	}
	if !result3.Changed {
		t.Errorf("incremental sync Changed = false, want true")
	}
	if result3.Run.Checkpoint != snapshot2SHA {
		t.Errorf("incremental sync checkpoint = %s, want %s", result3.Run.Checkpoint, snapshot2SHA)
	}
	if result3.Run.Counts.Updated != 1 || result3.Run.Counts.Added != 1 || result3.Run.Counts.Deleted != 1 {
		t.Errorf("incremental sync counts = %+v, want 1 updated, 1 added, 1 deleted", result3.Run.Counts)
	}

	// Verify updated raw document content
	rawIntroV2, ok := runtimeRawDocumentContent(t, host, config.Scope, sourceID, gitTestDocIntro)
	if !ok || !strings.Contains(string(rawIntroV2), "Updated welcome.") {
		t.Errorf("intro doc updated content: %q, ok=%v", rawIntroV2, ok)
	}
	rawNew, ok := runtimeRawDocumentContent(t, host, config.Scope, sourceID, gitTestDocNew)
	if !ok || !strings.Contains(string(rawNew), "Newly added document.") {
		t.Errorf("new doc content: %q, ok=%v", rawNew, ok)
	}
	// Verify document states in store
	stateStore, err := sqlitestore.Open(ctx, config.StorePath)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer func() { _ = stateStore.Close() }()

	activeStates, err := stateStore.DocumentStates(ctx, config.Scope, sourceID, app.DocumentListOptions{IncludeDeleted: false})
	if err != nil {
		t.Fatalf("read active states: %v", err)
	}
	if len(activeStates) != 2 {
		t.Errorf("active states count = %d, want 2", len(activeStates))
	}
	activeIDs := map[domain.DocumentID]bool{}
	for _, s := range activeStates {
		activeIDs[s.DocumentID] = true
	}
	if !activeIDs[domain.DocumentID(gitTestDocIntro)] || !activeIDs[domain.DocumentID(gitTestDocNew)] {
		t.Errorf("expected intro and new in active states, got: %+v", activeIDs)
	}
	if activeIDs[domain.DocumentID(gitTestDocGuide)] {
		t.Errorf("guide doc must not be active")
	}

	guideState, err := stateStore.DocumentState(ctx, config.Scope, sourceID, domain.DocumentID(gitTestDocGuide))
	if err != nil {
		t.Fatalf("read guide doc state: %v", err)
	}
	if !guideState.Deleted {
		t.Errorf("expected guide doc to be marked deleted, got Deleted = false")
	}

	// 4. Incomplete Scan Safety: error during ref resolution or listing preserves catalog state
	lister.setErr(sourcegit.WrapClassified(sourcegit.ClassReachability, errors.New("remote host unreachable"), "probe failed"))
	failedResult, err := host.SyncSource(ctx, sourceID)
	if err == nil {
		t.Fatal("expected error on failed sync")
	}
	if failedResult.Run.Status != domain.SyncStatusFailed {
		t.Errorf("failed sync status = %s, want failed", failedResult.Run.Status)
	}
	if failedResult.Run.FailureClass != sourcegit.ClassReachability {
		t.Errorf("failed sync failure_class = %s, want %s", failedResult.Run.FailureClass, sourcegit.ClassReachability)
	}

	// Verify status retains previous checkpoint S2
	statusAfterFailure, err := host.SourceStatus(ctx, sourceID)
	if err != nil {
		t.Fatalf("SourceStatus error: %v", err)
	}
	if statusAfterFailure.Checkpoint != snapshot2SHA {
		t.Errorf("status checkpoint after aborted scan = %s, want %s", statusAfterFailure.Checkpoint, snapshot2SHA)
	}

	// Incomplete scan must NOT delete existing documents
	rawIntroRetained, okIntro := runtimeRawDocumentContent(t, host, config.Scope, sourceID, gitTestDocIntro)
	rawNewRetained, okNew := runtimeRawDocumentContent(t, host, config.Scope, sourceID, gitTestDocNew)
	if !okIntro || !okNew {
		t.Fatalf("incomplete scan deleted active documents: intro=%v, new=%v", okIntro, okNew)
	}
	if !strings.Contains(string(rawIntroRetained), "Updated welcome.") || !strings.Contains(string(rawNewRetained), "Newly added document.") {
		t.Errorf("document content corrupted after failed scan")
	}

	// 5. A non-fast-forward remote update is rejected before catalog mutation.
	divergentTreeHash := storeMemTree(t, storer, &object.Tree{})
	divergentHash := storeMemCommit(t, storer, divergentTreeHash, commit1Hash)
	lister.setRef(gitTestBranchRef, divergentHash)
	rewritten, err := host.SyncSource(ctx, sourceID)
	if err == nil {
		t.Fatal("expected non-fast-forward history to be rejected")
	}
	if rewritten.Run.FailureClass != sourcegit.ClassRejectedHistoryRewrite {
		t.Fatalf("history rewrite failure_class = %q, want %q", rewritten.Run.FailureClass, sourcegit.ClassRejectedHistoryRewrite)
	}
	if rewritten.Run.Checkpoint != divergentHash.String() {
		t.Fatalf("rejected attempt checkpoint = %q, want %q", rewritten.Run.Checkpoint, divergentHash)
	}
	statusAfterRewrite, statusErr := host.SourceStatus(ctx, sourceID)
	if statusErr != nil || statusAfterRewrite.Checkpoint != snapshot2SHA || statusAfterRewrite.AttemptCheckpoint != divergentHash.String() {
		t.Fatalf("status after history rewrite = %#v, %v", statusAfterRewrite, statusErr)
	}

	// 6. Explicit branch rewrite policy adopts only the future checkpoint.
	if err := host.Stop(ctx); err != nil {
		t.Fatalf("stop default-policy host: %v", err)
	}
	rewriteSource := gitSource
	rewriteConfig := *gitSource.Config.Git
	rewriteConfig.AllowRewrite = true
	rewriteSource.Config.Git = &rewriteConfig
	rewriteHostConfig := config
	rewriteHostConfig.Sources = []domain.Source{rewriteSource}
	rewriteHost, err := knowl.New(ctx, knowl.Options{
		Config: rewriteHostConfig, Maintainer: provider.Fixture{},
		SourceAdapters: map[domain.SourceType]app.SourceAdapter{domain.SourceTypeGit: adapter},
	})
	if err != nil {
		t.Fatalf("create rewrite-policy host: %v", err)
	}
	defer func() { _ = rewriteHost.Stop(context.Background()) }()
	adopted, err := rewriteHost.SyncSource(ctx, sourceID)
	if err != nil || adopted.Run.Checkpoint != divergentHash.String() || adopted.Run.Status != domain.SyncStatusSucceeded {
		t.Fatalf("allow_rewrite sync = %#v, %v", adopted, err)
	}
}

func TestGitSourceRuntime_CacheRecovery(t *testing.T) {
	// Verify that deleting the bare repository cache directory recovers transparently
	originDir := t.TempDir()
	originRepo, err := gogit.PlainInit(originDir, true)
	if err != nil {
		t.Fatalf("PlainInit origin: %v", err)
	}

	blobHash := storeMemBlob(t, originRepo.Storer, []byte("# Cache Recovery Document\n"))
	treeHash := storeMemTree(t, originRepo.Storer, &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "doc.md", Mode: filemode.Regular, Hash: blobHash},
		},
	})
	commitHash := storeMemCommit(t, originRepo.Storer, treeHash)

	refName := plumbing.ReferenceName(gitTestBranchRef)
	ref := plumbing.NewReferenceFromStrings(refName.String(), commitHash.String())
	if err := originRepo.Storer.SetReference(ref); err != nil {
		t.Fatalf("set origin ref: %v", err)
	}
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, refName)
	if err := originRepo.Storer.SetReference(headRef); err != nil {
		t.Fatalf("set origin HEAD: %v", err)
	}

	cacheRoot := t.TempDir()
	cacheMgr := sourcegit.NewCacheManager(cacheRoot, nil)

	source := domain.Source{
		ID:   "cached-source",
		Type: domain.SourceTypeGit,
		Config: domain.SourceConfig{
			Git: &domain.GitSourceConfig{
				Remote:  originDir,
				Ref:     "main",
				RefKind: domain.GitRefKindBranch,
			},
		},
	}

	ctx := context.Background()

	// Initial clone into cache
	repo1, err := cacheMgr.OpenOrClone(ctx, source)
	if err != nil {
		t.Fatalf("initial clone: %v", err)
	}
	if repo1 == nil {
		t.Fatal("nil repo returned from cache")
	}

	sourceCacheDir := filepath.Join(cacheRoot, string(source.ID))
	if _, err := os.Stat(sourceCacheDir); os.IsNotExist(err) {
		t.Fatalf("cache directory %s does not exist", sourceCacheDir)
	}

	// Delete cache directory to simulate corruption / eviction
	if err := os.RemoveAll(sourceCacheDir); err != nil {
		t.Fatalf("remove cache dir: %v", err)
	}

	// OpenOrClone must transparently re-clone
	repo2, err := cacheMgr.OpenOrClone(ctx, source)
	if err != nil {
		t.Fatalf("recovery clone: %v", err)
	}
	if repo2 == nil {
		t.Fatal("nil repo returned after recovery")
	}
	if _, err := os.Stat(sourceCacheDir); os.IsNotExist(err) {
		t.Fatalf("cache directory %s was not recreated", sourceCacheDir)
	}
}

func TestGitSourceRuntime_TagMovePolicy(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	storer := memory.NewStorage()
	repo, err := gogit.Init(storer, nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyTree := storeMemTree(t, storer, &object.Tree{})
	firstCommit := storeMemCommit(t, storer, emptyTree)
	secondCommit := storeMemCommit(t, storer, emptyTree, firstCommit)
	lister := &stubGitRemoteLister{}
	lister.setRef("refs/tags/v1", firstCommit)
	adapter, err := sourcegit.NewAdapter(sourcegit.DefaultLimits(), sourcegit.NewRefResolver(lister), &stubGitRepoOpener{repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	source := domain.Source{
		ID: "git-release", Type: domain.SourceTypeGit, Enabled: true,
		Config: domain.SourceConfig{Git: &domain.GitSourceConfig{
			Remote: gitTestRemoteURL, Ref: "v1", RefKind: domain.GitRefKindTag, Include: []string{gitTestMarkdown},
		}},
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.Sources = []domain.Source{source}
	newHost := func(source domain.Source) *knowl.Host {
		t.Helper()
		config.Sources = []domain.Source{source}
		host, hostErr := knowl.New(ctx, knowl.Options{
			Config: config, Maintainer: provider.Fixture{},
			SourceAdapters: map[domain.SourceType]app.SourceAdapter{domain.SourceTypeGit: adapter},
		})
		if hostErr != nil {
			t.Fatal(hostErr)
		}
		return host
	}

	host := newHost(source)
	initial, err := host.SyncSource(ctx, source.ID)
	if err != nil || initial.Run.Checkpoint != firstCommit.String() {
		t.Fatalf("initial tag sync = %#v, %v", initial, err)
	}
	if err := host.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	lister.setRef("refs/tags/v1", secondCommit)
	host = newHost(source)
	rejected, err := host.SyncSource(ctx, source.ID)
	if err == nil || rejected.Run.FailureClass != sourcegit.ClassMovedTag || rejected.Run.Checkpoint != secondCommit.String() {
		t.Fatalf("moved tag rejection = %#v, %v", rejected, err)
	}
	status, statusErr := host.SourceStatus(ctx, source.ID)
	if statusErr != nil || status.Checkpoint != firstCommit.String() || status.AttemptCheckpoint != secondCommit.String() {
		t.Fatalf("moved tag status = %#v, %v", status, statusErr)
	}
	if err := host.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	rebound := source
	reboundGit := *source.Config.Git
	reboundGit.RebindAck = true
	rebound.Config.Git = &reboundGit
	host = newHost(rebound)
	t.Cleanup(func() { _ = host.Stop(context.Background()) })
	adopted, err := host.SyncSource(ctx, source.ID)
	if err != nil || adopted.Run.Status != domain.SyncStatusSucceeded || adopted.Run.Checkpoint != secondCommit.String() {
		t.Fatalf("acknowledged tag move = %#v, %v", adopted, err)
	}
}
