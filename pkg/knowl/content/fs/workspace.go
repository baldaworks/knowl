// Package fs implements the canonical Knowl workspace adapter.
package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl"
	"gopkg.in/yaml.v3"
)

const (
	schemaFile       = "schema.md"
	workspaceWikiDir = "wiki"
	workspaceRawDir  = "raw"
	knowlDir         = ".knowl"
	markdownExt      = ".md"
	defaultMaxBytes  = 4 << 20
)

// Workspace owns canonical filesystem content for one local Knowl workspace.
type Workspace struct {
	root           string
	maxSourceBytes int
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
	workspace := &Workspace{root: filepath.Clean(abs), maxSourceBytes: defaultMaxBytes}
	for _, option := range options {
		if option != nil {
			option(workspace)
		}
	}
	return workspace, nil
}

// Root returns the absolute workspace path.
func (workspace *Workspace) Root() string { return workspace.root }

// Init creates the canonical empty workspace without replacing existing files.
func (workspace *Workspace) Init() error {
	for _, relative := range []string{
		workspaceRawDir,
		filepath.Join(workspaceWikiDir, "entities"),
		filepath.Join(workspaceWikiDir, "concepts"),
		filepath.Join(workspaceWikiDir, "syntheses"),
		filepath.Join(knowlDir, "staging"),
		filepath.Join(knowlDir, "recovery"),
	} {
		if err := os.MkdirAll(filepath.Join(workspace.root, relative), 0o700); err != nil {
			return fmt.Errorf("create workspace directory %q: %w", relative, err)
		}
	}
	files := map[string]string{
		schemaFile: "# Knowl schema\n\nMaintainer plans may read this document but may not modify it.\n",
		filepath.Join(workspaceWikiDir, "index.md"): "# Knowl index\n\nNo pages have been committed yet.\n",
		filepath.Join(workspaceWikiDir, "log.md"):   "# Knowl log\n\n",
	}
	for relative, contents := range files {
		path := filepath.Join(workspace.root, relative)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat workspace file %q: %w", relative, err)
		}
		if err := writeAtomic(path, []byte(contents), 0o600); err != nil {
			return fmt.Errorf("write workspace file %q: %w", relative, err)
		}
	}
	return nil
}

// Validate checks the required workspace shape and rejects symlinked roots.
func (workspace *Workspace) Validate() error {
	if err := rejectSymlinkPath(workspace.root, workspace.root); err != nil {
		return err
	}
	for _, relative := range []string{schemaFile, filepath.Join(workspaceWikiDir, "index.md"), filepath.Join(workspaceWikiDir, "log.md")} {
		path := filepath.Join(workspace.root, relative)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("required workspace file %q: %w", relative, err)
		}
		if info.IsDir() {
			return fmt.Errorf("required workspace path %q is a directory: %w", relative, ErrWorkspaceInvalid)
		}
		if err := rejectSymlinkPath(workspace.root, path); err != nil {
			return err
		}
	}
	for _, relative := range []string{workspaceRawDir, workspaceWikiDir, knowlDir} {
		path := filepath.Join(workspace.root, relative)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("required workspace directory %q: %w", relative, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("required workspace path %q is not a directory: %w", relative, ErrWorkspaceInvalid)
		}
		if err := rejectSymlinkPath(workspace.root, path); err != nil {
			return err
		}
	}
	return nil
}

