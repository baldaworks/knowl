package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

const maxStageManifestBytes = 1 << 20

// StagePlan writes a validated plan below .knowl/staging without touching canonical files.
func (workspace *Workspace) StagePlan(ctx context.Context, plan knowl.ValidatedEditPlan) (knowl.StagedChange, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.StagedChange{}, err
	}
	if strings.TrimSpace(plan.OperationID) == "" {
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
		if err := validateMaintainerWikiPath(edit.Path); err != nil {
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

// LoadStage returns one complete, verified operation-keyed staged artifact.
func (workspace *Workspace) LoadStage(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.StagedChange, error) {
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
	info, err := os.Stat(stageDir)
	if errors.Is(err, os.ErrNotExist) {
		return knowl.StagedChange{}, app.ErrStageNotFound
	}
	if err != nil {
		return knowl.StagedChange{}, fmt.Errorf("inspect staged artifact: %w", err)
	}
	if !info.IsDir() {
		return knowl.StagedChange{}, ErrPlanConflict
	}
	manifestPath := filepath.Join(stageDir, "manifest.yaml")
	if err := rejectSymlinkPath(workspace.root, manifestPath); err != nil {
		return knowl.StagedChange{}, err
	}
	manifest, err := readStageManifest(manifestPath)
	if err != nil {
		return knowl.StagedChange{}, fmt.Errorf("read staged artifact manifest: %w", ErrPlanConflict)
	}
	if manifestWriter(manifest) != stageWriterMaintainer || manifest.OperationID != string(id) || manifestScope(manifest) != scope || !validStageManifest(manifest) {
		return knowl.StagedChange{}, ErrPlanConflict
	}
	if err := validateStagedPaths(workspace.root, stageDir, manifest.Entries); err != nil {
		return knowl.StagedChange{}, err
	}
	if err := validateStagedFileBounds(stageDir, manifest.Entries, app.DefaultPlanLimits().MaxFileBytes); err != nil {
		return knowl.StagedChange{}, err
	}
	schema, err := os.ReadFile(filepath.Join(workspace.root, schemaFile))
	if err != nil {
		return knowl.StagedChange{}, fmt.Errorf("read schema for staged artifact: %w", err)
	}
	if digestBytes(schema) != manifest.SchemaDigest {
		return knowl.StagedChange{}, ErrPrecondition
	}
	staged, err := stagedChangeFromManifest(stageDir, manifest)
	if err != nil {
		return knowl.StagedChange{}, err
	}
	return staged, nil
}

func validStageManifest(manifest stageManifest) bool {
	switch manifestWriter(manifest) {
	case stageWriterMaintainer:
		return validMaintainerStageManifest(manifest)
	case stageWriterSource:
		return validSourceStageManifest(manifest)
	default:
		return false
	}
}

func validMaintainerStageManifest(manifest stageManifest) bool {
	limits := app.DefaultPlanLimits()
	if manifest.SourceID != "" || !validSHA256(manifest.SchemaDigest) || !validSHA256(manifest.LogExpectedDigest) || !validSHA256(manifest.LogDigest) ||
		len(manifest.SourceRefs) == 0 || len(manifest.SourceRefs) > limits.MaxSourceRefs || len(manifest.Entries) > limits.MaxFiles {
		return false
	}
	seenRefs := make(map[string]struct{}, len(manifest.SourceRefs))
	for _, ref := range manifest.SourceRefs {
		trimmed := strings.TrimSpace(ref)
		if trimmed == "" {
			return false
		}
		if _, exists := seenRefs[trimmed]; exists {
			return false
		}
		seenRefs[trimmed] = struct{}{}
	}
	seenTargets := make(map[string]struct{}, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if entryAction(entry) != knowl.SourceMutationWrite || validateCommitTarget(entry.Target) != nil || !validSHA256(entry.Digest) || (entry.ExpectedDigest != "" && !validSHA256(entry.ExpectedDigest)) {
			return false
		}
		if _, exists := seenTargets[entry.Target]; exists {
			return false
		}
		seenTargets[entry.Target] = struct{}{}
	}
	return stageGeneration(manifest) != ""
}

func validSourceStageManifest(manifest stageManifest) bool {
	if app.ValidateSourceID(knowl.SourceID(manifest.SourceID)) != nil || app.ValidateSyncRunID(knowl.SyncRunID(manifest.OperationID)) != nil || strings.TrimSpace(manifest.Scope) == "" || manifest.SchemaDigest != "" || len(manifest.SourceRefs) != 0 || manifest.LogExpectedDigest != "" || manifest.LogDigest != "" || len(manifest.Entries) == 0 || len(manifest.Entries) > maxSourceStageEntries {
		return false
	}
	seenTargets := make(map[string]struct{}, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if validateSourceTarget(manifest.SourceID, entry.Target) != nil || (entry.ExpectedDigest != "" && !validSHA256(entry.ExpectedDigest)) {
			return false
		}
		switch entryAction(entry) {
		case knowl.SourceMutationWrite:
			if !validSHA256(entry.Digest) {
				return false
			}
		case knowl.SourceMutationDelete:
			if entry.ExpectedDigest == "" || entry.Digest != "" {
				return false
			}
		default:
			return false
		}
		if _, exists := seenTargets[entry.Target]; exists {
			return false
		}
		seenTargets[entry.Target] = struct{}{}
	}
	return stageGeneration(manifest) != ""
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateStagedFileBounds(stageDir string, entries []stageEntry, maxBytes int) error {
	for _, entry := range entries {
		path := filepath.Join(stageDir, filepath.FromSlash(entry.Target))
		if entryAction(entry) == knowl.SourceMutationDelete {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				return ErrPlanConflict
			}
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > int64(maxBytes) {
			return ErrPlanConflict
		}
	}
	return nil
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
	core := stageManifest{OperationID: manifest.OperationID, Writer: manifest.Writer, SourceID: manifest.SourceID, Scope: manifest.Scope, SchemaDigest: manifest.SchemaDigest, SourceRefs: manifest.SourceRefs, Entries: manifest.Entries}
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
		if entryAction(entry) != knowl.SourceMutationWrite {
			return nil, ErrPlanConflict
		}
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
		path := filepath.Join(stageDir, filepath.FromSlash(entry.Target))
		if entryAction(entry) == knowl.SourceMutationDelete {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				return false
			}
			continue
		}
		content, err := os.ReadFile(path)
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
