package knowl

import (
	"context"
	"errors"
	"sort"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type operatorSourceReader struct{ host *Host }

func newOperatorSourceReader(host *Host) app.OperatorSourceReader {
	return &operatorSourceReader{host: host}
}

func (reader *operatorSourceReader) ListSources(ctx context.Context, scope domain.ScopeRef, options app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorSourceSummary], error) {
	result := app.OperatorReadPage[domain.OperatorSourceSummary]{Items: make([]domain.OperatorSourceSummary, 0)}
	if options.Limit < 1 || options.Limit > 100 {
		return result, app.ErrOperatorLimitInvalid
	}
	if options.Continuation.Key != "" && app.ValidateSourceID(domain.SourceID(options.Continuation.Key)) != nil {
		return result, app.ErrOperatorCursorInvalid
	}
	ctx = nonNilHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	host := reader.host
	host.mu.Lock()
	sources, err := operatorScopeSourcesLocked(host, scope)
	if err != nil {
		host.mu.Unlock()
		return result, err
	}
	identities := make([]domain.OperatorSourceSummary, 0, len(sources))
	for _, source := range sources {
		if string(source.ID) > options.Continuation.Key {
			identities = append(identities, domain.OperatorSourceSummary{ID: source.ID, Type: source.Type, Enabled: source.Enabled})
		}
	}
	state := host.sourceState
	host.mu.Unlock()
	sort.Slice(identities, func(i, j int) bool { return identities[i].ID < identities[j].ID })
	if len(identities) > options.Limit {
		result.NextKey = string(identities[options.Limit-1].ID)
		identities = identities[:options.Limit]
	}
	for _, identity := range identities {
		status, err := operatorSourceStatus(ctx, state, scope, identity.ID)
		if err != nil {
			return result, err
		}
		identity.Status = status
		result.Items = append(result.Items, identity)
	}
	return result, nil
}

func (reader *operatorSourceReader) Source(ctx context.Context, scope domain.ScopeRef, id domain.SourceID) (domain.OperatorSourceSummary, error) {
	if app.ValidateSourceID(id) != nil {
		return domain.OperatorSourceSummary{}, app.ErrOperatorInvalidRequest
	}
	ctx = nonNilHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return domain.OperatorSourceSummary{}, err
	}
	host := reader.host
	host.mu.Lock()
	sources, err := operatorScopeSourcesLocked(host, scope)
	if err != nil {
		host.mu.Unlock()
		return domain.OperatorSourceSummary{}, err
	}
	var source domain.Source
	exists := false
	for _, configured := range sources {
		if configured.ID == id {
			source, exists = configured, true
			break
		}
	}
	state := host.sourceState
	identity := domain.OperatorSourceSummary{ID: source.ID, Type: source.Type, Enabled: source.Enabled}
	host.mu.Unlock()
	if !exists {
		return domain.OperatorSourceSummary{}, app.ErrSourceNotFound
	}
	status, err := operatorSourceStatus(ctx, state, scope, id)
	if err != nil {
		return domain.OperatorSourceSummary{}, err
	}
	identity.Status = status
	return identity, nil
}

func operatorScopeSourcesLocked(host *Host, scope domain.ScopeRef) ([]domain.Source, error) {
	if host.closed || host.resourcesClosed {
		return nil, ErrHostClosed
	}
	if host.config.scopeRegistry == nil || host.workspaces == nil {
		if scope != host.config.Scope {
			return nil, app.ErrOperatorWorkspaceUnavailable
		}
		return cloneSources(host.sources), nil
	}
	if _, err := host.workspaces.Resolve(scope); err != nil {
		return nil, app.ErrOperatorWorkspaceUnavailable
	}
	for _, configured := range host.config.scopeRegistry.All() {
		if configured.Ref == scope && !configured.Disabled {
			return cloneSources(configured.Sources), nil
		}
	}
	return nil, app.ErrOperatorWorkspaceUnavailable
}

func operatorSourceStatus(ctx context.Context, state app.SourceStateStore, scope domain.ScopeRef, id domain.SourceID) (*domain.OperatorSourceStatus, error) {
	if state == nil {
		return nil, app.ErrOperatorCapabilityUnavailable
	}
	status, err := state.SourceStatus(ctx, scope, id)
	if errors.Is(err, app.ErrSourceNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if status.Scope != scope || status.SourceID != id {
		return nil, app.ErrOperatorWorkspaceUnavailable
	}
	return &domain.OperatorSourceStatus{Status: status.Status, Counts: status.Counts, LastAttemptAt: status.LastAttemptAt, LastSuccessfulAt: status.LastSuccessfulAt, UpdatedAt: status.UpdatedAt, MaintenanceCounts: status.Maintenance.Counts}, nil
}
