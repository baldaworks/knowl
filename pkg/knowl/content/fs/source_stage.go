package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
	"gopkg.in/yaml.v3"
)

const (
	maxSourceStageEntries = 2048
	maxSourceStageFile    = 64 << 20
	maxSourceStageBytes   = 512 << 20
)

// StageSourcePlan writes a verified source-owned plan without changing canonical files.
func (workspace *Workspace) StageSourcePlan(ctx context.Context, plan knowl.SourceMutationPlan) (knowl.StagedSourceMutation, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.StagedSourceMutation{}, err
	}
	normalized, err := app.NormalizeSourceMutationPlan(plan)
	if err != nil {
		return knowl.StagedSourceMutation{}, err
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	stageDir := workspace.sourceStageDir(normalized.Scope, normalized.SourceID, normalized.RunID)
	if err := rejectSymlinkPath(workspace.root, stageDir); err != nil {
		return knowl.StagedSourceMutation{}, err
	}
	if _, err := os.Stat(stageDir); err == nil {
		manifest, readErr := readStageManifest(filepath.Join(stageDir, "manifest.yaml"))
		if readErr != nil || !validSourceStageManifest(manifest) || !sameSourceStagePlan(manifest, normalized) {
			return knowl.StagedSourceMutation{}, ErrPlanConflict
		}
		if err := validateStagedPaths(workspace.root, stageDir, manifest.Entries); err != nil {
			return knowl.StagedSourceMutation{}, err
		}
		if err := validateSourceStagedBounds(stageDir, manifest.Entries); err != nil {
			return knowl.StagedSourceMutation{}, err
		}
		if !stagedFilesMatch(stageDir, manifest.Entries) {
			return knowl.StagedSourceMutation{}, ErrPlanConflict
		}
		if err := workspace.validateProspectiveSourcePlanLocked(normalized); err != nil {
			return knowl.StagedSourceMutation{}, err
		}
		return stagedSourceFromManifest(manifest), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return knowl.StagedSourceMutation{}, fmt.Errorf("stat source staging directory: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return knowl.StagedSourceMutation{}, fmt.Errorf("create source staging directory: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(stageDir)
		}
	}()
	entries := make([]stageEntry, 0, len(normalized.Mutations))
	for _, mutation := range normalized.Mutations {
		target := filepath.Join(workspace.root, filepath.FromSlash(mutation.Path))
		if err := rejectSymlinkPath(workspace.root, target); err != nil {
			return knowl.StagedSourceMutation{}, err
		}
		current, readErr := os.ReadFile(target)
		exists := readErr == nil
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return knowl.StagedSourceMutation{}, fmt.Errorf("read source target %q: %w", mutation.Path, readErr)
		}
		if mutation.ExpectedDigest == "" && exists {
			return knowl.StagedSourceMutation{}, fmt.Errorf("source target %q must be absent: %w", mutation.Path, ErrPrecondition)
		}
		if mutation.ExpectedDigest != "" && (!exists || digestBytes(current) != mutation.ExpectedDigest) {
			return knowl.StagedSourceMutation{}, fmt.Errorf("source target %q digest changed: %w", mutation.Path, ErrPrecondition)
		}
		entry := stageEntry{Action: mutation.Action, Target: mutation.Path, ExpectedDigest: mutation.ExpectedDigest}
		if mutation.Action == knowl.SourceMutationWrite {
			entry.Digest = digestBytes(mutation.Content)
			stagedPath := filepath.Join(stageDir, filepath.FromSlash(mutation.Path))
			if err := os.MkdirAll(filepath.Dir(stagedPath), 0o700); err != nil {
				return knowl.StagedSourceMutation{}, fmt.Errorf("create source staged parent: %w", err)
			}
			if err := writeAtomic(stagedPath, mutation.Content, 0o600); err != nil {
				return knowl.StagedSourceMutation{}, fmt.Errorf("stage source target %q: %w", mutation.Path, err)
			}
		}
		entries = append(entries, entry)
	}
	if err := workspace.validateProspectiveSourcePlanLocked(normalized); err != nil {
		return knowl.StagedSourceMutation{}, err
	}
	manifest := stageManifest{
		OperationID: string(normalized.RunID), Writer: stageWriterSource, SourceID: string(normalized.SourceID),
		Scope: string(normalized.Scope), Entries: entries,
	}
	metadata, err := yaml.Marshal(manifest)
	if err != nil {
		return knowl.StagedSourceMutation{}, fmt.Errorf("marshal source staging manifest: %w", err)
	}
	if len(metadata) > maxStageManifestBytes {
		return knowl.StagedSourceMutation{}, ErrPlanConflict
	}
	if err := writeAtomic(filepath.Join(stageDir, "manifest.yaml"), metadata, 0o600); err != nil {
		return knowl.StagedSourceMutation{}, fmt.Errorf("write source staging manifest: %w", err)
	}
	complete = true
	return stagedSourceFromManifest(manifest), nil
}

