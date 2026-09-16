package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/source/git"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	testRepositoryA = "repository-a"
	testRepositoryB = "repository-b"
)

func TestCacheManager(t *testing.T) {
	t.Parallel()

	// Create a local Git repository to serve as the remote origin
	originDir := t.TempDir()
	originRepo, err := gogit.PlainInit(originDir, true)
	if err != nil {
		t.Fatalf("PlainInit origin: %v", err)
	}

	// Add an initial commit to origin
	blobHash := storeDiskBlob(t, originRepo, []byte("# Origin Doc"))
	treeHash := storeDiskTree(t, originRepo, &object.Tree{
		Entries: []object.TreeEntry{
			{Name: testTreeFileReadme, Mode: filemode.Regular, Hash: blobHash},
		},
	})
	commitHash := storeDiskCommit(t, originRepo, treeHash)

	refName := plumbing.ReferenceName(testRefFullBranchMain)
	ref := plumbing.NewReferenceFromStrings(refName.String(), commitHash.String())
	if err := originRepo.Storer.SetReference(ref); err != nil {
		t.Fatalf("set origin reference: %v", err)
	}

	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, refName)
	if err := originRepo.Storer.SetReference(headRef); err != nil {
		t.Fatalf("set HEAD reference: %v", err)
	}

	cacheRoot := t.TempDir()
	mgr := git.NewCacheManager(cacheRoot, nil)

	source := knowl.Source{
		ID:   "my-cached-source",
		Type: knowl.SourceTypeGit,
		Config: knowl.SourceConfig{
			Git: &knowl.GitSourceConfig{
				Remote:  originDir,
				Ref:     testRefBranchMain,
				RefKind: knowl.GitRefKindBranch,
			},
		},
	}

	ctx := context.Background()

	// 1. Initial clone
	repo1, err := mgr.OpenOrClone(ctx, source)
	if err != nil {
		t.Fatalf("initial OpenOrClone error: %v", err)
	}
	if repo1 == nil {
		t.Fatal("expected non-nil repo on initial clone")
	}

	cachedPath := filepath.Join(cacheRoot, string(source.ID))
	if _, err := os.Stat(cachedPath); os.IsNotExist(err) {
		t.Fatalf("expected cache dir %s to exist", cachedPath)
	}
	if cachedRepo, err := mgr.OpenCached(ctx, source); err != nil || cachedRepo == nil {
		t.Fatalf("OpenCached() = %#v, %v", cachedRepo, err)
	}

	// 2. Open existing cache (idempotent / incremental)
	repo2, err := mgr.OpenOrClone(ctx, source)
	if err != nil {
		t.Fatalf("second OpenOrClone error: %v", err)
	}
	if repo2 == nil {
		t.Fatal("expected non-nil repo on second open")
	}

	// 3. Delete cache directory -> transparent recovery
	if err := os.RemoveAll(cachedPath); err != nil {
		t.Fatalf("delete cache dir: %v", err)
	}
	if _, err := mgr.OpenCached(ctx, source); git.ClassOfError(err) != git.ClassScanInvalid {
		t.Fatalf("OpenCached() missing cache error = %v, want scan invalid", err)
	}
	if _, err := os.Stat(cachedPath); !os.IsNotExist(err) {
		t.Fatalf("OpenCached() recreated missing cache: %v", err)
	}

	repo3, err := mgr.OpenOrClone(ctx, source)
	if err != nil {
		t.Fatalf("OpenOrClone after deletion error: %v", err)
	}
	if repo3 == nil {
		t.Fatal("expected non-nil repo after cache recreation")
	}

	// 4. Corrupt cache -> transparent recovery
	// Overwrite a critical file in cache with garbage
	headFile := filepath.Join(cachedPath, "HEAD")
	if err := os.WriteFile(headFile, []byte("GARBAGE_HEAD"), 0600); err != nil {
		t.Fatalf("corrupt HEAD: %v", err)
	}

	repo4, err := mgr.OpenOrClone(ctx, source)
	if err != nil {
		t.Fatalf("OpenOrClone after corruption error: %v", err)
	}
	if repo4 == nil {
		t.Fatal("expected non-nil repo after recovery from corruption")
	}
}