// AcceptSource persists one immutable source version and its manifest.
func (workspace *Workspace) AcceptSource(ctx context.Context, envelope knowl.SourceEnvelope) (knowl.AcceptedSource, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.AcceptedSource{}, err
	}
	if strings.TrimSpace(string(envelope.Scope)) == "" || strings.TrimSpace(envelope.Source.Adapter) == "" || strings.TrimSpace(envelope.Source.ID) == "" || strings.TrimSpace(envelope.Version.Version) == "" || len(envelope.Content) == 0 || len(envelope.Content) > workspace.maxSourceBytes {
		return knowl.AcceptedSource{}, ErrInvalidSource
	}
	digest := digestBytes(envelope.Content)
	if strings.ToLower(strings.TrimSpace(envelope.Version.Digest)) != digest {
		return knowl.AcceptedSource{}, ErrDigestMismatch
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()

	keyDir := filepath.Join(workspace.root, workspaceRawDir, token(string(envelope.Scope)+"\x00"+envelope.Source.Adapter+"\x00"+envelope.Source.ID), token(envelope.Version.Version))
	sourcePath := filepath.Join(keyDir, "source")
	manifestPath := filepath.Join(keyDir, "manifest.yaml")
	if existing, err := readManifest(manifestPath); err == nil {
		if existing.Digest != digest {
			return knowl.AcceptedSource{}, ErrSourceConflict
		}
		return existing.accepted(), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return knowl.AcceptedSource{}, fmt.Errorf("read source manifest: %w", err)
	}
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return knowl.AcceptedSource{}, fmt.Errorf("create source directory: %w", err)
	}
	manifest := sourceManifest{
		Scope:      string(envelope.Scope),
		Adapter:    envelope.Source.Adapter,
		ID:         envelope.Source.ID,
		Version:    envelope.Version.Version,
		Digest:     digest,
		MediaType:  envelope.MediaType,
		ReceivedAt: envelope.ReceivedAt,
	}
	if manifest.ReceivedAt.IsZero() {
		manifest.ReceivedAt = time.Now().UTC()
	}
	manifestBytes, err := yaml.Marshal(manifest)
	if err != nil {
		return knowl.AcceptedSource{}, fmt.Errorf("marshal source manifest: %w", err)
	}
	if err := writeAtomic(sourcePath, envelope.Content, 0o600); err != nil {
		return knowl.AcceptedSource{}, fmt.Errorf("write source content: %w", err)
	}
	if err := writeAtomic(manifestPath, manifestBytes, 0o600); err != nil {
		_ = os.Remove(sourcePath)
		return knowl.AcceptedSource{}, fmt.Errorf("write source manifest: %w", err)
	}
	return manifest.accepted(), nil
}

// Schema reads and fingerprints the operator-owned schema document.
func (workspace *Workspace) Schema(ctx context.Context, scope knowl.ScopeRef) (knowl.SchemaDocument, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.SchemaDocument{}, err
	}
	content, err := os.ReadFile(filepath.Join(workspace.root, schemaFile))
	if err != nil {
		return knowl.SchemaDocument{}, fmt.Errorf("read schema: %w", err)
	}
	return knowl.SchemaDocument{Scope: scope, Digest: digestBytes(content), Version: schemaVersion(string(content)), Content: content}, nil
}

// ReadPages reads the requested Markdown pages by safe page-relative ID.
func (workspace *Workspace) ReadPages(ctx context.Context, scope knowl.ScopeRef, ids []knowl.PageID, limits knowl.ReadLimits) ([]knowl.PageSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	pages := make([]knowl.PageSnapshot, 0, len(ids))
	for _, id := range ids {
		if limits.Pages > 0 && len(pages) >= limits.Pages {
			break
		}
		relative, err := pageRelativePath(string(id))
		if err != nil {
			return nil, err
		}
		path := filepath.Join(workspace.root, relative)
		if err := rejectSymlinkPath(workspace.root, path); err != nil {
			return nil, err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read page %q: %w", id, err)
		}
		if limits.Bytes > 0 && len(content) > limits.Bytes {
			return nil, fmt.Errorf("page %q exceeds byte limit", id)
		}
		pages = append(pages, knowl.PageSnapshot{ID: id, Path: relative, Digest: digestBytes(content), Title: markdownTitle(content), Content: string(content)})
	}
	return pages, nil
}

