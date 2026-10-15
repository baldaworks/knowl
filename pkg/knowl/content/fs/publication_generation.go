package fs

import (
	"crypto/rand"
	"encoding/hex"
	"math"
)

const workspacePublicationEpochBytes = 16

type workspacePublicationFence struct {
	epoch      string
	generation uint64
}

func newWorkspacePublicationEpoch() (string, error) {
	var value [workspacePublicationEpochBytes]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

// publicationFenceLocked is valid only while workspace.lock is held. The
// supported writer set is the single Workspace owned by one Host coordinator.
func (workspace *Workspace) publicationFenceLocked() workspacePublicationFence {
	return workspacePublicationFence{epoch: workspace.publicationEpoch, generation: workspace.publicationGeneration}
}

// advancePublicationLocked must run immediately before a supported canonical
// or accepted-provenance mutation while workspace.lock is held.
func (workspace *Workspace) advancePublicationLocked() error {
	if workspace.publicationGeneration == math.MaxUint64 {
		return ErrWorkspaceInvalid
	}
	workspace.publicationGeneration++
	return nil
}
