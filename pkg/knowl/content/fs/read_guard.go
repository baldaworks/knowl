package fs

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

// checkPublishedLocked observes recovery state without restoring or deleting
// anything. The caller holds workspace.lock for this check and the entire read.
func (workspace *Workspace) checkPublishedLocked() error {
	if err := workspace.inspectPublicationLocked(); err != nil {
		return errors.Join(app.ErrOperatorWorkspaceUnavailable, err)
	}
	return nil
}

func (workspace *Workspace) inspectPublicationLocked() error {
	root, err := openReadRoot(workspace.root, knowlDir+"/recovery")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.close()
	count, total := 0, 0
	for {
		entries, err := root.file.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for _, entry := range entries {
			count++
			if count > maxHierarchySnapshotEntries {
				return app.ErrOperatorReadLimitExceeded
			}
			if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
				return ErrPathRejected
			}
			if entry.IsDir() {
				continue
			} // Prepared backups alone do not publish files.
			if !strings.HasSuffix(entry.Name(), ".yaml") {
				return ErrWorkspaceInvalid
			}
			content, _, readErr := root.read(entry.Name(), knowl.ReadLimits{}, maxRecoveryJournalBytes-total)
			if readErr != nil {
				return readErr
			}
			total += len(content)
			var journal recoveryJournal
			if err := yaml.Unmarshal(content, &journal); err != nil {
				return ErrWorkspaceInvalid
			}
			if _, err := validateRecoveryJournal(entry.Name(), journal); err != nil {
				return err
			}
			if journal.State != recoveryCommitted {
				return app.ErrOperatorWorkspaceUnavailable
			}
			if err := workspace.checkCommittedReadLocked(journal); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func (workspace *Workspace) checkCommittedReadLocked(journal recoveryJournal) error {
	root, err := openReadRoot(workspace.root, "")
	if err != nil {
		return err
	}
	defer root.close()
	total := 0
	for _, entry := range journal.Entries {
		content, _, err := root.read(entry.Target, knowl.ReadLimits{}, maxSourceStageFile)
		if entry.Action == knowl.SourceMutationDelete {
			if !errors.Is(err, os.ErrNotExist) {
				return ErrWorkspaceInvalid
			}
			continue
		}
		if err != nil {
			return err
		}
		total += len(content)
		if total > maxSourceStageBytes {
			return app.ErrOperatorReadLimitExceeded
		}
		if !validSHA256(entry.Digest) || digestBytes(content) != entry.Digest {
			return ErrWorkspaceInvalid
		}
	}
	return nil
}