func TestCacheManagerRefreshFetchesOnlyResolvedRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sourceID  string
		remoteRef plumbing.ReferenceName
		annotated bool
	}{
		{name: "branch", sourceID: "scoped-branch", remoteRef: testRefFullBranchMain},
		{name: "lightweight tag", sourceID: "scoped-lightweight-tag", remoteRef: "refs/tags/v1"},
		{name: "annotated tag", sourceID: "scoped-annotated-tag", remoteRef: "refs/tags/v1", annotated: true},
		{name: "explicit ref", sourceID: "scoped-explicit-ref", remoteRef: "refs/releases/stable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			remoteDir := t.TempDir()
			remote, err := gogit.PlainInit(remoteDir, true)
			if err != nil {
				t.Fatal(err)
			}
			tracked := storeTestCommit(t, remote, "tracked", nil)
			refTarget := tracked
			if tt.annotated {
				refTarget = storeDiskTag(t, remote, "v1", tracked)
			}
			if err := remote.Storer.SetReference(plumbing.NewHashReference(tt.remoteRef, refTarget)); err != nil {
				t.Fatal(err)
			}
			unrelatedCommits := map[plumbing.ReferenceName]plumbing.Hash{
				"refs/heads/unrelated":      storeTestCommit(t, remote, "unrelated branch", nil),
				"refs/tags/unrelated-light": storeTestCommit(t, remote, "unrelated lightweight tag", nil),
				"refs/notes/review":         storeTestCommit(t, remote, "unrelated notes", nil),
				"refs/releases/unrelated":   storeTestCommit(t, remote, "unrelated namespaced ref", nil),
			}
			for name, hash := range unrelatedCommits {
				if err := remote.Storer.SetReference(plumbing.NewHashReference(name, hash)); err != nil {
					t.Fatal(err)
				}
			}
			unrelatedAnnotatedCommit := storeTestCommit(t, remote, "unrelated annotated tag", nil)
			unrelatedTag := storeDiskTag(t, remote, "unrelated-annotated", unrelatedAnnotatedCommit)
			if err := remote.Storer.SetReference(plumbing.NewHashReference("refs/tags/unrelated-annotated", unrelatedTag)); err != nil {
				t.Fatal(err)
			}

			source := testGitSource(tt.sourceID, remoteDir)
			cached, err := git.NewCacheManager(t.TempDir(), nil).Refresh(context.Background(), source, git.ResolvedRef{
				Name: tt.remoteRef,
				Hash: tracked,
			})
			if err != nil {
				t.Fatalf("Refresh() error: %v", err)
			}
			if _, err := cached.CommitObject(tracked); err != nil {
				t.Fatalf("tracked commit unavailable: %v", err)
			}
			for name, hash := range unrelatedCommits {
				if _, err := cached.CommitObject(hash); err == nil {
					t.Fatalf("unrelated commit from %s was fetched", name)
				}
			}
			if _, err := cached.CommitObject(unrelatedAnnotatedCommit); err == nil {
				t.Fatal("unrelated annotated-tag commit was fetched")
			}
			if _, err := cached.TagObject(unrelatedTag); err == nil {
				t.Fatal("unrelated annotated-tag object was fetched")
			}
			assertScopedCacheConfig(t, cached, tt.remoteRef)
			assertOnlyScopedCacheRefs(t, cached)
		})
	}
}

func TestCacheManagerRefreshPreservesTrackedHistoryOnly(t *testing.T) {
	t.Parallel()

	remoteDir := t.TempDir()
	remote, err := gogit.PlainInit(remoteDir, true)
	if err != nil {
		t.Fatal(err)
	}
	mainRef := plumbing.ReferenceName(testRefFullBranchMain)
	base := storeTestCommit(t, remote, "base", nil)
	first := storeTestCommit(t, remote, "first", []plumbing.Hash{base})
	if err := remote.Storer.SetReference(plumbing.NewHashReference(mainRef, first)); err != nil {
		t.Fatal(err)
	}
	manager := git.NewCacheManager(t.TempDir(), nil)
	source := testGitSource("incremental-scoped", remoteDir)
	if _, err := manager.Refresh(context.Background(), source, git.ResolvedRef{Name: mainRef, Hash: first}); err != nil {
		t.Fatalf("initial Refresh(): %v", err)
	}

	second := storeTestCommit(t, remote, "second", []plumbing.Hash{first})
	unrelated := storeTestCommit(t, remote, "unrelated-later", nil)
	if err := remote.Storer.SetReference(plumbing.NewHashReference(mainRef, second)); err != nil {
		t.Fatal(err)
	}
	if err := remote.Storer.SetReference(plumbing.NewHashReference("refs/heads/unrelated", unrelated)); err != nil {
		t.Fatal(err)
	}
	cached, err := manager.Refresh(context.Background(), source, git.ResolvedRef{Name: mainRef, Hash: second})
	if err != nil {
		t.Fatalf("incremental Refresh(): %v", err)
	}
	for _, hash := range []plumbing.Hash{base, first, second} {
		if _, err := cached.CommitObject(hash); err != nil {
			t.Fatalf("tracked history commit %s unavailable: %v", hash, err)
		}
	}
	if _, err := cached.CommitObject(unrelated); err == nil {
		t.Fatal("new unrelated commit was fetched incrementally")
	}
}

