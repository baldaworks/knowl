package fs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

type canonicalCommitEntry struct {
	action         knowl.SourceMutationAction
	target         string
	expectedDigest string
	digest         string
	content        []byte
}

type canonicalCommitRequest struct {
	writer      string
	sourceID    string
	scope       string
	operationID string
	recoveryKey string
	generation  string
	files       []string
	entries     []canonicalCommitEntry
}

// Commit applies a staged maintainer plan through the common canonical writer.
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
	if manifestWriter(manifest) != stageWriterMaintainer || manifest.OperationID != staged.OperationID || !validMaintainerStageManifest(manifest) {
		return knowl.ContentCommit{}, fmt.Errorf("staged operation mismatch: %w", ErrPlanConflict)
	}
	generation := stageGeneration(manifest)
	if (staged.Digest != "" && staged.Digest != generation) || (staged.Files != nil && !slices.Equal(staged.Files, entryTargets(manifest.Entries))) {
		return knowl.ContentCommit{}, ErrPlanConflict
	}
	if err := workspace.validateStageForCommitLocked(stageDir, manifest, ""); err != nil {
		return knowl.ContentCommit{}, err
	}
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
	if err := workspace.validateProspectivePlanLocked(manifestScope(manifest), stagedEdits, manifest.RequiredSourceRef, manifest.SourceRefs); err != nil {
		return knowl.ContentCommit{}, err
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
			return contentCommit(staged.OperationID, generation, commitTargets(manifest.Entries)), nil
		}
		return knowl.ContentCommit{}, fmt.Errorf("canonical log changed after staging: %w", ErrPrecondition)
	}
	if digestBytes(logAfter) != manifest.LogDigest {
		return knowl.ContentCommit{}, fmt.Errorf("staged log digest changed: %w", ErrPlanConflict)
	}
	entries, err := stagedCommitEntries(stageDir, manifest.Entries)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	entries = append(entries, canonicalCommitEntry{action: knowl.SourceMutationWrite, target: canonicalLogPath, expectedDigest: manifest.LogExpectedDigest, digest: manifest.LogDigest, content: logAfter})
	return workspace.commitLocked(canonicalCommitRequest{
		writer: stageWriterMaintainer, operationID: staged.OperationID, recoveryKey: staged.OperationID,
		generation: generation, files: commitTargets(manifest.Entries), entries: entries,
	})
}

