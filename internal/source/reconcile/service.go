// Package reconcile owns the bounded multi-source synchronization service.
//
// The service composes validated application ports behind exact scope/source
// keyed nonblocking leases. Its stage engine is delivered incrementally by the
// reconciliation stage tasks; until composed, public entrypoints perform full
// input validation, leasing, and aggregation without mutating durable state.
package reconcile

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// Stable, redacted service outcomes.
var (
	// ErrSyncInProgress reports an overlapping invocation for one exact scope/source.
	ErrSyncInProgress = errors.New("source sync already in progress")
	// ErrSyncPartial reports that SyncAll or Recover completed while at least one source failed.
	ErrSyncPartial = errors.New("source sync completed with source failures")
	// ErrRecoveryFailed reports readiness restoration or resumable-run inspection
	// failure without exposing storage internals.
	ErrRecoveryFailed = errors.New("workspace recovery failed")
)

// Canonical service ceilings mirroring the bounded port contracts.
const (
	maxSyncAllSources  = 1000
	maxScanPages       = 1000
	maxScanDocuments   = 1000
	maxPlanMutations   = 2048
	maxRawBytes        = 64 << 20
	maxRecoveryRuns    = 1000
	defaultRunIDLength = 16

	// Stable failure classes recorded on redacted results.
	classCanceled   = "canceled"
	classInvalid    = "invalid"
	classInProgress = "in_progress"
	classState      = "state"
	classInternal   = "internal"
)

// Clock supplies deterministic time to durable transitions.
type Clock func() time.Time

// RunIDGenerator supplies bounded opaque durable run identifiers.
type RunIDGenerator func() knowl.SyncRunID

// Options carries conservative validated service bounds and injected clocks.
type Options struct {
	MaxSyncAllSources int
	MaxScanPages      int
	MaxScanDocuments  int
	MaxMutations      int
	MaxRawBytes       int
	MaxRecoveryRuns   int
	Clock             Clock
	NewRunID          RunIDGenerator
}

func (options Options) normalize() (Options, error) {
	for _, bound := range []struct {
		name     string
		value    int
		ceiling  int
		fallback int
	}{
		{name: "max_sync_all_sources", value: options.MaxSyncAllSources, ceiling: maxSyncAllSources},
		{name: "max_scan_pages", value: options.MaxScanPages, ceiling: maxScanPages},
		{name: "max_scan_documents", value: options.MaxScanDocuments, ceiling: maxScanDocuments},
		{name: "max_mutations", value: options.MaxMutations, ceiling: maxPlanMutations},
		{name: "max_raw_bytes", value: options.MaxRawBytes, ceiling: maxRawBytes},
		{name: "max_recovery_runs", value: options.MaxRecoveryRuns, ceiling: maxRecoveryRuns},
	} {
		switch {
		case bound.value < 0:
			return Options{}, fmt.Errorf("%s is negative: %w", bound.name, app.ErrSourceInvalid)
		case bound.value > bound.ceiling:
			return Options{}, fmt.Errorf("%s exceeds the service ceiling: %w", bound.name, app.ErrSourceInvalid)
		case bound.value == 0:
			switch bound.name {
			case "max_sync_all_sources":
				options.MaxSyncAllSources = maxSyncAllSources
			case "max_scan_pages":
				options.MaxScanPages = maxScanPages
			case "max_scan_documents":
				options.MaxScanDocuments = maxScanDocuments
			case "max_mutations":
				options.MaxMutations = maxPlanMutations
			case "max_raw_bytes":
				options.MaxRawBytes = maxRawBytes
			case "max_recovery_runs":
				options.MaxRecoveryRuns = maxRecoveryRuns
			}
		}
	}
	if options.Clock == nil {
		options.Clock = func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
	}
	if options.NewRunID == nil {
		options.NewRunID = defaultRunID
	}
	return options, nil
}

// defaultRunID returns bounded cryptographic randomness as a canonical ID.
func defaultRunID() knowl.SyncRunID {
	buffer := make([]byte, defaultRunIDLength)
	if _, err := cryptorand.Read(buffer); err != nil {
		return knowl.SyncRunID("reconcile-" + fmt.Sprintf("%x", time.Now().UTC().UnixNano()))
	}
	return knowl.SyncRunID(hex.EncodeToString(buffer))
}

