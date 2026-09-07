package git_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/source/git"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

const (
	testTreeFileReadme   = "README.md"
	testTreeFileGuide    = "docs/guide.md"
	testTreeFileCode     = "src/main.go"
	testTreeFileLFS      = "large-asset.md"
	testTreeFileSymlink  = "symlink.md"
	testTreeFileSubmod   = "submodule-ref"
	testLFSContentString = "version https://git-lfs.github.com/spec/v1\noid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1218da6f7982f6c62314027\nsize 12345\n"
)

func TestTreeWalker(t *testing.T) {
	t.Parallel()

	storer := memory.NewStorage()

	// Blobs
	readmeBlobHash := storeBlob(t, storer, []byte("# Hello Knowl"))
	guideBlobHash := storeBlob(t, storer, []byte("## Guide Content"))
	codeBlobHash := storeBlob(t, storer, []byte("package main"))
	lfsBlobHash := storeBlob(t, storer, []byte(testLFSContentString))

	// Sub-tree for docs/
	docsTree := &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "guide.md", Mode: filemode.Regular, Hash: guideBlobHash},
		},
	}
	docsTreeHash := storeTree(t, storer, docsTree)

	// Sub-tree for src/
	srcTree := &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "main.go", Mode: filemode.Regular, Hash: codeBlobHash},
		},
	}
	srcTreeHash := storeTree(t, storer, srcTree)

	// Root tree
	rootTree := &object.Tree{
		Entries: []object.TreeEntry{
			{Name: testTreeFileReadme, Mode: filemode.Regular, Hash: readmeBlobHash},
			{Name: "docs", Mode: filemode.Dir, Hash: docsTreeHash},
			{Name: "src", Mode: filemode.Dir, Hash: srcTreeHash},
			{Name: testTreeFileLFS, Mode: filemode.Regular, Hash: lfsBlobHash},
			{Name: testTreeFileSymlink, Mode: filemode.Symlink, Hash: readmeBlobHash},
			{Name: testTreeFileSubmod, Mode: filemode.Submodule, Hash: readmeBlobHash},
		},
	}
	rootTreeHash := storeTree(t, storer, rootTree)

	commitHash := storeCommitWithTree(t, storer, rootTreeHash)

	t.Run("walk with markdown include pattern", func(t *testing.T) {
		t.Parallel()
		walker := git.NewTreeWalker(git.DefaultLimits())
		matchers, err := git.CompileMatchers([]string{"**/*.md"})
		if err != nil {
			t.Fatalf("CompileMatchers error: %v", err)
		}

		docs, err := walker.Walk(context.Background(), storer, commitHash, matchers)
		if err != nil {
			t.Fatalf("Walk error: %v", err)
		}

		// Expected: README.md and docs/guide.md.
		// Excluded: src/main.go (doesn't match), large-asset.md (LFS pointer), symlink.md (symlink), submodule-ref (submodule).
		if len(docs) != 2 {
			t.Fatalf("got %d documents, want 2", len(docs))
		}

		if docs[0].Path != testTreeFileReadme || docs[0].Revision != readmeBlobHash.String() {
			t.Errorf("doc[0] = %+v, want %s", docs[0], testTreeFileReadme)
		}
		if docs[1].Path != testTreeFileGuide || docs[1].Revision != guideBlobHash.String() {
			t.Errorf("doc[1] = %+v, want %s", docs[1], testTreeFileGuide)
		}

		if docs[0].Metadata["snapshot"] != commitHash.String() {
			t.Errorf("doc[0].Metadata[snapshot] = %s, want %s", docs[0].Metadata["snapshot"], commitHash)
		}
		if docs[0].Metadata["blob_sha"] != readmeBlobHash.String() {
			t.Errorf("doc[0].Metadata[blob_sha] = %s, want %s", docs[0].Metadata["blob_sha"], readmeBlobHash)
		}
	})

	t.Run("max visited limit exceeded", func(t *testing.T) {
		t.Parallel()
		limits := git.DefaultLimits()
		limits.MaxVisited = 1
		walker := git.NewTreeWalker(limits)
		matchers, _ := git.CompileMatchers(nil)

		_, err := walker.Walk(context.Background(), storer, commitHash, matchers)
		if err == nil {
			t.Fatal("expected error exceeding MaxVisited")
		}
		if git.ClassOfError(err) != git.ClassResourceLimit {
			t.Errorf("class = %q, want %q", git.ClassOfError(err), git.ClassResourceLimit)
		}
		if !errors.Is(err, git.ErrLimit) {
			t.Errorf("expected errors.Is(err, ErrLimit)")
		}
	})

	t.Run("directory entries consume visited bound", func(t *testing.T) {
		t.Parallel()
		limits := git.DefaultLimits()
		// Root README plus docs directory exhaust the budget before docs/guide.
		limits.MaxVisited = 2
		walker := git.NewTreeWalker(limits)
		matchers, _ := git.CompileMatchers([]string{"docs/*.md"})
		_, err := walker.Walk(context.Background(), storer, commitHash, matchers)
		if git.ClassOfError(err) != git.ClassResourceLimit {
			t.Fatalf("Walk() directory-bound error = %v, want resource limit", err)
		}
	})

	t.Run("max documents limit exceeded", func(t *testing.T) {
		t.Parallel()
		limits := git.DefaultLimits()
		limits.MaxDocuments = 1
		walker := git.NewTreeWalker(limits)
		matchers, _ := git.CompileMatchers([]string{"**/*.md"})

		_, err := walker.Walk(context.Background(), storer, commitHash, matchers)
		if err == nil {
			t.Fatal("expected error exceeding MaxDocuments")
		}
		if git.ClassOfError(err) != git.ClassResourceLimit {
			t.Errorf("class = %q, want %q", git.ClassOfError(err), git.ClassResourceLimit)
		}
		if !errors.Is(err, git.ErrLimit) {
			t.Errorf("expected errors.Is(err, ErrLimit)")
		}
	})
}

func storeBlob(t *testing.T, s *memory.Storage, data []byte) plumbing.Hash {
	t.Helper()
	obj := s.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		t.Fatalf("blob writer: %v", err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("blob write: %v", err)
	}
	_ = w.Close()
	h, err := s.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("set blob object: %v", err)
	}
	return h
}

func storeTree(t *testing.T, s *memory.Storage, tree *object.Tree) plumbing.Hash {
	t.Helper()
	sort.Sort(object.TreeEntrySorter(tree.Entries))
	obj := s.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		t.Fatalf("encode tree: %v", err)
	}
	h, err := s.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("set tree object: %v", err)
	}
	return h
}

func storeCommitWithTree(t *testing.T, s *memory.Storage, treeHash plumbing.Hash) plumbing.Hash {
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
		Message:  "commit with tree",
		TreeHash: treeHash,
	}
	obj := s.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	h, err := s.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("set commit object: %v", err)
	}
	return h
}
