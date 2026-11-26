package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestStatisticsCurrentScopedSnapshot(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/statistics.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	insertStatisticsScope(t, store, "wanted", 7, 5)
	insertStatisticsScope(t, store, "foreign", 70, 50)
	for index, status := range []knowl.OperationStatus{knowl.StatusReceived, knowl.StatusPlanned, knowl.StatusCommitted} {
		insertStatisticsOperation(t, store, "wanted", index, status)
	}
	insertStatisticsOperation(t, store, "foreign", 99, knowl.StatusFailed)

	result, err := store.Statistics(t.Context(), "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if result.Product.Pages != 7 || result.Product.Links != 5 || result.Product.SourceDocuments != 1 || result.Product.OperationsTotal != 3 {
		t.Fatalf("product=%+v", result.Product)
	}
	if result.Product.OperationsByStatus[knowl.StatusReceived] != 1 || result.Product.OperationsByStatus[knowl.StatusFailed] != 0 || len(result.Product.OperationsByStatus) != 6 {
		t.Fatalf("statuses=%+v", result.Product.OperationsByStatus)
	}
	if result.Projection.LexicalStatus != knowl.ProjectionStatisticsReady || result.Projection.EmbeddingStatus != knowl.ProjectionStatisticsDisabled || result.Storage.Backend != knowl.StatisticsBackendSQLite || result.Storage.DatabaseBytes.Availability != knowl.StatisticsAvailable || len(result.Storage.Tables) != len(sqliteStatisticsTables) {
		t.Fatalf("projection=%+v storage=%+v", result.Projection, result.Storage)
	}
	if result.ObservedAt.IsZero() || result.ObservedAt.After(time.Now().UTC()) {
		t.Fatalf("observed_at=%v", result.ObservedAt)
	}
	pages := statisticsTable(t, result, knowl.StatisticsTablePages)
	if pages.Rows != 0 {
		t.Fatalf("deployment pages rows=%d", pages.Rows)
	}
	operations := statisticsTable(t, result, knowl.StatisticsTableOperations)
	if operations.Rows != 4 {
		t.Fatalf("deployment operation rows=%d", operations.Rows)
	}
}

func TestStatisticsSharedContract(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/statistics-contract.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	storetest.RunStatisticsContract(t, storetest.StatisticsHarness{
		Reader: store, Backend: knowl.StatisticsBackendSQLite, Scope: "wanted", Foreign: "statistics-foreign",
		Seed: func(t testing.TB, scope, foreign knowl.ScopeRef) {
			for _, fixture := range []struct {
				scope        knowl.ScopeRef
				pages, links int64
			}{{scope, 7, 5}, {foreign, 70, 50}} {
				insertStatisticsScopeTB(t, store, fixture.scope, fixture.pages, fixture.links)
			}
			for index, status := range []knowl.OperationStatus{knowl.StatusReceived, knowl.StatusPlanned, knowl.StatusCommitted} {
				insertStatisticsOperationTB(t, store, scope, index, status)
			}
			insertStatisticsOperationTB(t, store, foreign, 99, knowl.StatusFailed)
		},
	})
}

func TestStatisticsConcurrentCommitIsOldOrNewAndRefreshes(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/statistics-transition.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := knowl.ScopeRef("transition")
	insertStatisticsScope(t, store, scope, 7, 5)
	ctx := t.Context()
	storetest.RunStatisticsTransition(t, store, scope,
		storetest.StatisticsTuple{Pages: 7, Links: 5, SourceDocuments: 1},
		storetest.StatisticsTuple{Pages: 8, Links: 6, SourceDocuments: 2},
		func() error {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback() }()
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx, `UPDATE knowl_projection_state SET page_count=?,link_count=?,snapshot_digest=?,ready_at=? WHERE scope=?`, 8, 6, "snapshot-2", now, scope); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO knowl_source_documents(scope,source_id,document_id,revision,accepted_source,last_seen_run_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, scope, "source", "document-2", "revision-2", "accepted-2", "run-2", now, now); err != nil {
				return err
			}
			return tx.Commit()
		},
	)
}