func TestCacheManagerRefreshRejectsRefMovementAfterResolution(t *testing.T) {
	t.Parallel()

	remoteDir, first := newCacheTestRemote(t, "first")
	remote, err := gogit.PlainOpen(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	manager := git.NewCacheManager(t.TempDir(), nil)
	source := testGitSource("moved-after-resolution", remoteDir)
	resolved := git.ResolvedRef{Name: testRefFullBranchMain, Hash: first}
	if _, err := manager.Refresh(context.Background(), source, resolved); err != nil {
		t.Fatalf("initial Refresh(): %v", err)
	}

	second := storeTestCommit(t, remote, "second", []plumbing.Hash{first})
	if err := remote.Storer.SetReference(plumbing.NewHashReference(testRefFullBranchMain, second)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Refresh(context.Background(), source, resolved); git.ClassOfError(err) != git.ClassScanInvalid {
		t.Fatalf("Refresh() moved ref error = %v, want scan invalid", err)
	}
}

func TestCacheManagerRefreshMigratesLegacyMirror(t *testing.T) {
	t.Parallel()

	remoteDir, first := newCacheTestRemote(t, "legacy main")
	remote, err := gogit.PlainOpen(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	oldUnrelated := storeTestCommit(t, remote, "old unrelated", nil)
	if err := remote.Storer.SetReference(plumbing.NewHashReference("refs/heads/unrelated", oldUnrelated)); err != nil {
		t.Fatal(err)
	}
	cacheRoot := t.TempDir()
	source := testGitSource("legacy-mirror", remoteDir)
	cacheDir := filepath.Join(cacheRoot, string(source.ID))
	if _, err := gogit.PlainClone(cacheDir, true, &gogit.CloneOptions{URL: remoteDir, Mirror: true, Tags: gogit.AllTags}); err != nil {
		t.Fatalf("create legacy mirror: %v", err)
	}

	second := storeTestCommit(t, remote, "main second", []plumbing.Hash{first})
	newUnrelated := storeTestCommit(t, remote, "new unrelated", nil)
	if err := remote.Storer.SetReference(plumbing.NewHashReference(testRefFullBranchMain, second)); err != nil {
		t.Fatal(err)
	}
	if err := remote.Storer.SetReference(plumbing.NewHashReference("refs/heads/unrelated", newUnrelated)); err != nil {
		t.Fatal(err)
	}
	cached, err := git.NewCacheManager(cacheRoot, nil).Refresh(context.Background(), source, git.ResolvedRef{
		Name: testRefFullBranchMain,
		Hash: second,
	})
	if err != nil {
		t.Fatalf("Refresh() legacy mirror: %v", err)
	}
	if _, err := cached.CommitObject(newUnrelated); err == nil {
		t.Fatal("legacy mirror refresh fetched a new unrelated commit")
	}
	assertScopedCacheConfig(t, cached, testRefFullBranchMain)
	legacyRef, err := cached.Reference("refs/heads/unrelated", true)
	if err != nil || legacyRef.Hash() != oldUnrelated {
		t.Fatalf("legacy unrelated ref = %v, %v; want preserved at %s", legacyRef, err, oldUnrelated)
	}
}

func TestCacheManagerRejectsUnsafeSourceID(t *testing.T) {
	t.Parallel()
	cacheRoot := t.TempDir()
	mgr := git.NewCacheManager(cacheRoot, nil)
	source := knowl.Source{
		ID: "../outside", Type: knowl.SourceTypeGit,
		Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{Remote: testRemoteMain}},
	}
	if _, err := mgr.OpenOrClone(context.Background(), source); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("OpenOrClone() unsafe source ID = %v, want invalid source", err)
	}
	if _, err := mgr.OpenCached(context.Background(), source); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("OpenCached() unsafe source ID = %v, want invalid source", err)
	}
}