// StagePlan writes a validated plan below .knowl/staging without touching canonical files.
func (workspace *Workspace) StagePlan(ctx context.Context, plan knowl.ValidatedEditPlan) (knowl.StagedChange, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.StagedChange{}, err
	}
	if strings.TrimSpace(plan.OperationID) == "" || len(plan.Edits) == 0 {
		return knowl.StagedChange{}, ErrPlanConflict
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	stageDir := filepath.Join(workspace.root, knowlDir, "staging", token(plan.OperationID))
	if _, err := os.Stat(stageDir); err == nil {
		return knowl.StagedChange{}, ErrPlanConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return knowl.StagedChange{}, fmt.Errorf("stat staging directory: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return knowl.StagedChange{}, fmt.Errorf("create staging directory: %w", err)
	}
	entries := make([]stageEntry, 0, len(plan.Edits))
	for _, edit := range plan.Edits {
		if err := validateWikiPath(edit.Path); err != nil {
			return knowl.StagedChange{}, err
		}
		target := filepath.Join(workspace.root, filepath.FromSlash(edit.Path))
		if err := rejectSymlinkPath(workspace.root, target); err != nil {
			return knowl.StagedChange{}, err
		}
		current, err := os.ReadFile(target)
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return knowl.StagedChange{}, fmt.Errorf("read target %q: %w", edit.Path, err)
		}
		if exists && strings.TrimSpace(edit.ExpectedDigest) == "" {
			return knowl.StagedChange{}, fmt.Errorf("existing target %q requires expected digest: %w", edit.Path, ErrPrecondition)
		}
		if exists && edit.ExpectedDigest != digestBytes(current) {
			return knowl.StagedChange{}, fmt.Errorf("target %q digest changed: %w", edit.Path, ErrPrecondition)
		}
		stagedPath := filepath.Join(stageDir, filepath.FromSlash(edit.Path))
		if err := os.MkdirAll(filepath.Dir(stagedPath), 0o700); err != nil {
			return knowl.StagedChange{}, fmt.Errorf("create staged parent: %w", err)
		}
		if err := writeAtomic(stagedPath, edit.Content, 0o600); err != nil {
			return knowl.StagedChange{}, fmt.Errorf("stage %q: %w", edit.Path, err)
		}
		entries = append(entries, stageEntry{Target: edit.Path, ExpectedDigest: edit.ExpectedDigest, Digest: digestBytes(edit.Content)})
	}
	metadata, err := yaml.Marshal(stageManifest{OperationID: plan.OperationID, SchemaDigest: plan.SchemaDigest, Entries: entries})
	if err != nil {
		return knowl.StagedChange{}, fmt.Errorf("marshal staging manifest: %w", err)
	}
	if err := writeAtomic(filepath.Join(stageDir, "manifest.yaml"), metadata, 0o600); err != nil {
		return knowl.StagedChange{}, fmt.Errorf("write staging manifest: %w", err)
	}
	return knowl.StagedChange{OperationID: plan.OperationID, Digest: digestBytes(metadata), Files: entryTargets(entries), CreatedAt: time.Now().UTC()}, nil
}

// Commit applies a staged plan under the workspace writer lock.
func (workspace *Workspace) Commit(ctx context.Context, staged knowl.StagedChange) (knowl.ContentCommit, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.ContentCommit{}, err
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	stageDir := filepath.Join(workspace.root, knowlDir, "staging", token(staged.OperationID))
	manifest, err := readStageManifest(filepath.Join(stageDir, "manifest.yaml"))
	if err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("read staged plan: %w", err)
	}
	journalDir := filepath.Join(workspace.root, knowlDir, "recovery", token(staged.OperationID))
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("create recovery directory: %w", err)
	}
	journal := recoveryJournal{OperationID: staged.OperationID, State: "prepared", Entries: make([]recoveryEntry, 0, len(manifest.Entries))}
	for _, entry := range manifest.Entries {
		target := filepath.Join(workspace.root, filepath.FromSlash(entry.Target))
		old, readErr := os.ReadFile(target)
		hadOld := readErr == nil
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return knowl.ContentCommit{}, fmt.Errorf("read commit target %q: %w", entry.Target, readErr)
		}
		if entry.ExpectedDigest != "" && (!hadOld || digestBytes(old) != entry.ExpectedDigest) {
			return knowl.ContentCommit{}, fmt.Errorf("target %q changed after staging: %w", entry.Target, ErrPrecondition)
		}
		if entry.ExpectedDigest == "" && hadOld {
			return knowl.ContentCommit{}, fmt.Errorf("new target %q already exists: %w", entry.Target, ErrPrecondition)
		}
		backup := ""
		if hadOld {
			backup = filepath.Join(journalDir, token(entry.Target)+".old")
			if err := writeAtomic(backup, old, 0o600); err != nil {
				return knowl.ContentCommit{}, fmt.Errorf("write preimage %q: %w", entry.Target, err)
			}
		}
		journal.Entries = append(journal.Entries, recoveryEntry{Target: entry.Target, Backup: backup, HadOld: hadOld})
	}
	journalPath := filepath.Join(workspace.root, knowlDir, "recovery", token(staged.OperationID)+".yaml")
	if err := writeJournal(journalPath, journal); err != nil {
		return knowl.ContentCommit{}, err
	}
	for _, entry := range manifest.Entries {
		content, err := os.ReadFile(filepath.Join(stageDir, filepath.FromSlash(entry.Target)))
		if err != nil {
			return knowl.ContentCommit{}, fmt.Errorf("read staged file %q: %w", entry.Target, err)
		}
		if err := writeAtomic(filepath.Join(workspace.root, filepath.FromSlash(entry.Target)), content, 0o600); err != nil {
			return knowl.ContentCommit{}, fmt.Errorf("commit file %q: %w", entry.Target, err)
		}
	}
	journal.State = "committed"
	if err := writeJournal(journalPath, journal); err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := os.Remove(journalPath); err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("remove recovery journal: %w", err)
	}
	return knowl.ContentCommit{OperationID: staged.OperationID, Generation: staged.Digest, Files: entryTargets(manifest.Entries), CommittedAt: time.Now().UTC()}, nil
}

