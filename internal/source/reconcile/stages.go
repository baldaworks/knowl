package reconcile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// Stable scan and saga failure classes extending the shared vocabulary.
const (
	classAdapter     = "adapter"
	classScan        = "scan_invalid"
	classFetch       = "fetch"
	classRaw         = "raw"
	classMaintenance = "maintenance_reservation"
	classNormalize   = "normalize"
	classProjection  = "projection"

	// pageDocumentCeiling mirrors the bounded descriptor page contract.
	pageDocumentCeiling = 1000

	filesystemAdapterName = "wiki-filesystem"
)

// stageError pairs a fixed redacted failure class with a retained cause for
// errors.Is inspection while keeping rendered messages free of content.
type stageError struct {
	class string
	cause error
}

func (e *stageError) Error() string {
	message := fmt.Sprintf("source sync failed: %s", e.class)
	var detailed interface{ SafeDetail() string }
	if errors.As(e.cause, &detailed) {
		if detail := strings.TrimSpace(detailed.SafeDetail()); detail != "" {
			return message + ": " + detail
		}
	}
	return message
}

func (e *stageError) Unwrap() error { return e.cause }

func failStage(class string, cause error) *stageError {
	if cause == nil {
		cause = errors.New(class)
	}
	return &stageError{class: class, cause: cause}
}

// composeStages wires the engine seam; the saga tail lands with the canonical
// saga task and stays an explicit pending stub until then.
func composeStages(service *Service) {
	service.stageEngine = service.runStages
}

func (service *Service) runStages(ctx context.Context, scope knowl.ScopeRef, adapter app.SourceAdapter, source knowl.Source) (Result, error) {
	configDigest, err := effectiveConfigDigest(source)
	if err != nil {
		return Result{}, err
	}
	resumed, err := service.beginOrResumeScan(ctx, scope, source, configDigest)
	if err != nil {
		return Result{}, err
	}
	if resumed.prepared != nil {
		input, reconstructErr := service.reconstructPrepared(ctx, scope, source, resumed.run)
		if reconstructErr != nil {
			return Result{Run: resumed.run}, reconstructErr
		}
		return service.finalizeSaga(ctx, scope, source.ID, input)
	}
	catalog, err := service.listCatalog(ctx, scope, adapter, source, resumed.run, resumed.refs)
	if err != nil {
		failure := service.failScanSafe(ctx, resumed.run, err)
		return Result{Run: service.refreshRun(ctx, scope, resumed.run)}, failure
	}
	input, err := service.classifyAndPrepare(ctx, scope, adapter, source, resumed.run, catalog.refs)
	if err != nil {
		failure := service.failScanSafe(ctx, resumed.run, err)
		return Result{Run: service.refreshRun(ctx, scope, resumed.run)}, failure
	}
	return service.finalizeSaga(ctx, scope, source.ID, input)
}

// effectiveConfigDigest resolves the validated configuration identity.
func effectiveConfigDigest(source knowl.Source) (string, error) {
	if source.ConfigDigest != "" {
		return source.ConfigDigest, nil
	}
	computed, err := app.SourceConfigDigest(source)
	if err != nil {
		return "", failStage(classScan, err)
	}
	return computed, nil
}

// scanResume describes the durable starting point of one scan attempt.
type scanResume struct {
	run      knowl.SyncRun
	refs     []knowl.DocumentRef
	prepared *app.PreparedSyncRead
}

// beginOrResumeScan fails incompatible abandoned scans safely, resumes a
// compatible scanning run with its persisted ordinal descriptors, hands
// prepared-or-later runs to the saga tail, or begins a fresh durable run.
func (service *Service) beginOrResumeScan(ctx context.Context, scope knowl.ScopeRef, source knowl.Source, configDigest string) (scanResume, error) {
	resumable, err := service.state.ResumableSyncRuns(ctx, scope, service.options.MaxRecoveryRuns)
	if err != nil {
		return scanResume{}, fmt.Errorf("resume inspection: %w", ErrRecoveryFailed)
	}
	for index := range resumable {
		run := resumable[index]
		if run.Scope != scope || run.SourceID != source.ID || run.ConfigDigest != configDigest {
			continue
		}
		switch run.Status {
		case knowl.SyncStatusScanning:
			refs, err := service.state.ScanDocuments(ctx, scope, run.ID, service.options.MaxScanDocuments)
			if err != nil {
				return scanResume{}, failStage(classState, err)
			}
			if refs == nil {
				refs = make([]knowl.DocumentRef, 0)
			}
			return scanResume{run: run, refs: refs}, nil
		case knowl.SyncStatusPrepared, knowl.SyncStatusContentCommitted, knowl.SyncStatusProjected:
			prepared, err := service.state.PreparedSync(ctx, scope, run.ID)
			if err != nil {
				return scanResume{}, failStage(classState, err)
			}
			return scanResume{run: run, prepared: &prepared}, nil
		default:
			continue
		}
	}
	for index := range resumable {
		run := resumable[index]
		if run.Scope == scope && run.SourceID == source.ID && run.ConfigDigest != configDigest && run.Status == knowl.SyncStatusScanning {
			service.failRunDetached(run.ID, scope, "config_changed")
		}
	}
	now := service.options.Clock()
	created, replay, err := service.state.BeginSync(ctx, app.BeginSyncRequest{
		Run: knowl.SyncRun{
			ID: service.options.NewRunID(), Scope: scope, SourceID: source.ID, ConfigDigest: configDigest,
			Status: knowl.SyncStatusScanning, StartedAt: now, UpdatedAt: now,
		},
		Type: knowl.SourceTypeFilesystem,
	})
	if err != nil {
		return scanResume{}, failStage(classState, err)
	}
	if replay {
		refs, err := service.state.ScanDocuments(ctx, scope, created.ID, service.options.MaxScanDocuments)
		if err != nil {
			return scanResume{}, failStage(classState, err)
		}
		if refs == nil {
			refs = make([]knowl.DocumentRef, 0)
		}
		return scanResume{run: created, refs: refs}, nil
	}
	return scanResume{run: created, refs: make([]knowl.DocumentRef, 0)}, nil
}

