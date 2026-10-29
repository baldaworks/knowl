package knowl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type runtimeStatisticsFixture struct {
	scope domain.ScopeRef
}

const runtimeStatisticsScope = "statistics-trusted"

func (fixture *runtimeStatisticsFixture) Statistics(_ context.Context, scope domain.ScopeRef) (domain.OperatorStatistics, error) {
	fixture.scope = scope
	return domain.OperatorStatistics{ObservedAt: time.Unix(1, 0).UTC()}, nil
}

func TestOperatorStatisticsReaderUsesConfiguredSourceCount(t *testing.T) {
	fixture := &runtimeStatisticsFixture{}
	host := &Host{config: Config{Scope: runtimeStatisticsScope}, sources: []domain.Source{{ID: "one"}, {ID: "two"}}}
	result, err := (&operatorStatisticsReader{host: host, reader: fixture}).Statistics(t.Context(), runtimeStatisticsScope)
	if err != nil || fixture.scope != runtimeStatisticsScope || result.Product.ConfiguredSources != 2 {
		t.Fatalf("result=%+v scope=%q err=%v", result, fixture.scope, err)
	}
}

func TestOperatorStatisticsReaderRejectsForeignScope(t *testing.T) {
	host := &Host{config: Config{Scope: runtimeStatisticsScope}}
	_, err := (&operatorStatisticsReader{host: host, reader: &runtimeStatisticsFixture{}}).Statistics(t.Context(), "statistics-foreign")
	if !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
		t.Fatalf("error=%v", err)
	}
}
