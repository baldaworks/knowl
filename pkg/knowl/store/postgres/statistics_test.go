package postgres

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestStatistics(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("KNOWL_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set KNOWL_POSTGRES_DSN to run PostgreSQL statistics tests")
	}
	runStatisticsPostgres(t, dsn)
}

func runStatisticsPostgres(t *testing.T, dsn string) {
	t.Helper()
	store, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := knowl.ScopeRef(fmt.Sprintf("statistics_%s_%d", strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()), time.Now().UnixNano()))
	foreign := knowl.ScopeRef(string(scope) + "_foreign")
	storetest.RunStatisticsContract(t, storetest.StatisticsHarness{
		Reader: store, Backend: knowl.StatisticsBackendPostgres, Scope: scope, Foreign: foreign,
		Seed: func(t testing.TB, scope, foreign knowl.ScopeRef) {
			insertPostgresStatisticsScope(t, store, scope, 7, 5)
			insertPostgresStatisticsScope(t, store, foreign, 70, 50)
			for index, status := range []knowl.OperationStatus{knowl.StatusReceived, knowl.StatusPlanned, knowl.StatusCommitted} {
				insertPostgresStatisticsOperation(t, store, scope, index, status)
			}
			insertPostgresStatisticsOperation(t, store, foreign, 99, knowl.StatusFailed)
		},
	})

	result, err := store.Statistics(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if result.Product.Pages != 7 || result.Product.Links != 5 || result.Product.SourceDocuments != 1 || result.Product.OperationsTotal != 3 {
		t.Fatalf("product=%+v", result.Product)
	}
	if result.Product.OperationsByStatus[knowl.StatusReceived] != 1 || result.Product.OperationsByStatus[knowl.StatusFailed] != 0 || len(result.Product.OperationsByStatus) != 6 {
		t.Fatalf("statuses=%+v", result.Product.OperationsByStatus)
	}
	if result.Projection.LexicalStatus != knowl.ProjectionStatisticsReady || result.Projection.EmbeddingStatus != knowl.ProjectionStatisticsDisabled || result.Storage.Backend != knowl.StatisticsBackendPostgres || result.Storage.DatabaseBytes.Availability != knowl.StatisticsAvailable || len(result.Storage.Tables) != len(postgresStatisticsTables) {
		t.Fatalf("projection=%+v storage=%+v", result.Projection, result.Storage)
	}
	if result.ObservedAt.IsZero() || result.ObservedAt.After(time.Now().UTC()) {
		t.Fatalf("observed_at=%v", result.ObservedAt)
	}
	for index, table := range result.Storage.Tables {
		if table.Name != postgresStatisticsTables[index].name || table.Rows < 0 {
			t.Fatalf("table[%d]=%+v", index, table)
		}
	}

	transitionScope := knowl.ScopeRef(string(scope) + "_transition")
	insertPostgresStatisticsScope(t, store, transitionScope, 7, 5)
	ctx := t.Context()
	storetest.RunStatisticsTransition(t, store, transitionScope,
		storetest.StatisticsTuple{Pages: 7, Links: 5, SourceDocuments: 1},
		storetest.StatisticsTuple{Pages: 8, Links: 6, SourceDocuments: 2},
		func() error {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback() }()
			now := time.Now().UTC()
			if _, err := tx.ExecContext(ctx, `UPDATE knowl_projection_state SET page_count=$1,link_count=$2,snapshot_digest=$3,ready_at=$4 WHERE scope=$5`, 8, 6, "snapshot-2", now, transitionScope); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO knowl_source_documents(scope,source_id,document_id,revision,accepted_source,last_seen_run_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, transitionScope, "source", "document-2", "revision-2", "accepted-2", "run-2", now, now); err != nil {
				return err
			}
			return tx.Commit()
		},
	)
}

func insertPostgresStatisticsScope(t testing.TB, store *Store, scope knowl.ScopeRef, pages, links int64) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_projection_state(scope,schema_digest,snapshot_digest,page_count,link_count,ready_at) VALUES($1,$2,$3,$4,$5,$6)`, scope, "schema", "snapshot", pages, links, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_sources(scope,source_id,source_type,config_digest,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6)`, scope, "source", "filesystem", "digest", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_source_documents(scope,source_id,document_id,revision,accepted_source,last_seen_run_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, scope, "source", "document", "revision", "accepted", "run", now, now); err != nil {
		t.Fatal(err)
	}
}

func insertPostgresStatisticsOperation(t testing.TB, store *Store, scope knowl.ScopeRef, index int, status knowl.OperationStatus) {
	t.Helper()
	now := time.Now().UTC()
	identity := fmt.Sprintf("%d", index)
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO knowl_operations(operation_id,scope,source_adapter,source_id,source_version,source_digest,schema_digest,status,created_at,updated_at,work_ready_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, "statistics-operation-"+identity+"-"+string(scope), scope, "fixture", "source-"+identity, "version-"+identity, "digest", "schema", status, now, now, now); err != nil {
		t.Fatal(err)
	}
}
