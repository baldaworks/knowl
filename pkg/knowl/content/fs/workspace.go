// Package fs implements the canonical Knowl workspace adapter.
package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	internalwiki "github.com/baldaworks/knowl/internal/wiki"
	"github.com/baldaworks/knowl/pkg/knowl"
	"gopkg.in/yaml.v3"
)

const (
	schemaFile        = "schema.md"
	workspaceWikiDir  = "wiki"
	workspaceRawDir   = "raw"
	knowlDir          = ".knowl"
	markdownExt       = ".md"
	defaultMaxBytes   = 4 << 20
	recoveryPrepared  = "prepared"
	recoveryCommitted = "committed"
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

// ReadSource returns the immutable source bytes previously accepted by the workspace.
func (workspace *Workspace) ReadSource(ctx context.Context, source knowl.AcceptedSource, limits knowl.ReadLimits) ([]byte, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(source.Scope)) == "" || strings.TrimSpace(source.Source.Adapter) == "" || strings.TrimSpace(source.Source.ID) == "" || strings.TrimSpace(source.Version.Version) == "" {
		return nil, fmt.Errorf("source identity is incomplete: %w", ErrInvalidSource)
	}

	maxBytes := limits.Bytes
	if maxBytes <= 0 || maxBytes > workspace.maxSourceBytes {
		maxBytes = workspace.maxSourceBytes
	}
	path := filepath.Join(workspace.root, workspaceRawDir, token(string(source.Scope)+"\x00"+source.Source.Adapter+"\x00"+source.Source.ID), token(source.Version.Version), "source")
	if err := rejectSymlinkPath(workspace.root, path); err != nil {
		return nil, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", source.Source.ID, ErrSourceNotFound)
		}
		return nil, fmt.Errorf("read source: %w", err)
	}
	if len(content) > maxBytes {
		return nil, fmt.Errorf("source exceeds %d bytes: %w", maxBytes, ErrInvalidSource)
	}
	if limits.Characters > 0 && utf8.RuneCount(content) > limits.Characters {
		return nil, fmt.Errorf("source exceeds %d characters: %w", limits.Characters, ErrInvalidSource)
	}
	if source.Version.Digest != "" && digestBytes(content) != strings.ToLower(source.Version.Digest) {
		return nil, ErrDigestMismatch
	}
	return content, nil
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
		if limits.Characters > 0 && utf8.RuneCount(content) > limits.Characters {
			return nil, fmt.Errorf("page %q exceeds character limit", id)
		}
		info, infoErr := os.Stat(path)
		if infoErr != nil {
			return nil, fmt.Errorf("stat page %q: %w", id, infoErr)
		}
		pages = append(pages, knowl.PageSnapshot{ID: id, Path: relative, Digest: digestBytes(content), Title: markdownTitle(content), Content: string(content), SourceRefs: markdownSourceRefs(content), UpdatedAt: info.ModTime().UTC()})
	}
	return pages, nil
}

