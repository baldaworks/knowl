package app

import (
	"context"
	"math"
	"slices"
	"strings"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var statisticsOperationStatuses = [...]knowl.OperationStatus{
	knowl.StatusReceived,
	knowl.StatusPlanned,
	knowl.StatusAwaitingReview,
	knowl.StatusApplying,
	knowl.StatusCommitted,
	knowl.StatusFailed,
}

var statisticsTableNames = [...]string{
	knowl.StatisticsTableOperations,
	knowl.StatisticsTablePages,
	knowl.StatisticsTableLinks,
	knowl.StatisticsTableProjectionState,
	knowl.StatisticsTableSources,
	knowl.StatisticsTableSourceDocuments,
	knowl.StatisticsTablePageSources,
	knowl.StatisticsTableEmbeddingState,
	knowl.StatisticsTableEmbeddingChunks,
}

// Statistics returns one current snapshot bound to the service's trusted
// scope. No caller-controlled scope, table, or schema selector is accepted.
func (service *OperatorService) Statistics(ctx context.Context) (knowl.OperatorStatistics, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.OperatorStatistics{}, err
	}
	if service.readers.Statistics == nil {
		return knowl.OperatorStatistics{}, ErrOperatorCapabilityUnavailable
	}
	readCtx, cancel := boundedReadContext(nonNilContext(ctx), service.limits)
	defer cancel()
	result, err := service.readers.Statistics.Statistics(readCtx, service.scope)
	if err != nil {
		return knowl.OperatorStatistics{}, err
	}
	if !validStatistics(result) {
		return knowl.OperatorStatistics{}, ErrOperatorWorkspaceUnavailable
	}
	result.Product.OperationsByStatus = cloneStatisticsStatuses(result.Product.OperationsByStatus)
	result.Storage.Tables = slices.Clone(result.Storage.Tables)
	return result, nil
}

func validStatistics(result knowl.OperatorStatistics) bool {
	_, offset := result.ObservedAt.Zone()
	if result.ObservedAt.IsZero() || offset != 0 {
		return false
	}
	counts := [...]int64{result.Product.Pages, result.Product.Links, result.Product.ConfiguredSources, result.Product.SourceDocuments, result.Product.OperationsTotal, result.Projection.EligiblePages, result.Projection.EmbeddedPages, result.Projection.Chunks}
	for _, count := range counts {
		if count < 0 {
			return false
		}
	}
	var operations int64
	if len(result.Product.OperationsByStatus) != len(statisticsOperationStatuses) {
		return false
	}
	for _, status := range statisticsOperationStatuses {
		count, ok := result.Product.OperationsByStatus[status]
		if !ok || count < 0 || count > math.MaxInt64-operations {
			return false
		}
		operations += count
	}
	if operations != result.Product.OperationsTotal || !validStatisticsProjection(result.Projection) || strings.TrimSpace(result.Storage.Backend) == "" || len(result.Storage.Tables) != len(statisticsTableNames) || !validStatisticsMetric(result.Storage.DatabaseBytes) {
		return false
	}
	for index, table := range result.Storage.Tables {
		if table.Name != statisticsTableNames[index] || table.Rows < 0 || !validStatisticsMetric(table.DataBytes) || !validStatisticsMetric(table.IndexBytes) || !validStatisticsMetric(table.TotalBytes) {
			return false
		}
	}
	return true
}

func validStatisticsProjection(value knowl.ProjectionStatistics) bool {
	if value.EmbeddedPages > value.EligiblePages {
		return false
	}
	switch value.LexicalStatus {
	case knowl.ProjectionStatisticsUnavailable, knowl.ProjectionStatisticsReady, knowl.ProjectionStatisticsInvalid:
	default:
		return false
	}
	switch value.EmbeddingStatus {
	case knowl.ProjectionStatisticsDisabled, knowl.ProjectionStatisticsUnavailable, knowl.ProjectionStatisticsReady, knowl.ProjectionStatisticsIncomplete, knowl.ProjectionStatisticsInvalid:
		return true
	default:
		return false
	}
}

func validStatisticsMetric(metric knowl.StatisticsMetric) bool {
	if metric.Value < 0 {
		return false
	}
	switch metric.Availability {
	case knowl.StatisticsAvailable:
		return true
	case knowl.StatisticsUnsupported, knowl.StatisticsPermissionDenied, knowl.StatisticsTemporarilyUnavailable:
		return metric.Value == 0
	default:
		return false
	}
}

func cloneStatisticsStatuses(source map[knowl.OperationStatus]int64) map[knowl.OperationStatus]int64 {
	result := make(map[knowl.OperationStatus]int64, len(source))
	for status, count := range source {
		result[status] = count
	}
	return result
}
