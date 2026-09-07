package git_test

import (
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/source/git"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

const (
	testRemoteMain    = "https://github.com/org/repo.git"
	testRemoteNew     = "https://github.com/org/new-repo.git"
	testRemoteOld     = "https://github.com/org/old-repo.git"
	testRefBranchMain = "main"
	testRefTagV1      = "v1.0"
	testAuthorName    = "Test"
	testAuthorEmail   = "test@example.com"
)

func TestFindTargetRef(t *testing.T) {
	t.Parallel()

	hashMain := plumbing.NewHash("1111111111111111111111111111111111111111")
	hashTagObj := plumbing.NewHash("2222222222222222222222222222222222222222")
	hashTagPeeled := plumbing.NewHash("3333333333333333333333333333333333333333")

	refs := []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", hashMain.String()),
		plumbing.NewReferenceFromStrings("refs/tags/v1.0", hashTagObj.String()),
		plumbing.NewReferenceFromStrings("refs/tags/v1.0^{}", hashTagPeeled.String()),
	}

	t.Run("resolve branch short name", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch}
		res, err := git.FindTargetRef(refs, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Hash != hashMain {
			t.Errorf("got hash %s, want %s", res.Hash, hashMain)
		}
		if res.Name != "refs/heads/main" {
			t.Errorf("got ref %s, want refs/heads/main", res.Name)
		}
	})

	t.Run("resolve peeled tag", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{Ref: testRefTagV1, RefKind: knowl.GitRefKindTag}
		res, err := git.FindTargetRef(refs, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Hash != hashTagPeeled {
			t.Errorf("got peeled hash %s, want %s", res.Hash, hashTagPeeled)
		}
	})

	t.Run("missing ref fails with missing_ref", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{Ref: "nonexistent", RefKind: knowl.GitRefKindBranch}
		_, err := git.FindTargetRef(refs, cfg)
		if err == nil {
			t.Fatal("expected error for missing ref")
		}
		if git.ClassOfError(err) != git.ClassMissingRef {
			t.Errorf("class = %q, want %q", git.ClassOfError(err), git.ClassMissingRef)
		}
		if !errors.Is(err, git.ErrMissingRef) {
			t.Errorf("expected errors.Is(err, ErrMissingRef)")
		}
	})
}

func TestCheckLineage(t *testing.T) {
	t.Parallel()

	resolver := git.NewRefResolver(nil)

	t.Run("matching remote and repo ID succeeds", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{
			Remote:       testRemoteMain,
			RepositoryID: "repo-123",
		}
		if err := resolver.CheckLineage(cfg, testRemoteMain, "repo-123"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("remote mismatch fails without rebind_ack", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{
			Remote:    testRemoteNew,
			RebindAck: false,
		}
		err := resolver.CheckLineage(cfg, testRemoteOld, "")
		if err == nil {
			t.Fatal("expected error for remote mismatch")
		}
		if git.ClassOfError(err) != git.ClassRepositoryIdentityMismatch {
			t.Errorf("class = %q, want %q", git.ClassOfError(err), git.ClassRepositoryIdentityMismatch)
		}
	})

	t.Run("remote mismatch succeeds with rebind_ack", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{
			Remote:    testRemoteNew,
			RebindAck: true,
		}
		if err := resolver.CheckLineage(cfg, testRemoteOld, ""); err != nil {
			t.Fatalf("unexpected error with rebind_ack: %v", err)
		}
	})

	t.Run("repo ID mismatch fails without rebind_ack", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{
			Remote:       testRemoteMain,
			RepositoryID: "repo-new",
			RebindAck:    false,
		}
		err := resolver.CheckLineage(cfg, testRemoteMain, "repo-old")
		if err == nil {
			t.Fatal("expected error for repo ID mismatch")
		}
		if git.ClassOfError(err) != git.ClassRepositoryIdentityMismatch {
			t.Errorf("class = %q, want %q", git.ClassOfError(err), git.ClassRepositoryIdentityMismatch)
		}
	})
}