// Dependencies binds the validated application ports consumed by the service.
type Dependencies struct {
	Adapters      map[knowl.SourceType]app.SourceAdapter
	State         app.SourceStateStore
	Content       app.ContentStore
	SourceContent app.SourceContentStore
	Search        app.SearchIndex
	Maintenance   app.SourceMaintenanceQueue
}

// Result is the redacted observable outcome of one source synchronization attempt.
type Result struct {
	SourceID     knowl.SourceID
	Run          knowl.SyncRun
	Changed      bool
	FailureClass string
	Diagnostics  []knowl.SourceDiagnostic
}

// AllResult aggregates one deterministic result per attempted source.
type AllResult struct {
	Results []Result
}

// Service coordinates bounded idempotent source reconciliation.
type Service struct {
	adapters      map[knowl.SourceType]app.SourceAdapter
	state         app.SourceStateStore
	content       app.ContentStore
	sourceContent app.SourceContentStore
	search        app.SearchIndex
	maintenance   app.SourceMaintenanceQueue

	options Options

	stageEngine func(ctx context.Context, scope knowl.ScopeRef, adapter app.SourceAdapter, source knowl.Source) (Result, error)

	leaseMu sync.Mutex
	leases  map[leaseKey]struct{}
}

type leaseKey struct {
	scope  knowl.ScopeRef
	source knowl.SourceID
}

// NewService validates dependencies and options and returns a composed service.
func NewService(dependencies Dependencies, options Options) (*Service, error) {
	if len(dependencies.Adapters) == 0 {
		return nil, fmt.Errorf("adapters dependency: %w", app.ErrSourceInvalid)
	}
	for sourceType, adapter := range dependencies.Adapters {
		if adapter == nil {
			return nil, fmt.Errorf("adapter %q dependency: %w", sourceType, app.ErrSourceInvalid)
		}
	}
	if _, exists := dependencies.Adapters[knowl.SourceTypeFilesystem]; !exists {
		return nil, fmt.Errorf("filesystem adapter dependency: %w", app.ErrSourceInvalid)
	}
	if dependencies.State == nil {
		return nil, fmt.Errorf("state store dependency: %w", app.ErrSourceInvalid)
	}
	if dependencies.Content == nil {
		return nil, fmt.Errorf("content store dependency: %w", app.ErrSourceInvalid)
	}
	if dependencies.SourceContent == nil {
		return nil, fmt.Errorf("source content store dependency: %w", app.ErrSourceInvalid)
	}
	if dependencies.Search == nil {
		return nil, fmt.Errorf("search index dependency: %w", app.ErrSourceInvalid)
	}
	if dependencies.Maintenance == nil {
		return nil, fmt.Errorf("maintenance queue dependency: %w", app.ErrSourceInvalid)
	}
	normalized, err := options.normalize()
	if err != nil {
		return nil, err
	}
	service := &Service{
		adapters:      make(map[knowl.SourceType]app.SourceAdapter, len(dependencies.Adapters)),
		state:         dependencies.State,
		content:       dependencies.Content,
		sourceContent: dependencies.SourceContent,
		search:        dependencies.Search,
		maintenance:   dependencies.Maintenance,
		options:       normalized,
		leases:        make(map[leaseKey]struct{}),
	}
	for sourceType, adapter := range dependencies.Adapters {
		service.adapters[sourceType] = adapter
	}
	composeStages(service)
	return service, nil
}

// SyncSource synchronizes exactly one enabled configured source under its lease.
func (service *Service) SyncSource(ctx context.Context, scope knowl.ScopeRef, source knowl.Source) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{SourceID: source.ID, FailureClass: classCanceled}, err
	}
	adapter, err := service.validateTarget(scope, source)
	if err != nil {
		return Result{SourceID: source.ID, FailureClass: classInvalid}, err
	}
	key := leaseKey{scope: scope, source: source.ID}
	if !service.acquire(key) {
		return Result{SourceID: source.ID, FailureClass: classInProgress}, ErrSyncInProgress
	}
	defer service.release(key)
	result, err := service.stageEngine(ctx, scope, adapter, source)
	result.SourceID = source.ID
	if err != nil && result.FailureClass == "" {
		result.FailureClass = classifyError(err)
	}
	return result, err
}