// CommitHierarchy applies one staged catalog graph through the common
// canonical writer and recovery journal.
func (workspace *Workspace) CommitHierarchy(ctx context.Context, staged knowl.StagedChange) (knowl.ContentCommit, error) {
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
		return knowl.ContentCommit{}, fmt.Errorf("read staged hierarchy plan: %w", err)
	}
	generation := stageGeneration(manifest)
	if manifestWriter(manifest) != stageWriterHierarchy || manifest.OperationID != staged.OperationID || !validHierarchyStageManifest(manifest) ||
		(staged.Digest != "" && staged.Digest != generation) || (staged.Files != nil && !slices.Equal(staged.Files, entryTargets(manifest.Entries))) {
		return knowl.ContentCommit{}, ErrPlanConflict
	}
	if err := workspace.validateStageForCommitLocked(stageDir, manifest, ""); err != nil {
		return knowl.ContentCommit{}, err
	}
	if replayed, replayErr := workspace.hierarchyCommitReplayed(manifest, generation); replayErr != nil {
		return knowl.ContentCommit{}, replayErr
	} else if replayed {
		return contentCommit(staged.OperationID, generation, commitTargets(manifest.Entries)), nil
	}
	schema, err := os.ReadFile(filepath.Join(workspace.root, schemaFile))
	if err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("read schema before hierarchy commit: %w", err)
	}
	if digestBytes(schema) != manifest.SchemaDigest {
		return knowl.ContentCommit{}, fmt.Errorf("schema changed after hierarchy staging: %w", ErrPrecondition)
	}
	logPath := filepath.Join(workspace.root, canonicalLogPath)
	if err := rejectSymlinkPath(workspace.root, logPath); err != nil {
		return knowl.ContentCommit{}, err
	}
	logBefore, err := os.ReadFile(logPath)
	if err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("read canonical log before hierarchy commit: %w", err)
	}
	if digestBytes(logBefore) != manifest.LogExpectedDigest {
		if digestBytes(logBefore) == manifest.LogDigest && canonicalEntriesMatch(workspace.root, manifest.Entries) {
			receipt := hierarchyCommitReceipt(manifest, generation)
			if err := workspace.writeCommitReceipt(receipt); err != nil {
				return knowl.ContentCommit{}, err
			}
			return contentCommit(staged.OperationID, generation, commitTargets(manifest.Entries)), nil
		}
		return knowl.ContentCommit{}, fmt.Errorf("canonical log changed after hierarchy staging: %w", ErrPrecondition)
	}
	currentSnapshot, err := workspace.hierarchySnapshotDigestLocked(knowl.ScopeRef(manifest.Scope))
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	if currentSnapshot != manifest.SnapshotDigest {
		return knowl.ContentCommit{}, fmt.Errorf("canonical snapshot changed after hierarchy staging: %w", ErrPrecondition)
	}
	mutations, err := hierarchyMutationsFromStage(stageDir, manifest)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := workspace.validateProspectiveHierarchyLocked(mutations); err != nil {
		return knowl.ContentCommit{}, err
	}
	logAfter, err := appendLogEntry(logBefore, manifest, generation)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	if digestBytes(logAfter) != manifest.LogDigest {
		return knowl.ContentCommit{}, fmt.Errorf("staged hierarchy log digest changed: %w", ErrPlanConflict)
	}
	entries, err := stagedCommitEntries(stageDir, manifest.Entries)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	entries = append(entries, canonicalCommitEntry{action: knowl.SourceMutationWrite, target: canonicalLogPath, expectedDigest: manifest.LogExpectedDigest, digest: manifest.LogDigest, content: logAfter})
	return workspace.commitLocked(canonicalCommitRequest{
		writer: stageWriterHierarchy, scope: manifest.Scope, operationID: manifest.OperationID,
		recoveryKey: hierarchyRecoveryKey(manifest.Scope, manifest.OperationID), generation: generation,
		files: commitTargets(manifest.Entries), entries: entries,
	})
}

// CommitSource applies one exact staged source mutation through the common canonical writer.
func (workspace *Workspace) CommitSource(ctx context.Context, staged knowl.StagedSourceMutation) (knowl.ContentCommit, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.ContentCommit{}, err
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	stageDir := workspace.sourceStageDir(staged.Scope, staged.SourceID, staged.RunID)
	manifest, err := readStageManifest(filepath.Join(stageDir, "manifest.yaml"))
	if err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("read staged source plan: %w", err)
	}
	generation := stageGeneration(manifest)
	if manifestWriter(manifest) != stageWriterSource || !validSourceStageManifest(manifest) ||
		manifest.OperationID != string(staged.RunID) || manifest.Scope != string(staged.Scope) || manifest.SourceID != string(staged.SourceID) ||
		staged.Generation != generation || !slices.Equal(staged.Files, entryTargets(manifest.Entries)) {
		return knowl.ContentCommit{}, ErrPlanConflict
	}
	if err := workspace.validateStageForCommitLocked(stageDir, manifest, manifest.SourceID); err != nil {
		return knowl.ContentCommit{}, err
	}
	replayed, err := workspace.sourceCommitReplayed(manifest, generation)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	if replayed {
		return contentCommit(manifest.OperationID, generation, entryTargets(manifest.Entries)), nil
	}
	plan, err := sourcePlanFromStage(stageDir, manifest)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := workspace.validateProspectiveSourcePlanLocked(plan); err != nil {
		return knowl.ContentCommit{}, err
	}
	entries, err := stagedCommitEntries(stageDir, manifest.Entries)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	return workspace.commitLocked(canonicalCommitRequest{
		writer: stageWriterSource, sourceID: manifest.SourceID, operationID: manifest.OperationID,
		scope:       manifest.Scope,
		recoveryKey: sourceRecoveryKey(manifest.Scope, manifest.OperationID), generation: generation,
		files: entryTargets(manifest.Entries), entries: entries,
	})
}

