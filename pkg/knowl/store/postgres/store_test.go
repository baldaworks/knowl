package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/types"
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
	key := knowl.OperationKey{
		Scope:   scope,
		Source:  knowl.SourceRef{Adapter: "fixture", ID: "source"},
		Version: knowl.SourceVersion{Version: "1", Digest: "digest-a"},
	}
	operation, err := store.Reserve(ctx, key, knowl.OperationMeta{Key: key, SchemaDigest: testSchemaDigest})
	if err != nil {
		t.Fatalf("reserve operation: %v", err)
	}
	replayed, err := store.Reserve(ctx, key, knowl.OperationMeta{Key: key, SchemaDigest: testSchemaDigest})
	if err != nil {
		t.Fatalf("replay operation: %v", err)
	}
	if replayed.ID != operation.ID || replayed.Status != knowl.StatusReceived {
		t.Fatalf("replayed operation = %#v", replayed)
	}
	conflict := key
	conflict.Version.Digest = "digest-b"
	if _, err := store.Reserve(ctx, conflict, knowl.OperationMeta{Key: conflict}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error = %v, want conflict", err)
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

	snapshot := knowl.WorkspaceSnapshot{
		Scope: scope, SchemaDigest: testSchemaDigest, CapturedAt: time.Unix(1, 0).UTC(),
		Pages: []knowl.PageSnapshot{{
			ID: testPageID, Path: "wiki/entities/one.md", Title: "One",
			Content: "alpha knowledge", Digest: "digest",
			SourceRefs: []string{"fixture:source@1"},
		}},
		Links: []knowl.LinkReference{{From: testPageID, To: "two", Relation: "related"}},
	}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("rebuild projections: %v", err)
	}
	results, err := store.Search(ctx, scope, "alpha", knowl.ReadLimits{Pages: 5, Characters: 20})
	if err != nil {
		t.Fatalf("search projection: %v", err)
	}
	if len(results) != 1 || results[0].ID != testPageID || !results[0].Untrusted {
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
}
