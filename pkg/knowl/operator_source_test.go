package knowl

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestOperatorSourcesConfiguredStatus(t *testing.T) {
	store, err := sqlite.Open(t.Context(), t.TempDir()+"/sources.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const scope domain.ScopeRef = "source-adapter"
	const (
		operatorDisabledSourceID = "operator-disabled-source"
		operatorFailedSourceID   = "operator-failed-source"
		operatorNeverRunSourceID = "operator-never-run-source"
	)
	sources := []domain.Source{
		{ID: operatorDisabledSourceID, Type: domain.SourceTypeFilesystem, Config: domain.SourceConfig{Filesystem: &domain.FilesystemSourceConfig{Root: "/private/root-canary", URIBase: "https://private/uri-canary"}}},
		{ID: operatorFailedSourceID, Type: domain.SourceTypeGit, Enabled: true, Config: domain.SourceConfig{Git: &domain.GitSourceConfig{Remote: "https://remote-canary", Auth: domain.GitAuthConfig{SecretEnv: "auth-secret-canary", KeyFile: "/private/key-canary"}}}},
		{ID: operatorNeverRunSourceID, Type: domain.SourceTypeFilesystem, Enabled: true},
	}
	host := &Host{config: Config{Scope: scope}, sources: sources, sourceByID: sourceIndex(sources), sourceState: store}
	// Runtime mutation ports deliberately remain nil: a read must not invoke them.
	reader := newOperatorSourceReader(host)
	base := time.Unix(100, 0).UTC()
	run := domain.SyncRun{ID: "adapter-success", Scope: scope, SourceID: operatorFailedSourceID, ConfigDigest: strings.Repeat("a", 64), Status: domain.SyncStatusScanning, StartedAt: base, UpdatedAt: base}
	if _, _, err := store.BeginSync(t.Context(), app.BeginSyncRequest{Run: run, Type: domain.SourceTypeGit, RepositoryIdentity: strings.Repeat("b", 64)}); err != nil {
		t.Fatal(err)
	}
	prepared := app.PreparedSyncState{RunID: run.ID, Scope: scope, SourceID: run.SourceID, CompleteScan: true, Checkpoint: "private-checkpoint-canary", PreparedAt: base.Add(time.Second)}
	prepared.CandidateDigest, err = app.PreparedSyncDigest(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareSync(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	transition := app.SyncGeneration{RunID: run.ID, Scope: scope, SourceID: run.SourceID, Generation: "generation", UpdatedAt: base.Add(2 * time.Second)}
	if _, err := store.MarkContentCommitted(t.Context(), transition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkProjected(t.Context(), transition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeSync(t.Context(), app.SyncFinalization{RunID: run.ID, Scope: scope, SourceID: run.SourceID, CandidateDigest: prepared.CandidateDigest, Generation: transition.Generation, Checkpoint: prepared.Checkpoint, FinalizedAt: base.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	later := run
	later.ID = "adapter-failed"
	later.StartedAt = base.Add(10 * time.Second)
	later.UpdatedAt = later.StartedAt
	if _, _, err := store.BeginSync(t.Context(), app.BeginSyncRequest{Run: later, Type: domain.SourceTypeGit, RepositoryIdentity: strings.Repeat("b", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailSync(t.Context(), scope, later.ID, "provider_error", base.Add(11*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Disabled sources may have durable history even after configuration is disabled.
	disabled := run
	disabled.ID = "adapter-disabled"
	disabled.SourceID = operatorDisabledSourceID
	if _, _, err := store.BeginSync(t.Context(), app.BeginSyncRequest{Run: disabled, Type: domain.SourceTypeFilesystem}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailSync(t.Context(), scope, disabled.ID, "provider_error", base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	before, err := store.SourceStatus(t.Context(), scope, operatorFailedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	var got []domain.OperatorSourceSummary
	key := ""
	for range 4 {
		page, err := reader.ListSources(t.Context(), scope, app.OperatorReadOptions{Limit: 1, Continuation: app.OperatorContinuation{Key: key}})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page.Items...)
		key = page.NextKey
		if key == "" {
			break
		}
	}
	if key != "" || len(got) != 3 || got[0].ID != operatorDisabledSourceID || got[1].ID != operatorFailedSourceID || got[2].ID != operatorNeverRunSourceID {
		t.Fatalf("configured inventory: %+v", got)
	}
	if got[0].Enabled || got[0].Status == nil || got[2].Status != nil {
		t.Fatalf("disabled/never-run: %+v", got)
	}
	status := got[1].Status
	if status == nil || status.Status != domain.SyncStatusFailed || !status.LastAttemptAt.Equal(base.Add(11*time.Second)) || !status.LastSuccessfulAt.Equal(base.Add(3*time.Second)) {
		t.Fatalf("attempt and success: %+v", status)
	}
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	if err := json.Unmarshal(payload, &records); err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		for field := range record {
			if field != "id" && field != "type" && field != "enabled" && field != "status" {
				t.Fatalf("unexpected source field %q", field)
			}
		}
		if status, ok := record["status"].(map[string]any); ok {
			if record["id"] == operatorDisabledSourceID {
				if _, exists := status["last_successful_at"]; exists {
					t.Fatalf("source without successful run serialized success timestamp: %+v", status)
				}
			}
			allowed := map[string]bool{"status": true, "counts": true, "last_attempt_at": true, "last_successful_at": true, "updated_at": true, "maintenance_counts": true}
			for field := range status {
				if !allowed[field] {
					t.Fatalf("unexpected status field %q", field)
				}
			}
			if _, exists := status["maintenance_counts"]; !exists {
				t.Fatalf("maintenance counts missing: %+v", status)
			}
		}
		assertSourceCanaries(t, record)
	}
	service, err := app.NewOperatorService(scope, app.OperatorReaders{Sources: reader, Documents: store}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var serviceIDs []domain.SourceID
	cursor := ""
	for range 4 {
		page, err := service.Sources(t.Context(), app.OperatorListOptions{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			serviceIDs = append(serviceIDs, item.ID)
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if cursor != "" || !reflect.DeepEqual(serviceIDs, []domain.SourceID{operatorDisabledSourceID, operatorFailedSourceID, operatorNeverRunSourceID}) {
		t.Fatalf("signed source pagination: %+v", serviceIDs)
	}
	detail, err := service.Source(t.Context(), operatorNeverRunSourceID, app.OperatorListOptions{Limit: 1})
	if err != nil || detail.Source.Status != nil || detail.Documents.Items == nil || len(detail.Documents.Items) != 0 {
		t.Fatalf("never-run detail: %+v %v", detail, err)
	}
	single, err := reader.Source(t.Context(), scope, operatorDisabledSourceID)
	if err != nil || single.ID != operatorDisabledSourceID || single.Enabled {
		t.Fatalf("disabled read: %+v %v", single, err)
	}
	if _, err := host.SourceStatus(t.Context(), operatorDisabledSourceID); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("existing disabled behavior changed: %v", err)
	}
	for _, id := range []domain.SourceID{"missing", "invalid/source"} {
		_, err := reader.Source(t.Context(), scope, id)
		want := app.ErrSourceNotFound
		if id == "invalid/source" {
			want = app.ErrOperatorInvalidRequest
		}
		if !errors.Is(err, want) {
			t.Fatalf("source %q: %v", id, err)
		}
	}
	if _, err := reader.Source(t.Context(), "foreign", operatorFailedSourceID); !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
		t.Fatalf("foreign scope: %v", err)
	}
	for _, limit := range []int{0, -1, 101} {
		if _, err := reader.ListSources(t.Context(), scope, app.OperatorReadOptions{Limit: limit}); !errors.Is(err, app.ErrOperatorLimitInvalid) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	if _, err := reader.ListSources(t.Context(), scope, app.OperatorReadOptions{Limit: 1, Continuation: app.OperatorContinuation{Key: "bad/source"}}); !errors.Is(err, app.ErrOperatorCursorInvalid) {
		t.Fatalf("cursor: %v", err)
	}
	got[1].Status.Status = domain.SyncStatusSucceeded
	after, err := store.SourceStatus(t.Context(), scope, operatorFailedSourceID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("operator read mutated durable state: %v", err)
	}
	host.closed = true
	if _, err := reader.Source(t.Context(), scope, operatorFailedSourceID); !errors.Is(err, ErrHostClosed) {
		t.Fatalf("closed read: %v", err)
	}
}

func TestOperatorSourcesMissingCapability(t *testing.T) {
	host := &Host{config: Config{Scope: "empty"}}
	reader := newOperatorSourceReader(host)
	page, err := reader.ListSources(t.Context(), "empty", app.OperatorReadOptions{Limit: 100})
	if err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("empty registry: %+v %v", page, err)
	}
	host.sources = []domain.Source{{ID: "configured", Type: domain.SourceTypeFilesystem}}
	host.sourceByID = sourceIndex(host.sources)
	if _, err := reader.Source(t.Context(), "empty", "configured"); !errors.Is(err, app.ErrOperatorCapabilityUnavailable) {
		t.Fatalf("absent status port: %v", err)
	}
	if _, err := reader.ListSources(t.Context(), "empty", app.OperatorReadOptions{Limit: 100}); !errors.Is(err, app.ErrOperatorCapabilityUnavailable) {
		t.Fatalf("absent status list port: %v", err)
	}
}

func assertSourceCanaries(t *testing.T, value any) {
	t.Helper()
	switch value := value.(type) {
	case string:
		for _, canary := range []string{"/private/root-canary", "https://private/uri-canary", "https://remote-canary", "auth-secret-canary", "/private/key-canary", "private-checkpoint-canary"} {
			if value == canary {
				t.Fatalf("private source value serialized: %q", value)
			}
		}
	case map[string]any:
		for _, child := range value {
			assertSourceCanaries(t, child)
		}
	case []any:
		for _, child := range value {
			assertSourceCanaries(t, child)
		}
	}
}