func TestCacheManagerHonorsRemoteRebind(t *testing.T) {
	t.Parallel()

	remoteA, commitA := newCacheTestRemote(t, "# Remote A")
	remoteB, commitB := newCacheTestRemote(t, "# Remote B")
	cacheRoot := t.TempDir()
	manager := git.NewCacheManager(cacheRoot, nil)
	source := knowl.Source{
		ID: "rebound-source", Type: knowl.SourceTypeGit,
		Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
			Remote: remoteA, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
		}},
	}
	if _, err := manager.OpenOrClone(context.Background(), source); err != nil {
		t.Fatalf("clone remote A: %v", err)
	}

	rebound := source
	rebound.Config.Git = &knowl.GitSourceConfig{
		Remote: remoteB, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
	}
	if _, err := manager.OpenOrClone(context.Background(), rebound); git.ClassOfError(err) != git.ClassRepositoryIdentityMismatch {
		t.Fatalf("unacknowledged rebind error = %v, want identity mismatch", err)
	}
	cachedA, err := manager.OpenCached(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	originA, err := cachedA.Remote("origin")
	if err != nil || len(originA.Config().URLs) != 1 || originA.Config().URLs[0] != remoteA {
		t.Fatalf("cache changed after rejected rebind: %#v, %v", originA, err)
	}

	rebound.Config.Git.RebindAck = true
	cachedB, err := manager.OpenOrClone(context.Background(), rebound)
	if err != nil {
		t.Fatalf("acknowledged rebind: %v", err)
	}
	originB, err := cachedB.Remote("origin")
	if err != nil || len(originB.Config().URLs) != 1 || originB.Config().URLs[0] != remoteB {
		t.Fatalf("rebound origin = %#v, %v", originB, err)
	}
	if _, err := cachedB.CommitObject(commitB); err != nil {
		t.Fatalf("remote B commit unavailable: %v", err)
	}
	if _, err := cachedB.CommitObject(commitA); err == nil {
		t.Fatal("remote A commit remained in replaced cache")
	}
}

func TestCacheManagerRebindReplacesFullCache(t *testing.T) {
	t.Parallel()

	remoteA, _ := newCacheTestRemote(t, "# Remote A")
	remoteB, commitB := newCacheTestRemote(t, "# Remote B")
	cacheRoot := t.TempDir()
	manager := git.NewCacheManager(cacheRoot, nil)
	source := knowl.Source{
		ID: "full-rebound-source", Type: knowl.SourceTypeGit,
		Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
			Remote: remoteA, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
		}},
	}
	if _, err := manager.OpenOrClone(context.Background(), source); err != nil {
		t.Fatalf("clone remote A: %v", err)
	}

	cacheBytes := directoryFileBytes(t, filepath.Join(cacheRoot, string(source.ID)))
	probeRoot := t.TempDir()
	probeSource := source
	probeSource.Config.Git = &knowl.GitSourceConfig{
		Remote: remoteB, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
	}
	if _, err := git.NewCacheManager(probeRoot, nil).OpenOrClone(context.Background(), probeSource); err != nil {
		t.Fatalf("measure remote B cache: %v", err)
	}
	cacheLimit := max(cacheBytes, directoryFileBytes(t, filepath.Join(probeRoot, string(source.ID))))
	rebound := source
	rebound.Config.Git = &knowl.GitSourceConfig{
		Remote: remoteB, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
		RebindAck: true, MaxCacheBytes: cacheLimit,
	}
	cachedB, err := manager.OpenOrClone(context.Background(), rebound)
	if err != nil {
		t.Fatalf("acknowledged rebind at full capacity: %v", err)
	}
	if _, err := cachedB.CommitObject(commitB); err != nil {
		t.Fatalf("remote B commit unavailable: %v", err)
	}
}