func (workspace *Workspace) readControlPage(ctx context.Context, id knowl.PageID, limits knowl.ReadLimits) (knowl.PageSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.PageSnapshot{}, err
	}
	if id != "index" && id != "log" {
		return knowl.PageSnapshot{}, fmt.Errorf("unsupported control page %q: %w", id, ErrPathRejected)
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	relative := filepath.ToSlash(filepath.Join(workspaceWikiDir, string(id)+markdownExt))
	path := filepath.Join(workspace.root, filepath.FromSlash(relative))
	if err := rejectSymlinkPath(workspace.root, path); err != nil {
		return knowl.PageSnapshot{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return knowl.PageSnapshot{}, fmt.Errorf("read control page %q: %w", id, err)
	}
	if limits.Bytes > 0 && len(content) > limits.Bytes {
		return knowl.PageSnapshot{}, fmt.Errorf("control page %q exceeds byte limit", id)
	}
	if limits.Characters > 0 && utf8.RuneCount(content) > limits.Characters {
		return knowl.PageSnapshot{}, fmt.Errorf("control page %q exceeds character limit", id)
	}
	info, err := os.Stat(path)
	if err != nil {
		return knowl.PageSnapshot{}, fmt.Errorf("stat control page %q: %w", id, err)
	}
	return knowl.PageSnapshot{ID: id, Path: relative, Digest: digestBytes(content), Title: markdownTitle(content), Content: string(content), SourceRefs: markdownSourceRefs(content), UpdatedAt: info.ModTime().UTC()}, nil
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
	if err := rejectSymlinkPath(workspace.root, stageDir); err != nil {
		return knowl.StagedChange{}, err
	}
	if _, err := os.Stat(stageDir); err == nil {
		manifest, readErr := readStageManifest(filepath.Join(stageDir, "manifest.yaml"))
		if readErr != nil {
			return knowl.StagedChange{}, fmt.Errorf("read existing staging manifest: %w", ErrPlanConflict)
		}
		if !sameStagePlan(manifest, plan) {
			return knowl.StagedChange{}, ErrPlanConflict
		}
		if err := validateStagedPaths(workspace.root, stageDir, manifest.Entries); err != nil {
			return knowl.StagedChange{}, err
		}
		stagedEdits, stagedErr := readStagedPlanEdits(stageDir, manifest.Entries)
		if stagedErr != nil {
			return knowl.StagedChange{}, stagedErr
		}
		if err := workspace.validateProspectivePlanLocked(plan.Scope, stagedEdits); err != nil {
			return knowl.StagedChange{}, err
		}
		return stagedChangeFromManifest(stageDir, manifest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return knowl.StagedChange{}, fmt.Errorf("stat staging directory: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return knowl.StagedChange{}, fmt.Errorf("create staging directory: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(stageDir)
		}
	}()
	if strings.TrimSpace(plan.SchemaDigest) != "" {
		schema, schemaErr := os.ReadFile(filepath.Join(workspace.root, schemaFile))
		if schemaErr != nil {
			return knowl.StagedChange{}, fmt.Errorf("read schema before staging: %w", schemaErr)
		}
		if digestBytes(schema) != plan.SchemaDigest {
			return knowl.StagedChange{}, fmt.Errorf("schema changed after planning: %w", ErrPrecondition)
		}
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
	if err := workspace.validateProspectivePlanLocked(plan.Scope, prospectiveEditsFromPlan(plan)); err != nil {
		return knowl.StagedChange{}, err
	}
	manifest := stageManifest{OperationID: plan.OperationID, Scope: string(plan.Scope), SchemaDigest: plan.SchemaDigest, SourceRefs: append([]string(nil), plan.SourceRefs...), Entries: entries}
	coreMetadata, err := yaml.Marshal(manifest)
	if err != nil {
		return knowl.StagedChange{}, fmt.Errorf("marshal staging manifest: %w", err)
	}
	logPath := filepath.Join(workspace.root, workspaceWikiDir, "log.md")
	if err := rejectSymlinkPath(workspace.root, logPath); err != nil {
		return knowl.StagedChange{}, err
	}
	logBefore, err := os.ReadFile(logPath)
	if err != nil {
		return knowl.StagedChange{}, fmt.Errorf("read canonical log: %w", err)
	}
	generation := digestBytes(coreMetadata)
	logAfter, err := appendLogEntry(logBefore, manifest, generation)
	if err != nil {
		return knowl.StagedChange{}, err
	}
	manifest.LogExpectedDigest = digestBytes(logBefore)
	manifest.LogDigest = digestBytes(logAfter)
	metadata, err := yaml.Marshal(manifest)
	if err != nil {
		return knowl.StagedChange{}, fmt.Errorf("marshal staging manifest: %w", err)
	}
	if err := writeAtomic(filepath.Join(stageDir, "manifest.yaml"), metadata, 0o600); err != nil {
		return knowl.StagedChange{}, fmt.Errorf("write staging manifest: %w", err)
	}
	complete = true
	return knowl.StagedChange{OperationID: plan.OperationID, Digest: generation, Files: entryTargets(entries), CreatedAt: time.Now().UTC()}, nil
}

// Commit applies a staged plan under the workspace writer lock.
func (workspace *Workspace) Commit(ctx context.Context, staged knowl.StagedChange) (knowl.ContentCommit, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.ContentCommit{}, err
	}
	if strings.TrimSpace(staged.OperationID) == "" {
		return knowl.ContentCommit{}, ErrPlanConflict
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	stageDir := filepath.Join(workspace.root, knowlDir, "staging", token(staged.OperationID))
	manifest, err := readStageManifest(filepath.Join(stageDir, "manifest.yaml"))
	if err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("read staged plan: %w", err)
	}
	if manifest.OperationID != staged.OperationID {
		return knowl.ContentCommit{}, fmt.Errorf("staged operation mismatch: %w", ErrPlanConflict)
	}
	for _, entry := range manifest.Entries {
		if err := validateCommitTarget(entry.Target); err != nil {
			return knowl.ContentCommit{}, err
		}
		if err := rejectSymlinkPath(workspace.root, filepath.Join(workspace.root, filepath.FromSlash(entry.Target))); err != nil {
			return knowl.ContentCommit{}, err
		}
	}
	if err := validateStagedPaths(workspace.root, stageDir, manifest.Entries); err != nil {
		return knowl.ContentCommit{}, err
	}
	if !stagedFilesMatch(stageDir, manifest.Entries) {
		return knowl.ContentCommit{}, fmt.Errorf("staged file content changed: %w", ErrPlanConflict)
	}
	generation := stageGeneration(manifest)
	schema, err := os.ReadFile(filepath.Join(workspace.root, schemaFile))
	if err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("read schema before commit: %w", err)
	}
	if manifest.SchemaDigest != "" && digestBytes(schema) != manifest.SchemaDigest {
		return knowl.ContentCommit{}, fmt.Errorf("schema changed after staging: %w", ErrPrecondition)
	}
	stagedEdits, err := readStagedPlanEdits(stageDir, manifest.Entries)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := workspace.validateProspectivePlanLocked(manifestScope(manifest), stagedEdits); err != nil {
		return knowl.ContentCommit{}, err
	}
	journalDir := filepath.Join(workspace.root, knowlDir, "recovery", token(staged.OperationID))
	if err := rejectSymlinkPath(workspace.root, journalDir); err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("create recovery directory: %w", err)
	}
	logPath := filepath.Join(workspace.root, workspaceWikiDir, "log.md")
	if err := rejectSymlinkPath(workspace.root, logPath); err != nil {
		return knowl.ContentCommit{}, err
	}
	logBefore, err := os.ReadFile(logPath)
	if err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("read canonical log: %w", err)
	}
	logAfter, err := appendLogEntry(logBefore, manifest, generation)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	if digestBytes(logBefore) != manifest.LogExpectedDigest {
		if digestBytes(logBefore) == manifest.LogDigest && canonicalEntriesMatch(workspace.root, manifest.Entries) {
			return knowl.ContentCommit{OperationID: staged.OperationID, Generation: generation, Files: commitTargets(manifest.Entries), CommittedAt: time.Now().UTC()}, nil
		}
		return knowl.ContentCommit{}, fmt.Errorf("canonical log changed after staging: %w", ErrPrecondition)
	}
	if digestBytes(logAfter) != manifest.LogDigest {
		return knowl.ContentCommit{}, fmt.Errorf("staged log digest changed: %w", ErrPlanConflict)
	}
	commitEntries := append(append([]stageEntry(nil), manifest.Entries...), stageEntry{Target: filepath.ToSlash(filepath.Join(workspaceWikiDir, "log.md")), ExpectedDigest: manifest.LogExpectedDigest, Digest: manifest.LogDigest})
	journal := recoveryJournal{OperationID: staged.OperationID, State: recoveryPrepared, Entries: make([]recoveryEntry, 0, len(commitEntries))}
	for _, entry := range commitEntries {
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
	for _, entry := range commitEntries {
		var content []byte
		if entry.Target == filepath.ToSlash(filepath.Join(workspaceWikiDir, "log.md")) {
			content = logAfter
		} else {
			content, err = os.ReadFile(filepath.Join(stageDir, filepath.FromSlash(entry.Target)))
			if err != nil {
				return knowl.ContentCommit{}, fmt.Errorf("read staged file %q: %w", entry.Target, err)
			}
		}
		if err := writeAtomic(filepath.Join(workspace.root, filepath.FromSlash(entry.Target)), content, 0o600); err != nil {
			return knowl.ContentCommit{}, fmt.Errorf("commit file %q: %w", entry.Target, err)
		}
	}
	journal.State = recoveryCommitted
	if err := writeJournal(journalPath, journal); err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := os.Remove(journalPath); err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("remove recovery journal: %w", err)
	}
	return knowl.ContentCommit{OperationID: staged.OperationID, Generation: generation, Files: commitTargets(manifest.Entries), CommittedAt: time.Now().UTC()}, nil
}

