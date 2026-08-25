package knowl

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/baldaworks/knowl/internal/source/reconcile"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

// ErrHostClosed reports that a Host-owned runtime resource is no longer usable.
var ErrHostClosed = errors.New("knowl host is closed")

// SourceTrigger identifies why one source synchronization attempt began.
type SourceTrigger string

const (
	SourceTriggerManual   SourceTrigger = "manual"
	SourceTriggerOnStart  SourceTrigger = "on_start"
	SourceTriggerInterval SourceTrigger = "interval"
	SourceTriggerRetry    SourceTrigger = "retry"
)

// SourceAttempt is a bounded event suitable for structured logs and metrics.
type SourceAttempt struct {
	SourceID     domain.SourceID `json:"source_id"`
	Trigger      SourceTrigger   `json:"trigger"`
	Result       string          `json:"result"`
	FailureClass string          `json:"failure_class,omitempty"`
	Changed      bool            `json:"changed"`
	Duration     time.Duration   `json:"duration"`
}

// SourceObserver receives redacted attempt events. Implementations must return promptly.
type SourceObserver interface {
	ObserveSourceAttempt(attempt SourceAttempt)
}

// SourceSyncResult is the bounded, redacted result of one configured source attempt.
type SourceSyncResult struct {
	SourceID     domain.SourceID `json:"source_id"`
	Run          domain.SyncRun  `json:"run"`
	Changed      bool            `json:"changed"`
	FailureClass string          `json:"failure_class,omitempty"`
}

// SourceSyncAllResult contains one deterministic result per enabled source.
type SourceSyncAllResult struct {
	Results []SourceSyncResult `json:"results"`
}

// Sources returns a detached source-ID-sorted copy of the configured registry.
func (host *Host) Sources() []domain.Source {
	host.mu.Lock()
	defer host.mu.Unlock()
	return cloneSources(host.sources)
}

// SyncSource synchronizes one enabled source from the Host's trusted registry.
func (host *Host) SyncSource(ctx context.Context, id domain.SourceID) (SourceSyncResult, error) {
	ctx = nonNilHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return SourceSyncResult{}, err
	}
	source, err := host.configuredSource(id)
	if err != nil {
		return SourceSyncResult{SourceID: id}, err
	}
	result, err := host.sourceJobs.run(ctx, source, SourceTriggerManual)
	return publicSourceResult(result), err
}

// SyncAll synchronizes every enabled configured source in deterministic order.
func (host *Host) SyncAll(ctx context.Context) (SourceSyncAllResult, error) {
	ctx = nonNilHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return SourceSyncAllResult{}, err
	}
	if err := host.ensureSourceRuntimeOpen(); err != nil {
		return SourceSyncAllResult{}, err
	}
	public := SourceSyncAllResult{Results: make([]SourceSyncResult, 0, len(host.sources))}
	partial := false
	for _, source := range host.Sources() {
		if !source.Enabled {
			continue
		}
		result, err := host.sourceJobs.run(ctx, source, SourceTriggerManual)
		if err != nil {
			partial = true
		}
		public.Results = append(public.Results, publicSourceResult(result))
	}
	if partial {
		return public, reconcile.ErrSyncPartial
	}
	return public, nil
}

// SourceStatus returns the durable redacted status of one configured source.
func (host *Host) SourceStatus(ctx context.Context, id domain.SourceID) (domain.SourceStatus, error) {
	ctx = nonNilHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return domain.SourceStatus{}, err
	}
	if _, err := host.configuredSource(id); err != nil {
		return domain.SourceStatus{SourceID: id}, err
	}
	return host.sourceState.SourceStatus(ctx, host.config.Scope, id)
}

func (host *Host) configuredSource(id domain.SourceID) (domain.Source, error) {
	if err := app.ValidateSourceID(id); err != nil {
		return domain.Source{}, app.ErrSourceInvalid
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed || host.resourcesClosed {
		return domain.Source{}, ErrHostClosed
	}
	source, exists := host.sourceByID[id]
	if !exists {
		return domain.Source{}, app.ErrSourceNotFound
	}
	if !source.Enabled {
		return domain.Source{}, app.ErrSourceInvalid
	}
	return cloneSource(source), nil
}

func (host *Host) ensureSourceRuntimeOpen() error {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed || host.resourcesClosed {
		return ErrHostClosed
	}
	return nil
}

func sourceIndex(sources []domain.Source) map[domain.SourceID]domain.Source {
	index := make(map[domain.SourceID]domain.Source, len(sources))
	for _, source := range sources {
		index[source.ID] = cloneSource(source)
	}
	return index
}

func cloneSources(sources []domain.Source) []domain.Source {
	cloned := make([]domain.Source, len(sources))
	for index := range sources {
		cloned[index] = cloneSource(sources[index])
	}
	sort.Slice(cloned, func(left, right int) bool { return cloned[left].ID < cloned[right].ID })
	return cloned
}

func cloneSource(source domain.Source) domain.Source {
	if source.Config.Filesystem != nil {
		filesystem := *source.Config.Filesystem
		filesystem.Include = append([]string(nil), filesystem.Include...)
		source.Config.Filesystem = &filesystem
	}
	return source
}

func publicSourceResult(result reconcile.Result) SourceSyncResult {
	return SourceSyncResult{SourceID: result.SourceID, Run: result.Run, Changed: result.Changed, FailureClass: result.FailureClass}
}

func nonNilHostContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