func (workspace *Workspace) validateStageForCommitLocked(stageDir string, manifest stageManifest, sourceID string) error {
	for _, entry := range manifest.Entries {
		var err error
		if sourceID == "" {
			err = validateCommitTarget(entry.Target)
		} else {
			err = validateSourceTarget(sourceID, entry.Target)
		}
		if err != nil {
			return err
		}
		if err := rejectSymlinkPath(workspace.root, filepath.Join(workspace.root, filepath.FromSlash(entry.Target))); err != nil {
			return err
		}
	}
	if err := validateStagedPaths(workspace.root, stageDir, manifest.Entries); err != nil {
		return err
	}
	if !stagedFilesMatch(stageDir, manifest.Entries) {
		return fmt.Errorf("staged file content changed: %w", ErrPlanConflict)
	}
	return nil
}

func sourcePlanFromStage(stageDir string, manifest stageManifest) (knowl.SourceMutationPlan, error) {
	mutations := make([]knowl.SourceMutation, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		mutation := knowl.SourceMutation{Action: entryAction(entry), Path: entry.Target, ExpectedDigest: entry.ExpectedDigest}
		if mutation.Action == knowl.SourceMutationWrite {
			content, err := os.ReadFile(filepath.Join(stageDir, filepath.FromSlash(entry.Target)))
			if err != nil {
				return knowl.SourceMutationPlan{}, fmt.Errorf("read staged source %q: %w", entry.Target, err)
			}
			mutation.Content = content
		}
		mutations = append(mutations, mutation)
	}
	return knowl.SourceMutationPlan{RunID: knowl.SyncRunID(manifest.OperationID), Scope: knowl.ScopeRef(manifest.Scope), SourceID: knowl.SourceID(manifest.SourceID), Mutations: mutations}, nil
}

func stagedCommitEntries(stageDir string, entries []stageEntry) ([]canonicalCommitEntry, error) {
	result := make([]canonicalCommitEntry, 0, len(entries))
	for _, entry := range entries {
		commitEntry := canonicalCommitEntry{action: entryAction(entry), target: entry.Target, expectedDigest: entry.ExpectedDigest, digest: entry.Digest}
		if commitEntry.action == knowl.SourceMutationWrite {
			content, err := os.ReadFile(filepath.Join(stageDir, filepath.FromSlash(entry.Target)))
			if err != nil {
				return nil, fmt.Errorf("read staged file %q: %w", entry.Target, err)
			}
			commitEntry.content = content
		}
		result = append(result, commitEntry)
	}
	return result, nil
}