// listCatalog performs the bounded paged listing with atomic ordinal progress;
// absence is only authorized after the terminal page is durably recorded.
func (service *Service) listCatalog(ctx context.Context, scope knowl.ScopeRef, adapter app.SourceAdapter, source knowl.Source, run knowl.SyncRun, refs []knowl.DocumentRef) (catalogState, error) {
	state := catalogState{refs: refs}
	token := run.NextPageToken
	seen := make(map[knowl.DocumentID]struct{}, len(state.refs))
	for _, ref := range state.refs {
		seen[ref.ExternalID] = struct{}{}
	}
	for {
		if err := ctx.Err(); err != nil {
			return catalogState{}, failStage(classCanceled, err)
		}
		if len(state.pages) >= service.options.MaxScanPages {
			return catalogState{}, failStage(classScan, errors.New("page bound exceeded"))
		}
		page, err := adapter.List(ctx, source, token)
		if err != nil {
			return catalogState{}, failStage(classAdapter, err)
		}
		if err := app.ValidateDocumentPage(page, pageDocumentCeiling); err != nil {
			return catalogState{}, failStage(classScan, err)
		}
		for _, ref := range page.Documents {
			if ref.ExternalID != knowl.DocumentID(ref.Path) {
				return catalogState{}, failStage(classScan, errors.New("descriptor identity mismatch"))
			}
			if _, duplicate := seen[ref.ExternalID]; duplicate {
				return catalogState{}, failStage(classScan, errors.New("duplicate descriptor"))
			}
			if len(seen) >= service.options.MaxScanDocuments {
				return catalogState{}, failStage(classScan, errors.New("document bound exceeded"))
			}
			seen[ref.ExternalID] = struct{}{}
		}
		recorded, err := service.state.RecordScanPage(ctx, app.ScanPageRecord{
			RunID: run.ID, Scope: scope, SourceID: source.ID,
			ExpectedPageToken: token, NextPageToken: page.NextPageToken,
			Documents: page.Documents, RecordedAt: service.options.Clock(),
		})
		if err != nil {
			return catalogState{}, failStage(classScan, err)
		}
		state.pages = append(state.pages, pageRecord{next: recorded.NextPageToken, documents: page.Documents})
		state.refs = append(state.refs, page.Documents...)
		token = recorded.NextPageToken
		if token == "" {
			state.complete = true
			return state, nil
		}
	}
}

type pageRecord struct {
	next      string
	documents []knowl.DocumentRef
}

type catalogState struct {
	refs     []knowl.DocumentRef
	pages    []pageRecord
	complete bool
}

// failRunDetached records a stable terminal failure on a nonterminal run using
// a cancellation-independent bounded context; failures to record are ignored
// because the next attempt replays the same decision.
func (service *Service) failRunDetached(runID knowl.SyncRunID, scope knowl.ScopeRef, class string) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 5*time.Second)
	defer cancel()
	_, _ = service.state.FailSync(detached, scope, runID, class, service.options.Clock())
}

// failScanSafe maps a failed scan attempt onto its stable terminal outcome.
func (service *Service) failScanSafe(ctx context.Context, run knowl.SyncRun, err error) error {
	class := classFromError(err)
	if run.Status == knowl.SyncStatusScanning {
		detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, failErr := service.state.FailSync(detached, run.Scope, run.ID, class, service.options.Clock()); failErr != nil && !errors.Is(err, context.Canceled) {
			return errors.Join(err, fmt.Errorf("record scan failure: %w", ErrRecoveryFailed))
		}
	}
	return err
}

func classFromError(err error) string {
	var staged *stageError
	switch {
	case errors.As(err, &staged):
		return staged.class
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return classCanceled
	case errors.Is(err, app.ErrSourceInvalid):
		return classInvalid
	case errors.Is(err, app.ErrSyncConflict), errors.Is(err, app.ErrSyncStateTransition):
		return classState
	default:
		return classInternal
	}
}
