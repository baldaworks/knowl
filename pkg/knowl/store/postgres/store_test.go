package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/internal/knowledgetest"
	"github.com/baldaworks/knowl/pkg/knowl/internal/runnertest"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/contexttest"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/storetest"
	"github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/pressly/goose/v3"
)

const (
	testSchemaDigest = "schema"
	testPageID       = "one"
)

func TestOpenRequiresDSN(t *testing.T) {
	t.Parallel()
	if _, err := Open(context.Background(), " "); err == nil {
		t.Fatal("Open accepted an empty DSN")
	}
}

func TestSourceStateMigrationAndBindingArePostgresNative(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/00003_source_sync.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"CREATE TABLE knowl_sources", "CREATE TABLE knowl_sync_runs", "CREATE TABLE knowl_source_documents", "TIMESTAMPTZ"} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("source migration missing %q", required)
		}
	}
	if got, want := bindSourceQuery("SELECT id FROM source WHERE scope = ? AND id = ?"), "SELECT id FROM source WHERE scope = $1 AND id = $2"; got != want {
		t.Fatalf("bindSourceQuery() = %q, want %q", got, want)
	}
}

func TestPageSourceMetadataMigrationIsPostgresNative(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/00004_page_source_metadata.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"ADD COLUMN source_id TEXT", "ADD COLUMN source_document JSONB", "WHERE source_id IS NOT NULL"} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("page source metadata migration missing %q", required)
		}
	}
	if strings.Contains(string(content), "source_id TEXT NOT NULL") {
		t.Fatal("page source metadata migration makes curated source_id non-null")
	}
}

func TestOKFProjectionMigrationIsPostgresNative(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/00005_okf_projection.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"ADD COLUMN format TEXT", "ADD COLUMN description TEXT", "ADD COLUMN okf_metadata JSONB", "coalesce(description", "DELETE FROM knowl_projection_state"} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("OKF projection migration missing %q", required)
		}
	}
}

func TestOKFSearchTagsMigrationIsPostgresNative(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/00010_okf_search_tags.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"ADD COLUMN tags TEXT NOT NULL DEFAULT ''",
		"coalesce(title, '')), 'A'",
		"coalesce(tags, '')), 'B'",
		"coalesce(description, '')), 'C'",
		"coalesce(body, '')), 'D'",
		"USING GIN(search_vector)",
		"DELETE FROM knowl_projection_state",
		"DROP COLUMN tags",
	} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("OKF tag migration missing %q", required)
		}
	}
}

func TestOperationRetryMigrationIsAdditive(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/00011_operation_retry.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"failure_reason TEXT NOT NULL DEFAULT ''",
		"retry_attempt INTEGER NOT NULL DEFAULT 0",
		"manual_retry_count INTEGER NOT NULL DEFAULT 0",
	} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("operation retry migration missing %q", required)
		}
	}
}

func TestSourceMaintenanceMigrationIsAdditive(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/00006_source_maintenance.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"ALTER TABLE knowl_sync_candidates ADD COLUMN maintenance_revision",
		"ALTER TABLE knowl_sync_candidates ADD COLUMN maintenance_operation_id",
		"ALTER TABLE knowl_source_documents ADD COLUMN maintenance_revision",
		"ALTER TABLE knowl_source_documents ADD COLUMN maintenance_operation_id",
	} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("source maintenance migration missing %q", required)
		}
	}
	if strings.Contains(string(content), "DROP COLUMN mirror_") {
		t.Fatal("source maintenance migration drops legacy mirror state")
	}
}

func TestOperationSourceDocumentMigrationIsAdditive(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/00007_operation_source_document.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "ALTER TABLE knowl_operations ADD COLUMN accepted_source_document TEXT NOT NULL DEFAULT ''") {
		t.Fatal("operation source-document migration does not add a backward-compatible column")
	}
	for _, forbidden := range []string{"DROP COLUMN accepted_media_type", "DROP COLUMN source_manifest_ref", "DROP TABLE knowl_operations"} {
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("operation source-document migration contains destructive change %q", forbidden)
		}
	}
}