// SyncAll synchronizes enabled deduplicated sources sequentially and
// deterministically, isolating one source failure from another source's state.
func (service *Service) SyncAll(ctx context.Context, scope knowl.ScopeRef, sources []knowl.Source) (AllResult, error) {
	if err := ctx.Err(); err != nil {
		return AllResult{}, err
	}
	if strings.TrimSpace(string(scope)) == "" {
		return AllResult{}, fmt.Errorf("sync all scope: %w", app.ErrSourceInvalid)
	}
	selected, err := selectSources(sources)
	if err != nil {
		return AllResult{}, err
	}
	if len(selected) > service.options.MaxSyncAllSources {
		return AllResult{}, fmt.Errorf("sync all attempts %d sources beyond the bound: %w", len(selected), app.ErrSourceInvalid)
	}
	results := make([]Result, 0, len(selected))
	partial := false
	for _, source := range selected {
		result, err := service.SyncSource(ctx, scope, source)
		if err != nil {
			partial = true
			if result.FailureClass == "" {
				result.FailureClass = classifyError(err)
			}
		} else {
			result.FailureClass = ""
		}
		results = append(results, result)
	}
	if partial {
		return AllResult{Results: results}, ErrSyncPartial
	}
	return AllResult{Results: results}, nil
}

// Recover restores workspace readiness, then resumes every bounded nonterminal
// run belonging to the configured sources.
func (service *Service) Recover(ctx context.Context, scope knowl.ScopeRef, sources []knowl.Source) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(scope)) == "" {
		return nil, fmt.Errorf("recover scope: %w", app.ErrSourceInvalid)
	}
	configured := make(map[knowl.SourceID]knowl.Source, len(sources))
	for _, source := range sources {
		if app.ValidateSource(source) != nil {
			return nil, fmt.Errorf("recover source %q: %w", source.ID, app.ErrSourceInvalid)
		}
		configured[source.ID] = source
	}
	if _, err := service.content.Recover(ctx); err != nil {
		return nil, fmt.Errorf("workspace readiness: %w", ErrRecoveryFailed)
	}
	runs, err := service.state.ResumableSyncRuns(ctx, scope, service.options.MaxRecoveryRuns)
	if err != nil {
		return nil, fmt.Errorf("list resumable runs: %w", ErrRecoveryFailed)
	}
	pending := make(map[knowl.SourceID][]knowl.SyncRun)
	for _, run := range runs {
		if _, owned := configured[run.SourceID]; owned {
			pending[run.SourceID] = append(pending[run.SourceID], run)
		}
	}
	orderedIDs := make([]knowl.SourceID, 0, len(pending))
	for sourceID := range pending {
		orderedIDs = append(orderedIDs, sourceID)
	}
	sort.Slice(orderedIDs, func(left, right int) bool { return orderedIDs[left] < orderedIDs[right] })
	results := make([]Result, 0, len(orderedIDs))
	failed := false
	for _, sourceID := range orderedIDs {
		result := service.recoverSource(ctx, scope, configured[sourceID], pending[sourceID])
		results = append(results, result)
		if result.FailureClass != "" {
			failed = true
		}
	}
	if failed {
		return results, ErrSyncPartial
	}
	return results, nil
}

// recoverSource resumes every nonterminal run of one source under its lease.
func (service *Service) recoverSource(ctx context.Context, scope knowl.ScopeRef, source knowl.Source, runs []knowl.SyncRun) Result {
	key := leaseKey{scope: scope, source: source.ID}
	if !service.acquire(key) {
		return Result{SourceID: source.ID, FailureClass: classInProgress}
	}
	defer service.release(key)
	last := Result{SourceID: source.ID}
	for _, run := range runs {
		result, err := service.recoverRun(ctx, scope, source, run)
		result.SourceID = source.ID
		last = result
		if err != nil {
			last.FailureClass = classFromError(err)
		}
	}
	return last
}

