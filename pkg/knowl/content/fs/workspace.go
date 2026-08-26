// Package fs implements the canonical Knowl workspace adapter.
package fs

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
)

// Workspace owns canonical filesystem content for one local Knowl workspace.
type Workspace struct {
	root           string
	maxSourceBytes int
	commitFault    func(point string, index int) error
	now            func() time.Time
	mu             sync.Mutex
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
	workspace := &Workspace{root: filepath.Clean(abs), maxSourceBytes: defaultMaxBytes, now: time.Now}
	for _, option := range options {
		if option != nil {
			option(workspace)
		}
	}
	return workspace, nil
}

// Root returns the absolute workspace path.
func (workspace *Workspace) Root() string { return workspace.root }