// LoadSourceStage returns one complete source-owned staged artifact after restart.
func (workspace *Workspace) LoadSourceStage(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID, runID knowl.SyncRunID) (knowl.StagedSourceMutation, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.StagedSourceMutation{}, err
	}
	if strings.TrimSpace(string(scope)) == "" || app.ValidateSourceID(sourceID) != nil || app.ValidateSyncRunID(runID) != nil {
		return knowl.StagedSourceMutation{}, app.ErrSourceMutationInvalid
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	stageDir := workspace.sourceStageDir(scope, sourceID, runID)
	if err := rejectSymlinkPath(workspace.root, stageDir); err != nil {
		return knowl.StagedSourceMutation{}, err
	}
	manifest, err := readStageManifest(filepath.Join(stageDir, "manifest.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return knowl.StagedSourceMutation{}, app.ErrStageNotFound
	}
	if err != nil {
		return knowl.StagedSourceMutation{}, ErrPlanConflict
	}
	if manifestWriter(manifest) != stageWriterSource || manifest.OperationID != string(runID) || manifest.Scope != string(scope) || manifest.SourceID != string(sourceID) || !validSourceStageManifest(manifest) {
		return knowl.StagedSourceMutation{}, ErrPlanConflict
	}
	if err := validateStagedPaths(workspace.root, stageDir, manifest.Entries); err != nil {
		return knowl.StagedSourceMutation{}, err
	}
	if err := validateSourceStagedBounds(stageDir, manifest.Entries); err != nil {
		return knowl.StagedSourceMutation{}, err
	}
	if !stagedFilesMatch(stageDir, manifest.Entries) {
		return knowl.StagedSourceMutation{}, ErrPlanConflict
	}
	return stagedSourceFromManifest(manifest), nil
}

func (workspace *Workspace) sourceStageDir(scope knowl.ScopeRef, _ knowl.SourceID, runID knowl.SyncRunID) string {
	identity := stageWriterSource + "\x00" + string(scope) + "\x00" + string(runID)
	return filepath.Join(workspace.root, knowlDir, "staging", token(identity))
}

func stagedSourceFromManifest(manifest stageManifest) knowl.StagedSourceMutation {
	return knowl.StagedSourceMutation{
		RunID: knowl.SyncRunID(manifest.OperationID), Scope: knowl.ScopeRef(manifest.Scope), SourceID: knowl.SourceID(manifest.SourceID),
		Generation: stageGeneration(manifest), Files: entryTargets(manifest.Entries), CreatedAt: time.Now().UTC(),
	}
}

func sameSourceStagePlan(manifest stageManifest, plan knowl.SourceMutationPlan) bool {
	if manifestWriter(manifest) != stageWriterSource || manifest.OperationID != string(plan.RunID) || manifest.Scope != string(plan.Scope) || manifest.SourceID != string(plan.SourceID) || len(manifest.Entries) != len(plan.Mutations) {
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

func validateSourceStagedBounds(stageDir string, entries []stageEntry) error {
	if len(entries) > maxSourceStageEntries {
		return ErrPlanConflict
	}
	total := int64(0)
	for _, entry := range entries {
		if entryAction(entry) == knowl.SourceMutationDelete {
			continue
		}
		info, err := os.Stat(filepath.Join(stageDir, filepath.FromSlash(entry.Target)))
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxSourceStageFile || total > maxSourceStageBytes-info.Size() {
			return ErrPlanConflict
		}
		total += info.Size()
	}
	return nil
}

func (workspace *Workspace) validateProspectiveSourcePlanLocked(plan knowl.SourceMutationPlan) error {
	pageTargets, err := workspace.currentPageTargetsLocked()
	if err != nil {
		return err
	}
	for _, mutation := range plan.Mutations {
		pageID, markdown := knowlwiki.PageIDFromPath(mutation.Path)
		if !markdown {
			continue
		}
		if mutation.Action == knowl.SourceMutationDelete {
			delete(pageTargets, pageID)
		} else {
			pageTargets[pageID] = struct{}{}
		}
	}
	rawRefs, err := workspace.acceptedRawSourceKeysLocked(plan.Scope)
	if err != nil {
		return err
	}
	for _, mutation := range plan.Mutations {
		pageID, markdown := knowlwiki.PageIDFromPath(mutation.Path)
		if !markdown || mutation.Action != knowl.SourceMutationWrite {
			continue
		}
		content := string(mutation.Content)
		if err := validateOrdinaryPageEdit(mutation.Path, pageID, content, rawRefs, pageTargets); err != nil {
			return err
		}
		metadata, err := knowlwiki.ParseFrontmatter(content)
		if err != nil || metadata.SourceDocument == nil || app.ValidateOwnedSourceDocument(plan.SourceID, *metadata.SourceDocument) != nil {
			return contentInvalidError(mutation.Path, "frontmatter.source_document_invalid")
		}
	}
	return nil
}
