package app

import (
	"context"
	"errors"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type statisticsReaderFixture struct {
	scope  knowl.ScopeRef
	result knowl.OperatorStatistics
	err    error
}

func (fixture *statisticsReaderFixture) Statistics(_ context.Context, scope knowl.ScopeRef) (knowl.OperatorStatistics, error) {
	fixture.scope = scope
	return fixture.result, fixture.err
}

func TestOperatorStatisticsBindsScopeAndDetachesContainers(t *testing.T) {
	fixture := &statisticsReaderFixture{result: validStatisticsFixture()}
	service, err := NewOperatorService("statistics-trusted", OperatorReaders{Statistics: fixture}, OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Statistics(t.Context())
	if err != nil || fixture.scope != "statistics-trusted" {
		t.Fatalf("statistics scope=%q err=%v", fixture.scope, err)
	}
	result.Product.OperationsByStatus[knowl.StatusReceived] = 99
	result.Storage.Tables[0].Rows = 99
	if fixture.result.Product.OperationsByStatus[knowl.StatusReceived] != 1 || fixture.result.Storage.Tables[0].Rows != 1 {
		t.Fatal("operator statistics returned aliased containers")
	}
}

func TestOperatorStatisticsRejectsMalformedAdapterOutput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*knowl.OperatorStatistics)
	}{
		{"negative", func(value *knowl.OperatorStatistics) { value.Product.Pages = -1 }},
		{"missing status", func(value *knowl.OperatorStatistics) { delete(value.Product.OperationsByStatus, knowl.StatusFailed) }},
		{"unknown projection", func(value *knowl.OperatorStatistics) { value.Projection.EmbeddingStatus = "mystery" }},
		{"wrong table", func(value *knowl.OperatorStatistics) { value.Storage.Tables[0].Name = "secret" }},
		{"unavailable value", func(value *knowl.OperatorStatistics) {
			value.Storage.DatabaseBytes = knowl.StatisticsMetric{Availability: knowl.StatisticsUnsupported, Value: 1}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validStatisticsFixture()
			test.mutate(&value)
			service, err := NewOperatorService("statistics-trusted", OperatorReaders{Statistics: &statisticsReaderFixture{result: value}}, OperatorOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Statistics(t.Context()); !errors.Is(err, ErrOperatorWorkspaceUnavailable) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestOperatorStatisticsMissingCapability(t *testing.T) {
	service, err := NewOperatorService("statistics-trusted", OperatorReaders{}, OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Statistics(t.Context()); !errors.Is(err, ErrOperatorCapabilityUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

func validStatisticsFixture() knowl.OperatorStatistics {
	statuses := map[knowl.OperationStatus]int64{
		knowl.StatusReceived: 1, knowl.StatusPlanned: 2, knowl.StatusAwaitingReview: 3,
		knowl.StatusApplying: 4, knowl.StatusCommitted: 5, knowl.StatusFailed: 6,
	}
	names := statisticsTableNames
	tables := make([]knowl.TableStatistics, len(names))
	for index, name := range names {
		tables[index] = knowl.TableStatistics{Name: name, Rows: 1, DataBytes: availableStatisticsMetric(1), IndexBytes: availableStatisticsMetric(1), TotalBytes: availableStatisticsMetric(2)}
	}
	return knowl.OperatorStatistics{
		ObservedAt: time.Unix(100, 0).UTC(),
		Product:    knowl.ProductStatistics{Pages: 1, Links: 2, ConfiguredSources: 3, SourceDocuments: 4, OperationsTotal: 21, OperationsByStatus: statuses},
		Projection: knowl.ProjectionStatistics{LexicalStatus: knowl.ProjectionStatisticsReady, EmbeddingStatus: knowl.ProjectionStatisticsReady, EligiblePages: 1, EmbeddedPages: 1, Chunks: 2},
		Storage:    knowl.StorageStatistics{Backend: "statistics", DatabaseBytes: availableStatisticsMetric(10), Tables: tables},
	}
}

func availableStatisticsMetric(value int64) knowl.StatisticsMetric {
	return knowl.StatisticsMetric{Availability: knowl.StatisticsAvailable, Value: value}
}
