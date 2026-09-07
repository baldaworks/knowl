package git_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/baldaworks/knowl/internal/source/git"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

type mockRepoOpener struct {
	repo *gogit.Repository
}

func (m *mockRepoOpener) OpenOrClone(ctx context.Context, source knowl.Source) (*gogit.Repository, error) {
	return m.repo, nil
}

func TestAdapterListPagination(t *testing.T) {
	t.Parallel()

	storer := memory.NewStorage()
	repo, err := gogit.Init(storer, nil)
	if err != nil {
		t.Fatalf("gogit.Init error: %v", err)
	}

	// Create 5 markdown documents
	entries := make([]object.TreeEntry, 0, 5)
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("doc%d.md", i)
		content := fmt.Sprintf("# Content %d", i)
		blobHash := storeBlob(t, storer, []byte(content))
		entries = append(entries, object.TreeEntry{
			Name: name,
			Mode: filemode.Regular,
			Hash: blobHash,
		})
	}

	tree := &object.Tree{Entries: entries}
	treeHash := storeTree(t, storer, tree)
	commitHash := storeCommitWithTree(t, storer, treeHash)

	// Create branch ref in repo
	refName := plumbing.ReferenceName("refs/heads/main")
	ref := plumbing.NewReferenceFromStrings(refName.String(), commitHash.String())
	if err := storer.SetReference(ref); err != nil {
		t.Fatalf("set reference: %v", err)
	}

	// Remote mock configuration
	_, err = repo.CreateRemote(&gogitconfig.RemoteConfig{
		Name: "origin",
		URLs: []string{testRemoteMain},
	})
	if err != nil {
		t.Fatalf("create remote: %v", err)
	}

	limits := git.DefaultLimits()
	limits.PageSize = 2

	client := git.NewRemoteClient()
	resolver := git.NewRefResolver(client)
	adapter, err := git.NewAdapter(limits, resolver, &mockRepoOpener{repo: repo})
	if err != nil {
		t.Fatalf("NewAdapter error: %v", err)
	}

	source := knowl.Source{
		ID:   "test-git-source",
		Type: knowl.SourceTypeGit,
		Config: knowl.SourceConfig{
			Git: &knowl.GitSourceConfig{
				Remote:  testRemoteMain,
				Ref:     "main",
				RefKind: knowl.GitRefKindBranch,
				Include: []string{"*.md"},
			},
		},
	}

	// We can manually test pagination using a pageToken that has the snapshot hash!
	firstToken, err := git.EncodePageTokenForTest(1, commitHash.String(), "")
	if err != nil {
		t.Fatalf("encode token: %v", err)
	}

	// Fetch Page 1
	page1, err := adapter.List(context.Background(), source, firstToken)
	if err != nil {
		t.Fatalf("List page 1 error: %v", err)
	}
	if len(page1.Documents) != 2 {
		t.Fatalf("page 1 documents = %d, want 2", len(page1.Documents))
	}
	if page1.Documents[0].Path != "doc0.md" || page1.Documents[1].Path != "doc1.md" {
		t.Errorf("unexpected page 1 documents: %+v", page1.Documents)
	}
	if page1.NextPageToken == "" {
		t.Fatal("expected non-empty NextPageToken on page 1")
	}

	// Fetch Page 2
	page2, err := adapter.List(context.Background(), source, page1.NextPageToken)
	if err != nil {
		t.Fatalf("List page 2 error: %v", err)
	}
	if len(page2.Documents) != 2 {
		t.Fatalf("page 2 documents = %d, want 2", len(page2.Documents))
	}
	if page2.Documents[0].Path != "doc2.md" || page2.Documents[1].Path != "doc3.md" {
		t.Errorf("unexpected page 2 documents: %+v", page2.Documents)
	}
	if page2.NextPageToken == "" {
		t.Fatal("expected non-empty NextPageToken on page 2")
	}

	// Fetch Page 3 (Final)
	page3, err := adapter.List(context.Background(), source, page2.NextPageToken)
	if err != nil {
		t.Fatalf("List page 3 error: %v", err)
	}
	if len(page3.Documents) != 1 {
		t.Fatalf("page 3 documents = %d, want 1", len(page3.Documents))
	}
	if page3.Documents[0].Path != "doc4.md" {
		t.Errorf("unexpected page 3 documents: %+v", page3.Documents)
	}
	if page3.NextPageToken != "" {
		t.Errorf("expected empty NextPageToken on last page, got %q", page3.NextPageToken)
	}

	// Fetch with invalid token fails
	_, err = adapter.List(context.Background(), source, "invalid-base64-payload!!!")
	if err == nil {
		t.Fatal("expected error with invalid page token")
	}
	if git.ClassOfError(err) != git.ClassScanInvalid {
		t.Errorf("class = %q, want %q", git.ClassOfError(err), git.ClassScanInvalid)
	}
}

