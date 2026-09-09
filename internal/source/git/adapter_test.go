package git_test

import (
	"context"
	"errors"
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

const testIncludeMarkdown = "*.md"

type mockRepoOpener struct {
	repo         *gogit.Repository
	refreshCalls int
	localCalls   int
}

type recoveringRepoOpener struct {
	repo         *gogit.Repository
	localErr     error
	refreshCalls int
	localCalls   int
}

func (o *recoveringRepoOpener) OpenOrClone(context.Context, knowl.Source) (*gogit.Repository, error) {
	o.refreshCalls++
	return o.repo, nil
}

func (o *recoveringRepoOpener) OpenCached(context.Context, knowl.Source) (*gogit.Repository, error) {
	o.localCalls++
	if o.localErr != nil {
		err := o.localErr
		o.localErr = nil
		return nil, err
	}
	return o.repo, nil
}

func (m *mockRepoOpener) OpenOrClone(ctx context.Context, source knowl.Source) (*gogit.Repository, error) {
	m.refreshCalls++
	return m.repo, nil
}

func (m *mockRepoOpener) OpenCached(ctx context.Context, source knowl.Source) (*gogit.Repository, error) {
	m.localCalls++
	return m.repo, nil
}

type stubRemoteRefLister struct {
	refs []*plumbing.Reference
}

func (s stubRemoteRefLister) ListRemoteRefs(context.Context, knowl.GitSourceConfig) ([]*plumbing.Reference, error) {
	return s.refs, nil
}

func TestAdapterRefreshesOnceThenReadsLocally(t *testing.T) {
	t.Parallel()

	storer := memory.NewStorage()
	repo, err := gogit.Init(storer, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobHash := storeBlob(t, storer, []byte("# Pinned\n"))
	treeHash := storeTree(t, storer, &object.Tree{Entries: []object.TreeEntry{{Name: "doc.md", Mode: filemode.Regular, Hash: blobHash}}})
	commitHash := storeCommitWithTree(t, storer, treeHash)
	opener := &mockRepoOpener{repo: repo}
	resolver := git.NewRefResolver(stubRemoteRefLister{refs: []*plumbing.Reference{
		plumbing.NewHashReference("refs/heads/main", commitHash),
	}})
	adapter, err := git.NewAdapter(git.DefaultLimits(), resolver, opener)
	if err != nil {
		t.Fatal(err)
	}
	source := knowl.Source{ID: "prepared-source", Type: knowl.SourceTypeGit, Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
		Remote: testRemoteMain, Ref: testRefBranchMain, RefKind: knowl.GitRefKindBranch, Include: []string{testIncludeMarkdown},
	}}}

	prepared, err := adapter.PrepareSnapshot(context.Background(), source, "")
	if err != nil {
		t.Fatalf("PrepareSnapshot() error = %v", err)
	}
	page, err := adapter.List(context.Background(), source, prepared.PageToken)
	if err != nil || len(page.Documents) != 1 {
		t.Fatalf("List() = %#v, %v", page, err)
	}
	for i := 0; i < 2; i++ {
		doc, fetchErr := adapter.Fetch(context.Background(), source, page.Documents[0])
		if fetchErr != nil || string(doc.Content) != "# Pinned\n" {
			t.Fatalf("Fetch() = %#v, %v", doc, fetchErr)
		}
	}
	if opener.refreshCalls != 1 || opener.localCalls != 3 {
		t.Fatalf("repository calls = %d refresh, %d local; want 1, 3", opener.refreshCalls, opener.localCalls)
	}
}