func TestValidateHistory(t *testing.T) {
	t.Parallel()

	storer := memory.NewStorage()

	// Create root commit C1
	c1Hash := commitInStorage(t, storer, "initial commit", nil)

	// Create child commit C2 (fast forward from C1)
	c2Hash := commitInStorage(t, storer, "second commit", []plumbing.Hash{c1Hash})

	// Create divergent commit D1 (from C1, not descendant of C2)
	d1Hash := commitInStorage(t, storer, "divergent commit", []plumbing.Hash{c1Hash})

	resolver := git.NewRefResolver(nil)

	t.Run("first sync without checkpoint succeeds", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch}
		if err := resolver.ValidateHistory(storer, cfg, "", c1Hash); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("unchanged checkpoint succeeds", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch}
		if err := resolver.ValidateHistory(storer, cfg, c1Hash.String(), c1Hash); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("fast forward advance succeeds", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch}
		if err := resolver.ValidateHistory(storer, cfg, c1Hash.String(), c2Hash); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("divergent branch fails as rejected_history_rewrite", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch, AllowRewrite: false}
		// Try moving from C2 to D1
		err := resolver.ValidateHistory(storer, cfg, c2Hash.String(), d1Hash)
		if err == nil {
			t.Fatal("expected error for non-fast-forward push")
		}
		if git.ClassOfError(err) != git.ClassRejectedHistoryRewrite {
			t.Errorf("class = %q, want %q", git.ClassOfError(err), git.ClassRejectedHistoryRewrite)
		}
		if !errors.Is(err, git.ErrRejectedHistoryRewrite) {
			t.Errorf("expected errors.Is(err, ErrRejectedHistoryRewrite)")
		}
	})

	t.Run("divergent branch succeeds when allow_rewrite is true", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch, AllowRewrite: true}
		if err := resolver.ValidateHistory(storer, cfg, c2Hash.String(), d1Hash); err != nil {
			t.Fatalf("unexpected error with allow_rewrite: %v", err)
		}
	})

	t.Run("tag moved fails without rebind_ack", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{Ref: testRefTagV1, RefKind: knowl.GitRefKindTag, RebindAck: false}
		err := resolver.ValidateHistory(storer, cfg, c1Hash.String(), c2Hash)
		if err == nil {
			t.Fatal("expected error for tag movement")
		}
		if git.ClassOfError(err) != git.ClassMovedTag {
			t.Errorf("class = %q, want %q", git.ClassOfError(err), git.ClassMovedTag)
		}
		if !errors.Is(err, git.ErrMovedTag) {
			t.Errorf("expected errors.Is(err, ErrMovedTag)")
		}
	})

	t.Run("tag moved succeeds with rebind_ack", func(t *testing.T) {
		t.Parallel()
		cfg := knowl.GitSourceConfig{Ref: testRefTagV1, RefKind: knowl.GitRefKindTag, RebindAck: true}
		if err := resolver.ValidateHistory(storer, cfg, c1Hash.String(), c2Hash); err != nil {
			t.Fatalf("unexpected error for tag move with rebind_ack: %v", err)
		}
	})
}

func commitInStorage(t *testing.T, s *memory.Storage, msg string, parents []plumbing.Hash) plumbing.Hash {
	t.Helper()

	// Empty tree
	emptyTree := &object.Tree{}
	treeObj := s.NewEncodedObject()
	if err := emptyTree.Encode(treeObj); err != nil {
		t.Fatalf("encode tree: %v", err)
	}
	treeHash, err := s.SetEncodedObject(treeObj)
	if err != nil {
		t.Fatalf("save tree: %v", err)
	}

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
		Message:      msg,
		TreeHash:     treeHash,
		ParentHashes: parents,
	}

	commitObj := s.NewEncodedObject()
	if err := commit.Encode(commitObj); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	h, err := s.SetEncodedObject(commitObj)
	if err != nil {
		t.Fatalf("save commit: %v", err)
	}
	return h
}
