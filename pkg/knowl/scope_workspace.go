package knowl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/gofrs/flock"
)

const (
	scopeLayoutVersion    = 1
	scopeLayoutRecordName = "scope-layout.json"
	scopeLayoutLockName   = "scope-layout.lock"
	scopeLayoutRoot       = "root"
	maxScopeLayoutBytes   = 4 << 10
)

var (
	ErrScopeLayoutAmbiguous = errors.New("ambiguous scope workspace layout")
	ErrScopeWorkspaceUnsafe = errors.New("unsafe scope workspace layout")
	ErrScopeUnavailable     = errors.New("scope workspace is unavailable")
)

type scopeLayoutRecord struct {
	Version      int              `json:"version"`
	DefaultScope domain.ScopeName `json:"default_scope"`
	DefaultRoot  string           `json:"default_root"`
}

// ScopeWorkspace is one trusted durable identity and its isolated canonical
// filesystem adapter.
type ScopeWorkspace struct {
	Name      domain.ScopeName
	Ref       domain.ScopeRef
	Root      string
	Workspace *contentfs.Workspace
}

// ScopeWorkspaceResolver owns the stable mapping from trusted durable scope
// references to isolated filesystem adapters.
type ScopeWorkspaceResolver struct {
	hostRoot    string
	ordered     []ScopeWorkspace
	byRef       map[domain.ScopeRef]ScopeWorkspace
	unavailable map[domain.ScopeRef]error
}

// OpenScopeWorkspaces validates or records the canonical layout, then opens
// every configured scope. It never moves existing canonical content.
func OpenScopeWorkspaces(config Config) (*ScopeWorkspaceResolver, error) {
	return resolveScopeWorkspaces(config, true)
}

// ResolveScopeWorkspaces resolves trusted canonical/control paths without
// initializing or validating canonical content. It is used by commands such
// as validate, export, and format migration that own their next filesystem
// action explicitly.
func ResolveScopeWorkspaces(config Config) (*ScopeWorkspaceResolver, error) {
	return resolveScopeWorkspaces(config, false)
}

func resolveScopeWorkspaces(config Config, initialize bool) (*ScopeWorkspaceResolver, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	hostRoot, err := filepath.Abs(normalized.Workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve host workspace: %w", err)
	}
	hostRoot = filepath.Clean(hostRoot)
	if err := ensureDirectoryRoot(hostRoot); err != nil {
		return nil, err
	}
	registry := normalized.scopeRegistry
	for _, context := range registry.ordered {
		candidate := filepath.Join(hostRoot, "scopes", string(context.Name))
		if err := validateScopeCanonicalRoot(hostRoot, candidate, context.Name, false); err != nil {
			return nil, err
		}
	}
	layout, err := resolveScopeLayout(hostRoot, registry)
	if err != nil {
		return nil, err
	}

	resolver := &ScopeWorkspaceResolver{
		hostRoot:    hostRoot,
		ordered:     make([]ScopeWorkspace, 0, len(registry.ordered)),
		byRef:       make(map[domain.ScopeRef]ScopeWorkspace, len(registry.ordered)),
		unavailable: make(map[domain.ScopeRef]error),
	}
	for _, context := range registry.ordered {
		root := filepath.Join(hostRoot, "scopes", string(context.Name))
		legacyDefault := context.Name == registry.defaultName && layout.DefaultRoot == scopeLayoutRoot
		if legacyDefault {
			root = hostRoot
		}
		if err := validateScopeCanonicalRoot(hostRoot, root, context.Name, legacyDefault); err != nil {
			return nil, err
		}
		if err := ensureSafeDirectoryTree(hostRoot, root); err != nil {
			return nil, err
		}
		controlRoot := filepath.Join(hostRoot, ".knowl", "scopes", scopeControlKey(context.Ref))
		if legacyDefault {
			// Existing default staging/recovery remains readable without a migration.
			controlRoot = filepath.Join(hostRoot, ".knowl")
		}
		if err := ensureSafeDirectoryTree(hostRoot, controlRoot); err != nil {
			return nil, err
		}
		workspace, openErr := contentfs.New(root, contentfs.WithControlRoot(controlRoot, hostRoot))
		if openErr != nil {
			return nil, fmt.Errorf("open scope %q workspace: %w", context.Name, openErr)
		}
		resolved := ScopeWorkspace{Name: context.Name, Ref: context.Ref, Root: root, Workspace: workspace}
		resolver.ordered = append(resolver.ordered, resolved)
		resolver.byRef[context.Ref] = resolved
	}
	if err := validateResolvedCanonicalSets(resolver.ordered); err != nil {
		return nil, err
	}
	if !initialize {
		return resolver, nil
	}
	healthy := resolver.ordered[:0]
	for _, resolved := range resolver.ordered {
		if err := resolved.Workspace.Init(); err != nil {
			if errors.Is(err, contentfs.ErrPathRejected) {
				return nil, fmt.Errorf("initialize scope %q workspace: %w", resolved.Name, err)
			}
			resolver.unavailable[resolved.Ref] = fmt.Errorf("initialize scope %q workspace: %w", resolved.Name, err)
			delete(resolver.byRef, resolved.Ref)
			continue
		}
		if err := resolved.Workspace.Validate(); err != nil {
			if errors.Is(err, contentfs.ErrPathRejected) {
				return nil, fmt.Errorf("validate scope %q workspace: %w", resolved.Name, err)
			}
			resolver.unavailable[resolved.Ref] = fmt.Errorf("validate scope %q workspace: %w", resolved.Name, err)
			delete(resolver.byRef, resolved.Ref)
			continue
		}
		healthy = append(healthy, resolved)
	}
	resolver.ordered = healthy
	if len(resolver.ordered) == 0 {
		return nil, ErrScopeUnavailable
	}
	return resolver, nil
}

