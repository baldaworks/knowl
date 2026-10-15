package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/internal/maintenanceauthority"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// BindMaintenanceAuthority marks a configured Host workspace as requiring
// sealed publication requests. Lifecycle decisions remain outside filesystem.
func (workspace *Workspace) BindMaintenanceAuthority(binding maintenanceauthority.Binding) error {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.maintenanceAuthority || !binding.Valid() {
		return ErrPlanConflict
	}
	workspace.maintenanceAuthority = true
	return nil
}
func (workspace *Workspace) StageMaintenanceBound(ctx context.Context, request maintenanceauthority.StageRequest) (knowl.StagedChange, error) {
	if request.Stamp() == nil {
		return knowl.StagedChange{}, ErrPlanConflict
	}
	return workspace.stagePlan(ctx, request.Plan(), request.Stamp())
}
func (workspace *Workspace) StageHierarchyMaintenanceBound(ctx context.Context, request maintenanceauthority.HierarchyStageRequest) (knowl.StagedChange, error) {
	if request.Stamp() == nil {
		return knowl.StagedChange{}, ErrPlanConflict
	}
	return workspace.stageHierarchyPlan(ctx, request.ID(), request.Plan(), request.Stamp())
}
func (workspace *Workspace) CommitMaintenanceBound(ctx context.Context, request maintenanceauthority.CommitRequest) (knowl.ContentCommit, error) {
	if request.Digest() == "" || request.Digest() != request.Stage().Digest {
		return knowl.ContentCommit{}, ErrPlanConflict
	}
	if request.Hierarchy() {
		return workspace.commitHierarchy(ctx, request.Stage())
	}
	return workspace.commitMaintainer(ctx, request.Stage(), false)
}

// InspectMaintenanceStage authenticates one stage and its receipt for the
// private Host capability. It reads no live lifecycle/configuration state.
func (workspace *Workspace) InspectMaintenanceStage(ctx context.Context, request maintenanceauthority.InspectRequest) (maintenanceauthority.Inspection, error) {
	unlock, err := workspace.lock(ctx)
	if err != nil {
		return maintenanceauthority.Inspection{}, err
	}
	defer unlock()
	staged := request.Stage()
	stageDir := filepath.Join(workspace.root, knowlDir, "staging", token(staged.OperationID))
	manifest, err := readStageManifest(filepath.Join(stageDir, "manifest.yaml"))
	if err != nil {
		return maintenanceauthority.Inspection{}, err
	}
	writer := stageWriterMaintainer
	if request.Hierarchy() {
		writer = stageWriterHierarchy
	}
	if manifestWriter(manifest) != writer || manifest.OperationID != staged.OperationID || !validStageManifest(manifest) {
		return maintenanceauthority.Inspection{}, ErrPlanConflict
	}
	authenticated, err := stagedChangeFromManifest(stageDir, manifest)
	if err != nil {
		return maintenanceauthority.Inspection{}, err
	}
	if staged.Digest != "" && staged.Digest != authenticated.Digest {
		return maintenanceauthority.Inspection{}, ErrPlanConflict
	}
	if staged.Files != nil && !slices.Equal(staged.Files, authenticated.Files) {
		return maintenanceauthority.Inspection{}, ErrPlanConflict
	}
	if staged.Diagnostics != nil && !slices.Equal(staged.Diagnostics, authenticated.Diagnostics) {
		return maintenanceauthority.Inspection{}, ErrPlanConflict
	}
	if !sameRetirementContext(staged.Retirement, manifest.Retirement) {
		return maintenanceauthority.Inspection{}, ErrPlanConflict
	}
	result := maintenanceauthority.Inspection{Scope: manifestScope(manifest), Stage: authenticated, Stamp: manifest.MaintenanceAuthority}
	edits, err := readStagedPlanEdits(stageDir, manifest.Entries)
	if err != nil {
		return result, err
	}
	for _, edit := range edits {
		result.Edits = append(result.Edits, knowl.FileEdit{Action: edit.Action, Path: edit.Target, Content: []byte(edit.Content)})
	}
	result.Declared = slices.Clone(manifest.SourceRefs)
	if manifest.RequiredSourceRef != "" && !slices.Contains(result.Declared, manifest.RequiredSourceRef) {
		result.Declared = append(result.Declared, manifest.RequiredSourceRef)
	}
	var committed bool
	switch {
	case request.Hierarchy():
		committed, err = workspace.hierarchyCommitReplayed(manifest, authenticated.Digest)
	case manifest.Retirement != nil:
		committed, err = workspace.retirementCommitReplayed(manifest, authenticated.Digest)
		result.Declared = nil
	default:
		committed, err = workspace.ordinaryCommitReplayed(manifest, authenticated.Digest)
		if err == nil && !committed && manifest.MaintenanceAuthority == nil {
			committed, err = workspace.legacyOrdinaryCommitReplayedLocked(manifest, authenticated.Digest)
		}
	}
	if err != nil {
		return result, err
	}
	if committed {
		commit := contentCommit(manifest.OperationID, authenticated.Digest, commitTargets(manifest.Entries), manifest.Diagnostics)
		result.Committed = &commit
	}
	return result, nil
}

