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

const (
	defaultTransferBytes = int64(500 << 20)
	defaultCacheBytes    = int64(512 << 20)
	maxCacheEntries      = 1_000_000
)

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
	transferLimit, cacheLimit := gitResourceLimits(gitCfg)
	cacheBytes, err := cacheUsage(dir, cacheLimit)
	if err != nil {
		if ClassOfError(err) == ClassResourceLimit {
			_ = os.RemoveAll(dir)
		}
		return nil, err
	}
	transferBudget := min(transferLimit, cacheLimit-cacheBytes)
	if transferBudget <= 0 {
		return nil, cacheLimitError("Git cache has no remaining capacity")
	}
	transferCtx := withTransferLimit(ctx, transferBudget)

	auth, err := ResolveAuth(gitCfg)
	if err != nil {
		return nil, err
	}

	// Try opening existing repository
	repo, err := gogit.PlainOpen(dir)
	if err == nil {
		fetchErr := repo.FetchContext(transferCtx, &gogit.FetchOptions{
			RemoteName: "origin",
			Auth:       auth,
			Force:      true,
			Tags:       gogit.AllTags,
		})
		if fetchErr == nil || errors.Is(fetchErr, gogit.NoErrAlreadyUpToDate) {
			if _, usageErr := cacheUsage(dir, cacheLimit); usageErr != nil {
				_ = os.RemoveAll(dir)
				return nil, usageErr
			}
			return repo, nil
		}

		classified := m.client.classifyTransportError(fetchErr, gitCfg.Remote)
		if ClassOfError(classified) == ClassResourceLimit {
			_ = os.RemoveAll(dir)
			return nil, classified
		}
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
	cacheBytes, err = cacheUsage(dir, cacheLimit)
	if err != nil {
		return nil, err
	}
	transferBudget = min(transferLimit, cacheLimit-cacheBytes)
	if transferBudget <= 0 {
		return nil, cacheLimitError("Git cache has no remaining capacity")
	}
	transferCtx = withTransferLimit(ctx, transferBudget)
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

	repo, err = gogit.PlainCloneContext(transferCtx, dir, true, cloneOpts)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, m.client.classifyTransportError(err, gitCfg.Remote)
	}
	if _, err := cacheUsage(dir, cacheLimit); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	return repo, nil
}

func gitResourceLimits(config knowl.GitSourceConfig) (int64, int64) {
	transferLimit, cacheLimit := config.MaxTransferBytes, config.MaxCacheBytes
	if transferLimit <= 0 {
		transferLimit = defaultTransferBytes
	}
	if cacheLimit <= 0 {
		cacheLimit = defaultCacheBytes
	}
	return transferLimit, cacheLimit
}

func cacheUsage(root string, limit int64) (int64, error) {
	var total int64
	entries := 0
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > maxCacheEntries {
			return cacheLimitError("Git cache entry count exceeded safety limit")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > limit {
			return cacheLimitError("Git cache exceeded configured byte limit")
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		if ClassOfError(err) != "" {
			return 0, err
		}
		return 0, WrapClassified(ClassScanInvalid, err, "cannot inspect Git cache usage")
	}
	return total, nil
}

func cacheLimitError(detail string) error {
	return WrapClassified(ClassResourceLimit, ErrLimit, detail)
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
