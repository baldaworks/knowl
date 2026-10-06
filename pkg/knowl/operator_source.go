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
	if err := reader.checkScopeLocked(scope); err != nil {
		host.mu.Unlock()
		return result, err
	}
	identities := make([]domain.OperatorSourceSummary, 0, len(host.sources))
	for _, source := range host.sources {
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
	if err := reader.checkScopeLocked(scope); err != nil {
		host.mu.Unlock()
		return domain.OperatorSourceSummary{}, err
	}
	source, exists := host.sourceByID[id]
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

func (reader *operatorSourceReader) checkScopeLocked(scope domain.ScopeRef) error {
	if reader.host.closed || reader.host.resourcesClosed {
		return ErrHostClosed
	}
	if scope != reader.host.config.Scope {
		return app.ErrOperatorWorkspaceUnavailable
	}
	return nil
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
