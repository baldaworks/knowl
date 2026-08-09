package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl"
)

const (
	testLocalScope   = "local"
	testSchemaDigest = "schema"
	testSharedPageID = "shared"
)

func TestStoreMigratesAndReservesIdempotently(t *testing.T) {
	const pageID = "one"
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/knowl.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	key := knowl.OperationKey{Scope: testLocalScope, Source: knowl.SourceRef{Adapter: "fixture", ID: pageID}, Version: knowl.SourceVersion{Version: "1", Digest: "digest-a"}}
	meta := knowl.OperationMeta{Key: key, SchemaDigest: testSchemaDigest, CreatedAt: time.Unix(1, 0).UTC()}
	first, err := store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatalf("reserve operation: %v", err)
	}
	second, err := store.Reserve(ctx, key, meta)
	if err != nil {
		t.Fatalf("replay operation: %v", err)
	}
	if first.ID != second.ID || first.Status != knowl.StatusReceived {
		t.Fatalf("replay operation changed: %#v %#v", first, second)
	}
	conflict := key
	conflict.Version.Digest = "digest-b"
	if _, err := store.Reserve(ctx, conflict, meta); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error = %v, want conflict", err)
	}
}

func TestStorePersistsOperationTransitionsAndReopens(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/knowl.sqlite"
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	key := knowl.OperationKey{Scope: testLocalScope, Source: knowl.SourceRef{Adapter: "fixture", ID: "transition"}, Version: knowl.SourceVersion{Version: "1", Digest: "digest"}}
	operation, err := store.Reserve(ctx, key, knowl.OperationMeta{Key: key, SchemaDigest: testSchemaDigest})
	if err != nil {
		t.Fatalf("reserve operation: %v", err)
	}
	if err := store.SavePlan(ctx, operation.ID, knowl.PlanSummary{OperationID: string(operation.ID), Digest: "plan"}); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := store.MarkAwaitingReview(ctx, operation.ID); err != nil {
		t.Fatalf("mark review: %v", err)
	}
	lease := knowl.Lease{Token: "lease-1", ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.MarkApplying(ctx, operation.ID, lease); err != nil {
		t.Fatalf("mark applying: %v", err)
	}
	if err := store.MarkApplying(ctx, operation.ID, knowl.Lease{Token: "lease-2", ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("active lease error = %v, want lease conflict", err)
	}
	if err := store.CommitOutcome(ctx, operation.ID, knowl.ContentCommit{OperationID: string(operation.ID), Generation: "generation"}); err != nil {
		t.Fatalf("commit operation: %v", err)
	}
	if err := store.CommitOutcome(ctx, operation.ID, knowl.ContentCommit{OperationID: string(operation.ID), Generation: "generation"}); err != nil {
		t.Fatalf("replay commit: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = store.Close() }()
	reopened, err := store.Operation(ctx, key.Scope, operation.ID)
	if err != nil {
		t.Fatalf("read reopened operation: %v", err)
	}
	if reopened.Status != knowl.StatusCommitted || reopened.Attempt != 1 {
		t.Fatalf("reopened operation = %#v", reopened)
	}
}

func TestStoreRebuildsAndSearchesCanonicalSnapshot(t *testing.T) {
	const pageID = "one"
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/knowl.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	snapshot := knowl.WorkspaceSnapshot{
		Scope:      "local",
		CapturedAt: time.Unix(1, 0).UTC(),
		Pages:      []knowl.PageSnapshot{{ID: pageID, Path: "wiki/entities/one.md", Title: "One", Content: "alpha knowledge", Digest: "digest", SourceRefs: []string{"fixture:one@1"}}},
		Links:      []knowl.LinkReference{{From: pageID, To: "two", Relation: "related"}},
	}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("rebuild projections: %v", err)
	}
	results, err := store.Search(ctx, "local", "alpha", knowl.ReadLimits{Pages: 5, Characters: 20})
	if err != nil {
		t.Fatalf("search projection: %v", err)
	}
	if len(results) != 1 || !results[0].Untrusted || results[0].ID != pageID {
		t.Fatalf("search results = %#v", results)
	}
	links, err := store.Links(ctx, "local", pageID, knowl.ReadLimits{Pages: 5})
	if err != nil {
		t.Fatalf("links projection: %v", err)
	}
	if len(links) != 1 || !links[0].Untrusted {
		t.Fatalf("links = %#v", links)
	}
}

func TestStoreRebuildIsScopedAndDetectsDrift(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/knowl.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	snapshot := knowl.WorkspaceSnapshot{
		Scope:        testLocalScope,
		SchemaDigest: testSchemaDigest,
		Pages:        []knowl.PageSnapshot{{ID: testSharedPageID, Path: "wiki/shared.md", Title: "Shared", Content: "local alpha", Digest: "digest-local"}},
		Links:        []knowl.LinkReference{{From: testSharedPageID, To: "other", Relation: "related"}},
	}
	other := knowl.WorkspaceSnapshot{
		Scope:        "other",
		SchemaDigest: "schema",
		Pages:        []knowl.PageSnapshot{{ID: testSharedPageID, Path: "wiki/shared.md", Title: "Shared", Content: "other beta", Digest: "digest-other"}},
	}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("rebuild local projection: %v", err)
	}
	if err := store.Rebuild(ctx, other); err != nil {
		t.Fatalf("rebuild other projection: %v", err)
	}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("repeat local rebuild: %v", err)
	}
	results, err := store.Search(ctx, "local", "alpha", knowl.ReadLimits{Pages: 5})
	if err != nil {
		t.Fatalf("search local projection: %v", err)
	}
	if len(results) != 1 || results[0].ID != testSharedPageID {
		t.Fatalf("local search results = %#v", results)
	}
	otherResults, err := store.Search(ctx, "other", "beta", knowl.ReadLimits{Pages: 5})
	if err != nil {
		t.Fatalf("search other projection: %v", err)
	}
	if len(otherResults) != 1 || otherResults[0].ID != testSharedPageID {
		t.Fatalf("other search results = %#v", otherResults)
	}
	state, err := store.ProjectionStatus(ctx, "local")
	if err != nil {
		t.Fatalf("projection status: %v", err)
	}
	if state.PageCount != 1 || state.LinkCount != 1 {
		t.Fatalf("projection state = %#v", state)
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

func TestStoreProjectsEmptyCanonicalScope(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir()+"/knowl.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	snapshot := knowl.WorkspaceSnapshot{Scope: "empty", SchemaDigest: "schema"}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("rebuild empty scope: %v", err)
	}
	if _, err := store.ProjectionStatus(ctx, snapshot.Scope); err != nil {
		t.Fatalf("empty scope readiness: %v", err)
	}
}