func TestAdapterResumedListRecoversCacheAndKeepsPinnedSnapshot(t *testing.T) {
	t.Parallel()

	storer := memory.NewStorage()
	repo, err := gogit.Init(storer, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstBlob := storeBlob(t, storer, []byte("# First pinned\n"))
	secondBlob := storeBlob(t, storer, []byte("# Second pinned\n"))
	pinnedTree := storeTree(t, storer, &object.Tree{Entries: []object.TreeEntry{
		{Name: "first.md", Mode: filemode.Regular, Hash: firstBlob},
		{Name: "second.md", Mode: filemode.Regular, Hash: secondBlob},
	}})
	pinnedCommit := storeCommitWithTree(t, storer, pinnedTree)
	currentBlob := storeBlob(t, storer, []byte("# Current\n"))
	currentTree := storeTree(t, storer, &object.Tree{Entries: []object.TreeEntry{
		{Name: "current.md", Mode: filemode.Regular, Hash: currentBlob},
	}})
	_ = storeCommitWithTree(t, storer, currentTree)

	opener := &recoveringRepoOpener{repo: repo, localErr: errors.New("cache missing")}
	limits := git.DefaultLimits()
	limits.PageSize = 1
	adapter, err := git.NewAdapter(limits, nil, opener)
	if err != nil {
		t.Fatal(err)
	}
	source := knowl.Source{ID: "resumed-source", Type: knowl.SourceTypeGit, Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
		Remote: testRemoteMain, Include: []string{testIncludeMarkdown},
	}}}
	token, err := git.EncodePageTokenForTest(1, pinnedCommit.String(), "")
	if err != nil {
		t.Fatal(err)
	}

	firstPage, err := adapter.List(context.Background(), source, token)
	if err != nil {
		t.Fatalf("List(first page) error = %v", err)
	}
	if len(firstPage.Documents) != 1 || firstPage.Documents[0].Path != "first.md" {
		t.Fatalf("List(first page) documents = %#v, want pinned first.md", firstPage.Documents)
	}
	secondPage, err := adapter.List(context.Background(), source, firstPage.NextPageToken)
	if err != nil {
		t.Fatalf("List(second page) error = %v", err)
	}
	if len(secondPage.Documents) != 1 || secondPage.Documents[0].Path != "second.md" {
		t.Fatalf("List(second page) documents = %#v, want pinned second.md", secondPage.Documents)
	}
	doc, err := adapter.Fetch(context.Background(), source, firstPage.Documents[0])
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if string(doc.Content) != "# First pinned\n" {
		t.Fatalf("Fetch() content = %q, want pinned content", doc.Content)
	}
	if opener.refreshCalls != 1 || opener.localCalls != 3 {
		t.Fatalf("repository calls = %d refresh, %d local; want 1, 3", opener.refreshCalls, opener.localCalls)
	}
}

func TestAdapterResumedListFailsWhenRecoveredCacheLacksPinnedSnapshot(t *testing.T) {
	t.Parallel()

	storer := memory.NewStorage()
	repo, err := gogit.Init(storer, nil)
	if err != nil {
		t.Fatal(err)
	}
	currentBlob := storeBlob(t, storer, []byte("# Current\n"))
	currentTree := storeTree(t, storer, &object.Tree{Entries: []object.TreeEntry{
		{Name: "current.md", Mode: filemode.Regular, Hash: currentBlob},
	}})
	_ = storeCommitWithTree(t, storer, currentTree)

	opener := &recoveringRepoOpener{repo: repo, localErr: errors.New("cache missing")}
	adapter, err := git.NewAdapter(git.DefaultLimits(), nil, opener)
	if err != nil {
		t.Fatal(err)
	}
	source := knowl.Source{ID: "resumed-source", Type: knowl.SourceTypeGit, Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{
		Remote: testRemoteMain, Include: []string{testIncludeMarkdown},
	}}}
	missingSnapshot := plumbing.NewHash("1111111111111111111111111111111111111111")
	token, err := git.EncodePageTokenForTest(1, missingSnapshot.String(), "")
	if err != nil {
		t.Fatal(err)
	}

	page, err := adapter.List(context.Background(), source, token)
	if err == nil {
		t.Fatalf("List() = %#v, want pinned-snapshot error", page)
	}
	if git.ClassOfError(err) != git.ClassScanInvalid {
		t.Fatalf("List() class = %q, want %q", git.ClassOfError(err), git.ClassScanInvalid)
	}
	if opener.refreshCalls != 1 || opener.localCalls != 1 {
		t.Fatalf("repository calls = %d refresh, %d local; want 1, 1", opener.refreshCalls, opener.localCalls)
	}
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
				Ref:     testRefBranchMain,
				RefKind: knowl.GitRefKindBranch,
				Include: []string{testIncludeMarkdown},
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