// Legacy ordinary writers had no durable receipt. Authenticate their exact
// already-published page/log state and reconstruct the log preimage, without
// republishing or changing any artifact. A prepared journal is never proof of
// a commit even when all of its prospective bytes have already been applied.
func (workspace *Workspace) legacyOrdinaryCommitReplayedLocked(manifest stageManifest, generation string) (bool, error) {
	if err := workspace.checkPublishedLocked(); err != nil {
		return false, err
	}
	logPath := filepath.Join(workspace.root, canonicalLogPath)
	if err := rejectSymlinkPath(workspace.root, logPath); err != nil {
		return false, err
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		return false, err
	}
	if len(log) > defaultMaxBytes || digestBytes(log) != manifest.LogDigest || !canonicalEntriesMatch(workspace.root, manifest.Entries) {
		return false, nil
	}
	entry, err := json.Marshal(logEntry{OperationID: manifest.OperationID, Generation: generation, SchemaDigest: manifest.SchemaDigest, SourceRefs: slices.Clone(manifest.SourceRefs), Files: entryTargets(manifest.Entries)})
	if err != nil {
		return false, err
	}
	if manifest.LogDate == "" {
		suffix := []byte("- " + string(entry) + "\n")
		if !bytes.HasSuffix(log, suffix) {
			return false, nil
		}
		before := log[:len(log)-len(suffix)]
		if digestBytes(before) == manifest.LogExpectedDigest {
			return true, nil
		}
		return len(before) > 0 && before[len(before)-1] == '\n' && digestBytes(before[:len(before)-1]) == manifest.LogExpectedDigest, nil
	}
	limits := okfLimits(defaultMaxBytes)
	document, err := okf.ValidateLog("log.md", log, limits)
	if err != nil {
		return false, ErrPlanConflict
	}
	audit := "**Update**: Committed a Knowl operation. <!-- knowl:" + strings.ReplaceAll(string(entry), "-", `\u002d`) + " -->"
	for index, group := range document.Groups {
		if group.Date.Format("2006-01-02") != manifest.LogDate {
			continue
		}
		if len(group.Entries) == 0 || group.Entries[0] != audit {
			return false, nil
		}
		document.Groups[index].Entries = group.Entries[1:]
		if len(document.Groups[index].Entries) == 0 {
			document.Groups = append(document.Groups[:index], document.Groups[index+1:]...)
		}
		before, err := okf.RenderLog("log.md", document, limits)
		if err != nil {
			return false, ErrPlanConflict
		}
		return digestBytes(before) == manifest.LogExpectedDigest, nil
	}
	return false, nil
}
func (workspace *Workspace) ordinaryCommitReplayed(manifest stageManifest, generation string) (bool, error) {
	// A committed stage can be superseded by later safe page work. Its durable
	// receipt authenticates historical publication without requiring that its
	// page/log bytes are still the current canonical snapshot.
	path := workspace.commitReceiptPath(stageWriterMaintainer, manifest.Scope, manifest.OperationID)
	if err := rejectSymlinkPath(workspace.root, path); err != nil {
		return false, err
	}
	receipt, err := readCommitReceipt(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, ErrPlanConflict
	}
	if receipt.Writer != stageWriterMaintainer || receipt.Scope != manifest.Scope || receipt.OperationID != manifest.OperationID || receipt.Generation != generation || !slices.Equal(receipt.Files, commitTargets(manifest.Entries)) {
		return false, ErrPlanConflict
	}
	return true, nil
}