func TestCacheManagerRepositoryIDRebindReplacesFullCache(t *testing.T) {
	t.Parallel()

	remote, _ := newCacheTestRemote(t, "# Shared remote")
	cacheRoot := t.TempDir()
	manager := git.NewCacheManager(cacheRoot, nil)
	source := knowl.Source{
		ID: "identity-rebound-source", Type: knowl.SourceTypeGit,
		Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
			Remote: remote, RepositoryID: testRepositoryA, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
		}},
	}
	cachedA, err := manager.OpenOrClone(context.Background(), source)
	if err != nil {
		t.Fatalf("clone repository A identity: %v", err)
	}
	staleBlob := storeDiskBlob(t, cachedA, []byte("stale cache object"))

	cacheBytes := directoryFileBytes(t, filepath.Join(cacheRoot, string(source.ID)))
	rebound := source
	rebound.Config.Git = &knowl.GitSourceConfig{
		Remote: remote, RepositoryID: testRepositoryB, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
		MaxCacheBytes: cacheBytes,
	}
	if _, err := manager.OpenOrClone(context.Background(), rebound); git.ClassOfError(err) != git.ClassRepositoryIdentityMismatch {
		t.Fatalf("unacknowledged repository ID rebind error = %v, want identity mismatch", err)
	}
	rebound.Config.Git.RebindAck = true
	cachedB, err := manager.OpenOrClone(context.Background(), rebound)
	if err != nil {
		t.Fatalf("acknowledged repository ID rebind at full capacity: %v", err)
	}
	if _, err := cachedB.BlobObject(staleBlob); err == nil {
		t.Fatal("stale object remained after repository ID rebind")
	}
}

func TestCacheManagerRecoversMalformedRepositoryIdentity(t *testing.T) {
	t.Parallel()

	remote, _ := newCacheTestRemote(t, "# Recover metadata")
	cacheRoot := t.TempDir()
	manager := git.NewCacheManager(cacheRoot, nil)
	source := knowl.Source{
		ID: "corrupt-identity-source", Type: knowl.SourceTypeGit,
		Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
			Remote: remote, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
		}},
	}
	source.Config.Git.RepositoryID = git.RepositoryIdentity(*source.Config.Git)
	cached, err := manager.OpenOrClone(context.Background(), source)
	if err != nil {
		t.Fatalf("initial clone: %v", err)
	}
	staleBlob := storeDiskBlob(t, cached, []byte("stale cache object"))
	cacheDir := filepath.Join(cacheRoot, string(source.ID))
	if err := os.WriteFile(filepath.Join(cacheDir, "knowl.repository-identity"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := manager.OpenOrClone(context.Background(), source)
	if err != nil {
		t.Fatalf("recover malformed repository identity: %v", err)
	}
	if _, err := recovered.BlobObject(staleBlob); err == nil {
		t.Fatal("stale object remained after malformed metadata recovery")
	}
}

func TestCacheManagerOpenCachedRejectsDifferentLineage(t *testing.T) {
	t.Parallel()

	remoteA, _ := newCacheTestRemote(t, "# Remote A")
	remoteB, _ := newCacheTestRemote(t, "# Remote B")
	cacheRoot := t.TempDir()
	manager := git.NewCacheManager(cacheRoot, nil)
	source := knowl.Source{
		ID: "cached-lineage-source", Type: knowl.SourceTypeGit,
		Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
			Remote: remoteA, RepositoryID: testRepositoryA, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
		}},
	}
	if _, err := manager.OpenOrClone(context.Background(), source); err != nil {
		t.Fatalf("initial clone: %v", err)
	}

	changedRemote := source
	changedRemote.Config.Git = &knowl.GitSourceConfig{
		Remote: remoteB, RepositoryID: testRepositoryA, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
	}
	if _, err := manager.OpenCached(context.Background(), changedRemote); git.ClassOfError(err) != git.ClassRepositoryIdentityMismatch {
		t.Fatalf("changed remote error = %v, want identity mismatch", err)
	}

	changedIdentity := source
	changedIdentity.Config.Git = &knowl.GitSourceConfig{
		Remote: remoteA, RepositoryID: testRepositoryB, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
	}
	if _, err := manager.OpenCached(context.Background(), changedIdentity); git.ClassOfError(err) != git.ClassRepositoryIdentityMismatch {
		t.Fatalf("changed repository ID error = %v, want identity mismatch", err)
	}
}

