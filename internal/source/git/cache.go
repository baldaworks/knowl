package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	gogit "github.com/go-git/go-git/v5"
)

const defaultCacheSubdir = ".knowl/cache/git"

// CacheManager manages on-disk bare repository mirror caches.
type CacheManager struct {
	baseDir string
	client  *RemoteClient
}

// NewCacheManager constructs a CacheManager rooted at baseDir.
func NewCacheManager(baseDir string, client *RemoteClient) *CacheManager {
	if strings.TrimSpace(baseDir) == "" {
		baseDir = defaultCacheSubdir
	}
	if client == nil {
		client = NewRemoteClient()
	}
	return &CacheManager{
		baseDir: baseDir,
		client:  client,
	}
}

// OpenOrClone opens an existing bare mirror cache or transparently clones a new one.
func (m *CacheManager) OpenOrClone(ctx context.Context, source knowl.Source) (*gogit.Repository, error) {
	if source.Config.Git == nil || app.ValidateSourceID(source.ID) != nil {
		return nil, app.ErrSourceInvalid
	}
	gitCfg := *source.Config.Git
	dir := filepath.Join(m.baseDir, string(source.ID))

	auth, err := ResolveAuth(gitCfg)
	if err != nil {
		return nil, err
	}

	// Try opening existing repository
	repo, err := gogit.PlainOpen(dir)
	if err == nil {
		fetchErr := repo.FetchContext(ctx, &gogit.FetchOptions{
			RemoteName: "origin",
			Auth:       auth,
			Force:      true,
			Tags:       gogit.AllTags,
		})
		if fetchErr == nil || errors.Is(fetchErr, gogit.NoErrAlreadyUpToDate) {
			return repo, nil
		}

		classified := m.client.classifyTransportError(fetchErr, gitCfg.Remote)
		// Transport failures must preserve the last usable cache. Only errors that
		// identify local object/database damage authorize disposable-cache repair.
		if !isCacheCorruption(fetchErr) {
			return nil, classified
		}
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			return nil, WrapClassified(ClassScanInvalid, removeErr, "failed to replace corrupted Git cache")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			return nil, WrapClassified(ClassScanInvalid, removeErr, "failed to replace unreadable Git cache")
		}
	}

	// Fresh clone
	if err := os.MkdirAll(m.baseDir, 0755); err != nil {
		return nil, WrapClassified(ClassScanInvalid, err, fmt.Sprintf("failed to create cache root %s", m.baseDir))
	}

	cloneOpts := &gogit.CloneOptions{
		URL:        gitCfg.Remote,
		Auth:       auth,
		Mirror:     true,
		NoCheckout: true,
		Tags:       gogit.AllTags,
	}

	repo, err = gogit.PlainCloneContext(ctx, dir, true, cloneOpts)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, m.client.classifyTransportError(err, gitCfg.Remote)
	}

	return repo, nil
}

func isCacheCorruption(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "object not found") ||
		strings.Contains(lower, "malformed object") ||
		strings.Contains(lower, "invalid pack") ||
		strings.Contains(lower, "invalid index") ||
		strings.Contains(lower, "corrupt")
}