// Resolve returns the workspace for one trusted durable scope reference.
func (resolver *ScopeWorkspaceResolver) Resolve(ref domain.ScopeRef) (ScopeWorkspace, error) {
	if resolver == nil || strings.TrimSpace(string(ref)) == "" {
		return ScopeWorkspace{}, fmt.Errorf("%w: scope workspace is unavailable", domain.ErrScopeUnknown)
	}
	resolved, ok := resolver.byRef[ref]
	if !ok {
		if cause, unavailable := resolver.unavailable[ref]; unavailable {
			return ScopeWorkspace{}, errors.Join(ErrScopeUnavailable, cause)
		}
		return ScopeWorkspace{}, fmt.Errorf("%w: durable scope is not configured", domain.ErrScopeUnknown)
	}
	return resolved, nil
}

// All returns all trusted workspaces in stable public-name order.
func (resolver *ScopeWorkspaceResolver) All() []ScopeWorkspace {
	if resolver == nil {
		return nil
	}
	return append([]ScopeWorkspace(nil), resolver.ordered...)
}

func resolveScopeLayout(hostRoot string, registry *ScopeRegistry) (scopeLayoutRecord, error) {
	defaultNested := filepath.Join(hostRoot, "scopes", string(registry.defaultName))
	rootMarkers, err := scopeCanonicalMarkers(hostRoot)
	if err != nil {
		return scopeLayoutRecord{}, err
	}
	nestedMarkers, err := scopeCanonicalMarkers(defaultNested)
	if err != nil {
		return scopeLayoutRecord{}, err
	}
	if rootMarkers && nestedMarkers {
		return scopeLayoutRecord{}, ErrScopeLayoutAmbiguous
	}
	controlRoot := filepath.Join(hostRoot, ".knowl")
	if err := ensureSafeDirectoryTree(hostRoot, controlRoot); err != nil {
		return scopeLayoutRecord{}, fmt.Errorf("prepare host control directory: %w", err)
	}
	lockPath := filepath.Join(controlRoot, scopeLayoutLockName)
	if err := rejectUnsafeControlFile(lockPath); err != nil {
		return scopeLayoutRecord{}, err
	}
	lock := flock.New(lockPath)
	if err := lock.Lock(); err != nil {
		return scopeLayoutRecord{}, fmt.Errorf("lock scope layout: %w", err)
	}
	defer func() { _ = lock.Unlock() }()
	if err := rejectUnsafeControlFile(lockPath); err != nil {
		return scopeLayoutRecord{}, err
	}

	// Recheck after acquiring the cross-process layout lock.
	rootMarkers, err = scopeCanonicalMarkers(hostRoot)
	if err != nil {
		return scopeLayoutRecord{}, err
	}
	nestedMarkers, err = scopeCanonicalMarkers(defaultNested)
	if err != nil {
		return scopeLayoutRecord{}, err
	}
	if rootMarkers && nestedMarkers {
		return scopeLayoutRecord{}, ErrScopeLayoutAmbiguous
	}
	recordPath := filepath.Join(controlRoot, scopeLayoutRecordName)
	record, found, err := readScopeLayout(recordPath)
	if err != nil {
		return scopeLayoutRecord{}, err
	}
	if found {
		if err := validateScopeLayoutRecord(record, registry, rootMarkers, nestedMarkers); err != nil {
			return scopeLayoutRecord{}, err
		}
		return record, nil
	}
	defaultRoot := filepath.ToSlash(filepath.Join("scopes", string(registry.defaultName)))
	if rootMarkers || registry.legacyLayout && !nestedMarkers {
		defaultRoot = scopeLayoutRoot
	}
	record = scopeLayoutRecord{Version: scopeLayoutVersion, DefaultScope: registry.defaultName, DefaultRoot: defaultRoot}
	if err := writeScopeLayout(recordPath, record); err != nil {
		return scopeLayoutRecord{}, err
	}
	return record, nil
}

func rejectUnsafeControlFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrScopeWorkspaceUnsafe
	}
	return nil
}

func validateScopeLayoutRecord(record scopeLayoutRecord, registry *ScopeRegistry, rootMarkers, nestedMarkers bool) error {
	wantNested := filepath.ToSlash(filepath.Join("scopes", string(registry.defaultName)))
	if record.Version != scopeLayoutVersion || record.DefaultScope != registry.defaultName ||
		(record.DefaultRoot != scopeLayoutRoot && record.DefaultRoot != wantNested) {
		return ErrScopeLayoutAmbiguous
	}
	if record.DefaultRoot == scopeLayoutRoot && nestedMarkers || record.DefaultRoot == wantNested && rootMarkers {
		return ErrScopeLayoutAmbiguous
	}
	return nil
}

func readScopeLayout(path string) (scopeLayoutRecord, bool, error) {
	info, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return scopeLayoutRecord{}, false, nil
	}
	if statErr != nil {
		return scopeLayoutRecord{}, false, statErr
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxScopeLayoutBytes {
		return scopeLayoutRecord{}, false, ErrScopeLayoutAmbiguous
	}
	file, err := os.Open(path)
	if err != nil {
		return scopeLayoutRecord{}, false, fmt.Errorf("open scope layout record: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, maxScopeLayoutBytes+1))
	decoder.DisallowUnknownFields()
	var record scopeLayoutRecord
	if err := decoder.Decode(&record); err != nil {
		return scopeLayoutRecord{}, false, fmt.Errorf("decode scope layout record: %w", errors.Join(ErrScopeLayoutAmbiguous, err))
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return scopeLayoutRecord{}, false, ErrScopeLayoutAmbiguous
	}
	return record, true, nil
}

func writeScopeLayout(path string, record scopeLayoutRecord) error {
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".scope-layout-*")
	if err != nil {
		return fmt.Errorf("create scope layout record: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish scope layout record: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func ensureDirectoryRoot(root string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return fmt.Errorf("create host workspace: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrScopeWorkspaceUnsafe
	}
	return nil
}

func ensureSafeDirectoryTree(boundary, target string) error {
	if !pathContains(boundary, target) {
		return ErrScopeWorkspaceUnsafe
	}
	relative, err := filepath.Rel(boundary, target)
	if err != nil {
		return err
	}
	current := boundary
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if mkdirErr := os.Mkdir(current, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return mkdirErr
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrScopeWorkspaceUnsafe
		}
	}
	return nil
}

func scopeCanonicalMarkers(root string) (bool, error) {
	found := false
	for _, name := range scopeCanonicalMarkerNames() {
		info, err := os.Lstat(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return false, ErrScopeWorkspaceUnsafe
		}
		found = true
	}
	return found, nil
}

func scopeCanonicalMarkerNames() []string {
	return []string{
		filepath.Base("canonical/schema.md"),
		filepath.Base("canonical/raw"),
		filepath.Base("canonical/wiki"),
	}
}

func validateScopeCanonicalRoot(hostRoot, root string, name domain.ScopeName, legacyDefault bool) error {
	if !pathContains(hostRoot, root) {
		return ErrScopeWorkspaceUnsafe
	}
	if !legacyDefault {
		want := filepath.Join(hostRoot, "scopes", string(name))
		if filepath.Clean(root) != filepath.Clean(want) {
			return ErrScopeWorkspaceUnsafe
		}
	}
	current := hostRoot
	relative, err := filepath.Rel(hostRoot, root)
	if err != nil {
		return err
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrScopeWorkspaceUnsafe
		}
	}
	return nil
}