func (workspace *Workspace) commitLocked(request canonicalCommitRequest) (knowl.ContentCommit, error) {
	type preimage struct {
		content []byte
		mode    os.FileMode
		hadOld  bool
	}
	preimages := make([]preimage, len(request.entries))
	for index, entry := range request.entries {
		target := filepath.Join(workspace.root, filepath.FromSlash(entry.target))
		if err := rejectSymlinkPath(workspace.root, target); err != nil {
			return knowl.ContentCommit{}, err
		}
		old, err := os.ReadFile(target)
		hadOld := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return knowl.ContentCommit{}, fmt.Errorf("read commit target %q: %w", entry.target, err)
		}
		if entry.expectedDigest != "" && (!hadOld || digestBytes(old) != entry.expectedDigest) {
			return knowl.ContentCommit{}, fmt.Errorf("target %q changed after staging: %w", entry.target, ErrPrecondition)
		}
		if entry.expectedDigest == "" && hadOld {
			return knowl.ContentCommit{}, fmt.Errorf("new target %q already exists: %w", entry.target, ErrPrecondition)
		}
		mode := os.FileMode(0o600)
		if hadOld {
			info, statErr := os.Stat(target)
			if statErr != nil || !info.Mode().IsRegular() {
				return knowl.ContentCommit{}, fmt.Errorf("inspect commit target %q: %w", entry.target, ErrPrecondition)
			}
			mode = info.Mode().Perm()
		}
		preimages[index] = preimage{content: old, mode: mode, hadOld: hadOld}
	}
	if err := workspace.injectCommitFault("before_journal", -1); err != nil {
		return knowl.ContentCommit{}, err
	}
	recoveryRoot := filepath.Join(workspace.root, knowlDir, "recovery")
	journalToken := token(request.recoveryKey)
	journalDir := filepath.Join(recoveryRoot, journalToken)
	journalPath := filepath.Join(recoveryRoot, journalToken+".yaml")
	if err := rejectSymlinkPath(workspace.root, journalDir); err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("create recovery directory: %w", err)
	}
	journal := recoveryJournal{
		OperationID: request.operationID, Writer: request.writer, SourceID: request.sourceID, Scope: request.scope,
		State: recoveryPrepared, Entries: make([]recoveryEntry, 0, len(request.entries)), Generation: request.generation,
		Files: append([]string(nil), request.files...),
	}
	for index, entry := range request.entries {
		preimage := preimages[index]
		backup := ""
		if preimage.hadOld {
			backup = filepath.Join(journalDir, token(entry.target)+".old")
			if err := writeAtomic(backup, preimage.content, preimage.mode); err != nil {
				return knowl.ContentCommit{}, fmt.Errorf("write preimage %q: %w", entry.target, err)
			}
		}
		journal.Entries = append(journal.Entries, recoveryEntry{Action: entry.action, Target: entry.target, Backup: backup, HadOld: preimage.hadOld, Mode: uint32(preimage.mode.Perm()), Digest: entry.digest})
	}
	if err := workspace.injectCommitFault("after_preimages", -1); err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := writeJournal(journalPath, journal); err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := workspace.injectCommitFault("prepared", -1); err != nil {
		return knowl.ContentCommit{}, err
	}
	for index, entry := range request.entries {
		target := filepath.Join(workspace.root, filepath.FromSlash(entry.target))
		switch entry.action {
		case knowl.SourceMutationWrite:
			if digestBytes(entry.content) != entry.digest {
				return knowl.ContentCommit{}, fmt.Errorf("commit content %q changed: %w", entry.target, ErrPlanConflict)
			}
			if err := writeAtomic(target, entry.content, 0o600); err != nil {
				return knowl.ContentCommit{}, fmt.Errorf("commit file %q: %w", entry.target, err)
			}
		case knowl.SourceMutationDelete:
			if err := os.Remove(target); err != nil {
				return knowl.ContentCommit{}, fmt.Errorf("delete file %q: %w", entry.target, err)
			}
		default:
			return knowl.ContentCommit{}, ErrPlanConflict
		}
		if err := workspace.injectCommitFault(commitFaultApplied, index); err != nil {
			return knowl.ContentCommit{}, err
		}
	}
	journal.State = recoveryCommitted
	if err := writeJournal(journalPath, journal); err != nil {
		return knowl.ContentCommit{}, err
	}
	if err := workspace.injectCommitFault("committed", -1); err != nil {
		return knowl.ContentCommit{}, err
	}
	if request.writer == stageWriterSource || request.writer == stageWriterHierarchy {
		if err := workspace.writeCommitReceipt(commitReceipt{
			Writer: request.writer, SourceID: request.sourceID, Scope: request.scope, OperationID: request.operationID,
			Generation: request.generation, Files: append([]string(nil), request.files...),
		}); err != nil {
			return knowl.ContentCommit{}, err
		}
		if err := workspace.injectCommitFault(commitFaultReceipt, -1); err != nil {
			return knowl.ContentCommit{}, err
		}
	}
	if err := os.Remove(journalPath); err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("remove recovery journal: %w", err)
	}
	if err := os.RemoveAll(journalDir); err != nil {
		return knowl.ContentCommit{}, fmt.Errorf("remove recovery preimages: %w", err)
	}
	return contentCommit(request.operationID, request.generation, request.files), nil
}

