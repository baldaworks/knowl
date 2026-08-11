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

	"github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

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