// Recover restores prepared journals and clears completed journals before readiness.
func (workspace *Workspace) Recover(ctx context.Context) ([]knowl.RecoveryResult, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	recoveryRoot := filepath.Join(workspace.root, knowlDir, "recovery")
	if err := rejectSymlinkPath(workspace.root, recoveryRoot); err != nil {
		return nil, err
	}
	stagingRoot := filepath.Join(workspace.root, knowlDir, "staging")
	if err := rejectSymlinkPath(workspace.root, stagingRoot); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(recoveryRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
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
		case recoveryPrepared:
			for _, recovery := range journal.Entries {
				if err := validateRecoveryTarget(recovery.Target); err != nil {
					return nil, err
				}
				target := filepath.Join(workspace.root, filepath.FromSlash(recovery.Target))
				if err := rejectSymlinkPath(workspace.root, target); err != nil {
					return nil, err
				}
				if recovery.HadOld {
					if err := validateRecoveryBackup(recovery.Backup, recoveryRoot); err != nil {
						return nil, err
					}
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
		case recoveryCommitted:
			for _, recovery := range journal.Entries {
				if err := validateRecoveryTarget(recovery.Target); err != nil {
					return nil, err
				}
				if err := rejectSymlinkPath(workspace.root, filepath.Join(workspace.root, filepath.FromSlash(recovery.Target))); err != nil {
					return nil, err
				}
			}
			results = append(results, knowl.RecoveryResult{OperationID: journal.OperationID, Action: "completed"})
		default:
			return nil, fmt.Errorf("unknown recovery state %q: %w", journal.State, ErrWorkspaceInvalid)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove recovery journal %q: %w", entry.Name(), err)
		}
		if err := os.RemoveAll(filepath.Join(recoveryRoot, token(journal.OperationID))); err != nil {
			return nil, fmt.Errorf("remove recovery backups %q: %w", entry.Name(), err)
		}
	}
	stagingEntries, err := os.ReadDir(stagingRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read staging directory: %w", err)
	}
	for _, entry := range stagingEntries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink in staging: %w", ErrPathRejected)
		}
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(stagingRoot, entry.Name(), "manifest.yaml")
		if _, statErr := os.Stat(manifestPath); statErr == nil {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect staging directory %q: %w", entry.Name(), statErr)
		}
		if err := os.RemoveAll(filepath.Join(stagingRoot, entry.Name())); err != nil {
			return nil, fmt.Errorf("discard incomplete staging %q: %w", entry.Name(), err)
		}
		results = append(results, knowl.RecoveryResult{Action: "discarded_staging"})
	}
	recoveryEntries, err := os.ReadDir(recoveryRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("re-read recovery directory: %w", err)
	}
	for _, entry := range recoveryEntries {
		if !entry.IsDir() {
			continue
		}
		journalPath := filepath.Join(recoveryRoot, entry.Name()+".yaml")
		if _, statErr := os.Stat(journalPath); statErr == nil {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect recovery directory %q: %w", entry.Name(), statErr)
		}
		if err := os.RemoveAll(filepath.Join(recoveryRoot, entry.Name())); err != nil {
			return nil, fmt.Errorf("discard orphan recovery %q: %w", entry.Name(), err)
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
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	digests := make(map[string]string)
	pages := make([]knowl.PageSnapshot, 0)
	links := make([]knowl.LinkReference, 0)
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
		relative = filepath.ToSlash(relative)
		digest := digestBytes(content)
		digests[relative] = digest
		if relative == filepath.ToSlash(filepath.Join(workspaceWikiDir, "index.md")) || relative == filepath.ToSlash(filepath.Join(workspaceWikiDir, "log.md")) {
			return nil
		}
		pageID, _ := internalwiki.PageIDFromPath(relative)
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		pages = append(pages, knowl.PageSnapshot{
			ID:         pageID,
			Path:       relative,
			Digest:     digest,
			Title:      markdownTitle(content),
			Content:    string(content),
			SourceRefs: markdownSourceRefs(content),
			UpdatedAt:  info.ModTime().UTC(),
		})
		links = append(links, markdownLinks(pageID, content)...)
		return nil
	})
	if err != nil {
		return knowl.WorkspaceSnapshot{}, fmt.Errorf("snapshot wiki: %w", err)
	}
	sort.Slice(pages, func(left, right int) bool { return pages[left].Path < pages[right].Path })
	sort.Slice(links, func(left, right int) bool {
		if links[left].From == links[right].From {
			if links[left].To == links[right].To {
				return links[left].Relation < links[right].Relation
			}
			return links[left].To < links[right].To
		}
		return links[left].From < links[right].From
	})
	links = uniqueLinks(links)
	return knowl.WorkspaceSnapshot{Scope: scope, SchemaDigest: schema.Digest, PageDigests: digests, Pages: pages, Links: links, CapturedAt: time.Now().UTC()}, nil
}