func TestStatisticsEmbeddingStates(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/embedding-statistics.sqlite", app.EmbeddingOptions{Provider: fixedEmbeddingProvider{}, Space: app.EmbeddingSpace{Model: "statistics", Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := knowl.ScopeRef("scope")
	snapshot := strings.Repeat("a", 64)
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_projection_state(scope,schema_digest,snapshot_digest,page_count,link_count,ready_at) VALUES(?,?,?,?,?,?)`, scope, "schema", snapshot, 1, 0, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_pages(scope,page_id,path,title,body,digest,source_refs,updated_at) VALUES(?,?,?,?,?,?,?,?)`, scope, "page", "page.md", "Page", "body", "statistics-digest", "[]", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	coverage, err := hybrid.EncodeCoverage([]hybrid.PageCoverage{{PageID: "page", PageDigest: "statistics-digest", Chunks: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_embedding_state(scope,space,snapshot_digest,dimensions,mode,reason,chunk_count,omitted_chunks,omitted_runes,ready_at,coverage,native_format) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`, scope, store.embedding.Fingerprint, snapshot, 2, knowl.RetrievalHybrid, "", 1, 0, 0, time.Now().UTC().Format(time.RFC3339Nano), coverage); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_embedding_chunks(scope,space,page_id,page_digest,ordinal,content_hash,dimensions,vector) VALUES(?,?,?,?,?,?,?,?)`, scope, store.embedding.Fingerprint, "page", "statistics-digest", 0, strings.Repeat("c", 64), 2, make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	result, err := store.Statistics(t.Context(), scope)
	if err != nil || result.Projection.EmbeddingStatus != knowl.ProjectionStatisticsReady || result.Projection.EligiblePages != 1 || result.Projection.EmbeddedPages != 1 || result.Projection.Chunks != 1 {
		t.Fatalf("ready projection=%+v err=%v", result.Projection, err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_state SET coverage='{' WHERE scope=?`, scope); err != nil {
		t.Fatal(err)
	}
	result, err = store.Statistics(t.Context(), scope)
	if err != nil || result.Projection.EmbeddingStatus != knowl.ProjectionStatisticsInvalid {
		t.Fatalf("invalid projection=%+v err=%v", result.Projection, err)
	}
	if _, err := store.db.ExecContext(t.Context(), `DELETE FROM knowl_embedding_chunks WHERE scope=?`, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_embedding_state SET mode='degraded',reason='unavailable',chunk_count=0,omitted_chunks=1,coverage='' WHERE scope=?`, scope); err != nil {
		t.Fatal(err)
	}
	result, err = store.Statistics(t.Context(), scope)
	if err != nil || result.Projection.EmbeddingStatus != knowl.ProjectionStatisticsIncomplete {
		t.Fatalf("incomplete projection=%+v err=%v", result.Projection, err)
	}
}

func TestStatisticsHonorsCanceledContext(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir()+"/cancel-statistics.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Statistics(ctx, "scope"); err == nil {
		t.Fatal("canceled statistics read succeeded")
	}
}

func insertStatisticsScope(t *testing.T, store *Store, scope knowl.ScopeRef, pages, links int64) {
	t.Helper()
	insertStatisticsScopeTB(t, store, scope, pages, links)
}

func insertStatisticsScopeTB(t testing.TB, store *Store, scope knowl.ScopeRef, pages, links int64) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_projection_state(scope,schema_digest,snapshot_digest,page_count,link_count,ready_at) VALUES(?,?,?,?,?,?)`, scope, "schema", "snapshot", pages, links, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_sources(scope,source_id,source_type,config_digest,created_at,updated_at) VALUES(?,?,?,?,?,?)`, scope, "source", "filesystem", "digest", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_source_documents(scope,source_id,document_id,revision,accepted_source,last_seen_run_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, scope, "source", "document", "revision", "accepted", "run", now, now); err != nil {
		t.Fatal(err)
	}
}

func insertStatisticsOperation(t *testing.T, store *Store, scope knowl.ScopeRef, index int, status knowl.OperationStatus) {
	t.Helper()
	insertStatisticsOperationTB(t, store, scope, index, status)
}

func insertStatisticsOperationTB(t testing.TB, store *Store, scope knowl.ScopeRef, index int, status knowl.OperationStatus) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	identity := fmt.Sprintf("%d", index)
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_operations(operation_id,scope,source_adapter,source_id,source_version,source_digest,schema_digest,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, "operation-"+identity, scope, "fixture", "source-"+identity, "version-"+identity, "digest", "schema", status, now, now); err != nil {
		t.Fatal(err)
	}
}

func statisticsTable(t *testing.T, result knowl.OperatorStatistics, name string) knowl.TableStatistics {
	t.Helper()
	for _, table := range result.Storage.Tables {
		if table.Name == name {
			return table
		}
	}
	t.Fatalf("statistics table %q not found", name)
	return knowl.TableStatistics{}
}
