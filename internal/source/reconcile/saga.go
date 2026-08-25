package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// Stable saga-tail failure classes.
const (
	classCommit    = "commit"
	classStaging   = "staging"
	noOpDomain     = "knowl-reconcile-noop-v1"
	durableTimeout = 10 * time.Second
)

// finalizeSaga executes one prepared candidate set through staging, canonical
// commit, projection, and atomic finalization. Post-prepare failures stay
// nonterminal so a later invocation converges from durable state. Committed or
// projected runs resume at their persisted stage without rewriting content.
func (service *Service) finalizeSaga(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID, input sagaInput) (Result, error) {
	run := input.run
	var (
		generation     string
		committedFiles []string
		err            error
	)
	switch run.Status {
	case knowl.SyncStatusPrepared:
		generation, committedFiles, err = service.commitPrepared(ctx, scope, sourceID, input)
		if err != nil {
			return Result{SourceID: sourceID, Run: service.refreshRun(ctx, scope, run), Changed: input.changed}, err
		}
	case knowl.SyncStatusContentCommitted, knowl.SyncStatusProjected:
		generation = run.ContentGeneration
		if strings.TrimSpace(generation) == "" {
			return Result{SourceID: sourceID, Run: run, Changed: input.changed}, failStage(classState, errors.New("committed run lost its generation"))
		}
	default:
		return Result{SourceID: sourceID, Run: run, Changed: input.changed}, failStage(classState, app.ErrSyncStateTransition)
	}
	snapshot, err := service.content.Snapshot(ctx, scope)
	if err != nil {
		return Result{SourceID: sourceID, Run: service.refreshRun(ctx, scope, run), Changed: input.changed}, failStage(classProjection, err)
	}
	commit := knowl.ContentCommit{
		OperationID: string(run.ID), Generation: generation,
		Files: committedFiles, Snapshot: snapshot,
	}
	if err := service.search.Project(ctx, commit); err != nil {
		return Result{SourceID: sourceID, Run: service.refreshRun(ctx, scope, run), Changed: input.changed}, failStage(classProjection, err)
	}
	if err := service.markProjected(ctx, app.SyncGeneration{
		RunID: run.ID, Scope: scope, SourceID: sourceID,
		Generation: generation, UpdatedAt: service.options.Clock(),
	}); err != nil {
		return Result{SourceID: sourceID, Run: service.refreshRun(ctx, scope, run), Changed: input.changed}, err
	}
	stateCtx, cancel := service.durableContext(ctx)
	defer cancel()
	finalized, err := service.state.FinalizeSync(stateCtx, app.SyncFinalization{
		RunID: run.ID, Scope: scope, SourceID: sourceID,
		CandidateDigest: input.prepared.CandidateDigest, Generation: generation,
		Checkpoint: input.prepared.Checkpoint, Counts: input.prepared.Counts,
		FinalizedAt: service.options.Clock(),
	})
	if err != nil {
		return Result{SourceID: sourceID, Run: service.refreshRun(ctx, scope, run), Changed: input.changed}, failStage(classState, err)
	}
	return Result{SourceID: sourceID, Run: finalized, Changed: input.changed}, nil
}

// refreshRun re-reads the durable run row so error results expose the exact
// persisted stage reached before the failure.
func (service *Service) refreshRun(ctx context.Context, scope knowl.ScopeRef, run knowl.SyncRun) knowl.SyncRun {
	stateCtx, cancel := service.durableContext(ctx)
	defer cancel()
	refreshed, err := service.state.SyncRun(stateCtx, scope, run.ID)
	if err != nil {
		return run
	}
	return refreshed
}

// commitPrepared stages and commits the exact prepared mutations, or derives
// the deterministic no-op generation that advances durable state without any
// canonical rewrite.
func (service *Service) commitPrepared(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID, input sagaInput) (string, []string, error) {
	if len(input.mutations) == 0 {
		inventory, err := service.canonicalInventory(ctx, scope, sourceID)
		if err != nil {
			return "", nil, err
		}
		generation := noOpGeneration(input.run.ID, input.prepared.CandidateDigest, inventory)
		if err := service.markContentCommitted(ctx, app.SyncGeneration{
			RunID: input.run.ID, Scope: scope, SourceID: sourceID,
			Generation: generation, UpdatedAt: service.options.Clock(),
		}); err != nil {
			return "", nil, err
		}
		return generation, nil, nil
	}
	staged, err := service.sourceContent.StageSourcePlan(ctx, knowl.SourceMutationPlan{
		RunID: input.run.ID, Scope: scope, SourceID: sourceID, Mutations: input.mutations,
	})
	if err != nil {
		return "", nil, failStage(classStaging, err)
	}
	commit, err := service.sourceContent.CommitSource(ctx, staged)
	if err != nil {
		return "", nil, failStage(classCommit, err)
	}
	if err := service.markContentCommitted(ctx, app.SyncGeneration{
		RunID: input.run.ID, Scope: scope, SourceID: sourceID,
		Generation: staged.Generation, UpdatedAt: service.options.Clock(),
	}); err != nil {
		return "", nil, err
	}
	return staged.Generation, commit.Files, nil
}

func (service *Service) markContentCommitted(ctx context.Context, transition app.SyncGeneration) error {
	stateCtx, cancel := service.durableContext(ctx)
	defer cancel()
	if _, err := service.state.MarkContentCommitted(stateCtx, transition); err != nil {
		return failStage(classState, err)
	}
	return nil
}

func (service *Service) markProjected(ctx context.Context, transition app.SyncGeneration) error {
	stateCtx, cancel := service.durableContext(ctx)
	defer cancel()
	if _, err := service.state.MarkProjected(stateCtx, transition); err != nil {
		return failStage(classState, err)
	}
	return nil
}

// durableContext decouples durable state recording from caller cancellation.
func (service *Service) durableContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), durableTimeout)
}

// noOpGeneration derives a domain-separated deterministic identity for runs
// whose prepared payload produces zero canonical mutations.
func noOpGeneration(runID knowl.SyncRunID, candidateDigest string, inventory map[string]string) string {
	entries := make([]inventoryEntry, 0, len(inventory))
	for path, digest := range inventory {
		entries = append(entries, inventoryEntry{Path: path, Digest: digest})
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Path < entries[right].Path })
	encoded, _ := json.Marshal(struct {
		Domain          string           `json:"domain"`
		RunID           knowl.SyncRunID  `json:"run_id"`
		CandidateDigest string           `json:"candidate_digest"`
		Inventory       []inventoryEntry `json:"inventory"`
	}{noOpDomain, runID, candidateDigest, entries})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

type inventoryEntry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