func validateResolvedCanonicalSets(workspaces []ScopeWorkspace) error {
	type canonicalPath struct {
		scope domain.ScopeName
		path  string
	}
	paths := make([]canonicalPath, 0, len(workspaces)*3)
	for _, workspace := range workspaces {
		for _, relative := range scopeCanonicalMarkerNames() {
			paths = append(paths, canonicalPath{scope: workspace.Name, path: filepath.Join(workspace.Root, relative)})
		}
	}
	sort.Slice(paths, func(left, right int) bool { return paths[left].path < paths[right].path })
	for left := range paths {
		for right := left + 1; right < len(paths); right++ {
			if paths[left].scope != paths[right].scope &&
				(pathContains(paths[left].path, paths[right].path) || pathContains(paths[right].path, paths[left].path)) {
				return ErrScopeWorkspaceUnsafe
			}
		}
	}
	return nil
}

func pathContains(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func scopeControlKey(ref domain.ScopeRef) string {
	digest := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(digest[:16])
}

// ScopedContentStore routes canonical operations only by trusted ScopeRef.
type ScopedContentStore struct{ resolver *ScopeWorkspaceResolver }

var _ app.ContentStore = (*ScopedContentStore)(nil)
var _ app.SourceContentStore = (*ScopedContentStore)(nil)
var _ app.RetirementContentReader = (*ScopedContentStore)(nil)
var _ app.CanonicalProvenanceReader = (*ScopedContentStore)(nil)
var _ app.WorkspaceReader = (*ScopedContentStore)(nil)
var _ app.WikiDirectoryReader = (*ScopedContentStore)(nil)
var _ app.WikiPageTreeReader = (*ScopedContentStore)(nil)
var _ app.RetirementProjectionReader = (*ScopedContentStore)(nil)

func NewScopedContentStore(resolver *ScopeWorkspaceResolver) (*ScopedContentStore, error) {
	if resolver == nil || len(resolver.ordered) == 0 {
		return nil, ErrScopeWorkspaceUnsafe
	}
	return &ScopedContentStore{resolver: resolver}, nil
}

func (store *ScopedContentStore) workspace(ref domain.ScopeRef) (*contentfs.Workspace, error) {
	resolved, err := store.resolver.Resolve(ref)
	if err != nil {
		return nil, err
	}
	return resolved.Workspace, nil
}

func (store *ScopedContentStore) AcceptSource(ctx context.Context, envelope domain.SourceEnvelope) (domain.AcceptedSource, error) {
	workspace, err := store.workspace(envelope.Scope)
	if err != nil {
		return domain.AcceptedSource{}, err
	}
	return workspace.AcceptSource(ctx, envelope)
}

func (store *ScopedContentStore) ReadSource(ctx context.Context, source domain.AcceptedSource, limits domain.ReadLimits) ([]byte, error) {
	workspace, err := store.workspace(source.Scope)
	if err != nil {
		return nil, err
	}
	return workspace.ReadSource(ctx, source, limits)
}

func (store *ScopedContentStore) Schema(ctx context.Context, ref domain.ScopeRef) (domain.SchemaDocument, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return domain.SchemaDocument{}, err
	}
	return workspace.Schema(ctx, ref)
}

