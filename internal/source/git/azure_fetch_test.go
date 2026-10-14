package git

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
)

// This serial test registers an isolated protocol before parallel tests run.
// The server reproduces Azure's empty response without multi_ack_detailed and
// go-git's unsupported incremental acknowledgement negotiation.
func TestAzureSSHCacheRefresh(t *testing.T) {
	client := NewRemoteClient()
	const protocol = "knowl-azure-test"
	gitclient.InstallProtocol(protocol, azureTestTransport{Transport: server.NewClient(server.DefaultLoader)})
	t.Cleanup(func() { delete(gitclient.Protocols, protocol) })
	for _, host := range []string{"ssh.dev.azure.com", "git.example.com"} {
		t.Run(host, func(t *testing.T) {
			remoteDir := t.TempDir()
			origin, err := gogit.PlainInit(remoteDir, true)
			if err != nil {
				t.Fatal(err)
			}
			refName := plumbing.ReferenceName("refs/heads/wikiMaster")
			if err := origin.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, refName)); err != nil {
				t.Fatal(err)
			}
			source := knowl.Source{ID: "wiki", Type: knowl.SourceTypeGit, Config: knowl.SourceConfig{Git: &knowl.GitSourceConfig{Remote: protocol + "://" + host + remoteDir, Ref: refName.String(), RefKind: knowl.GitRefKindBranch}}}
			manager := NewCacheManager(t.TempDir(), client)
			var previous plumbing.Hash
			for _, text := range []string{"# First revision", "# Second revision", "# Third revision"} {
				hash := storeAzureTestCommit(t, origin, text, previous)
				if err := origin.Storer.SetReference(plumbing.NewHashReference(refName, hash)); err != nil {
					t.Fatal(err)
				}
				resolved, err := NewRefResolver(client).ResolveRemoteRef(context.Background(), *source.Config.Git)
				if err != nil {
					t.Fatal(err)
				}
				repo, err := manager.Refresh(context.Background(), source, *resolved)
				if err != nil {
					t.Fatalf("refresh %q: %v", text, err)
				}
				commit, err := repo.CommitObject(hash)
				if err != nil {
					t.Fatal(err)
				}
				tree, err := commit.Tree()
				if err != nil {
					t.Fatal(err)
				}
				file, err := tree.File("README.md")
				if err != nil {
					t.Fatal(err)
				}
				contents, err := file.Contents()
				if err != nil {
					t.Fatal(err)
				}
				if contents != text {
					t.Fatalf("contents = %q, want %q", contents, text)
				}
				if !previous.IsZero() {
					if err := NewRefResolver(client).ValidateHistory(repo.Storer, *source.Config.Git, previous.String(), hash); err != nil {
						t.Fatalf("validate incremental history: %v", err)
					}
				}
				if _, err := manager.Refresh(context.Background(), source, *resolved); err != nil {
					t.Fatalf("unchanged refresh: %v", err)
				}
				previous = hash
			}
			source.Config.Git.MaxTransferBytes = 1
			_, err = NewCacheManager(t.TempDir(), client).Refresh(context.Background(), source, ResolvedRef{Name: refName, Hash: previous})
			if ClassOfError(err) != ClassResourceLimit {
				t.Fatalf("limited refresh error = %v, want resource_limit", err)
			}
		})
	}
}

type azureTestTransport struct{ transport.Transport }

func (t azureTestTransport) NewUploadPackSession(endpoint *transport.Endpoint, auth transport.AuthMethod) (transport.UploadPackSession, error) {
	session, err := t.Transport.NewUploadPackSession(endpoint, auth)
	if err != nil {
		return nil, err
	}
	// Retain the original host while routing the test through an in-process
	// server instead of a network connection or private Azure credentials.
	endpointCopy := *endpoint
	endpointCopy.Protocol = sshProtocol
	bounded := limitingTransport{base: azureTestSessionTransport{session: session, azure: strings.EqualFold(endpoint.Host, "ssh.dev.azure.com")}}
	return bounded.NewUploadPackSession(&endpointCopy, auth)
}

type azureTestSessionTransport struct {
	transport.Transport
	session transport.UploadPackSession
	azure   bool
}

func (t azureTestSessionTransport) NewUploadPackSession(_ *transport.Endpoint, _ transport.AuthMethod) (transport.UploadPackSession, error) {
	return azureTestSession{UploadPackSession: t.session, azure: t.azure}, nil
}

type azureTestSession struct {
	transport.UploadPackSession
	azure bool
}

var errAzureTestNegotiation = errors.New("unsupported acknowledgement negotiation")

func (s azureTestSession) UploadPack(ctx context.Context, request *packp.UploadPackRequest) (*packp.UploadPackResponse, error) {
	if s.azure {
		if !request.Capabilities.Supports(capability.MultiACKDetailed) {
			return nil, transport.ErrEmptyUploadPackRequest
		}
		if len(request.Haves) != 0 {
			return nil, errAzureTestNegotiation
		}
	} else if request.Capabilities.Supports(capability.MultiACKDetailed) {
		return nil, errAzureTestNegotiation
	}
	// The embedded server emits a decoded pack. Its supported capabilities omit
	// acknowledgement negotiation, so remove that wire capability on its copy.
	copyRequest := *request
	copyRequest.Capabilities = capability.NewList()
	for _, c := range request.Capabilities.All() {
		if c != capability.MultiACKDetailed {
			if err := copyRequest.Capabilities.Set(c, request.Capabilities.Get(c)...); err != nil {
				return nil, err
			}
		}
	}
	return s.UploadPackSession.UploadPack(ctx, &copyRequest)
}

func storeAzureTestCommit(t *testing.T, repo *gogit.Repository, text string, parent plumbing.Hash) plumbing.Hash {
	t.Helper()
	blob := repo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	w, err := blob.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, text); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	blobHash, err := repo.Storer.SetEncodedObject(blob)
	if err != nil {
		t.Fatal(err)
	}
	tree := &object.Tree{Entries: []object.TreeEntry{{Name: "README.md", Mode: filemode.Regular, Hash: blobHash}}}
	encoded := repo.Storer.NewEncodedObject()
	if err := tree.Encode(encoded); err != nil {
		t.Fatal(err)
	}
	treeHash, err := repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	signature := object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1, 0).UTC()}
	commit := &object.Commit{TreeHash: treeHash, Author: signature, Committer: signature, Message: text}
	if !parent.IsZero() {
		commit.ParentHashes = []plumbing.Hash{parent}
	}
	encoded = repo.Storer.NewEncodedObject()
	if err := commit.Encode(encoded); err != nil {
		t.Fatal(err)
	}
	hash, err := repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
