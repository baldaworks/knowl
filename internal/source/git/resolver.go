package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
)

// RepositoryIdentity returns the credential-free durable lineage digest for a
// normalized config, and safely derives it for explicitly composed adapters.
func RepositoryIdentity(config knowl.GitSourceConfig) string {
	if len(config.RepositoryID) == 64 {
		if _, err := hex.DecodeString(config.RepositoryID); err == nil {
			return strings.ToLower(config.RepositoryID)
		}
	}
	identity := strings.TrimSpace(config.RepositoryID)
	if identity == "" {
		identity = strings.TrimSpace(config.Remote)
	}
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}

// ResolvedRef is the resolved remote reference name and immutable commit hash.
type ResolvedRef struct {
	Name plumbing.ReferenceName
	Hash plumbing.Hash
}

// RemoteRefLister defines the interface for querying remote repository references.
type RemoteRefLister interface {
	ListRemoteRefs(ctx context.Context, config knowl.GitSourceConfig) ([]*plumbing.Reference, error)
}

// RefResolver resolves remote tracked refs to immutable commit snapshots and verifies history integrity.
type RefResolver struct {
	client RemoteRefLister
}

// NewRefResolver constructs a new RefResolver.
func NewRefResolver(client RemoteRefLister) *RefResolver {
	if client == nil {
		client = NewRemoteClient()
	}
	return &RefResolver{client: client}
}

// ResolveRemoteRef probes the remote repository references and binds the tracked ref to an immutable commit SHA.
func (r *RefResolver) ResolveRemoteRef(ctx context.Context, config knowl.GitSourceConfig) (*ResolvedRef, error) {
	refs, err := r.client.ListRemoteRefs(ctx, config)
	if err != nil {
		return nil, err
	}
	return FindTargetRef(refs, config)
}

// FindTargetRef selects the matching ref from a list of remote references, resolving peeled tags when applicable.
func FindTargetRef(refs []*plumbing.Reference, config knowl.GitSourceConfig) (*ResolvedRef, error) {
	refMap := make(map[string]plumbing.Hash, len(refs))
	peeledMap := make(map[string]plumbing.Hash)

	for _, ref := range refs {
		name := ref.Name().String()
		if strings.HasSuffix(name, "^{}") {
			base := strings.TrimSuffix(name, "^{}")
			peeledMap[base] = ref.Hash()
		} else {
			refMap[name] = ref.Hash()
		}
	}

	candidates := candidateRefNames(config)
	for _, cand := range candidates {
		if peeledHash, ok := peeledMap[cand]; ok {
			return &ResolvedRef{
				Name: plumbing.ReferenceName(cand),
				Hash: peeledHash,
			}, nil
		}
		if directHash, ok := refMap[cand]; ok {
			return &ResolvedRef{
				Name: plumbing.ReferenceName(cand),
				Hash: directHash,
			}, nil
		}
	}

	return nil, WrapClassified(ClassMissingRef, ErrMissingRef, fmt.Sprintf("tracked git ref %q not found on remote", config.Ref))
}

func candidateRefNames(config knowl.GitSourceConfig) []string {
	ref := strings.TrimSpace(config.Ref)
	switch config.RefKind {
	case knowl.GitRefKindBranch:
		if strings.HasPrefix(ref, "refs/heads/") {
			return []string{ref}
		}
		return []string{"refs/heads/" + ref, ref}
	case knowl.GitRefKindTag:
		if strings.HasPrefix(ref, "refs/tags/") {
			return []string{ref}
		}
		return []string{"refs/tags/" + ref, ref}
	default:
		if strings.HasPrefix(ref, "refs/heads/") || strings.HasPrefix(ref, "refs/tags/") {
			return []string{ref}
		}
		return []string{
			"refs/heads/" + ref,
			"refs/tags/" + ref,
			ref,
		}
	}
}

// CheckLineage enforces remote URL and repository identity consistency.
func (r *RefResolver) CheckLineage(config knowl.GitSourceConfig, prevRemote, prevRepoID string) error {
	if config.RebindAck {
		return nil
	}

	if prevRemote != "" && prevRemote != config.Remote {
		return WrapClassified(ClassRepositoryIdentityMismatch, ErrRepositoryIdentityMismatch,
			fmt.Sprintf("remote URL changed from %s to %s without rebind authorization", RedactURL(prevRemote), RedactURL(config.Remote)))
	}

	if prevRepoID != "" && config.RepositoryID != "" && prevRepoID != config.RepositoryID {
		return WrapClassified(ClassRepositoryIdentityMismatch, ErrRepositoryIdentityMismatch,
			fmt.Sprintf("repository ID changed from %s to %s without rebind authorization", prevRepoID, config.RepositoryID))
	}

	return nil
}

// ValidateHistory verifies that advancing to targetHash complies with history integrity rules.
func (r *RefResolver) ValidateHistory(storer storage.Storer, config knowl.GitSourceConfig, prevCheckpoint string, targetHash plumbing.Hash) error {
	prevCheckpoint = strings.TrimSpace(prevCheckpoint)
	if prevCheckpoint == "" {
		return nil
	}

	if prevCheckpoint == targetHash.String() {
		return nil
	}

	isTag := config.RefKind == knowl.GitRefKindTag || strings.HasPrefix(config.Ref, "refs/tags/")
	if isTag {
		if !config.RebindAck {
			return WrapClassified(ClassMovedTag, ErrMovedTag,
				fmt.Sprintf("tracked tag %s changed target from %s to %s without rebind authorization", config.Ref, prevCheckpoint, targetHash.String()))
		}
		return nil
	}

	// For branch refs, verify fast-forward / commit ancestry
	if config.AllowRewrite {
		return nil
	}

	prevHash := plumbing.NewHash(prevCheckpoint)
	if prevHash.IsZero() {
		return WrapClassified(ClassScanInvalid, ErrScanInvalid, "invalid previous checkpoint hash format")
	}

	prevCommit, err := object.GetCommit(storer, prevHash)
	if err != nil {
		return WrapClassified(ClassRejectedHistoryRewrite, ErrRejectedHistoryRewrite,
			fmt.Sprintf("previous checkpoint commit %s not found in repository (possible history rewrite)", prevCheckpoint))
	}

	targetCommit, err := object.GetCommit(storer, targetHash)
	if err != nil {
		return WrapClassified(ClassScanInvalid, err, fmt.Sprintf("target commit %s not found in repository", targetHash.String()))
	}

	isAncestor, err := prevCommit.IsAncestor(targetCommit)
	if err != nil {
		return WrapClassified(ClassRejectedHistoryRewrite, err,
			fmt.Sprintf("ancestry check failed between %s and %s: %v", prevCheckpoint, targetHash.String(), err))
	}

	if !isAncestor {
		return WrapClassified(ClassRejectedHistoryRewrite, ErrRejectedHistoryRewrite,
			fmt.Sprintf("non-fast-forward branch update: previous checkpoint %s is not an ancestor of %s", prevCheckpoint, targetHash.String()))
	}

	return nil
}
