package webui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type statisticsFixture struct {
	value domain.OperatorStatistics
}

func (fixture statisticsFixture) Statistics(context.Context, domain.ScopeRef) (domain.OperatorStatistics, error) {
	return fixture.value, nil
}

func TestStatisticsFragmentRendersCurrentScopedAndDeploymentFacts(t *testing.T) {
	value := webStatisticsFixture()
	service, err := app.NewOperatorService("trusted", app.OperatorReaders{Statistics: statisticsFixture{value: value}}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Dependencies{Operator: service})
	if err != nil {
		t.Fatal(err)
	}
	response, document := fragmentDocument(t, handler, statisticsFragment)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	text := strings.Join(strings.Fields(nodeText(document)), " ")
	for _, expected := range []string{
		"Current state when this page was opened",
		"Knowledge · current scope",
		"Operation lifecycle stages",
		"Deployment-wide storage",
		"Physical values cover this Knowl database, not only the current scope.",
		"1.0 KiB",
		"Unavailable (unsupported)",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in %q", expected, text)
		}
	}
}

func TestStatisticsFragmentRejectsSelectors(t *testing.T) {
	service, err := app.NewOperatorService("trusted", app.OperatorReaders{Statistics: statisticsFixture{value: webStatisticsFixture()}}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Dependencies{Operator: service})
	if err != nil {
		t.Fatal(err)
	}
	response, _ := fragmentDocument(t, handler, statisticsFragment+"?scope=foreign")
	if response.Code != 400 || response.Header().Get("X-Knowl-Error") != errorInvalidRequest {
		t.Fatalf("status=%d error=%q", response.Code, response.Header().Get("X-Knowl-Error"))
	}
}

func webStatisticsFixture() domain.OperatorStatistics {
	statuses := map[domain.OperationStatus]int64{
		domain.StatusReceived: 1, domain.StatusPlanned: 2, domain.StatusAwaitingReview: 3,
		domain.StatusApplying: 4, domain.StatusCommitted: 5, domain.StatusFailed: 6,
	}
	names := []string{
		domain.StatisticsTableOperations, domain.StatisticsTablePages, domain.StatisticsTableLinks,
		domain.StatisticsTableProjectionState, domain.StatisticsTableSources, domain.StatisticsTableSourceDocuments,
		domain.StatisticsTablePageSources, domain.StatisticsTableEmbeddingState, domain.StatisticsTableEmbeddingChunks,
	}
	tables := make([]domain.TableStatistics, len(names))
	for index, name := range names {
		tables[index] = domain.TableStatistics{
			Name: name, Rows: int64(index),
			DataBytes:  domain.StatisticsMetric{Availability: domain.StatisticsAvailable, Value: 1024},
			IndexBytes: domain.StatisticsMetric{Availability: domain.StatisticsUnsupported},
			TotalBytes: domain.StatisticsMetric{Availability: domain.StatisticsAvailable, Value: 1024},
		}
	}
	return domain.OperatorStatistics{
		ObservedAt: time.Unix(100, 0).UTC(),
		Product:    domain.ProductStatistics{Pages: 10, Links: 11, ConfiguredSources: 2, SourceDocuments: 3, OperationsTotal: 21, OperationsByStatus: statuses},
		Projection: domain.ProjectionStatistics{LexicalStatus: "ready", EmbeddingStatus: "ready", EligiblePages: 10, EmbeddedPages: 9, Chunks: 20},
		Storage:    domain.StorageStatistics{Backend: "fixture", DatabaseBytes: domain.StatisticsMetric{Availability: domain.StatisticsAvailable, Value: 1024}, Tables: tables},
	}
}
