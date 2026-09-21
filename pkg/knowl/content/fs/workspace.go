// Package fs implements the canonical Knowl workspace adapter.
package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

const (
	schemaFile         = "schema.md"
	workspaceWikiDir   = "wiki"
	workspaceRawDir    = "raw"
	knowlDir           = ".knowl"
	markdownExt        = ".md"
	okfIndexFilename   = "index.md"
	okfLogFilename     = "log.md"
	defaultMaxBytes    = 4 << 20
	canonicalIndexPath = "wiki/index.md"
	canonicalLogPath   = "wiki/log.md"
	recoveryPrepared   = "prepared"
	recoveryCommitted  = "committed"
	recoveryRolledBack = "rolled_back"
	recoveryCompleted  = "completed"
	commitFaultApplied = "applied"
	commitFaultReceipt = "receipt"
	workspaceLockFile  = "workspace.lock"
	lockRetryInterval  = 10 * time.Millisecond
	lockWaitTimeout    = 10 * time.Second
)

// Workspace owns canonical filesystem content for one local Knowl workspace.
type Workspace struct {
	root           string
	maxSourceBytes int
	commitFault    func(point string, index int) error
	exportFault    func(point string) error
	now            func() time.Time
	mu             sync.Mutex
	processLock    *flock.Flock
}

// Option configures a Workspace.
type Option func(*Workspace)

// WithMaxSourceBytes bounds accepted source content.
func WithMaxSourceBytes(maxBytes int) Option {
	return func(workspace *Workspace) {
		if maxBytes > 0 {
			workspace.maxSourceBytes = maxBytes
		}
	}
}

// WithClock supplies the clock used for snapshot capture and derived OKF
// staleness. A nil clock is ignored.
func WithClock(now func() time.Time) Option {
	return func(workspace *Workspace) {
		if now != nil {
			workspace.now = now
		}
	}
}

// New returns a filesystem workspace rooted at root.
func New(root string, options ...Option) (*Workspace, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return nil, fmt.Errorf("workspace root is empty: %w", ErrWorkspaceInvalid)
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	cleanRoot := filepath.Clean(abs)
	workspace := &Workspace{
		root:           cleanRoot,
		maxSourceBytes: defaultMaxBytes,
		now:            time.Now,
		processLock:    flock.New(filepath.Join(cleanRoot, knowlDir, workspaceLockFile)),
	}
	for _, option := range options {
		if option != nil {
			option(workspace)
		}
	}
	return workspace, nil
}

// Root returns the absolute workspace path.
func (workspace *Workspace) Root() string { return workspace.root }

func (workspace *Workspace) lock(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	controlDir := filepath.Join(workspace.root, knowlDir)
	info, err := os.Stat(controlDir)
	if err != nil {
		return nil, fmt.Errorf("inspect workspace control directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace control path is not a directory: %w", ErrWorkspaceInvalid)
	}
	if err := rejectSymlinkPath(workspace.root, controlDir); err != nil {
		return nil, err
	}
	workspace.mu.Lock()
	locked, err := workspace.processLock.TryLockContext(ctx, lockRetryInterval)
	if err != nil {
		workspace.mu.Unlock()
		if contextErr(ctx) != nil {
			return nil, fmt.Errorf("acquire workspace lock: %w", errors.Join(ErrWorkspaceBusy, err))
		}
		return nil, fmt.Errorf("acquire workspace lock: %w", err)
	}
	if !locked {
		workspace.mu.Unlock()
		return nil, ErrWorkspaceBusy
	}
	return func() {
		_ = workspace.processLock.Unlock()
		workspace.mu.Unlock()
	}, nil
}

func (workspace *Workspace) lockBounded() (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), lockWaitTimeout)
	unlock, err := workspace.lock(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	return func() {
		unlock()
		cancel()
	}, nil
}

func (workspace *Workspace) prepareLockDirectory() error {
	controlDir := filepath.Join(workspace.root, knowlDir)
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		return fmt.Errorf("create workspace control directory: %w", err)
	}
	if err := rejectSymlinkPath(workspace.root, controlDir); err != nil {
		return err
	}
	return nil
}