func (store *ScopedContentStore) ReadPages(ctx context.Context, ref domain.ScopeRef, ids []domain.PageID, limits domain.ReadLimits) ([]domain.PageSnapshot, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return nil, err
	}
	return workspace.ReadPages(ctx, ref, ids, limits)
}

func (store *ScopedContentStore) HierarchySnapshotDigest(ctx context.Context, ref domain.ScopeRef) (string, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return "", err
	}
	return workspace.HierarchySnapshotDigest(ctx, ref)
}

func (store *ScopedContentStore) StagePlan(ctx context.Context, plan domain.ValidatedEditPlan) (domain.StagedChange, error) {
	workspace, err := store.workspace(plan.Scope)
	if err != nil {
		return domain.StagedChange{}, err
	}
	return workspace.StagePlan(ctx, plan)
}

func (store *ScopedContentStore) LoadStage(ctx context.Context, ref domain.ScopeRef, id domain.OperationID) (domain.StagedChange, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return domain.StagedChange{}, err
	}
	return workspace.LoadStage(ctx, ref, id)
}

func (store *ScopedContentStore) Commit(ctx context.Context, staged domain.StagedChange) (domain.ContentCommit, error) {
	workspace, err := store.workspace(staged.Scope)
	if err != nil {
		return domain.ContentCommit{}, err
	}
	return workspace.Commit(ctx, staged)
}

func (store *ScopedContentStore) StageHierarchyPlan(ctx context.Context, id domain.OperationID, plan domain.ValidatedHierarchyPlan) (domain.StagedChange, error) {
	workspace, err := store.workspace(plan.Scope)
	if err != nil {
		return domain.StagedChange{}, err
	}
	return workspace.StageHierarchyPlan(ctx, id, plan)
}

func (store *ScopedContentStore) LoadHierarchyStage(ctx context.Context, ref domain.ScopeRef, id domain.OperationID) (domain.StagedChange, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return domain.StagedChange{}, err
	}
	return workspace.LoadHierarchyStage(ctx, ref, id)
}

func (store *ScopedContentStore) CommitHierarchy(ctx context.Context, staged domain.StagedChange) (domain.ContentCommit, error) {
	workspace, err := store.workspace(staged.Scope)
	if err != nil {
		return domain.ContentCommit{}, err
	}
	return workspace.CommitHierarchy(ctx, staged)
}

func (store *ScopedContentStore) Recover(ctx context.Context) ([]domain.RecoveryResult, error) {
	var results []domain.RecoveryResult
	for _, resolved := range store.resolver.ordered {
		recovered, err := resolved.Workspace.Recover(ctx)
		if err != nil {
			return nil, fmt.Errorf("recover scope %q: %w", resolved.Name, err)
		}
		results = append(results, recovered...)
	}
	return results, nil
}

func (store *ScopedContentStore) Snapshot(ctx context.Context, ref domain.ScopeRef) (domain.WorkspaceSnapshot, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return domain.WorkspaceSnapshot{}, err
	}
	return workspace.Snapshot(ctx, ref)
}

func (store *ScopedContentStore) Inspect(ctx context.Context, ref domain.ScopeRef) (domain.WorkspaceInspection, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return domain.WorkspaceInspection{}, err
	}
	return workspace.Inspect(ctx, ref)
}

func (store *ScopedContentStore) SourceDigests(ctx context.Context, ref domain.ScopeRef, sourceID domain.SourceID, limit int) ([]app.SourceDigestEntry, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return nil, err
	}
	return workspace.SourceDigests(ctx, ref, sourceID, limit)
}

func (store *ScopedContentStore) StageSourcePlan(ctx context.Context, plan domain.SourceMutationPlan) (domain.StagedSourceMutation, error) {
	workspace, err := store.workspace(plan.Scope)
	if err != nil {
		return domain.StagedSourceMutation{}, err
	}
	return workspace.StageSourcePlan(ctx, plan)
}