// Inspect captures the bounded metadata required by deterministic workspace lint.
func (workspace *Workspace) Inspect(ctx context.Context, scope knowl.ScopeRef) (knowl.WorkspaceInspection, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.WorkspaceInspection{}, err
	}
	snapshot, err := workspace.Snapshot(ctx, scope)
	if err != nil {
		return knowl.WorkspaceInspection{}, err
	}
	controlPages := make([]knowl.PageSnapshot, 0, 2)
	for _, id := range []knowl.PageID{"index", "log"} {
		controlPage, controlErr := workspace.readControlPage(ctx, id, knowl.ReadLimits{Bytes: workspace.maxSourceBytes})
		if controlErr != nil {
			return knowl.WorkspaceInspection{}, fmt.Errorf("read control page %q: %w", id, controlErr)
		}
		controlPages = append(controlPages, controlPage)
	}
	rawSources, err := workspace.inspectRawSources(ctx, scope)
	if err != nil {
		return knowl.WorkspaceInspection{}, err
	}
	return knowl.WorkspaceInspection{Scope: scope, Snapshot: snapshot, Index: controlPages[0], Log: controlPages[1], RawSources: rawSources}, nil
}

type rawDirectoryState struct {
	relative    string
	hasManifest bool
	hasSource   bool
	record      *knowl.RawSourceRecord
}

