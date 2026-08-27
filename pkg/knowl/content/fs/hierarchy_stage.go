package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

// StageHierarchyPlan stages catalog-only writes and managed deletes without
// changing canonical content.
func (workspace *Workspace) StageHierarchyPlan(ctx context.Context, id knowl.OperationID, plan knowl.ValidatedHierarchyPlan) (knowl.StagedChange, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.StagedChange{}, err
	}
	if strings.TrimSpace(string(id)) == "" || strings.TrimSpace(string(plan.Scope)) == "" ||
		!validSHA256(plan.SchemaDigest) || !validSHA256(plan.SnapshotDigest) || len(plan.Mutations) == 0 {
		return knowl.StagedChange{}, ErrPlanConflict
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	stageDir := filepath.Join(workspace.root, knowlDir, "staging", token(string(id)))
	if err := rejectSymlinkPath(workspace.root, stageDir); err != nil {
		return knowl.StagedChange{}, err
	}
	if _, err := os.Stat(stageDir); err == nil {
		manifest, readErr := readStageManifest(filepath.Join(stageDir, "manifest.yaml"))
		if readErr != nil || !validHierarchyStageManifest(manifest) || !sameHierarchyStagePlan(manifest, id, plan) {
			return knowl.StagedChange{}, ErrPlanConflict
		}
		if err := validateStagedPaths(workspace.root, stageDir, manifest.Entries); err != nil {
			return knowl.StagedChange{}, err
		}
		if err := validateStagedFileBounds(stageDir, manifest.Entries, app.DefaultHierarchyLimits().MaxCatalogBytes); err != nil {
			return knowl.StagedChange{}, err
		}
		return stagedChangeFromManifest(stageDir, manifest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return knowl.StagedChange{}, fmt.Errorf("stat hierarchy staging directory: %w", err)
	}

	schema, err := os.ReadFile(filepath.Join(workspace.root, schemaFile))
	if err != nil {
		return knowl.StagedChange{}, fmt.Errorf("read schema before hierarchy staging: %w", err)
	}
	if digestBytes(schema) != plan.SchemaDigest {
		return knowl.StagedChange{}, fmt.Errorf("schema changed after hierarchy planning: %w", ErrPrecondition)
	}
	snapshotDigest, err := workspace.hierarchySnapshotDigestLocked(plan.Scope)
	if err != nil {
		return knowl.StagedChange{}, err
	}
	if snapshotDigest != plan.SnapshotDigest {
		return knowl.StagedChange{}, fmt.Errorf("canonical snapshot changed after hierarchy planning: %w", ErrPrecondition)
	}

	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return knowl.StagedChange{}, fmt.Errorf("create hierarchy staging directory: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(stageDir)
		}
	}()
	entries, err := workspace.stageHierarchyMutationsLocked(stageDir, plan.Mutations)
	if err != nil {
		return knowl.StagedChange{}, err
	}
	if err := workspace.validateProspectiveHierarchyLocked(plan.Mutations); err != nil {
		return knowl.StagedChange{}, err
	}
	manifest := stageManifest{
		OperationID: string(id), Writer: stageWriterHierarchy, Scope: string(plan.Scope),
		SchemaDigest: plan.SchemaDigest, SnapshotDigest: plan.SnapshotDigest, Entries: entries,
		LogDate: workspace.now().UTC().Format(time.DateOnly),
	}
	coreMetadata, err := yaml.Marshal(manifest)
	if err != nil {
		return knowl.StagedChange{}, fmt.Errorf("marshal hierarchy staging manifest: %w", err)
	}
	generation := digestBytes(coreMetadata)
	logPath := filepath.Join(workspace.root, canonicalLogPath)
	if err := rejectSymlinkPath(workspace.root, logPath); err != nil {
		return knowl.StagedChange{}, err
	}
	logBefore, err := os.ReadFile(logPath)
	if err != nil {
		return knowl.StagedChange{}, fmt.Errorf("read canonical log before hierarchy staging: %w", err)
	}
	logAfter, err := appendLogEntry(logBefore, manifest, generation)
	if err != nil {
		return knowl.StagedChange{}, err
	}
	manifest.LogExpectedDigest = digestBytes(logBefore)
	manifest.LogDigest = digestBytes(logAfter)
	metadata, err := yaml.Marshal(manifest)
	if err != nil {
		return knowl.StagedChange{}, fmt.Errorf("marshal hierarchy staging manifest: %w", err)
	}
	if len(metadata) > maxStageManifestBytes {
		return knowl.StagedChange{}, fmt.Errorf("hierarchy staging manifest bytes %d exceed %d: %w", len(metadata), maxStageManifestBytes, ErrPlanConflict)
	}
	if err := writeAtomic(filepath.Join(stageDir, "manifest.yaml"), metadata, 0o600); err != nil {
		return knowl.StagedChange{}, fmt.Errorf("write hierarchy staging manifest: %w", err)
	}
	complete = true
	return knowl.StagedChange{OperationID: string(id), Digest: stageGeneration(manifest), Files: entryTargets(entries), CreatedAt: workspace.now().UTC()}, nil
}