// Recover restores prepared journals and clears completed journals before readiness.
func (workspace *Workspace) Recover(ctx context.Context) ([]knowl.RecoveryResult, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	recoveryRoot := filepath.Join(workspace.root, knowlDir, "recovery")
	entries, err := os.ReadDir(recoveryRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read recovery directory: %w", err)
	}
	results := make([]knowl.RecoveryResult, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(recoveryRoot, entry.Name())
		journal, err := readJournal(path)
		if err != nil {
			return nil, fmt.Errorf("read recovery journal %q: %w", entry.Name(), err)
		}
		switch journal.State {
		case "prepared":
			for _, recovery := range journal.Entries {
				target := filepath.Join(workspace.root, filepath.FromSlash(recovery.Target))
				if recovery.HadOld {
					old, readErr := os.ReadFile(recovery.Backup)
					if readErr != nil {
						return nil, fmt.Errorf("read preimage %q: %w", recovery.Target, readErr)
					}
					if err := writeAtomic(target, old, 0o600); err != nil {
						return nil, fmt.Errorf("restore preimage %q: %w", recovery.Target, err)
					}
				} else if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("remove partial file %q: %w", recovery.Target, err)
				}
			}
			results = append(results, knowl.RecoveryResult{OperationID: journal.OperationID, Action: "rolled_back"})
		case "committed":
			results = append(results, knowl.RecoveryResult{OperationID: journal.OperationID, Action: "completed"})
		default:
			return nil, fmt.Errorf("unknown recovery state %q: %w", journal.State, ErrWorkspaceInvalid)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove recovery journal %q: %w", entry.Name(), err)
		}
	}
	return results, nil
}

// Snapshot captures canonical Markdown digests for projection rebuilds.
func (workspace *Workspace) Snapshot(ctx context.Context, scope knowl.ScopeRef) (knowl.WorkspaceSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.WorkspaceSnapshot{}, err
	}
	if err := workspace.Validate(); err != nil {
		return knowl.WorkspaceSnapshot{}, err
	}
	schema, err := workspace.Schema(ctx, scope)
	if err != nil {
		return knowl.WorkspaceSnapshot{}, err
	}
	digests := make(map[string]string)
	wikiRoot := filepath.Join(workspace.root, workspaceWikiDir)
	err = filepath.WalkDir(wikiRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in wiki: %w", ErrPathRejected)
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != markdownExt {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relative, relErr := filepath.Rel(workspace.root, path)
		if relErr != nil {
			return relErr
		}
		digests[filepath.ToSlash(relative)] = digestBytes(content)
		return nil
	})
	if err != nil {
		return knowl.WorkspaceSnapshot{}, fmt.Errorf("snapshot wiki: %w", err)
	}
	return knowl.WorkspaceSnapshot{Scope: scope, SchemaDigest: schema.Digest, PageDigests: digests, CapturedAt: time.Now().UTC()}, nil
}