func (workspace *Workspace) inspectRawSources(ctx context.Context, scope knowl.ScopeRef) ([]knowl.RawSourceRecord, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	return workspace.inspectRawSourcesLocked(scope)
}

func (workspace *Workspace) inspectRawSourcesLocked(scope knowl.ScopeRef) ([]knowl.RawSourceRecord, error) {
	rawRoot := filepath.Join(workspace.root, workspaceRawDir)
	if err := rejectSymlinkPath(workspace.root, rawRoot); err != nil {
		return nil, err
	}
	states := make(map[string]*rawDirectoryState)
	err := filepath.WalkDir(rawRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in raw source tree: %w", ErrPathRejected)
		}
		if entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(workspace.root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		directory := filepath.ToSlash(filepath.Dir(relative))
		state := states[directory]
		if state == nil {
			state = &rawDirectoryState{relative: directory}
			states[directory] = state
		}
		switch filepath.Base(path) {
		case "source":
			state.hasSource = true
		case "manifest.yaml":
			state.hasManifest = true
			record := &knowl.RawSourceRecord{Path: relative}
			state.record = record
			manifestBytes, readErr := os.ReadFile(path)
			if readErr != nil {
				record.ErrorClass = "manifest_unreadable"
				return nil
			}
			var manifest sourceManifest
			if unmarshalErr := yaml.Unmarshal(manifestBytes, &manifest); unmarshalErr != nil {
				record.ErrorClass = "manifest_invalid"
				return nil
			}
			record.Source = manifest.accepted()
			if strings.TrimSpace(manifest.Scope) != "" && knowl.ScopeRef(manifest.Scope) != scope {
				state.record = nil
				return nil
			}
			record.Valid = validSourceManifest(manifest)
			if !record.Valid {
				record.ErrorClass = "manifest_invalid"
				return nil
			}
			sourcePath := filepath.Join(filepath.Dir(path), "source")
			info, statErr := os.Stat(sourcePath)
			if errors.Is(statErr, os.ErrNotExist) {
				record.Valid = false
				record.ErrorClass = "source_missing"
				return nil
			}
			if statErr != nil {
				record.Valid = false
				record.ErrorClass = "source_unreadable"
				return nil
			}
			if info.Size() > int64(workspace.maxSourceBytes) {
				record.Valid = false
				record.ErrorClass = "source_too_large"
				return nil
			}
			content, contentErr := os.ReadFile(sourcePath)
			if contentErr != nil {
				record.Valid = false
				record.ErrorClass = "source_unreadable"
				return nil
			}
			record.ContentDigest = digestBytes(content)
			if record.ContentDigest != manifest.Digest {
				record.Valid = false
				record.ErrorClass = "source_digest_mismatch"
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspect raw sources: %w", err)
	}
	records := make([]knowl.RawSourceRecord, 0, len(states))
	for _, state := range states {
		if state.record != nil {
			records = append(records, *state.record)
			continue
		}
		if state.hasSource && state.hasManifest {
			continue
		}
		if state.hasSource {
			records = append(records, knowl.RawSourceRecord{Path: filepath.ToSlash(filepath.Join(state.relative, "source")), ErrorClass: "manifest_missing"})
		}
	}
	sort.Slice(records, func(left, right int) bool { return records[left].Path < records[right].Path })
	return records, nil
}

func validSourceManifest(manifest sourceManifest) bool {
	if strings.TrimSpace(manifest.Scope) == "" || strings.TrimSpace(manifest.Adapter) == "" || strings.TrimSpace(manifest.ID) == "" || strings.TrimSpace(manifest.Version) == "" || len(manifest.Digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(manifest.Digest)
	return err == nil
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
	OperationID       string       `yaml:"operation_id"`
	Scope             string       `yaml:"scope,omitempty"`
	SchemaDigest      string       `yaml:"schema_digest"`
	SourceRefs        []string     `yaml:"source_refs,omitempty"`
	Entries           []stageEntry `yaml:"entries"`
	LogExpectedDigest string       `yaml:"log_expected_digest,omitempty"`
	LogDigest         string       `yaml:"log_digest,omitempty"`
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

type logEntry struct {
	OperationID  string   `json:"operation_id"`
	Generation   string   `json:"generation"`
	SchemaDigest string   `json:"schema_digest"`
	SourceRefs   []string `json:"source_refs,omitempty"`
	Files        []string `json:"files"`
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

func stagedChangeFromManifest(stageDir string, manifest stageManifest) (knowl.StagedChange, error) {
	if _, err := os.Stat(filepath.Join(stageDir, "manifest.yaml")); err != nil {
		return knowl.StagedChange{}, fmt.Errorf("read staging metadata: %w", err)
	}
	for _, entry := range manifest.Entries {
		if err := validateCommitTarget(entry.Target); err != nil {
			return knowl.StagedChange{}, err
		}
	}
	if !stagedFilesMatch(stageDir, manifest.Entries) {
		return knowl.StagedChange{}, fmt.Errorf("staged file content changed: %w", ErrPlanConflict)
	}
	return knowl.StagedChange{OperationID: manifest.OperationID, Digest: stageGeneration(manifest), Files: entryTargets(manifest.Entries), CreatedAt: time.Now().UTC()}, nil
}

func sameStagePlan(manifest stageManifest, plan knowl.ValidatedEditPlan) bool {
	if manifest.OperationID != plan.OperationID || manifest.SchemaDigest != plan.SchemaDigest || len(manifest.SourceRefs) != len(plan.SourceRefs) || len(manifest.Entries) != len(plan.Edits) {
		return false
	}
	if scope := manifestScope(manifest); scope != "" && scope != plan.Scope {
		return false
	}
	for index, sourceRef := range manifest.SourceRefs {
		if sourceRef != plan.SourceRefs[index] {
			return false
		}
	}
	for index, entry := range manifest.Entries {
		edit := plan.Edits[index]
		if entry.Target != edit.Path || entry.ExpectedDigest != edit.ExpectedDigest || entry.Digest != digestBytes(edit.Content) {
			return false
		}
	}
	return true
}

func stageGeneration(manifest stageManifest) string {
	core := stageManifest{OperationID: manifest.OperationID, Scope: manifest.Scope, SchemaDigest: manifest.SchemaDigest, SourceRefs: manifest.SourceRefs, Entries: manifest.Entries}
	metadata, err := yaml.Marshal(core)
	if err != nil {
		return ""
	}
	return digestBytes(metadata)
}

type prospectiveEdit struct {
	Target  string
	Content string
}

func prospectiveEditsFromPlan(plan knowl.ValidatedEditPlan) []prospectiveEdit {
	edits := make([]prospectiveEdit, 0, len(plan.Edits))
	for _, edit := range plan.Edits {
		edits = append(edits, prospectiveEdit{Target: edit.Path, Content: string(edit.Content)})
	}
	return edits
}

func readStagedPlanEdits(stageDir string, entries []stageEntry) ([]prospectiveEdit, error) {
	edits := make([]prospectiveEdit, 0, len(entries))
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(stageDir, filepath.FromSlash(entry.Target)))
		if err != nil {
			return nil, fmt.Errorf("read staged file %q: %w", entry.Target, err)
		}
		edits = append(edits, prospectiveEdit{Target: entry.Target, Content: string(content)})
	}
	return edits, nil
}

func manifestScope(manifest stageManifest) knowl.ScopeRef {
	if scope := strings.TrimSpace(manifest.Scope); scope != "" {
		return knowl.ScopeRef(scope)
	}
	scope, _, ok := strings.Cut(strings.TrimSpace(manifest.OperationID), ":")
	if !ok || scope == "" {
		return ""
	}
	return knowl.ScopeRef(scope)
}

func (workspace *Workspace) validateProspectivePlanLocked(scope knowl.ScopeRef, edits []prospectiveEdit) error {
	pageTargets, err := workspace.currentPageTargetsLocked()
	if err != nil {
		return err
	}
	for _, edit := range edits {
		pageID, ok := internalwiki.PageIDFromPath(edit.Target)
		if ok {
			pageTargets[pageID] = struct{}{}
		}
	}
	rawRefs, err := workspace.acceptedRawSourceKeysLocked(scope)
	if err != nil {
		return err
	}
	indexPath := filepath.ToSlash(filepath.Join(workspaceWikiDir, "index.md"))
	for _, edit := range edits {
		if edit.Target == indexPath {
			targets, malformed := internalwiki.IndexTargets(edit.Content)
			if malformed {
				return contentInvalidError(edit.Target, "index.malformed")
			}
			for _, target := range targets {
				if _, exists := pageTargets[knowl.PageID(target)]; !exists {
					return contentInvalidError(edit.Target, "index.broken_page")
				}
			}
			continue
		}
		pageID, ok := internalwiki.PageIDFromPath(edit.Target)
		if !ok {
			continue
		}
		if err := validateOrdinaryPageEdit(edit.Target, pageID, edit.Content, rawRefs, pageTargets); err != nil {
			return err
		}
	}
	return nil
}

func validateOrdinaryPageEdit(target string, pageID knowl.PageID, content string, rawRefs map[string]struct{}, pageTargets map[knowl.PageID]struct{}) error {
	metadata, err := internalwiki.ParseFrontmatter(content)
	if err != nil {
		return contentInvalidError(target, "frontmatter.malformed")
	}
	if metadata.ID == "" {
		return contentInvalidError(target, "frontmatter.id_missing")
	}
	if metadata.ID != string(pageID) {
		return contentInvalidError(target, "frontmatter.id_mismatch")
	}
	if metadata.Title == "" {
		return contentInvalidError(target, "frontmatter.title_missing")
	}
	if metadata.Type == "" {
		return contentInvalidError(target, "frontmatter.type_missing")
	}
	nonEmptySourceRefs := 0
	for _, sourceRef := range metadata.SourceRefs {
		if sourceRef == "" {
			continue
		}
		nonEmptySourceRefs++
		if _, exists := rawRefs[sourceRef]; !exists {
			return contentInvalidError(target, "citation.unknown_source")
		}
	}
	if nonEmptySourceRefs == 0 {
		return contentInvalidError(target, "citation.missing")
	}
	targets, malformed := internalwiki.MarkdownTargets(content)
	if malformed {
		return contentInvalidError(target, "link.malformed")
	}
	for _, linkedTarget := range targets {
		if _, exists := pageTargets[knowl.PageID(linkedTarget)]; !exists {
			return contentInvalidError(target, "link.broken")
		}
	}
	return nil
}

func (workspace *Workspace) acceptedRawSourceKeysLocked(scope knowl.ScopeRef) (map[string]struct{}, error) {
	records, err := workspace.inspectRawSourcesLocked(scope)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record.Valid {
			keys[sourceRefKey(record.Source)] = struct{}{}
		}
	}
	return keys, nil
}

func (workspace *Workspace) currentPageTargetsLocked() (map[knowl.PageID]struct{}, error) {
	wikiRoot := filepath.Join(workspace.root, workspaceWikiDir)
	if err := rejectSymlinkPath(workspace.root, wikiRoot); err != nil {
		return nil, err
	}
	targets := make(map[knowl.PageID]struct{})
	err := filepath.WalkDir(wikiRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in wiki: %w", ErrPathRejected)
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != markdownExt {
			return nil
		}
		relative, relErr := filepath.Rel(workspace.root, path)
		if relErr != nil {
			return relErr
		}
		pageID, ok := internalwiki.PageIDFromPath(filepath.ToSlash(relative))
		if ok {
			targets[pageID] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enumerate canonical pages: %w", err)
	}
	return targets, nil
}

func sourceRefKey(source knowl.AcceptedSource) string {
	return source.Source.Adapter + ":" + source.Source.ID + "@" + source.Version.Version
}

func contentInvalidError(target, rule string) error {
	return fmt.Errorf("content validation failed for %q (%s): %w", target, rule, ErrContentInvalid)
}

func stagedFilesMatch(stageDir string, entries []stageEntry) bool {
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(stageDir, filepath.FromSlash(entry.Target)))
		if err != nil || digestBytes(content) != entry.Digest {
			return false
		}
	}
	return true
}

func validateStagedPaths(root, stageDir string, entries []stageEntry) error {
	for _, entry := range entries {
		if err := rejectSymlinkPath(root, filepath.Join(stageDir, filepath.FromSlash(entry.Target))); err != nil {
			return err
		}
	}
	return nil
}

func appendLogEntry(existing []byte, manifest stageManifest, generation string) ([]byte, error) {
	entry, err := json.Marshal(logEntry{
		OperationID:  manifest.OperationID,
		Generation:   generation,
		SchemaDigest: manifest.SchemaDigest,
		SourceRefs:   append([]string(nil), manifest.SourceRefs...),
		Files:        entryTargets(manifest.Entries),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal commit log entry: %w", err)
	}
	content := append([]byte(nil), existing...)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	content = append(content, []byte("- ")...)
	content = append(content, entry...)
	content = append(content, '\n')
	return content, nil
}

func canonicalEntriesMatch(root string, entries []stageEntry) bool {
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Target)))
		if err != nil || digestBytes(content) != entry.Digest {
			return false
		}
	}
	return true
}