func TestCacheManagerMalformedRepositoryIdentityRequiresExplicitRebindAck(t *testing.T) {
	t.Parallel()

	remote, _ := newCacheTestRemote(t, "# Protected metadata")
	cacheRoot := t.TempDir()
	manager := git.NewCacheManager(cacheRoot, nil)
	source := knowl.Source{
		ID: "corrupt-explicit-identity-source", Type: knowl.SourceTypeGit,
		Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
			Remote: remote, RepositoryID: testRepositoryA, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
		}},
	}
	cached, err := manager.OpenOrClone(context.Background(), source)
	if err != nil {
		t.Fatalf("initial clone: %v", err)
	}
	staleBlob := storeDiskBlob(t, cached, []byte("stale cache object"))
	cacheDir := filepath.Join(cacheRoot, string(source.ID))
	if err := os.WriteFile(filepath.Join(cacheDir, "knowl.repository-identity"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}

	rebound := source
	rebound.Config.Git = &knowl.GitSourceConfig{
		Remote: remote, RepositoryID: testRepositoryB, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch,
	}
	if _, err := manager.OpenOrClone(context.Background(), rebound); git.ClassOfError(err) != git.ClassRepositoryIdentityMismatch {
		t.Fatalf("unacknowledged recovery error = %v, want identity mismatch", err)
	}
	if _, err := cached.BlobObject(staleBlob); err != nil {
		t.Fatalf("cache changed after rejected recovery: %v", err)
	}

	rebound.Config.Git.RebindAck = true
	recovered, err := manager.OpenOrClone(context.Background(), rebound)
	if err != nil {
		t.Fatalf("acknowledged recovery: %v", err)
	}
	if _, err := recovered.BlobObject(staleBlob); err == nil {
		t.Fatal("stale object remained after acknowledged recovery")
	}
}

func directoryFileBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	if err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	}); err != nil {
		t.Fatalf("measure cache: %v", err)
	}
	return total
}

func newCacheTestRemote(t *testing.T, content string) (string, plumbing.Hash) {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	blobHash := storeDiskBlob(t, repo, []byte(content))
	treeHash := storeDiskTree(t, repo, &object.Tree{Entries: []object.TreeEntry{
		{Name: testTreeFileReadme, Mode: filemode.Regular, Hash: blobHash},
	}})
	commitHash := storeDiskCommit(t, repo, treeHash)
	refName := plumbing.ReferenceName(testRefFullBranchMain)
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, commitHash)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, refName)); err != nil {
		t.Fatal(err)
	}
	return dir, commitHash
}

func TestCacheManagerEnforcesTransferAndDiskLimits(t *testing.T) {
	t.Parallel()

	originDir := t.TempDir()
	originRepo, err := gogit.PlainInit(originDir, true)
	if err != nil {
		t.Fatal(err)
	}
	blobHash := storeDiskBlob(t, originRepo, make([]byte, 64<<10))
	treeHash := storeDiskTree(t, originRepo, &object.Tree{Entries: []object.TreeEntry{{Name: "large.md", Mode: filemode.Regular, Hash: blobHash}}})
	commitHash := storeDiskCommit(t, originRepo, treeHash)
	refName := plumbing.ReferenceName(testRefFullBranchMain)
	if err := originRepo.Storer.SetReference(plumbing.NewHashReference(refName, commitHash)); err != nil {
		t.Fatal(err)
	}
	if err := originRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, refName)); err != nil {
		t.Fatal(err)
	}

	t.Run("pack transfer", func(t *testing.T) {
		cacheRoot := t.TempDir()
		source := knowl.Source{ID: "transfer-limited", Type: knowl.SourceTypeGit, Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
			Remote: originDir, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch, MaxTransferBytes: 1, MaxCacheBytes: 1 << 20,
		}}}
		_, err := git.NewCacheManager(cacheRoot, nil).OpenOrClone(context.Background(), source)
		if git.ClassOfError(err) != git.ClassResourceLimit {
			t.Fatalf("OpenOrClone() transfer error = %v, want resource limit", err)
		}
		if _, statErr := os.Stat(filepath.Join(cacheRoot, string(source.ID))); !os.IsNotExist(statErr) {
			t.Fatalf("partial cache remains after transfer limit: %v", statErr)
		}
	})

	t.Run("existing cache disk usage", func(t *testing.T) {
		cacheRoot := t.TempDir()
		source := knowl.Source{ID: "disk-limited", Type: knowl.SourceTypeGit, Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
			Remote: originDir, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch, MaxTransferBytes: 1 << 20, MaxCacheBytes: 8,
		}}}
		cacheDir := filepath.Join(cacheRoot, string(source.ID))
		if err := os.MkdirAll(cacheDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cacheDir, "oversized"), []byte("0123456789"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := git.NewCacheManager(cacheRoot, nil).OpenOrClone(context.Background(), source)
		if git.ClassOfError(err) != git.ClassResourceLimit {
			t.Fatalf("OpenOrClone() disk error = %v, want resource limit", err)
		}
		if _, statErr := os.Stat(cacheDir); !os.IsNotExist(statErr) {
			t.Fatalf("oversized cache remains after rejection: %v", statErr)
		}
	})
}