// recoverRun dispatches one nonterminal run to its persisted stage.
func (service *Service) recoverRun(ctx context.Context, scope knowl.ScopeRef, source knowl.Source, run knowl.SyncRun) (Result, error) {
	switch run.Status {
	case knowl.SyncStatusScanning:
		configDigest, digestErr := effectiveConfigDigest(source)
		if digestErr != nil || run.ConfigDigest != configDigest {
			service.failRunDetached(run.ID, scope, "config_changed")
			return Result{Run: run}, failStage(classScan, errors.New("configuration changed"))
		}
		adapter, adapterErr := service.adapterFor(source.Type)
		if adapterErr != nil {
			return Result{Run: run}, adapterErr
		}
		return service.runStages(ctx, scope, adapter, source)
	case knowl.SyncStatusPrepared:
		input, inputErr := service.reconstructPrepared(ctx, scope, source, run)
		if inputErr != nil {
			return Result{Run: run}, inputErr
		}
		return service.finalizeSaga(ctx, scope, source.ID, input)
	case knowl.SyncStatusContentCommitted, knowl.SyncStatusProjected:
		// Canonical content and receipts are already committed; only
		// projection replay and finalization remain.
		read, readErr := service.state.PreparedSync(ctx, scope, run.ID)
		if readErr != nil {
			return Result{Run: run}, failStage(classState, readErr)
		}
		return service.finalizeSaga(ctx, scope, source.ID, sagaInput{run: run, prepared: read})
	default:
		return Result{Run: run}, failStage(classState, app.ErrSyncStateTransition)
	}
}

func (service *Service) validateTarget(scope knowl.ScopeRef, source knowl.Source) (app.SourceAdapter, error) {
	if strings.TrimSpace(string(scope)) == "" {
		return nil, fmt.Errorf("sync scope: %w", app.ErrSourceInvalid)
	}
	if app.ValidateSource(source) != nil || !source.Enabled {
		return nil, fmt.Errorf("sync source %q: %w", source.ID, app.ErrSourceInvalid)
	}
	return service.adapterFor(source.Type)
}

func (service *Service) adapterFor(sourceType knowl.SourceType) (app.SourceAdapter, error) {
	adapter, exists := service.adapters[sourceType]
	if !exists {
		return nil, fmt.Errorf("source type %q: %w", sourceType, app.ErrSourceInvalid)
	}
	return adapter, nil
}

func (service *Service) acquire(key leaseKey) bool {
	service.leaseMu.Lock()
	defer service.leaseMu.Unlock()
	if _, busy := service.leases[key]; busy {
		return false
	}
	service.leases[key] = struct{}{}
	return true
}

func (service *Service) release(key leaseKey) {
	service.leaseMu.Lock()
	defer service.leaseMu.Unlock()
	delete(service.leases, key)
}

// selectSources filters enabled sources, rejects duplicate identities with
// conflicting configuration, and returns them in canonical source-ID order.
func selectSources(sources []knowl.Source) ([]knowl.Source, error) {
	enabled := make([]knowl.Source, 0, len(sources))
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		if app.ValidateSource(source) != nil {
			return nil, fmt.Errorf("sync source %q: %w", source.ID, app.ErrSourceInvalid)
		}
		enabled = append(enabled, source)
	}
	sort.SliceStable(enabled, func(left, right int) bool { return enabled[left].ID < enabled[right].ID })
	deduped := make([]knowl.Source, 0, len(enabled))
	for index, source := range enabled {
		if index > 0 && enabled[index-1].ID == source.ID {
			previousDigest, digestErr := effectiveConfigDigest(enabled[index-1])
			currentDigest, currentErr := effectiveConfigDigest(source)
			if digestErr != nil || currentErr != nil || previousDigest != currentDigest {
				return nil, fmt.Errorf("duplicate source %q with conflicting configuration: %w", source.ID, app.ErrSourceInvalid)
			}
			continue
		}
		deduped = append(deduped, source)
	}
	return deduped, nil
}

func classifyError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return classCanceled
	case errors.Is(err, ErrSyncInProgress):
		return classInProgress
	case errors.Is(err, app.ErrSourceInvalid):
		return classInvalid
	case errors.Is(err, app.ErrSyncConflict), errors.Is(err, app.ErrSyncStateTransition), errors.Is(err, app.ErrSyncRunNotFound):
		return classState
	default:
		var staged *stageError
		if errors.As(err, &staged) {
			return staged.class
		}
		return classInternal
	}
}
