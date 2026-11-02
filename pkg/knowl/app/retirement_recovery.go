package app

import (
	"context"
	"errors"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// recoverOrphan observes one bounded history/provenance window. Cursors are
// disposable: restarts repeat observation; only the store CAS grants retirement.
func (coordinator *SourceRetirementCoordinator) recoverOrphan(ctx context.Context) (resultErr error) {
	defer func() {
		if resultErr != nil {
			coordinator.finishRecoveryCandidate()
		}
	}()
	store, ok := coordinator.lifecycles.(SourceLifecycleRecoveryStore)
	content, readable := coordinator.provenance.(RetirementContentReader)
	if !ok || !readable {
		return nil
	}
	if coordinator.recoveryCandidate == nil {
		page, err := store.OrphanSourceOperations(ctx, OrphanSourceOperationReadRequest{Scope: coordinator.scope, After: coordinator.recoveryAfter, Limit: 1})
		if err != nil {
			return err
		}
		if len(page.Items) == 0 {
			coordinator.recoveryAfter = ""
			return nil
		}
		candidate := page.Items[0]
		coordinator.recoveryAfter = candidate.OperationID
		if candidate.Invalid {
			return ErrSourceLifecycleInvalid
		}
		coordinator.recoveryCandidate = &candidate
	}
	candidate := *coordinator.recoveryCandidate
	page, err := coordinator.provenance.PageProvenance(ctx, coordinator.scope, candidate.Owner, coordinator.recoveryCursor, coordinator.limits)
	if err != nil {
		coordinator.finishRecoveryCandidate()
		return err
	}
	if len(page.Items) == 0 {
		coordinator.recoveryCursor = page.NextCursor
		if page.EOF {
			coordinator.finishRecoveryCandidate()
		}
		return nil
	}
	// One authenticated citation suffices. Unmatched windows keep their cursor;
	// malformed proof advances history and is revisited on the next round.
	release, err := coordinator.applyCoordinator.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	limits := knowl.ReadLimits{Pages: 1, Bytes: coordinator.limits.PageBytes, Characters: coordinator.limits.PageBytes}
	for _, proof := range page.Items {
		pages, readErr := content.ReadPages(ctx, coordinator.scope, []knowl.PageID{proof.ID}, limits)
		if readErr != nil {
			return readErr
		}
		if len(pages) != 1 || !sameCurrentRetirementPage(pages[0], proof) || digestHierarchyBytes([]byte(pages[0].Content)) != proof.Digest {
			return ErrRetirementPageStale
		}
		sources, resolveErr := content.ResolveAcceptedSources(ctx, coordinator.scope, pages[0].SourceRefs)
		if resolveErr != nil {
			return resolveErr
		}
		matched := false
		for _, source := range sources {
			if source == candidate.Source {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		raw, rawErr := content.ReadSource(ctx, candidate.Source, limits)
		if rawErr != nil {
			return rawErr
		}
		if digestHierarchyBytes(raw) != candidate.Source.Version.Digest {
			return ErrSourceLifecycleInvalid
		}
		authority, authorityErr := coordinator.lifecycles.SourceLifecycleAuthority(ctx, coordinator.scope)
		if authorityErr != nil {
			return authorityErr
		}
		coordinator.finishRecoveryCandidate()
		_, err = store.RecoverSourceLifecycle(ctx, SourceLifecycleRecoveryRequest{Scope: coordinator.scope, Owner: candidate.Owner, AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch, OperationID: candidate.OperationID, Source: candidate.Source, PageID: proof.ID, PageDigest: proof.Digest, RecoveredAt: SourceTransitionTime(coordinator.now(), authority.UpdatedAt)})
		if errors.Is(err, ErrSourceLifecycleConflict) {
			return nil
		}
		return err
	}
	coordinator.recoveryCursor = page.NextCursor
	if page.EOF {
		coordinator.finishRecoveryCandidate()
	}
	return nil
}

func (coordinator *SourceRetirementCoordinator) finishRecoveryCandidate() {
	coordinator.recoveryCandidate = nil
	coordinator.recoveryCursor = ""
}