func storeDiskBlob(t *testing.T, repo *gogit.Repository, data []byte) plumbing.Hash {
	t.Helper()
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		t.Fatalf("blob writer: %v", err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("blob write: %v", err)
	}
	_ = w.Close()
	h, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("set blob object: %v", err)
	}
	return h
}

func storeDiskTree(t *testing.T, repo *gogit.Repository, tree *object.Tree) plumbing.Hash {
	t.Helper()
	obj := repo.Storer.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		t.Fatalf("encode tree: %v", err)
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("set tree object: %v", err)
	}
	return h
}

func storeDiskCommit(t *testing.T, repo *gogit.Repository, treeHash plumbing.Hash) plumbing.Hash {
	return storeDiskCommitWithParents(t, repo, treeHash, nil)
}

func storeDiskCommitWithParents(t *testing.T, repo *gogit.Repository, treeHash plumbing.Hash, parents []plumbing.Hash) plumbing.Hash {
	t.Helper()
	commit := &object.Commit{
		Author: object.Signature{
			Name:  testAuthorName,
			Email: testAuthorEmail,
			When:  time.Now(),
		},
		Committer: object.Signature{
			Name:  testAuthorName,
			Email: testAuthorEmail,
			When:  time.Now(),
		},
		Message:      "origin commit",
		TreeHash:     treeHash,
		ParentHashes: parents,
	}
	obj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("set commit object: %v", err)
	}
	return h
}

func storeTestCommit(t *testing.T, repo *gogit.Repository, content string, parents []plumbing.Hash) plumbing.Hash {
	t.Helper()
	blobHash := storeDiskBlob(t, repo, []byte(content))
	treeHash := storeDiskTree(t, repo, &object.Tree{Entries: []object.TreeEntry{
		{Name: testTreeFileReadme, Mode: filemode.Regular, Hash: blobHash},
	}})
	return storeDiskCommitWithParents(t, repo, treeHash, parents)
}

func storeDiskTag(t *testing.T, repo *gogit.Repository, name string, target plumbing.Hash) plumbing.Hash {
	t.Helper()
	tag := &object.Tag{
		Name:       name,
		Tagger:     object.Signature{Name: testAuthorName, Email: testAuthorEmail, When: time.Now()},
		Message:    "annotated test tag",
		TargetType: plumbing.CommitObject,
		Target:     target,
	}
	obj := repo.Storer.NewEncodedObject()
	if err := tag.Encode(obj); err != nil {
		t.Fatalf("encode tag: %v", err)
	}
	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("set tag object: %v", err)
	}
	return hash
}

func testGitSource(id, remote string) knowl.Source {
	return knowl.Source{
		ID:   knowl.SourceID(id),
		Type: knowl.SourceTypeGit,
		Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
			Remote:  remote,
			Ref:     testRefBranchMain,
			RefKind: knowl.GitRefKindBranch,
		}},
	}
}

func assertScopedCacheConfig(t *testing.T, repo *gogit.Repository, sourceRef plumbing.ReferenceName) {
	t.Helper()
	origin, err := repo.Remote(gogit.DefaultRemoteName)
	if err != nil {
		t.Fatalf("origin remote: %v", err)
	}
	wantFetch := "+" + sourceRef.String() + ":refs/knowl/tracked"
	config := origin.Config()
	if config.Mirror || len(config.Fetch) != 1 || config.Fetch[0].String() != wantFetch {
		t.Fatalf("origin config = mirror:%t fetch:%v, want mirror:false fetch:[%s]", config.Mirror, config.Fetch, wantFetch)
	}
}

func assertOnlyScopedCacheRefs(t *testing.T, repo *gogit.Repository) {
	t.Helper()
	refs, err := repo.References()
	if err != nil {
		t.Fatalf("cache references: %v", err)
	}
	defer refs.Close()
	if err := refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name() != plumbing.HEAD && ref.Name() != plumbing.ReferenceName("refs/knowl/tracked") {
			t.Errorf("unexpected cached ref %s", ref.Name())
		}
		return nil
	}); err != nil {
		t.Fatalf("iterate cache references: %v", err)
	}
}
