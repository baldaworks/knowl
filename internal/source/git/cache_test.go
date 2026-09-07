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
			{Name: "README.md", Mode: filemode.Regular, Hash: blobHash},
		},
	})
	commitHash := storeDiskCommit(t, originRepo, treeHash)

	refName := plumbing.ReferenceName("refs/heads/main")
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
				Ref:     "main",
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
		Message:  "origin commit",
		TreeHash: treeHash,
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