func TestPageSourcesMigrationIsAdditiveAndNormalized(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/00008_page_sources.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"ALTER TABLE knowl_pages ADD COLUMN source_documents JSONB NOT NULL DEFAULT '[]'::jsonb",
		"CREATE TABLE knowl_page_sources",
		"PRIMARY KEY(scope, page_id, source_id, document_id, revision)",
		"ON knowl_page_sources(scope, source_id, page_id)",
	} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("page sources migration missing %q", required)
		}
	}
}

func TestGenericOperationsMigrationIsAdditive(t *testing.T) {
	content, err := migrationFiles.ReadFile("migrations/00009_generic_operations.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"work_kind TEXT NOT NULL DEFAULT 'source'", "execution_payload TEXT NOT NULL DEFAULT ''", "downgrading"} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("generic operation migration missing %q", required)
		}
	}
}

func TestStoreContract(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("KNOWL_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set KNOWL_POSTGRES_DSN to run PostgreSQL integration tests")
	}
	runStoreContract(t, dsn)
}

func runStoreContract(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	scope := knowl.ScopeRef("test_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	storetest.RunWorkContract(t, storetest.WorkHarness{
		Store: store,
		OpenPeer: func(t *testing.T) app.OperationStore {
			t.Helper()
			peer, err := Open(ctx, dsn)
			if err != nil {
				t.Fatalf("open contract peer: %v", err)
			}
			t.Cleanup(func() { _ = peer.Close() })
			return peer
		},
		Expire: func(t *testing.T, _ knowl.ScopeRef, id knowl.OperationID) {
			t.Helper()
			if _, err := store.db.ExecContext(ctx, `UPDATE knowl_operations SET work_lease_expires_at = $1 WHERE operation_id = $2`, time.Unix(1, 0).UTC(), id); err != nil {
				t.Fatalf("expire contract work lease: %v", err)
			}
		},
		ReadyNow: func(t *testing.T, _ knowl.ScopeRef, id knowl.OperationID) {
			t.Helper()
			if _, err := store.db.ExecContext(ctx, `UPDATE knowl_operations SET work_ready_at = $1 WHERE operation_id = $2`, time.Unix(1, 0).UTC(), id); err != nil {
				t.Fatalf("ready contract work: %v", err)
			}
		},
		WorkAttempts: func(t *testing.T, scope knowl.ScopeRef, id knowl.OperationID) int {
			t.Helper()
			var attempts int
			if err := store.db.QueryRowContext(ctx, `SELECT work_attempt FROM knowl_operations WHERE scope = $1 AND operation_id = $2`, scope, id).Scan(&attempts); err != nil {
				t.Fatalf("read contract work attempts: %v", err)
			}
			return attempts
		},
		IsConflict: func(err error) bool { return errors.Is(err, ErrConflict) },
		Scope:      knowl.ScopeRef(string(scope) + "_shared_contract"),
	})
	storetest.RunSourceRetryContract(t, storetest.SourceRetryHarness{
		Store: store,
		Seed: func(t *testing.T, fixtureScope knowl.ScopeRef, sourceID knowl.SourceID, fixtures []storetest.SourceRetryFixture) {
			t.Helper()
			now := time.Now().UTC()
			if _, err := store.db.ExecContext(ctx, `INSERT INTO knowl_sources (scope, source_id, source_type, config_digest, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $5) ON CONFLICT(scope, source_id) DO NOTHING`, fixtureScope, sourceID, knowl.SourceTypeFilesystem, strings.Repeat("a", 64), now); err != nil {
				t.Fatal(err)
			}
			for _, fixture := range fixtures {
				var workExpiry, applyExpiry any
				leaseDelta := time.Hour
				if fixture.LeasesExpired {
					leaseDelta = -time.Hour
				}
				if fixture.WorkToken != "" {
					workExpiry = time.Now().UTC().Add(leaseDelta)
				}
				if fixture.ApplyToken != "" {
					applyExpiry = time.Now().UTC().Add(leaseDelta)
				}
				if _, err := store.db.ExecContext(ctx, `INSERT INTO knowl_operations (
					operation_id, scope, source_adapter, source_id, source_version, source_digest, schema_digest,
					status, plan_digest, failure_class, failure_reason, commit_generation, lease_token, lease_expires_at,
					created_at, updated_at, work_attempt, work_lease_token, work_lease_expires_at, work_ready_at,
					work_kind, retry_attempt, manual_retry_count
				) VALUES ($1, $2, 'fixture', $1, '1', $3, $4, $5, 'planned-digest', $6, $7, 'commit-generation', $8, $9, $10, $10, $11, $12, $13, $10, $14, $15, $16)`,
					fixture.OperationID, fixture.OperationScope, strings.Repeat("b", 64), testSchemaDigest,
					fixture.Status, fixture.FailureClass, fixture.FailureReason, fixture.ApplyToken, applyExpiry, now,
					fixture.WorkAttempt, fixture.WorkToken, workExpiry, fixture.Kind, fixture.RetryAttempt, fixture.ManualRetryCount); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.ExecContext(ctx, `INSERT INTO knowl_source_documents (
					scope, source_id, document_id, revision, accepted_source, maintenance_revision, maintenance_operation_id,
					last_seen_run_id, created_at, updated_at
				) VALUES ($1, $2, $3, $4, '{}', $5, $6, 'retry-run', $7, $7)`,
					fixtureScope, sourceID, fixture.DocumentID, fixture.Revision, fixture.MaintenanceRevision, fixture.OperationID, now); err != nil {
					t.Fatal(err)
				}
			}
		},
		Audit: func(t *testing.T, id knowl.OperationID) storetest.SourceRetryAudit {
			t.Helper()
			var audit storetest.SourceRetryAudit
			if err := store.db.QueryRowContext(ctx, `SELECT plan_digest, commit_generation, work_lease_token, lease_token FROM knowl_operations WHERE operation_id = $1`, id).
				Scan(&audit.PlanDigest, &audit.CommitGeneration, &audit.WorkToken, &audit.ApplyToken); err != nil {
				t.Fatal(err)
			}
			return audit
		},
		Scope: knowl.ScopeRef(string(scope) + "_retry_contract"),
	})
	sourceScope := knowl.ScopeRef(string(scope) + "_source_contract")
	storetest.RunSourceContract(t, storetest.SourceHarness{
		Store: store,
		OpenPeer: func(t *testing.T) app.SourceStateStore {
			t.Helper()
			peer, err := Open(ctx, dsn)
			if err != nil {
				t.Fatalf("open source-state peer: %v", err)
			}
			t.Cleanup(func() { _ = peer.Close() })
			return peer
		},
		IsConflict: func(err error) bool { return errors.Is(err, app.ErrSyncConflict) },
		Scope:      sourceScope,
	})
	var sourceOperationRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowl_operations WHERE scope = $1`, sourceScope).Scan(&sourceOperationRows); err != nil || sourceOperationRows != 0 {
		t.Fatalf("source sync operation rows = %d, %v", sourceOperationRows, err)
	}
	runnertest.Run(t, store, store, knowl.ScopeRef(string(scope)+"_runner"))
	assertResumableMigration(t, ctx, store, dsn)
	assertPostgresHierarchyWork(t, ctx, store, knowl.ScopeRef(string(scope)+"_hierarchy"))
	key, meta := postgresExecutionFixture(scope, "source", time.Unix(1, 0).UTC())
	operation, err := store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatalf("reserve operation: %v", err)
	}
	replayed, err := store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatalf("replay operation: %v", err)
	}
	if replayed.ID != operation.ID || replayed.Status != knowl.StatusReceived {
		t.Fatalf("replayed operation = %#v", replayed)
	}
	conflict := key
	conflict.Version.Digest = strings.Repeat("b", 64)
	if _, err := store.Reserve(ctx, conflict, knowl.OperationMeta{Key: conflict}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error = %v, want conflict", err)
	}
	if replayed.Descriptor.Schema.Digest != meta.Schema.Digest {
		t.Fatalf("replayed descriptor = %#v", replayed.Descriptor)
	}
	descriptor, err := store.Execution(ctx, scope, operation.ID)
	if err != nil || descriptor.Source != meta.AcceptedSource {
		t.Fatalf("execution descriptor = %#v, err = %v", descriptor, err)
	}
	ready, err := store.ResumeReady(ctx, scope, 10)
	if err != nil || len(ready) != 1 || ready[0] != operation.ID {
		t.Fatalf("ready = %v, err = %v", ready, err)
	}
	assertConcurrentPostgresClaim(t, ctx, dsn, scope, operation.ID)
	if err := store.ReleaseClaim(ctx, scope, operation.ID, "worker-0"); err != nil {
		if err := store.ReleaseClaim(ctx, scope, operation.ID, "worker-1"); err != nil {
			t.Fatalf("release concurrent claim: %v", err)
		}
	}
	claim, err := store.ClaimReady(ctx, scope, knowl.WorkLease{Token: "expiry-owner", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("claim expiry fixture: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE knowl_operations SET work_lease_expires_at = $1 WHERE operation_id = $2`, time.Unix(1, 0).UTC(), claim.Operation.ID); err != nil {
		t.Fatalf("expire work lease: %v", err)
	}
	reclaimed, err := store.ClaimReady(ctx, scope, knowl.WorkLease{Token: "reclaimer", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || reclaimed.Operation.ID != operation.ID {
		t.Fatalf("reclaimed = %#v, err = %v", reclaimed, err)
	}
	if err := store.ReleaseClaim(ctx, scope, operation.ID, "reclaimer"); err != nil {
		t.Fatalf("release reclaimed work: %v", err)
	}
	legacyKey := knowl.OperationKey{
		Scope:   scope,
		Source:  knowl.SourceRef{Adapter: "fixture", ID: "legacy"},
		Version: knowl.SourceVersion{Version: "1", Digest: strings.Repeat("c", 64)},
	}
	legacy, err := store.Reserve(ctx, legacyKey, knowl.OperationMeta{Key: legacyKey, SchemaDigest: "historical-schema"})
	if err != nil {
		t.Fatalf("reserve legacy operation: %v", err)
	}
	if _, err := store.Execution(ctx, scope, legacy.ID); !errors.Is(err, app.ErrExecutionDescriptorUnavailable) {
		t.Fatalf("legacy descriptor error = %v", err)
	}
	failures, err := store.DescriptorFailures(ctx, scope, 10)
	if err != nil || len(failures) != 1 || failures[0] != legacy.ID {
		t.Fatalf("descriptor failures = %v, err = %v", failures, err)
	}

	if err := store.SavePlan(ctx, operation.ID, knowl.PlanSummary{Digest: "plan"}); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := store.MarkAwaitingReview(ctx, operation.ID); err != nil {
		t.Fatalf("mark review: %v", err)
	}
	if err := store.MarkApplying(ctx, operation.ID, knowl.Lease{Token: "lease-1", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatalf("mark applying: %v", err)
	}
	if err := store.MarkApplying(ctx, operation.ID, knowl.Lease{Token: "lease-2", ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("lease conflict = %v, want lease conflict", err)
	}
	if err := store.CommitOutcome(ctx, operation.ID, knowl.ContentCommit{OperationID: string(operation.ID), Generation: "generation"}); err != nil {
		t.Fatalf("commit operation: %v", err)
	}

	sourceDocument := &knowl.SourceDocument{
		SourceID: "engineering", DocumentID: "docs/one.md", Revision: "revision-1", URI: "file:///source/docs/one.md",
	}
	snapshot := knowl.WorkspaceSnapshot{
		Scope: scope, SchemaDigest: testSchemaDigest, CapturedAt: time.Unix(1, 0).UTC(),
		Pages: []knowl.PageSnapshot{{
			ID: testPageID, Path: "wiki/entities/one.md", Title: "One",
			Content: "alpha knowledge", Digest: "digest",
			SourceRefs: []string{"fixture:source@1"}, SourceDocument: sourceDocument,
		}},
		Links: []knowl.LinkReference{{From: testPageID, To: "two", Relation: "related"}},
	}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("rebuild projections: %v", err)
	}
	results, err := store.Search(ctx, scope, "alpha", knowl.ReadLimits{Pages: 5, Characters: 20}, nil)
	if err != nil {
		t.Fatalf("search projection: %v", err)
	}
	if len(results) != 1 || results[0].ID != testPageID || !results[0].Untrusted || results[0].SourceDocument == nil || *results[0].SourceDocument != *sourceDocument {
		t.Fatalf("search results = %#v", results)
	}
	links, err := store.Links(ctx, scope, testPageID, knowl.ReadLimits{Pages: 5})
	if err != nil {
		t.Fatalf("links projection: %v", err)
	}
	if len(links) != 1 || !links[0].Untrusted {
		t.Fatalf("links = %#v", links)
	}
	if err := store.CheckProjection(ctx, snapshot); err != nil {
		t.Fatalf("check projection: %v", err)
	}
	drifted := snapshot
	drifted.Pages[0].Content = "changed canonical text"
	if err := store.CheckProjection(ctx, drifted); !errors.Is(err, ErrProjectionDrift) {
		t.Fatalf("drift error = %v, want projection drift", err)
	}
	assertOKFTagSearch(t, ctx, store, knowl.ScopeRef(string(scope)+"_tags"))
	searchtest.Run(t, store, func(err error) bool { return errors.Is(err, ErrInvalidQuery) })
	contexttest.Run(t, store)
	metrics, err := knowledgetest.EvaluateProjectionReplay(ctx, store, knowl.ScopeRef(string(scope)+"_golden"))
	if err != nil {
		t.Fatalf("evaluate golden projection replay: %v", err)
	}
	if metrics.Total != knowledgetest.QueryCount || metrics.Hits < knowledgetest.MinimumHits {
		t.Fatalf("golden metrics = %#v", metrics)
	}
}

func assertOKFTagSearch(t *testing.T, ctx context.Context, store *Store, scope knowl.ScopeRef) {
	t.Helper()
	const term = "prioritybeacon"
	page := func(id, title, tags, description, body string) knowl.PageSnapshot {
		return knowl.PageSnapshot{
			ID: knowl.PageID(id), Path: "wiki/" + id + ".md", Title: title, Body: body, Content: body,
			Digest: "digest-" + id, SourceRefs: []string{"raw:" + id + "@1"},
			OKF: &okf.Metadata{Type: "Reference", Title: title, Tags: []string{tags}, Description: description},
		}
	}
	snapshot := knowl.WorkspaceSnapshot{Scope: scope, SchemaDigest: "schema", Pages: []knowl.PageSnapshot{
		page("title", term, "other", "neutral summary", "neutral body"),
		page("tags", "Tag page", term, "neutral summary", "neutral body"),
		page("description", "Description page", "other", term, "neutral body"),
		page("body", "Body page", "other", "neutral summary", term),
	}}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	results, err := store.Search(ctx, scope, term, knowl.ReadLimits{Pages: 10, Characters: 64}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []knowl.PageID{"title", "tags", "description", "body"}
	if len(results) != len(want) {
		t.Fatalf("tag search results = %#v", results)
	}
	for index, id := range want {
		if results[index].ID != id {
			t.Fatalf("tag search order = %#v, want %q", results, want)
		}
	}
	if !strings.Contains(results[1].Snippet, "tag: "+term) || results[1].OKF == nil || results[1].OKF.Tags[0] != term {
		t.Fatalf("tag evidence = %#v", results[1])
	}
}

func assertResumableMigration(t *testing.T, ctx context.Context, root *Store, dsn string) {
	t.Helper()
	schema := fmt.Sprintf("knowl_migration_%d", time.Now().UTC().UnixNano())
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if _, err := root.db.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create migration schema: %v", err)
	}
	t.Cleanup(func() { _, _ = root.db.ExecContext(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE") })
	migrationDSN, err := dsnWithSearchPath(dsn, schema)
	if err != nil {
		t.Fatalf("build migration DSN: %v", err)
	}
	db, err := sql.Open("pgx", migrationDSN)
	if err != nil {
		t.Fatalf("open version-one fixture: %v", err)
	}
	directory, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("open migration fixtures: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, directory)
	if err != nil {
		t.Fatalf("create version-one provider: %v", err)
	}
	if _, err := provider.UpTo(ctx, 2); err != nil {
		t.Fatalf("migrate fixture to version two: %v", err)
	}
	const (
		scope       = "postgres_migration"
		committedID = "postgres_migration:fixture:committed@1#aaaaaaaaaaaaaaaa"
		applyingID  = "postgres_migration:fixture:applying@1#bbbbbbbbbbbbbbbb"
		failedID    = "postgres_migration:fixture:failed@1#cccccccccccccccc"
	)
	createdAt := time.Unix(10, 0).UTC()
	leaseExpiry := time.Unix(100, 0).UTC()
	insert := `INSERT INTO knowl_operations (
		operation_id, scope, source_adapter, source_id, source_version, source_digest,
		schema_digest, status, attempt, plan_digest, failure_class, commit_generation,
		lease_token, lease_expires_at, created_at, updated_at, work_ready_at
	) VALUES ($1, $2, 'fixture', $3, '1', $4, 'schema-v1', $5, $6, $7, $8, $9, $10, $11, $12, $12, $12)`
	if _, err := db.ExecContext(ctx, insert, committedID, scope, "committed", strings.Repeat("a", 64), knowl.StatusCommitted, 2, "plan-committed", "", "generation-1", "", nil, createdAt); err != nil {
		t.Fatalf("insert committed version-one row: %v", err)
	}
	if _, err := db.ExecContext(ctx, insert, applyingID, scope, "applying", strings.Repeat("b", 64), knowl.StatusApplying, 3, "plan-applying", "", "", "apply-owner", leaseExpiry, createdAt); err != nil {
		t.Fatalf("insert applying version-one row: %v", err)
	}
	if _, err := db.ExecContext(ctx, insert, failedID, scope, "failed", strings.Repeat("c", 64), knowl.StatusFailed, 1, "plan-failed", "provider", "", "", nil, createdAt); err != nil {
		t.Fatalf("insert failed version-one row: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowl_pages (scope, page_id, path, title, body, digest, source_refs, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8)`, scope, "legacy-page", "wiki/legacy.md", "Legacy page", "preserved projection body", "page-digest", `["raw/legacy.json"]`, createdAt); err != nil {
		t.Fatalf("insert version-two projection page: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowl_projection_state (scope, schema_digest, snapshot_digest, page_count, link_count, ready_at) VALUES ($1, $2, $3, $4, $5, $6)`, scope, "schema-v1", "snapshot-v2", 1, 0, createdAt); err != nil {
		t.Fatalf("insert version-two projection state: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version-one fixture: %v", err)
	}

	migrated, err := Open(ctx, migrationDSN)
	if err != nil {
		t.Fatalf("apply resumable migration: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	committed, err := migrated.Operation(ctx, scope, committedID)
	if err != nil || committed.Status != knowl.StatusCommitted || committed.Attempt != 2 {
		t.Fatalf("migrated committed operation = %#v, err = %v", committed, err)
	}
	applying, err := migrated.Operation(ctx, scope, applyingID)
	if err != nil || applying.Status != knowl.StatusApplying || applying.Attempt != 3 {
		t.Fatalf("migrated applying operation = %#v, err = %v", applying, err)
	}
	failed, err := migrated.Operation(ctx, scope, failedID)
	if err != nil || failed.Status != knowl.StatusFailed || failed.Failure == nil || failed.Failure.Class != "provider" {
		t.Fatalf("migrated failed operation = %#v, err = %v", failed, err)
	}
	var planDigest, generation, leaseToken string
	var migratedLeaseExpiry, workReadyAt time.Time
	if err := migrated.db.QueryRowContext(ctx, `SELECT plan_digest, commit_generation, lease_token, lease_expires_at, work_ready_at FROM knowl_operations WHERE operation_id = $1`, applyingID).Scan(
		&planDigest, &generation, &leaseToken, &migratedLeaseExpiry, &workReadyAt,
	); err != nil {
		t.Fatalf("read preserved applying fields: %v", err)
	}
	if planDigest != "plan-applying" || generation != "" || leaseToken != "apply-owner" || !migratedLeaseExpiry.Equal(leaseExpiry) || !workReadyAt.Equal(createdAt) {
		t.Fatalf("preserved applying fields = %q %q %q %s %s", planDigest, generation, leaseToken, migratedLeaseExpiry, workReadyAt)
	}
	var committedGeneration string
	if err := migrated.db.QueryRowContext(ctx, `SELECT commit_generation FROM knowl_operations WHERE operation_id = $1`, committedID).Scan(&committedGeneration); err != nil {
		t.Fatalf("read preserved commit generation: %v", err)
	}
	if committedGeneration != "generation-1" {
		t.Fatalf("preserved commit generation = %q", committedGeneration)
	}
	var pageTitle, pageBody, pageDigest, pageFormat, pageTags, pageDescription string
	var sourceRefs, sourceDocument, metadata []byte
	var sourceID sql.NullString
	if err := migrated.db.QueryRowContext(ctx, `SELECT title, body, digest, source_refs, source_id, source_document, format, tags, description, okf_metadata FROM knowl_pages WHERE scope = $1 AND page_id = $2`, scope, "legacy-page").Scan(&pageTitle, &pageBody, &pageDigest, &sourceRefs, &sourceID, &sourceDocument, &pageFormat, &pageTags, &pageDescription, &metadata); err != nil {
		t.Fatalf("read preserved projection page: %v", err)
	}
	if pageTitle != "Legacy page" || pageBody != "preserved projection body" || pageDigest != "page-digest" || string(sourceRefs) != `["raw/legacy.json"]` || sourceID.Valid || len(sourceDocument) != 0 || pageFormat != "" || pageTags != "" || pageDescription != "" || len(metadata) != 0 {
		t.Fatalf("preserved projection page = %q %q %q %s %#v %s %q %q %q %s", pageTitle, pageBody, pageDigest, sourceRefs, sourceID, sourceDocument, pageFormat, pageTags, pageDescription, metadata)
	}
	var snapshotDigest string
	var pageCount, linkCount int
	if err := migrated.db.QueryRowContext(ctx, `SELECT snapshot_digest, page_count, link_count FROM knowl_projection_state WHERE scope = $1`, scope).Scan(&snapshotDigest, &pageCount, &linkCount); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("projection readiness after metadata migration = %v, want invalidated", err)
	}
	failures, err := migrated.DescriptorFailures(ctx, scope, 10)
	if err != nil || len(failures) != 1 || failures[0] != applyingID {
		t.Fatalf("migrated descriptor failures = %v, err = %v", failures, err)
	}
	ready, err := migrated.ResumeReady(ctx, scope, 10)
	if err != nil || len(ready) != 0 {
		t.Fatalf("migrated ready operations = %v, err = %v", ready, err)
	}
}

func dsnWithSearchPath(dsn, schema string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}
	if strings.ContainsAny(schema, " \\t\\r\\n'") {
		return "", fmt.Errorf("invalid postgres schema name")
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema, nil
}

func assertConcurrentPostgresClaim(t *testing.T, ctx context.Context, dsn string, scope knowl.ScopeRef, wantID knowl.OperationID) {
	t.Helper()
	stores := make([]*Store, 2)
	for index := range stores {
		store, err := Open(ctx, dsn)
		if err != nil {
			t.Fatalf("open concurrent store: %v", err)
		}
		stores[index] = store
		t.Cleanup(func() { _ = store.Close() })
	}
	start := make(chan struct{})
	results := make(chan error, len(stores))
	claims := make(chan knowl.WorkClaim, len(stores))
	var wait sync.WaitGroup
	for index, store := range stores {
		wait.Add(1)
		go func(index int, store *Store) {
			defer wait.Done()
			<-start
			claim, err := store.ClaimReady(ctx, scope, knowl.WorkLease{
				Token: fmt.Sprintf("worker-%d", index), ExpiresAt: time.Now().Add(time.Minute),
			})
			if err == nil {
				claims <- claim
			}
			results <- err
		}(index, store)
	}
	close(start)
	wait.Wait()
	close(results)
	close(claims)
	var successes, empty int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, app.ErrNoReadyOperation):
			empty++
		default:
			t.Fatalf("concurrent claim error = %v", err)
		}
	}
	if successes != 1 || empty != 1 {
		t.Fatalf("concurrent claims: successes=%d empty=%d", successes, empty)
	}
	claim := <-claims
	if claim.Operation.ID != wantID {
		t.Fatalf("claimed %q, want %q", claim.Operation.ID, wantID)
	}
}

func assertPostgresHierarchyWork(t *testing.T, ctx context.Context, store *Store, scope knowl.ScopeRef) {
	t.Helper()
	identity, descriptor := postgresHierarchyExecutionFixture(t, scope, "planner-v1", strings.Repeat("b", 64))
	first, err := store.ReserveOperation(ctx, identity, descriptor)
	if err != nil {
		t.Fatalf("reserve hierarchy operation: %v", err)
	}
	replayed, err := store.ReserveOperation(ctx, identity, descriptor)
	if err != nil || replayed.New || replayed.ID != first.ID || replayed.Kind != knowl.WorkHierarchy {
		t.Fatalf("hierarchy replay = %#v, %v", replayed, err)
	}
	secondIdentity, secondDescriptor := postgresHierarchyExecutionFixture(t, scope, "planner-v1", strings.Repeat("c", 64))
	if _, err := store.ReserveOperation(ctx, secondIdentity, secondDescriptor); err != nil {
		t.Fatalf("reserve second hierarchy operation: %v", err)
	}
	ready, err := store.ResumeReady(ctx, scope, 10)
	if err != nil || len(ready) != 2 {
		t.Fatalf("hierarchy ready = %v, %v", ready, err)
	}
	claim, err := store.ClaimReady(ctx, scope, knowl.WorkLease{Token: "hierarchy-worker", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || claim.Descriptor.Kind != knowl.WorkHierarchy || claim.Descriptor.Hierarchy == nil || claim.Operation.Kind != knowl.WorkHierarchy {
		t.Fatalf("hierarchy claim = %#v, %v", claim, err)
	}
	if err := store.ReleaseClaim(ctx, scope, claim.Operation.ID, claim.Lease.Token); err != nil {
		t.Fatalf("release hierarchy claim: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE knowl_operations SET execution_payload = $1 WHERE operation_id = $2`, `{"version":2}`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Execution(ctx, scope, first.ID); !errors.Is(err, app.ErrExecutionDescriptorUnavailable) {
		t.Fatalf("corrupt hierarchy descriptor error = %v", err)
	}
	failures, err := store.DescriptorFailures(ctx, scope, 10)
	if err != nil || !slices.Contains(failures, first.ID) {
		t.Fatalf("hierarchy descriptor failures = %v, %v", failures, err)
	}
}

func postgresHierarchyExecutionFixture(t *testing.T, scope knowl.ScopeRef, planner, snapshot string) (knowl.OperationIdentity, knowl.ExecutionDescriptor) {
	t.Helper()
	schema := []byte("# Hierarchy schema\n")
	schemaDigest := fmt.Sprintf("%x", sha256.Sum256(schema))
	identity := knowl.OperationIdentity{Scope: scope, Kind: knowl.WorkHierarchy, Subject: planner, Revision: schemaDigest, Digest: snapshot}
	id, err := app.OperationIDForIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	return identity, knowl.ExecutionDescriptor{
		OperationID: id, Kind: knowl.WorkHierarchy,
		Hierarchy: &knowl.HierarchyExecutionDescriptor{SnapshotDigest: snapshot, PlannerVersion: planner},
		Schema:    knowl.SchemaDocument{Scope: scope, Digest: schemaDigest, Version: "1", Content: schema},
	}
}

func postgresExecutionFixture(scope knowl.ScopeRef, id string, createdAt time.Time) (knowl.OperationKey, knowl.OperationMeta) {
	schema := []byte("# Schema\n\nversion: 1\n")
	digest := fmt.Sprintf("%x", sha256.Sum256(schema))
	key := knowl.OperationKey{
		Scope:   scope,
		Source:  knowl.SourceRef{Adapter: "fixture", ID: id},
		Version: knowl.SourceVersion{Version: "1", Digest: strings.Repeat("a", 64)},
	}
	return key, knowl.OperationMeta{
		Key: key,
		AcceptedSource: knowl.AcceptedSource{
			Scope: scope, Source: key.Source, Version: key.Version,
			MediaType: "text/markdown",
			SourceDocument: knowl.SourceDocument{
				SourceID: "configured-wiki", DocumentID: knowl.DocumentID(id + ".md"), Revision: "1",
				URI: "file:///srv/wiki/" + id + ".md",
			},
			ManifestRef: "raw/source/version/manifest.yaml",
		},
		Schema:       knowl.SchemaDocument{Scope: scope, Digest: digest, Version: "1", Content: schema},
		SchemaDigest: digest,
		CreatedAt:    createdAt,
	}
}
