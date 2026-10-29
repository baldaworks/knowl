package knowl

import (
	"context"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type operatorStatisticsReader struct {
	host   *Host
	reader app.StatisticsReader
}

func (reader *operatorStatisticsReader) Statistics(ctx context.Context, scope domain.ScopeRef) (domain.OperatorStatistics, error) {
	ctx = nonNilHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return domain.OperatorStatistics{}, err
	}
	reader.host.mu.Lock()
	if reader.host.closed || reader.host.resourcesClosed || scope != reader.host.config.Scope {
		reader.host.mu.Unlock()
		return domain.OperatorStatistics{}, app.ErrOperatorWorkspaceUnavailable
	}
	configuredSources := int64(len(reader.host.sources))
	reader.host.mu.Unlock()
	if reader.reader == nil {
		return domain.OperatorStatistics{}, app.ErrOperatorCapabilityUnavailable
	}
	result, err := reader.reader.Statistics(ctx, scope)
	if err != nil {
		return domain.OperatorStatistics{}, err
	}
	result.Product.ConfiguredSources = configuredSources
	return result, nil
}