func (store *ScopedContentStore) LoadSourceStage(ctx context.Context, ref domain.ScopeRef, sourceID domain.SourceID, runID domain.SyncRunID) (domain.StagedSourceMutation, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return domain.StagedSourceMutation{}, err
	}
	return workspace.LoadSourceStage(ctx, ref, sourceID, runID)
}

func (store *ScopedContentStore) CommitSource(ctx context.Context, staged domain.StagedSourceMutation) (domain.ContentCommit, error) {
	workspace, err := store.workspace(staged.Scope)
	if err != nil {
		return domain.ContentCommit{}, err
	}
	return workspace.CommitSource(ctx, staged)
}

func (store *ScopedContentStore) ResolveAcceptedSources(ctx context.Context, ref domain.ScopeRef, refs []string) (map[string]domain.AcceptedSource, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return nil, err
	}
	return workspace.ResolveAcceptedSources(ctx, ref, refs)
}

func (store *ScopedContentStore) PageProvenance(ctx context.Context, ref domain.ScopeRef, owner domain.SourceOwner, cursor app.CanonicalProvenanceCursor, limits app.CanonicalProvenanceLimits) (app.CanonicalProvenancePage, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return app.CanonicalProvenancePage{}, err
	}
	return workspace.PageProvenance(ctx, ref, owner, cursor, limits)
}

func (store *ScopedContentStore) CatalogChildren(ctx context.Context, ref domain.ScopeRef, parent domain.PageID, options app.OperatorReadOptions) (app.OperatorCatalogRead, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return app.OperatorCatalogRead{}, err
	}
	return workspace.CatalogChildren(ctx, ref, parent, options)
}

func (store *ScopedContentStore) WikiDirectoryChildren(ctx context.Context, ref domain.ScopeRef, directory string, options app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorWikiEntry], error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return app.OperatorReadPage[domain.OperatorWikiEntry]{}, err
	}
	return workspace.WikiDirectoryChildren(ctx, ref, directory, options)
}

func (store *ScopedContentStore) WikiPageChildren(ctx context.Context, ref domain.ScopeRef, directory string, options app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorWikiPage], error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return app.OperatorReadPage[domain.OperatorWikiPage]{}, err
	}
	return workspace.WikiPageChildren(ctx, ref, directory, options)
}

func (store *ScopedContentStore) PageSummaries(ctx context.Context, ref domain.ScopeRef, options app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorPageSummary], error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return app.OperatorReadPage[domain.OperatorPageSummary]{}, err
	}
	return workspace.PageSummaries(ctx, ref, options)
}

func (store *ScopedContentStore) Page(ctx context.Context, ref domain.ScopeRef, id domain.PageID, limits domain.ReadLimits) (domain.OperatorPage, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return domain.OperatorPage{}, err
	}
	return workspace.Page(ctx, ref, id, limits)
}

func (store *ScopedContentStore) SourceRevision(ctx context.Context, ref domain.ScopeRef, sourceRef string, limits domain.ReadLimits) (domain.OperatorSourceRevision, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return domain.OperatorSourceRevision{}, err
	}
	return workspace.SourceRevision(ctx, ref, sourceRef, limits)
}

func (store *ScopedContentStore) RetirementProjectionPending(ctx context.Context, ref domain.ScopeRef, id domain.OperationID) (bool, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return false, err
	}
	return workspace.RetirementProjectionPending(ctx, ref, id)
}

// ExportOKF exports exactly one selected portable bundle.
func (store *ScopedContentStore) ExportOKF(ctx context.Context, ref domain.ScopeRef, destination string, limits contentfs.ExportLimits) error {
	workspace, err := store.workspace(ref)
	if err != nil {
		return err
	}
	return workspace.ExportOKF(ctx, destination, limits)
}

// MigrateOKFV02 applies the explicit OKF migration only to one selected scope.
func (store *ScopedContentStore) MigrateOKFV02(ctx context.Context, ref domain.ScopeRef) (contentfs.MigrationResult, error) {
	workspace, err := store.workspace(ref)
	if err != nil {
		return contentfs.MigrationResult{}, err
	}
	return workspace.MigrateOKFV02(ctx)
}