func (workspace *Workspace) injectCommitFault(point string, index int) error {
	if workspace.commitFault == nil {
		return nil
	}
	if err := workspace.commitFault(point, index); err != nil {
		return fmt.Errorf("commit fault at %s[%d]: %w", point, index, err)
	}
	return nil
}

func contentCommit(operationID, generation string, files []string) knowl.ContentCommit {
	return knowl.ContentCommit{OperationID: operationID, Generation: generation, Files: append([]string(nil), files...), CommittedAt: time.Now().UTC()}
}

func appendLogEntry(existing []byte, manifest stageManifest, generation string) ([]byte, error) {
	entry, err := json.Marshal(logEntry{OperationID: manifest.OperationID, Generation: generation, SchemaDigest: manifest.SchemaDigest, SourceRefs: append([]string(nil), manifest.SourceRefs...), Files: entryTargets(manifest.Entries)})
	if err != nil {
		return nil, fmt.Errorf("marshal commit log entry: %w", err)
	}
	if manifest.LogDate == "" {
		return appendLegacyLogEntry(existing, entry), nil
	}
	limits := okfLimits(defaultMaxBytes)
	logDocument, err := okf.ValidateLog("log.md", existing, limits)
	if err != nil {
		return nil, fmt.Errorf("parse canonical OKF log: %w", ErrWorkspaceInvalid)
	}
	date, err := time.Parse(time.DateOnly, manifest.LogDate)
	if err != nil || date.Format(time.DateOnly) != manifest.LogDate {
		return nil, fmt.Errorf("invalid staged log date: %w", ErrPlanConflict)
	}
	auditJSON := strings.ReplaceAll(string(entry), "-", `\u002d`)
	if len(auditJSON) > maxLogAuditBytes {
		return nil, fmt.Errorf("commit log audit exceeds bound: %w", ErrPlanConflict)
	}
	audit := "**Update**: Committed a Knowl operation. <!-- knowl:" + auditJSON + " -->"
	inserted := false
	for index := range logDocument.Groups {
		if logDocument.Groups[index].Date.Equal(date) {
			logDocument.Groups[index].Entries = append([]string{audit}, logDocument.Groups[index].Entries...)
			inserted = true
			break
		}
		if logDocument.Groups[index].Date.Before(date) {
			group := okf.LogGroup{Date: date, Entries: []string{audit}}
			logDocument.Groups = append(logDocument.Groups, okf.LogGroup{})
			copy(logDocument.Groups[index+1:], logDocument.Groups[index:])
			logDocument.Groups[index] = group
			inserted = true
			break
		}
	}
	if !inserted {
		logDocument.Groups = append(logDocument.Groups, okf.LogGroup{Date: date, Entries: []string{audit}})
	}
	rendered, err := okf.RenderLog("log.md", logDocument, limits)
	if err != nil {
		return nil, fmt.Errorf("render canonical OKF log: %w", ErrPlanConflict)
	}
	return rendered, nil
}

const maxLogAuditBytes = 128 << 10

func appendLegacyLogEntry(existing, entry []byte) []byte {
	content := append([]byte(nil), existing...)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	content = append(content, []byte("- ")...)
	content = append(content, entry...)
	content = append(content, '\n')
	return content
}

func canonicalEntriesMatch(root string, entries []stageEntry) bool {
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Target)))
		if entryAction(entry) == knowl.SourceMutationDelete {
			if !errors.Is(err, os.ErrNotExist) {
				return false
			}
			continue
		}
		if err != nil || digestBytes(content) != entry.Digest {
			return false
		}
	}
	return true
}

func commitTargets(entries []stageEntry) []string {
	targets := append([]stageEntry(nil), entries...)
	targets = append(targets, stageEntry{Target: canonicalLogPath})
	return entryTargets(targets)
}