type sourceManifest struct {
	Scope      string    `yaml:"scope"`
	Adapter    string    `yaml:"adapter"`
	ID         string    `yaml:"id"`
	Version    string    `yaml:"version"`
	Digest     string    `yaml:"digest"`
	MediaType  string    `yaml:"media_type"`
	ReceivedAt time.Time `yaml:"received_at"`
}

func (manifest sourceManifest) accepted() knowl.AcceptedSource {
	return knowl.AcceptedSource{Scope: knowl.ScopeRef(manifest.Scope), Source: knowl.SourceRef{Adapter: manifest.Adapter, ID: manifest.ID}, Version: knowl.SourceVersion{Version: manifest.Version, Digest: manifest.Digest}, MediaType: manifest.MediaType, ManifestRef: filepath.ToSlash(filepath.Join(workspaceRawDir, token(manifest.Scope+"\x00"+manifest.Adapter+"\x00"+manifest.ID), token(manifest.Version), "manifest.yaml"))}
}

type stageEntry struct {
	Target         string `yaml:"target"`
	ExpectedDigest string `yaml:"expected_digest,omitempty"`
	Digest         string `yaml:"digest"`
}

type stageManifest struct {
	OperationID  string       `yaml:"operation_id"`
	SchemaDigest string       `yaml:"schema_digest"`
	Entries      []stageEntry `yaml:"entries"`
}

type recoveryEntry struct {
	Target string `yaml:"target"`
	Backup string `yaml:"backup,omitempty"`
	HadOld bool   `yaml:"had_old"`
}

type recoveryJournal struct {
	OperationID string          `yaml:"operation_id"`
	State       string          `yaml:"state"`
	Entries     []recoveryEntry `yaml:"entries"`
}

func readManifest(path string) (sourceManifest, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return sourceManifest{}, err
	}
	var manifest sourceManifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return sourceManifest{}, err
	}
	return manifest, nil
}

func readStageManifest(path string) (stageManifest, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return stageManifest{}, err
	}
	var manifest stageManifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return stageManifest{}, err
	}
	return manifest, nil
}

func writeJournal(path string, journal recoveryJournal) error {
	content, err := yaml.Marshal(journal)
	if err != nil {
		return fmt.Errorf("marshal recovery journal: %w", err)
	}
	if err := writeAtomic(path, content, 0o600); err != nil {
		return fmt.Errorf("write recovery journal: %w", err)
	}
	return nil
}

func readJournal(path string) (recoveryJournal, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return recoveryJournal{}, err
	}
	var journal recoveryJournal
	if err := yaml.Unmarshal(content, &journal); err != nil {
		return recoveryJournal{}, err
	}
	return journal, nil
}

func entryTargets(entries []stageEntry) []string {
	targets := make([]string, 0, len(entries))
	for _, entry := range entries {
		targets = append(targets, entry.Target)
	}
	sort.Strings(targets)
	return targets
}

func validateWikiPath(raw string) error {
	clean := filepath.ToSlash(filepath.Clean(raw))
	if clean == "." || !strings.HasPrefix(clean, workspaceWikiDir+"/") || clean == filepath.Join(workspaceWikiDir, "log.md") || filepath.Ext(clean) != markdownExt || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return fmt.Errorf("wiki path %q: %w", raw, ErrPathRejected)
	}
	return nil
}

func pageRelativePath(raw string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean(raw))
	if !strings.HasPrefix(clean, workspaceWikiDir+"/") {
		clean = filepath.ToSlash(filepath.Join(workspaceWikiDir, clean))
	}
	if filepath.Ext(clean) == "" {
		clean += markdownExt
	}
	if err := validateWikiPath(clean); err != nil {
		return "", err
	}
	return clean, nil
}

func markdownTitle(content []byte) string {
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func schemaVersion(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "schema_version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "schema_version:"))
		}
	}
	return "1"
}

func digestBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func token(value string) string {
	return digestBytes([]byte(value))[:32]
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func writeAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".knowl-tmp-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryName)
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func rejectSymlinkPath(root, target string) error {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	cleanTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes workspace: %w", ErrPathRejected)
	}
	current := cleanRoot
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %q: %w", current, ErrPathRejected)
		}
	}
	return nil
}
