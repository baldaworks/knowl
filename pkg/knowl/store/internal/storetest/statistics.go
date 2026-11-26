package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// StatisticsHarness supplies backend-specific fixture writes to one shared
// public statistics contract.
type StatisticsHarness struct {
	Reader  app.StatisticsReader
	Backend string
	Scope   knowl.ScopeRef
	Foreign knowl.ScopeRef
	Seed    func(testing.TB, knowl.ScopeRef, knowl.ScopeRef)
}

// StatisticsTuple is the cross-table logical state whose consistency is
// observable through one statistics snapshot.
type StatisticsTuple struct {
	Pages           int64
	Links           int64
	SourceDocuments int64
}

// RunStatisticsContract verifies logical parity, scope isolation, the fixed
// technical allowlist, and detached current-snapshot semantics.
func RunStatisticsContract(t *testing.T, harness StatisticsHarness) {
	t.Helper()
	if harness.Reader == nil || harness.Seed == nil || harness.Scope == "" || harness.Foreign == "" {
		t.Fatal("statistics harness is incomplete")
	}
	harness.Seed(t, harness.Scope, harness.Foreign)
	before := time.Now().UTC()
	result, err := harness.Reader.Statistics(context.Background(), harness.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservedAt.Before(before) || result.ObservedAt.After(time.Now().UTC()) {
		t.Fatalf("observed_at=%v before=%v", result.ObservedAt, before)
	}
	if result.Product.Pages != 7 || result.Product.Links != 5 || result.Product.SourceDocuments != 1 || result.Product.OperationsTotal != 3 {
		t.Fatalf("scope-bound product=%+v", result.Product)
	}
	wantStatuses := map[knowl.OperationStatus]int64{
		knowl.StatusReceived: 1, knowl.StatusPlanned: 1, knowl.StatusAwaitingReview: 0,
		knowl.StatusApplying: 0, knowl.StatusCommitted: 1, knowl.StatusFailed: 0,
	}
	if len(result.Product.OperationsByStatus) != len(wantStatuses) {
		t.Fatalf("operation statuses=%+v", result.Product.OperationsByStatus)
	}
	for status, want := range wantStatuses {
		if got := result.Product.OperationsByStatus[status]; got != want {
			t.Fatalf("operation status %q=%d want %d", status, got, want)
		}
	}
	if result.Projection.LexicalStatus != "ready" || result.Projection.EmbeddingStatus != "disabled" || result.Projection.EligiblePages != 7 {
		t.Fatalf("projection=%+v", result.Projection)
	}
	if result.Storage.Backend != harness.Backend {
		t.Fatalf("backend=%q want %q", result.Storage.Backend, harness.Backend)
	}
	wantTables := []string{
		knowl.StatisticsTableOperations, knowl.StatisticsTablePages, knowl.StatisticsTableLinks,
		knowl.StatisticsTableProjectionState, knowl.StatisticsTableSources, knowl.StatisticsTableSourceDocuments,
		knowl.StatisticsTablePageSources, knowl.StatisticsTableEmbeddingState, knowl.StatisticsTableEmbeddingChunks,
	}
	if len(result.Storage.Tables) != len(wantTables) {
		t.Fatalf("tables=%+v", result.Storage.Tables)
	}
	for index, name := range wantTables {
		if result.Storage.Tables[index].Name != name || result.Storage.Tables[index].Rows < 0 {
			t.Fatalf("table[%d]=%+v want %q", index, result.Storage.Tables[index], name)
		}
	}
	if got := result.Storage.Tables[0].Rows; got < 4 {
		t.Fatalf("deployment operation rows=%d want at least foreign+scope rows", got)
	}
}

// RunStatisticsTransition verifies that a commit racing with a read yields a
// complete old or new tuple and that a later request observes the committed
// state with a fresh observation time.
func RunStatisticsTransition(t *testing.T, reader app.StatisticsReader, scope knowl.ScopeRef, oldState, newState StatisticsTuple, commit func() error) {
	t.Helper()
	before, err := reader.Statistics(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	assertStatisticsTuple(t, before, oldState)

	start := make(chan struct{})
	committed := make(chan error, 1)
	go func() {
		<-start
		committed <- commit()
	}()
	close(start)
	during, err := reader.Statistics(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if got := statisticsTuple(during); got != oldState && got != newState {
		t.Fatalf("concurrent snapshot mixed states: got=%+v old=%+v new=%+v", got, oldState, newState)
	}
	if err := <-committed; err != nil {
		t.Fatalf("commit statistics transition: %v", err)
	}

	time.Sleep(time.Millisecond)
	after, err := reader.Statistics(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	assertStatisticsTuple(t, after, newState)
	if !after.ObservedAt.After(during.ObservedAt) {
		t.Fatalf("fresh snapshot observed_at=%v previous=%v", after.ObservedAt, during.ObservedAt)
	}
}

func assertStatisticsTuple(t *testing.T, result knowl.OperatorStatistics, want StatisticsTuple) {
	t.Helper()
	if got := statisticsTuple(result); got != want {
		t.Fatalf("statistics tuple=%+v want=%+v", got, want)
	}
}

func statisticsTuple(result knowl.OperatorStatistics) StatisticsTuple {
	return StatisticsTuple{Pages: result.Product.Pages, Links: result.Product.Links, SourceDocuments: result.Product.SourceDocuments}
}
