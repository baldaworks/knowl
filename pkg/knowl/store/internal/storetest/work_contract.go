// Package storetest provides shared behavioral contracts for operational stores.
package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// WorkHarness supplies engine-specific lifecycle and fault controls to the
// shared resumable-work contract.
type WorkHarness struct {
	Store        app.OperationStore
	OpenPeer     func(t *testing.T) app.OperationStore
	Expire       func(t *testing.T, scope knowl.ScopeRef, id knowl.OperationID)
	WorkAttempts func(t *testing.T, scope knowl.ScopeRef, id knowl.OperationID) int
	IsConflict   func(error) bool
	Scope        knowl.ScopeRef
}

// RunWorkContract verifies the observable resumable-work behavior shared by
// SQLite and PostgreSQL without depending on either engine's SQL details.
func RunWorkContract(t *testing.T, harness WorkHarness) {
	t.Helper()
	if harness.Store == nil || harness.OpenPeer == nil || harness.Expire == nil || harness.WorkAttempts == nil || harness.IsConflict == nil || harness.Scope == "" {
		t.Fatal("resumable-work harness is incomplete")
	}

	t.Run("reservation_replay_durability_and_redaction", func(t *testing.T) {
		ctx := context.Background()
		scope := childScope(harness.Scope, "reservation")
		key, meta := Fixture(scope, "decision", time.Unix(10, 0).UTC())
		first, err := harness.Store.Reserve(ctx, key, meta)
		if err != nil {
			t.Fatalf("reserve: %v", err)
		}
		wantID := knowl.OperationID(fmt.Sprintf("%s:fixture:decision@1#%s", scope, key.Version.Digest[:16]))
		if !first.New || first.ID != wantID || first.Descriptor.OperationID != first.ID {
			t.Fatalf("first reservation = %#v, want ID %q", first, wantID)
		}
		replay, err := harness.Store.Reserve(ctx, key, meta)
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		if replay.New || replay.ID != first.ID || !reflect.DeepEqual(replay.Descriptor, first.Descriptor) {
			t.Fatalf("replayed reservation = %#v, first = %#v", replay, first)
		}

		peer := harness.OpenPeer(t)
		descriptor, err := peer.Execution(ctx, scope, first.ID)
		if err != nil {
			t.Fatalf("read descriptor from independent handle: %v", err)
		}
		if !reflect.DeepEqual(descriptor, first.Descriptor) {
			t.Fatalf("durable descriptor = %#v, want %#v", descriptor, first.Descriptor)
		}
		operation, err := peer.Operation(ctx, scope, first.ID)
		if err != nil {
			t.Fatalf("read public operation: %v", err)
		}
		encoded, err := json.Marshal(operation)
		if err != nil {
			t.Fatalf("marshal public operation: %v", err)
		}
		for _, forbidden := range []string{"schema", "descriptor", "manifest_ref", "lease", "token"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("public operation %q contains %q", encoded, forbidden)
			}
		}

		conflictKey := key
		conflictKey.Version.Digest = strings.Repeat("b", 64)
		_, conflictMeta := Fixture(scope, "decision", meta.CreatedAt)
		conflictMeta.Key = conflictKey
		conflictMeta.AcceptedSource.Version = conflictKey.Version
		if _, err := harness.Store.Reserve(ctx, conflictKey, conflictMeta); !harness.IsConflict(err) {
			t.Fatalf("conflicting immutable revision error = %v", err)
		}
		afterConflict, err := harness.Store.Execution(ctx, scope, first.ID)
		if err != nil || !reflect.DeepEqual(afterConflict, first.Descriptor) {
			t.Fatalf("descriptor after conflict = %#v, err = %v", afterConflict, err)
		}
	})

	t.Run("equal_readiness_tie_break_limit_and_non_mutation", func(t *testing.T) {
		ctx := context.Background()
		scope := childScope(harness.Scope, "ties")
		createdAt := time.Unix(10, 0).UTC()
		zetaKey, zetaMeta := Fixture(scope, "zeta", createdAt)
		zeta, err := harness.Store.Reserve(ctx, zetaKey, zetaMeta)
		if err != nil {
			t.Fatalf("reserve zeta: %v", err)
		}
		alphaKey, alphaMeta := Fixture(scope, "alpha", createdAt)
		alpha, err := harness.Store.Reserve(ctx, alphaKey, alphaMeta)
		if err != nil {
			t.Fatalf("reserve alpha: %v", err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			ready, err := harness.Store.ResumeReady(ctx, scope, 1)
			if err != nil || len(ready) != 1 || ready[0] != alpha.ID {
				t.Fatalf("ready inspection %d = %v, err = %v", attempt, ready, err)
			}
		}
		if attempts := harness.WorkAttempts(t, scope, alpha.ID); attempts != 0 {
			t.Fatalf("ready inspection mutated work attempts to %d", attempts)
		}
		claim, err := harness.Store.ClaimReady(ctx, scope, futureLease("tie-owner"))
		if err != nil || claim.Operation.ID != alpha.ID {
			t.Fatalf("tie-broken claim = %#v, err = %v", claim, err)
		}
		if attempts := harness.WorkAttempts(t, scope, alpha.ID); attempts != 1 {
			t.Fatalf("first claim work attempts = %d, want 1", attempts)
		}
		ready, err := harness.Store.ResumeReady(ctx, scope, 10)
		if err != nil || len(ready) != 1 || ready[0] != zeta.ID {
			t.Fatalf("ready after tie claim = %v, err = %v", ready, err)
		}
	})

	t.Run("descriptor_validation_and_legacy_classification", func(t *testing.T) {
		ctx := context.Background()
		scope := childScope(harness.Scope, "validation")
		key, meta := Fixture(scope, "oversized", time.Unix(10, 0).UTC())
		meta.Schema.Content = make([]byte, (4<<20)+1)
		meta.Schema.Digest = digest(meta.Schema.Content)
		meta.SchemaDigest = meta.Schema.Digest
		if _, err := harness.Store.Reserve(ctx, key, meta); !errors.Is(err, app.ErrExecutionDescriptorUnavailable) {
			t.Fatalf("oversized descriptor error = %v", err)
		}
		oversizedID := knowl.OperationID(fmt.Sprintf("%s:fixture:oversized@1#%s", scope, key.Version.Digest[:16]))
		if _, err := harness.Store.Operation(ctx, scope, oversizedID); !errors.Is(err, app.ErrOperationNotFound) {
			t.Fatalf("partial oversized operation error = %v", err)
		}

		legacyKey, _ := Fixture(scope, "legacy", time.Unix(20, 0).UTC())
		legacy, err := harness.Store.Reserve(ctx, legacyKey, knowl.OperationMeta{
			Key: legacyKey, SchemaDigest: "historical-schema", CreatedAt: time.Unix(20, 0).UTC(),
		})
		if err != nil {
			t.Fatalf("reserve legacy row: %v", err)
		}
		if _, err := harness.Store.Execution(ctx, scope, legacy.ID); !errors.Is(err, app.ErrExecutionDescriptorUnavailable) {
			t.Fatalf("legacy execution error = %v", err)
		}
		failures, err := harness.Store.DescriptorFailures(ctx, scope, 10)
		if err != nil || len(failures) != 1 || failures[0] != legacy.ID {
			t.Fatalf("descriptor failures = %v, err = %v", failures, err)
		}
		ready, err := harness.Store.ResumeReady(ctx, scope, 10)
		if err != nil || len(ready) != 0 {
			t.Fatalf("legacy ready set = %v, err = %v", ready, err)
		}
	})

	t.Run("deterministic_readiness_renew_release_and_expiry", func(t *testing.T) {
		ctx := context.Background()
		scope := childScope(harness.Scope, "leases")
		laterKey, laterMeta := Fixture(scope, "later", time.Unix(20, 0).UTC())
		later, err := harness.Store.Reserve(ctx, laterKey, laterMeta)
		if err != nil {
			t.Fatalf("reserve later work: %v", err)
		}
		earlierKey, earlierMeta := Fixture(scope, "earlier", time.Unix(10, 0).UTC())
		earlier, err := harness.Store.Reserve(ctx, earlierKey, earlierMeta)
		if err != nil {
			t.Fatalf("reserve earlier work: %v", err)
		}
		ready, err := harness.Store.ResumeReady(ctx, scope, 10)
		if err != nil || len(ready) != 2 || ready[0] != earlier.ID || ready[1] != later.ID {
			t.Fatalf("ordered ready set = %v, err = %v", ready, err)
		}

		firstLease := futureLease("worker-first")
		claim, err := harness.Store.ClaimReady(ctx, scope, firstLease)
		if err != nil || claim.Operation.ID != earlier.ID || claim.Descriptor.OperationID != earlier.ID {
			t.Fatalf("first claim = %#v, err = %v", claim, err)
		}
		if attempts := harness.WorkAttempts(t, scope, earlier.ID); attempts != 1 {
			t.Fatalf("first claim work attempts = %d, want 1", attempts)
		}
		if err := harness.Store.RenewClaim(ctx, scope, earlier.ID, "wrong-token", futureLease("wrong")); !errors.Is(err, app.ErrWorkLeaseConflict) {
			t.Fatalf("wrong-token renewal error = %v", err)
		}
		renewed := futureLease("worker-renewed")
		if err := harness.Store.RenewClaim(ctx, scope, earlier.ID, firstLease.Token, renewed); err != nil {
			t.Fatalf("renew claim: %v", err)
		}
		if err := harness.Store.ReleaseClaim(ctx, scope, earlier.ID, renewed.Token); err != nil {
			t.Fatalf("release claim: %v", err)
		}
		claim, err = harness.Store.ClaimReady(ctx, scope, futureLease("expiry-owner"))
		if err != nil {
			t.Fatalf("claim expiry fixture = %#v, err = %v", claim, err)
		}
		expiringID := claim.Operation.ID
		harness.Expire(t, scope, expiringID)
		reclaimed, err := harness.Store.ClaimReady(ctx, scope, futureLease("reclaimer"))
		if err != nil || reclaimed.Operation.ID != expiringID {
			t.Fatalf("reclaimed work = %#v, err = %v", reclaimed, err)
		}
		if reclaimed.Lease.Token == claim.Lease.Token {
			t.Fatalf("reclaim reused work lease token %q", reclaimed.Lease.Token)
		}
		if attempts := harness.WorkAttempts(t, scope, expiringID); attempts != 2 {
			t.Fatalf("reclaimed work attempts = %d, want 2", attempts)
		}
	})

	t.Run("concurrent_exclusion_scope_and_terminal_filtering", func(t *testing.T) {
		ctx := context.Background()
		scope := childScope(harness.Scope, "concurrency")
		key, meta := Fixture(scope, "exclusive", time.Unix(10, 0).UTC())
		reserved, err := harness.Store.Reserve(ctx, key, meta)
		if err != nil {
			t.Fatalf("reserve exclusive work: %v", err)
		}
		stores := []app.OperationStore{harness.OpenPeer(t), harness.OpenPeer(t)}
		start := make(chan struct{})
		results := make(chan claimResult, len(stores))
		var wait sync.WaitGroup
		for index, store := range stores {
			wait.Add(1)
			go func(index int, store app.OperationStore) {
				defer wait.Done()
				<-start
				claim, claimErr := store.ClaimReady(ctx, scope, futureLease(fmt.Sprintf("concurrent-%d", index)))
				results <- claimResult{claim: claim, err: claimErr}
			}(index, store)
		}
		close(start)
		wait.Wait()
		close(results)
		var success, empty int
		var owner string
		for result := range results {
			switch {
			case result.err == nil:
				success++
				owner = result.claim.Lease.Token
				if result.claim.Operation.ID != reserved.ID {
					t.Fatalf("claimed %q, want %q", result.claim.Operation.ID, reserved.ID)
				}
			case errors.Is(result.err, app.ErrNoReadyOperation):
				empty++
			default:
				t.Fatalf("concurrent claim error = %v", result.err)
			}
		}
		if success != 1 || empty != 1 {
			t.Fatalf("concurrent claims: success=%d empty=%d", success, empty)
		}
		if err := harness.Store.ReleaseClaim(ctx, scope, reserved.ID, owner); err != nil {
			t.Fatalf("release concurrent owner: %v", err)
		}
		if err := harness.Store.Fail(ctx, reserved.ID, knowl.Failure{Class: "fixture", OperationID: string(reserved.ID)}); err != nil {
			t.Fatalf("mark terminal: %v", err)
		}
		ready, err := harness.Store.ResumeReady(ctx, scope, 10)
		if err != nil || len(ready) != 0 {
			t.Fatalf("terminal ready set = %v, err = %v", ready, err)
		}

		otherScope := childScope(harness.Scope, "other")
		otherKey, otherMeta := Fixture(otherScope, "exclusive", time.Unix(10, 0).UTC())
		other, err := harness.Store.Reserve(ctx, otherKey, otherMeta)
		if err != nil {
			t.Fatalf("reserve other scope: %v", err)
		}
		otherReady, err := harness.Store.ResumeReady(ctx, otherScope, 10)
		if err != nil || len(otherReady) != 1 || otherReady[0] != other.ID {
			t.Fatalf("other-scope ready set = %v, err = %v", otherReady, err)
		}
		if ready, err := harness.Store.ResumeReady(ctx, scope, 10); err != nil || len(ready) != 0 {
			t.Fatalf("scope isolation ready set = %v, err = %v", ready, err)
		}
		if _, err := harness.Store.Execution(ctx, otherScope, reserved.ID); !errors.Is(err, app.ErrOperationNotFound) {
			t.Fatalf("cross-scope descriptor error = %v", err)
		}
	})
}