func sourceRecoveryKey(scope, operationID string) string {
	return stageWriterSource + "\x00" + scope + "\x00" + operationID
}

func hierarchyRecoveryKey(scope, operationID string) string {
	return stageWriterHierarchy + "\x00" + scope + "\x00" + operationID
}

func (workspace *Workspace) sourceCommitReplayed(manifest stageManifest, generation string) (bool, error) {
	path := workspace.sourceCommitReceiptPath(manifest.Scope, manifest.OperationID)
	if err := rejectSymlinkPath(workspace.root, path); err != nil {
		return false, err
	}
	receipt, err := readCommitReceipt(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read source commit receipt: %w", ErrPlanConflict)
	}
	wantFiles := entryTargets(manifest.Entries)
	if receipt.Writer != stageWriterSource || receipt.SourceID != manifest.SourceID || receipt.Scope != manifest.Scope ||
		receipt.OperationID != manifest.OperationID || receipt.Generation != generation || !slices.Equal(receipt.Files, wantFiles) {
		return false, ErrPlanConflict
	}
	if !canonicalEntriesMatch(workspace.root, manifest.Entries) {
		return false, fmt.Errorf("committed source content diverged: %w", ErrPlanConflict)
	}
	return true, nil
}

func (workspace *Workspace) hierarchyCommitReplayed(manifest stageManifest, generation string) (bool, error) {
	path := workspace.hierarchyCommitReceiptPath(manifest.Scope, manifest.OperationID)
	if err := rejectSymlinkPath(workspace.root, path); err != nil {
		return false, err
	}
	receipt, err := readCommitReceipt(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read hierarchy commit receipt: %w", ErrPlanConflict)
	}
	want := hierarchyCommitReceipt(manifest, generation)
	if receipt.Writer != want.Writer || receipt.SourceID != want.SourceID || receipt.Scope != want.Scope ||
		receipt.OperationID != want.OperationID || receipt.Generation != want.Generation || !slices.Equal(receipt.Files, want.Files) ||
		!canonicalEntriesMatch(workspace.root, manifest.Entries) {
		return false, fmt.Errorf("committed hierarchy content diverged: %w", ErrPlanConflict)
	}
	log, err := os.ReadFile(filepath.Join(workspace.root, canonicalLogPath))
	if err != nil || digestBytes(log) != manifest.LogDigest {
		return false, fmt.Errorf("committed hierarchy log diverged: %w", ErrPlanConflict)
	}
	return true, nil
}

func hierarchyCommitReceipt(manifest stageManifest, generation string) commitReceipt {
	return commitReceipt{
		Writer: stageWriterHierarchy, Scope: manifest.Scope, OperationID: manifest.OperationID,
		Generation: generation, Files: commitTargets(manifest.Entries),
	}
}

func (workspace *Workspace) sourceCommitReceiptPath(scope, operationID string) string {
	return workspace.commitReceiptPath(stageWriterSource, scope, operationID)
}

func (workspace *Workspace) hierarchyCommitReceiptPath(scope, operationID string) string {
	return workspace.commitReceiptPath(stageWriterHierarchy, scope, operationID)
}

func (workspace *Workspace) commitReceiptPath(writer, scope, operationID string) string {
	key := sourceRecoveryKey(scope, operationID)
	if writer == stageWriterHierarchy {
		key = hierarchyRecoveryKey(scope, operationID)
	}
	return filepath.Join(workspace.root, knowlDir, "commits", token(key)+".yaml")
}

func (workspace *Workspace) writeCommitReceipt(receipt commitReceipt) error {
	content, err := yaml.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("marshal commit receipt: %w", err)
	}
	if len(content) > maxStageManifestBytes {
		return ErrPlanConflict
	}
	path := workspace.commitReceiptPath(receipt.Writer, receipt.Scope, receipt.OperationID)
	if err := rejectSymlinkPath(workspace.root, path); err != nil {
		return err
	}
	if err := writeAtomic(path, content, 0o600); err != nil {
		return fmt.Errorf("write commit receipt: %w", err)
	}
	return nil
}