// LoadHierarchyStage loads a complete hierarchy artifact after restart.
func (workspace *Workspace) LoadHierarchyStage(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.StagedChange, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.StagedChange{}, err
	}
	if strings.TrimSpace(string(scope)) == "" || strings.TrimSpace(string(id)) == "" {
		return knowl.StagedChange{}, ErrPlanConflict
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	stageDir := filepath.Join(workspace.root, knowlDir, "staging", token(string(id)))
	if err := rejectSymlinkPath(workspace.root, stageDir); err != nil {
		return knowl.StagedChange{}, err
	}
	manifest, err := readStageManifest(filepath.Join(stageDir, "manifest.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return knowl.StagedChange{}, app.ErrStageNotFound
	}
	if err != nil || manifestWriter(manifest) != stageWriterHierarchy || manifest.OperationID != string(id) ||
		manifestScope(manifest) != scope || !validHierarchyStageManifest(manifest) {
		return knowl.StagedChange{}, ErrPlanConflict
	}
	if err := validateStagedPaths(workspace.root, stageDir, manifest.Entries); err != nil {
		return knowl.StagedChange{}, err
	}
	if err := validateStagedFileBounds(stageDir, manifest.Entries, app.DefaultHierarchyLimits().MaxCatalogBytes); err != nil {
		return knowl.StagedChange{}, err
	}
	schema, err := os.ReadFile(filepath.Join(workspace.root, schemaFile))
	if err != nil || digestBytes(schema) != manifest.SchemaDigest {
		return knowl.StagedChange{}, ErrPrecondition
	}
	return stagedChangeFromManifest(stageDir, manifest)
}

func (workspace *Workspace) stageHierarchyMutationsLocked(stageDir string, mutations []knowl.HierarchyMutation) ([]stageEntry, error) {
	limits := app.DefaultHierarchyLimits()
	if len(mutations) > limits.MaxEdits {
		return nil, fmt.Errorf("hierarchy mutation count %d exceeds %d: %w", len(mutations), limits.MaxEdits, ErrPlanConflict)
	}
	entries := make([]stageEntry, 0, len(mutations))
	previous := ""
	for _, mutation := range mutations {
		if !app.IsManagedHierarchyCatalog(mutation.Path) || (previous != "" && !hierarchyTargetLess(previous, mutation.Path)) {
			return nil, fmt.Errorf("hierarchy target %q is outside or out of order: %w", mutation.Path, ErrPathRejected)
		}
		previous = mutation.Path
		target := filepath.Join(workspace.root, filepath.FromSlash(mutation.Path))
		if err := rejectSymlinkPath(workspace.root, target); err != nil {
			return nil, err
		}
		current, readErr := os.ReadFile(target)
		exists := readErr == nil
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, fmt.Errorf("read hierarchy target %q: %w", mutation.Path, readErr)
		}
		if mutation.ExpectedDigest == "" && exists {
			return nil, fmt.Errorf("existing hierarchy target %q requires expected digest: %w", mutation.Path, ErrPrecondition)
		}
		if mutation.ExpectedDigest != "" && (!exists || digestBytes(current) != mutation.ExpectedDigest) {
			return nil, fmt.Errorf("hierarchy target %q digest changed: %w", mutation.Path, ErrPrecondition)
		}
		entry := stageEntry{Action: mutation.Action, Target: mutation.Path, ExpectedDigest: mutation.ExpectedDigest}
		switch mutation.Action {
		case knowl.SourceMutationWrite:
			if len(mutation.Content) == 0 || len(mutation.Content) > limits.MaxCatalogBytes {
				return nil, fmt.Errorf("hierarchy target %q content bytes exceed bound: %w", mutation.Path, ErrPlanConflict)
			}
			bundleRelative := strings.TrimPrefix(mutation.Path, workspaceWikiDir+"/")
			index, validateErr := okf.ValidateIndex(bundleRelative, mutation.Content, okfLimits(limits.MaxCatalogBytes))
			if validateErr != nil || (mutation.Path == canonicalIndexPath && index.ObservedVersion != okf.Version) {
				return nil, okfContentInvalidError(mutation.Path, validateErr)
			}
			entry.Digest = digestBytes(mutation.Content)
			stagedPath := filepath.Join(stageDir, filepath.FromSlash(mutation.Path))
			if err := os.MkdirAll(filepath.Dir(stagedPath), 0o700); err != nil {
				return nil, fmt.Errorf("create hierarchy staged parent: %w", err)
			}
			if err := writeAtomic(stagedPath, mutation.Content, 0o600); err != nil {
				return nil, fmt.Errorf("stage hierarchy target %q: %w", mutation.Path, err)
			}
		case knowl.SourceMutationDelete:
			if mutation.Path == canonicalIndexPath || !exists || mutation.ExpectedDigest == "" || len(mutation.Content) != 0 {
				return nil, fmt.Errorf("invalid hierarchy delete %q: %w", mutation.Path, ErrPlanConflict)
			}
		default:
			return nil, fmt.Errorf("invalid hierarchy action at %q: %w", mutation.Path, ErrPlanConflict)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func validHierarchyStageManifest(manifest stageManifest) bool {
	limits := app.DefaultHierarchyLimits()
	if manifestWriter(manifest) != stageWriterHierarchy || strings.TrimSpace(manifest.OperationID) == "" || strings.TrimSpace(manifest.Scope) == "" ||
		manifest.SourceID != "" || manifest.RequiredSourceRef != "" || len(manifest.SourceRefs) != 0 ||
		!validSHA256(manifest.SchemaDigest) || !validSHA256(manifest.SnapshotDigest) ||
		!validSHA256(manifest.LogExpectedDigest) || !validSHA256(manifest.LogDigest) || len(manifest.Entries) == 0 || len(manifest.Entries) > limits.MaxEdits {
		return false
	}
	if parsed, err := time.Parse(time.DateOnly, manifest.LogDate); err != nil || parsed.Format(time.DateOnly) != manifest.LogDate {
		return false
	}
	previous := ""
	for _, entry := range manifest.Entries {
		if !app.IsManagedHierarchyCatalog(entry.Target) || (previous != "" && !hierarchyTargetLess(previous, entry.Target)) ||
			(entry.ExpectedDigest != "" && !validSHA256(entry.ExpectedDigest)) {
			return false
		}
		previous = entry.Target
		switch entryAction(entry) {
		case knowl.SourceMutationWrite:
			if !validSHA256(entry.Digest) {
				return false
			}
		case knowl.SourceMutationDelete:
			if entry.Target == canonicalIndexPath || entry.ExpectedDigest == "" || entry.Digest != "" {
				return false
			}
		default:
			return false
		}
	}
	return stageGeneration(manifest) != ""
}

func hierarchyTargetLess(left, right string) bool {
	if left == canonicalIndexPath {
		return right != canonicalIndexPath
	}
	if right == canonicalIndexPath {
		return false
	}
	return left < right
}

func sameHierarchyStagePlan(manifest stageManifest, id knowl.OperationID, plan knowl.ValidatedHierarchyPlan) bool {
	if manifest.OperationID != string(id) || manifest.Scope != string(plan.Scope) || manifest.SchemaDigest != plan.SchemaDigest ||
		manifest.SnapshotDigest != plan.SnapshotDigest || len(manifest.Entries) != len(plan.Mutations) {
		return false
	}
	for index, entry := range manifest.Entries {
		mutation := plan.Mutations[index]
		digest := ""
		if mutation.Action == knowl.SourceMutationWrite {
			digest = digestBytes(mutation.Content)
		}
		if entryAction(entry) != mutation.Action || entry.Target != mutation.Path || entry.ExpectedDigest != mutation.ExpectedDigest || entry.Digest != digest {
			return false
		}
	}
	return true
}

func (workspace *Workspace) validateProspectiveHierarchyLocked(mutations []knowl.HierarchyMutation) error {
	documents, err := workspace.currentWikiDocumentsLocked()
	if err != nil {
		return err
	}
	for _, mutation := range mutations {
		if mutation.Action == knowl.SourceMutationDelete {
			delete(documents, mutation.Path)
		} else {
			documents[mutation.Path] = string(mutation.Content)
		}
	}
	return validateCatalogGraph(documents, nil, workspace.maxSourceBytes)
}

func hierarchyMutationsFromStage(stageDir string, manifest stageManifest) ([]knowl.HierarchyMutation, error) {
	mutations := make([]knowl.HierarchyMutation, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		mutation := knowl.HierarchyMutation{Action: entryAction(entry), Path: entry.Target, ExpectedDigest: entry.ExpectedDigest}
		if mutation.Action == knowl.SourceMutationWrite {
			content, err := os.ReadFile(filepath.Join(stageDir, filepath.FromSlash(entry.Target)))
			if err != nil {
				return nil, fmt.Errorf("read staged hierarchy target %q: %w", entry.Target, err)
			}
			mutation.Content = content
		}
		mutations = append(mutations, mutation)
	}
	sort.Slice(mutations, func(left, right int) bool { return mutations[left].Path < mutations[right].Path })
	return mutations, nil
}
