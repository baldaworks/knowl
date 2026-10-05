package sqlite

import (
	"github.com/pressly/goose/v3"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestOperationDetailsPersistence(t *testing.T) {
	path := t.TempDir() + "/diagnostics.sqlite"
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	storetest.RunOperationDetails(t, storetest.OperationDetailsHarness{
		Store: store, Scope: "operation-details", Conflict: ErrConflict, InvalidState: ErrInvalidState,
		OpenPeer: func(t *testing.T) app.OperationStore {
			t.Helper()
			peer, err := Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = peer.Close() })
			return peer
		},
		ContextPayload: func(t *testing.T, id knowl.OperationID, payload *string) {
			t.Helper()
			if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_operations SET context_report=? WHERE operation_id=?`, payload, id); err != nil {
				t.Fatal(err)
			}
		},
		LegacyPlan: func(t *testing.T, id knowl.OperationID) {
			t.Helper()
			if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_operations SET plan_file_count=NULL WHERE operation_id=?`, id); err != nil {
				t.Fatal(err)
			}
		},
		ReadyAt: func(t *testing.T, id knowl.OperationID, readyAt time.Time) {
			t.Helper()
			if _, err := store.db.ExecContext(t.Context(), `UPDATE knowl_operations SET work_ready_at=? WHERE operation_id=? AND work_lease_token=''`, readyAt.UTC().Format(time.RFC3339Nano), id); err != nil {
				t.Fatal(err)
			}
		},
	})
}

func TestOperationDetailsMigrationPreservesLegacy(t *testing.T) {
	store, snapshot, _, _ := embeddingFixture(t)
	key, meta := executionFixture(snapshot.Scope, "details-migration", time.Unix(1, 0).UTC())
	reserved, err := store.Reserve(t.Context(), key, meta)
	if err != nil {
		t.Fatal(err)
	}
	summary := knowl.PlanSummary{Digest: strings.Repeat("d", 64), FileCount: 3}
	if err := store.SavePlan(t.Context(), reserved.ID, summary); err != nil {
		t.Fatal(err)
	}
	report := knowl.OperationContextReport{Version: 1, Outcome: knowl.ContextSelectionFailed}
	if err := store.SaveOperationContextReport(t.Context(), key.Scope, reserved.ID, 0, report); err != nil {
		t.Fatal(err)
	}
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, store.db, directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(t.Context(), 16); err != nil {
		t.Fatal(err)
	}
	var digest string
	var status knowl.OperationStatus
	if err := store.db.QueryRowContext(t.Context(), `SELECT plan_digest, status FROM knowl_operations WHERE operation_id=?`, reserved.ID).Scan(&digest, &status); err != nil || digest != summary.Digest || status != knowl.StatusPlanned {
		t.Fatalf("old operation changed: %s %s %v", digest, status, err)
	}
	if err := store.CheckProjection(t.Context(), snapshot); err != nil {
		t.Fatalf("canonical projection changed: %v", err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), store.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	operation, err := reopened.Operation(t.Context(), key.Scope, reserved.ID)
	if err != nil || operation.Context != nil || operation.Plan == nil || operation.Plan.FileCount != nil || operation.Plan.Digest != summary.Digest || operation.Status != knowl.StatusPlanned {
		t.Fatalf("legacy migration read: %+v %v", operation, err)
	}
	descriptor, err := reopened.Execution(t.Context(), key.Scope, reserved.ID)
	if err != nil || !reflect.DeepEqual(descriptor, reserved.Descriptor) {
		t.Fatalf("legacy execution changed: %+v %v", descriptor, err)
	}
}