func TestAdapterFetch(t *testing.T) {
	t.Parallel()

	storer := memory.NewStorage()
	repo, err := gogit.Init(storer, nil)
	if err != nil {
		t.Fatalf("gogit.Init error: %v", err)
	}

	rawContent := "# Markdown Title\n\nSome documentation body."
	blobHash := storeBlob(t, storer, []byte(rawContent))
	docsTreeHash := storeTree(t, storer, &object.Tree{Entries: []object.TreeEntry{{Name: "readme.md", Mode: filemode.Regular, Hash: blobHash}}})
	rootTreeHash := storeTree(t, storer, &object.Tree{Entries: []object.TreeEntry{{Name: "docs", Mode: filemode.Dir, Hash: docsTreeHash}}})
	commitHash := storeCommitWithTree(t, storer, rootTreeHash)

	adapter, err := git.NewAdapter(git.DefaultLimits(), nil, &mockRepoOpener{repo: repo})
	if err != nil {
		t.Fatalf("NewAdapter error: %v", err)
	}

	source := knowl.Source{
		ID:   "my-git-source",
		Type: knowl.SourceTypeGit,
		Config: knowl.SourceConfig{
			Git: &knowl.GitSourceConfig{
				Remote:  testRemoteMain,
				URIBase: "https://github.com/org/repo/blob/main",
			},
		},
	}

	ref := knowl.DocumentRef{
		ExternalID: "docs/readme.md",
		Path:       "docs/readme.md",
		Revision:   blobHash.String(),
		Metadata:   map[string]string{"snapshot": commitHash.String()},
	}

	// 1. Fetch valid document
	doc, err := adapter.Fetch(context.Background(), source, ref)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if string(doc.Content) != rawContent {
		t.Errorf("content = %q, want %q", string(doc.Content), rawContent)
	}
	if doc.Title != "Markdown Title" {
		t.Errorf("title = %q, want %q", doc.Title, "Markdown Title")
	}
	if doc.MediaType != "text/markdown" {
		t.Errorf("mediaType = %q, want text/markdown", doc.MediaType)
	}
	wantURI := "https://github.com/org/repo/blob/main/" + commitHash.String() + "/docs/readme.md"
	if doc.URI != wantURI {
		t.Errorf("URI = %q, want %q", doc.URI, wantURI)
	}

	// 2. Fetch without URIBase falls back to knowl://
	sourceNoBase := source
	sourceNoBase.Config.Git = &knowl.GitSourceConfig{
		Remote: testRemoteMain,
	}
	docNoBase, err := adapter.Fetch(context.Background(), sourceNoBase, ref)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if docNoBase.URI != "knowl://sources/my-git-source/docs/readme.md" {
		t.Errorf("URI fallback = %q, want knowl://sources/my-git-source/docs/readme.md", docNoBase.URI)
	}

	// 3. Exceed MaxFileBytes
	strictLimits := git.DefaultLimits()
	strictLimits.MaxFileBytes = 5
	strictAdapter, _ := git.NewAdapter(strictLimits, nil, &mockRepoOpener{repo: repo})
	_, err = strictAdapter.Fetch(context.Background(), source, ref)
	if err == nil {
		t.Fatal("expected error exceeding MaxFileBytes")
	}
	if git.ClassOfError(err) != git.ClassResourceLimit {
		t.Errorf("class = %q, want %q", git.ClassOfError(err), git.ClassResourceLimit)
	}

	// 4. Missing blob
	missingRef := ref
	missingRef.Revision = plumbing.NewHash("9999999999999999999999999999999999999999").String()
	_, err = adapter.Fetch(context.Background(), source, missingRef)
	if err == nil {
		t.Fatal("expected error for missing blob")
	}
	if git.ClassOfError(err) != git.ClassFetch {
		t.Errorf("class = %q, want %q", git.ClassOfError(err), git.ClassFetch)
	}
}