func commitTargets(entries []stageEntry) []string {
	targets := append([]stageEntry(nil), entries...)
	targets = append(targets, stageEntry{Target: filepath.ToSlash(filepath.Join(workspaceWikiDir, "log.md"))})
	return entryTargets(targets)
}

func validateWikiPath(raw string) error {
	clean := filepath.ToSlash(filepath.Clean(raw))
	if clean == "." || !strings.HasPrefix(clean, workspaceWikiDir+"/") || clean == filepath.Join(workspaceWikiDir, "log.md") || filepath.Ext(clean) != markdownExt || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return fmt.Errorf("wiki path %q: %w", raw, ErrPathRejected)
	}
	return nil
}

func validateCommitTarget(target string) error {
	if target == filepath.ToSlash(filepath.Join(workspaceWikiDir, "log.md")) {
		return nil
	}
	return validateWikiPath(target)
}

func validateRecoveryTarget(target string) error {
	if err := validateCommitTarget(target); err != nil {
		return fmt.Errorf("recovery target %q: %w", target, err)
	}
	return nil
}

func validateRecoveryBackup(backup, recoveryRoot string) error {
	if strings.TrimSpace(backup) == "" {
		return fmt.Errorf("recovery preimage is missing: %w", ErrWorkspaceInvalid)
	}
	absolute, err := filepath.Abs(backup)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(recoveryRoot)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("recovery preimage escapes recovery directory: %w", ErrPathRejected)
	}
	return rejectSymlinkPath(root, absolute)
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

func markdownSourceRefs(content []byte) []string {
	return internalwiki.SourceRefs(string(content))
}

func markdownLinks(from knowl.PageID, content []byte) []knowl.LinkReference {
	return internalwiki.Links(from, string(content))
}

func uniqueLinks(links []knowl.LinkReference) []knowl.LinkReference {
	if len(links) < 2 {
		return links
	}
	unique := links[:0]
	for _, link := range links {
		if len(unique) == 0 || unique[len(unique)-1].From != link.From || unique[len(unique)-1].To != link.To || unique[len(unique)-1].Relation != link.Relation {
			unique = append(unique, link)
		}
	}
	return unique
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