type claimResult struct {
	claim knowl.WorkClaim
	err   error
}

// Fixture returns one valid bounded durable execution descriptor.
func Fixture(scope knowl.ScopeRef, id string, createdAt time.Time) (knowl.OperationKey, knowl.OperationMeta) {
	schema := []byte("# Schema\n\nversion: 1\n")
	schemaDigest := digest(schema)
	key := knowl.OperationKey{
		Scope:   scope,
		Source:  knowl.SourceRef{Adapter: "fixture", ID: id},
		Version: knowl.SourceVersion{Version: "1", Digest: strings.Repeat("a", 64)},
	}
	return key, knowl.OperationMeta{
		Key: key,
		AcceptedSource: knowl.AcceptedSource{
			Scope: scope, Source: key.Source, Version: key.Version,
			MediaType: "text/markdown", ManifestRef: "raw/source/version/manifest.yaml",
		},
		Schema:       knowl.SchemaDocument{Scope: scope, Digest: schemaDigest, Version: "1", Content: schema},
		SchemaDigest: schemaDigest,
		CreatedAt:    createdAt,
	}
}

func childScope(parent knowl.ScopeRef, suffix string) knowl.ScopeRef {
	return knowl.ScopeRef(fmt.Sprintf("%s_%s", parent, suffix))
}

func futureLease(token string) knowl.WorkLease {
	return knowl.WorkLease{Token: token, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
}

func digest(content []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(content))
}
