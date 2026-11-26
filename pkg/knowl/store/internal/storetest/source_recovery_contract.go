package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type recoveryOperationReserver interface {
	Reserve(ctx context.Context, key knowl.OperationKey, meta knowl.OperationMeta) (app.OperationReservation, error)
}

type recoveryFixture struct {
	request app.SourceLifecycleRecoveryRequest
	active  knowl.SourceLifecycle
}

type SourceLifecycleRecoveryHarness struct {
	SourceLifecycleHarness
	ReadProof        func(*testing.T, knowl.ScopeRef, knowl.SourceOwner) string
	WriteProof       func(*testing.T, knowl.ScopeRef, knowl.SourceOwner, string)
	CorruptOperation func(*testing.T, knowl.ScopeRef, knowl.OperationID)
}

func sourceRecoveryFixture(t *testing.T, h SourceLifecycleHarness, suffix string, ready bool) recoveryFixture {
	t.Helper()
	scope := knowl.ScopeRef(string(h.Scope) + suffix)
	at := time.Unix(1_850_000_000, 0).UTC()
	sourceID := knowl.SourceID("historical_fs")
	source := knowl.AcceptedSource{Scope: scope, Source: knowl.SourceRef{Adapter: string(knowl.SourceAdapterFilesystem), ID: string(sourceID) + "/doc.md"},
		Version: knowl.SourceVersion{Version: "v1", Digest: strings.Repeat("a", 64)}, MediaType: "text/markdown", ManifestRef: "raw/old/manifest.yaml",
		SourceDocument: knowl.SourceDocument{SourceID: sourceID, DocumentID: "doc.md", Revision: "v1", URI: "https://example.invalid/doc.md"}}
	key := knowl.OperationKey{Scope: scope, Source: source.Source, Version: source.Version}
	schemaText := []byte("# Recovery fixture schema")
	digest := sha256.Sum256(schemaText)
	schema := knowl.SchemaDocument{Scope: scope, Digest: hex.EncodeToString(digest[:]), Version: "v1", Content: schemaText}
	reserver, ok := h.Store.(recoveryOperationReserver)
	if !ok {
		t.Fatal("missing operation reservation")
	}
	reserved, err := reserver.Reserve(t.Context(), key, knowl.OperationMeta{Key: key, AcceptedSource: source, Schema: schema, SchemaDigest: schema.Digest, CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	desired := []app.DesiredSourceOwner{{Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "current_git"}, ConfigDigest: strings.Repeat("b", 64)}}
	authority := reconcileLifecycle(t, t.Context(), h.Store, app.SourceLifecycleReconcileRequest{Scope: scope, Desired: desired, UpdatedAt: at.Add(time.Second)})
	active := requireLifecycle(t, t.Context(), h.Store, scope, desired[0].Owner)
	if ready {
		active, err = h.Store.MarkSourceLifecycleReady(t.Context(), app.SourceLifecycleReadyUpdate{Scope: scope, Owner: active.Owner, AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch, ActivatedEpoch: active.ActivatedEpoch, ConfigDigest: active.ConfigDigest, ReadyAt: at.Add(2 * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
	}
	return recoveryFixture{request: app.SourceLifecycleRecoveryRequest{Scope: scope, Owner: knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: sourceID},
		AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch, OperationID: reserved.ID, Source: source, PageID: "concepts/shared", PageDigest: strings.Repeat("c", 64), RecoveredAt: at.Add(3 * time.Second)}, active: active}
}

func RunSourceLifecycleRecoveryContract(t *testing.T, h SourceLifecycleRecoveryHarness) {
	t.Helper()
	store, ok := h.Store.(app.SourceLifecycleRecoveryStore)
	if !ok {
		t.Fatal("missing recovery capability")
	}
	t.Run("recover_replay_restart_finalize_and_tombstone", func(t *testing.T) {
		fixture := sourceRecoveryFixture(t, h.SourceLifecycleHarness, "_recover", true)
		request := fixture.request
		if _, err := h.Store.SourceLifecycle(t.Context(), request.Scope, request.Owner); !errors.Is(err, app.ErrSourceLifecycleUnowned) {
			t.Fatalf("orphan fixture: %v", err)
		}
		candidates, err := store.OrphanSourceOperations(t.Context(), app.OrphanSourceOperationReadRequest{Scope: request.Scope, Limit: 1})
		if err != nil || len(candidates.Items) != 1 || candidates.Items[0].Invalid || candidates.Items[0].Source != request.Source || candidates.Next != request.OperationID {
			t.Fatalf("candidates=%+v err=%v", candidates, err)
		}
		state, err := store.RecoverSourceLifecycle(t.Context(), request)
		proof, proofErr := app.SourceLifecycleRecoveryProofDigest(request)
		if err != nil || proofErr != nil || state.State != knowl.SourceLifecycleRetiring || state.ConfigDigest != "" || state.RecoveryProofDigest != proof || state.Ready || state.ReplacementPending != 0 {
			t.Fatalf("recovery=%#v %v", state, err)
		}
		if state.ActivatedAt != request.RecoveredAt || state.RetiringAt != request.RecoveredAt {
			t.Fatalf("recovery initialization presented as history: %+v", state)
		}
		proofPayload := requireRecoveryProof(t, h, request, state)
		again := request
		again.RecoveredAt = again.RecoveredAt.Add(time.Second)
		replay, err := store.RecoverSourceLifecycle(t.Context(), again)
		if err != nil || replay != state {
			t.Fatalf("non-idempotent recovery: %#v %v", replay, err)
		}
		peer := h.OpenPeer(t)
		if got := requireLifecycle(t, t.Context(), peer, request.Scope, request.Owner); got != state {
			t.Fatalf("restart changed recovery: %#v", got)
		}
		empty, err := store.OrphanSourceOperations(t.Context(), app.OrphanSourceOperationReadRequest{Scope: request.Scope, Limit: 10})
		if err != nil || len(empty.Items) != 0 {
			t.Fatalf("recovered owner remained candidate: %+v %v", empty, err)
		}
		state, err = h.Store.UpdateSourceRetirement(t.Context(), app.SourceRetirementUpdate{Scope: state.Scope, Owner: state.Owner, AuthorityGeneration: state.AuthorityGeneration, AuthorityEpoch: state.AuthorityEpoch, RetirementGeneration: state.RetirementGeneration, RetirementEpoch: state.RetirementEpoch, ScanComplete: true, UpdatedAt: request.RecoveredAt.Add(2 * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		deleted, err := h.Store.FinalizeSourceRetirement(t.Context(), app.SourceRetirementFinalization{Scope: state.Scope, Owner: state.Owner, AuthorityGeneration: state.AuthorityGeneration, AuthorityEpoch: state.AuthorityEpoch, RetirementGeneration: state.RetirementGeneration, RetirementEpoch: state.RetirementEpoch, FinalizedAt: request.RecoveredAt.Add(3 * time.Second)})
		if err != nil || deleted.RecoveryProofDigest != proof || deleted.State != knowl.SourceLifecycleDeleted {
			t.Fatalf("finalize=%+v %v", deleted, err)
		}
		if actual := requireRecoveryProof(t, h, request, deleted); actual != proofPayload {
			t.Fatal("finalization changed the historical recovery proof")
		}
		if _, err := store.RecoverSourceLifecycle(t.Context(), request); !errors.Is(err, app.ErrSourceLifecycleConflict) {
			t.Fatalf("revived tombstone: %v", err)
		}
	})
	t.Run("readiness_not_operation_completion", func(t *testing.T) {
		fixture := sourceRecoveryFixture(t, h.SourceLifecycleHarness, "_ready", false)
		if _, err := store.RecoverSourceLifecycle(t.Context(), fixture.request); !errors.Is(err, app.ErrSourceReplacementNotReady) {
			t.Fatalf("unready cleanup: %v", err)
		}
		if _, err := h.Store.SourceLifecycle(t.Context(), fixture.request.Scope, fixture.request.Owner); !errors.Is(err, app.ErrSourceLifecycleUnowned) {
			t.Fatalf("unready recovery mutated state: %v", err)
		}
		active := fixture.active
		_, err := h.Store.MarkSourceLifecycleReady(t.Context(), app.SourceLifecycleReadyUpdate{Scope: active.Scope, Owner: active.Owner, AuthorityGeneration: active.AuthorityGeneration, AuthorityEpoch: active.AuthorityEpoch, ActivatedEpoch: active.ActivatedEpoch, ConfigDigest: active.ConfigDigest, ReadyAt: fixture.request.RecoveredAt})
		if err != nil {
			t.Fatal(err)
		}
		// The historical operation is still received, not committed. Durable sync
		// readiness is the existing gate, not successful maintenance completion.
		if _, err := store.RecoverSourceLifecycle(t.Context(), fixture.request); err != nil {
			t.Fatalf("ready pending operation failed recovery: %v", err)
		}
	})
	t.Run("bad_proof_and_authority", func(t *testing.T) {
		fixture := sourceRecoveryFixture(t, h.SourceLifecycleHarness, "_bad", true)
		for _, kind := range []string{"operation", "accepted_descriptor", "owner", "scope", "authority", "epoch", "page", "time"} {
			request := fixture.request
			switch kind {
			case "operation":
				request.OperationID = "missing-operation"
			case "accepted_descriptor":
				request.Source.ManifestRef = "raw/other/manifest.yaml"
			case "owner":
				request.Owner.SourceID = "unrelated"
			case "scope":
				request.Scope = "wrong-scope"
				request.Source.Scope = request.Scope
			case "authority":
				request.AuthorityGeneration = strings.Repeat("d", 64)
			case "epoch":
				request.AuthorityEpoch++
			case "page":
				request.PageID = "../escape"
			case "time":
				request.RecoveredAt = fixture.active.ActivatedAt.Add(-time.Second)
			}
			_, err := store.RecoverSourceLifecycle(t.Context(), request)
			if !errors.Is(err, app.ErrSourceLifecycleInvalid) && !errors.Is(err, app.ErrSourceLifecycleConflict) {
				t.Fatalf("%s error=%v", kind, err)
			}
		}
		if _, err := h.Store.SourceLifecycle(t.Context(), fixture.request.Scope, fixture.request.Owner); !errors.Is(err, app.ErrSourceLifecycleUnowned) {
			t.Fatalf("bad proof created row: %v", err)
		}
	})
	t.Run("concurrent_recovery_and_real_activation", func(t *testing.T) {
		fixture := sourceRecoveryFixture(t, h.SourceLifecycleHarness, "_race", true)
		peers := []app.SourceLifecycleStore{h.Store, h.OpenPeer(t)}
		start := make(chan struct{})
		failures := make(chan error, 2)
		var wait sync.WaitGroup
		for _, peer := range peers {
			wait.Go(func() {
				<-start
				_, err := peer.(app.SourceLifecycleRecoveryStore).RecoverSourceLifecycle(t.Context(), fixture.request)
				failures <- err
			})
		}
		close(start)
		wait.Wait()
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatalf("concurrent proof replay: %v", err)
			}
		}
		state := requireLifecycle(t, t.Context(), h.Store, fixture.request.Scope, fixture.request.Owner)
		proofPayload := requireRecoveryProof(t, h, fixture.request, state)
		desired := []app.DesiredSourceOwner{{Owner: fixture.active.Owner, ConfigDigest: fixture.active.ConfigDigest}, {Owner: state.Owner, ConfigDigest: strings.Repeat("e", 64)}}
		reconcileLifecycle(t, t.Context(), h.Store, app.SourceLifecycleReconcileRequest{Scope: state.Scope, Desired: desired, ExpectedAuthorityEpoch: state.AuthorityEpoch, UpdatedAt: fixture.request.RecoveredAt.Add(time.Second)})
		active := requireLifecycle(t, t.Context(), h.Store, state.Scope, state.Owner)
		if active.State != knowl.SourceLifecycleActive || active.ConfigDigest != desired[1].ConfigDigest || active.RecoveryProofDigest != "" {
			t.Fatalf("real activation lost config-only identity: %+v", active)
		}
		if actual := requireRecoveryProof(t, h, fixture.request, active); actual != proofPayload {
			t.Fatal("genuine activation erased the historical recovery proof")
		}
		if _, err := store.RecoverSourceLifecycle(t.Context(), fixture.request); !errors.Is(err, app.ErrSourceLifecycleConflict) {
			t.Fatalf("recovery overwrote activation: %v", err)
		}
	})
	t.Run("recovery_payload_corruption_is_rejected", func(t *testing.T) {
		fixture := sourceRecoveryFixture(t, h.SourceLifecycleHarness, "_payload", true)
		state, err := store.RecoverSourceLifecycle(t.Context(), fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		payload := requireRecoveryProof(t, h, fixture.request, state)
		var proof struct {
			Request app.SourceLifecycleRecoveryRequest `json:"request"`
			Digest  string                             `json:"digest"`
		}
		if err := json.Unmarshal([]byte(payload), &proof); err != nil {
			t.Fatal(err)
		}
		proof.Request.PageDigest = strings.Repeat("f", 64)
		corrupt, err := json.Marshal(proof)
		if err != nil {
			t.Fatal(err)
		}
		h.WriteProof(t, state.Scope, state.Owner, string(corrupt))
		if _, err := h.Store.SourceLifecycle(t.Context(), state.Scope, state.Owner); !errors.Is(err, app.ErrSourceLifecycleInvalid) {
			t.Fatalf("corrupt proof accepted: %v", err)
		}
		h.WriteProof(t, state.Scope, state.Owner, payload)
		if restored := requireLifecycle(t, t.Context(), h.Store, state.Scope, state.Owner); restored != state {
			t.Fatalf("restoring authentic proof changed lifecycle: %#v", restored)
		}
	})
	t.Run("invalid_operation_does_not_stall_cursor", func(t *testing.T) {
		fixture := sourceRecoveryFixture(t, h.SourceLifecycleHarness, "_cursor", true)
		original, err := h.Store.(interface {
			Execution(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.ExecutionDescriptor, error)
		}).Execution(t.Context(), fixture.request.Scope, fixture.request.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		next := fixture.request.Source
		next.Version.Version, next.SourceDocument.Revision, next.ManifestRef = "v2", "v2", "raw/v2/manifest.yaml"
		key := knowl.OperationKey{Scope: next.Scope, Source: next.Source, Version: next.Version}
		reserved, err := h.Store.(recoveryOperationReserver).Reserve(t.Context(), key, knowl.OperationMeta{Key: key, AcceptedSource: next, Schema: original.Schema, SchemaDigest: original.Schema.Digest, CreatedAt: fixture.request.RecoveredAt})
		if err != nil {
			t.Fatal(err)
		}
		ids := []knowl.OperationID{fixture.request.OperationID, reserved.ID}
		slices.Sort(ids)
		h.CorruptOperation(t, fixture.request.Scope, ids[0])
		first, err := store.OrphanSourceOperations(t.Context(), app.OrphanSourceOperationReadRequest{Scope: fixture.request.Scope, Limit: 1})
		if err != nil || len(first.Items) != 1 || first.Items[0].OperationID != ids[0] || !first.Items[0].Invalid || first.Next != ids[0] {
			t.Fatalf("invalid cursor window: %+v %v", first, err)
		}
		second, err := store.OrphanSourceOperations(t.Context(), app.OrphanSourceOperationReadRequest{Scope: fixture.request.Scope, After: first.Next, Limit: 1})
		if err != nil || len(second.Items) != 1 || second.Items[0].OperationID != ids[1] || second.Items[0].Invalid {
			t.Fatalf("valid row after invalid window: %+v %v", second, err)
		}
	})
	t.Run("ordinary_retiring_owner_is_never_overwritten", func(t *testing.T) {
		fixture := sourceRecoveryFixture(t, h.SourceLifecycleHarness, "_ordinary", true)
		desired := []app.DesiredSourceOwner{{Owner: fixture.active.Owner, ConfigDigest: fixture.active.ConfigDigest}, {Owner: fixture.request.Owner, ConfigDigest: strings.Repeat("e", 64)}}
		authority := reconcileLifecycle(t, t.Context(), h.Store, app.SourceLifecycleReconcileRequest{Scope: fixture.request.Scope, Desired: desired, ExpectedAuthorityEpoch: fixture.active.AuthorityEpoch, UpdatedAt: fixture.request.RecoveredAt})
		authority = reconcileLifecycle(t, t.Context(), h.Store, app.SourceLifecycleReconcileRequest{Scope: fixture.request.Scope, Desired: desired[:1], ExpectedAuthorityEpoch: authority.Epoch, UpdatedAt: fixture.request.RecoveredAt.Add(time.Second)})
		before := requireLifecycle(t, t.Context(), h.Store, fixture.request.Scope, fixture.request.Owner)
		fixture.request.AuthorityGeneration, fixture.request.AuthorityEpoch, fixture.request.RecoveredAt = authority.Generation, authority.Epoch, fixture.request.RecoveredAt.Add(2*time.Second)
		if _, err := store.RecoverSourceLifecycle(t.Context(), fixture.request); !errors.Is(err, app.ErrSourceLifecycleConflict) {
			t.Fatalf("ordinary retirement replaced: %v", err)
		}
		if after := requireLifecycle(t, t.Context(), h.Store, before.Scope, before.Owner); after != before {
			t.Fatalf("ordinary retirement mutated: %+v", after)
		}
	})
	t.Run("recovery_races_real_activation", func(t *testing.T) {
		fixture := sourceRecoveryFixture(t, h.SourceLifecycleHarness, "_activation_race", true)
		peer := h.OpenPeer(t)
		desired := []app.DesiredSourceOwner{{Owner: fixture.active.Owner, ConfigDigest: fixture.active.ConfigDigest}, {Owner: fixture.request.Owner, ConfigDigest: strings.Repeat("e", 64)}}
		request := app.SourceLifecycleReconcileRequest{Scope: fixture.request.Scope, Desired: desired, ExpectedAuthorityEpoch: fixture.active.AuthorityEpoch, UpdatedAt: fixture.request.RecoveredAt}
		request.AuthorityGeneration = app.SourceLifecycleGeneration(desired)
		start := make(chan struct{})
		recovered, activated := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			_, err := store.RecoverSourceLifecycle(t.Context(), fixture.request)
			recovered <- err
		}()
		go func() { <-start; activated <- peer.ReconcileSourceLifecycles(t.Context(), request) }()
		close(start)
		if err := <-recovered; err != nil && !errors.Is(err, app.ErrSourceLifecycleConflict) {
			t.Fatalf("recovery race: %v", err)
		}
		if err := <-activated; err != nil {
			t.Fatalf("activation race: %v", err)
		}
		state := requireLifecycle(t, t.Context(), h.Store, fixture.request.Scope, fixture.request.Owner)
		if state.State != knowl.SourceLifecycleActive || state.ConfigDigest != desired[1].ConfigDigest || state.RecoveryProofDigest != "" {
			t.Fatalf("recovery won active authority over real config: %+v", state)
		}
	})
}

func requireRecoveryProof(t *testing.T, h SourceLifecycleRecoveryHarness, request app.SourceLifecycleRecoveryRequest, state knowl.SourceLifecycle) string {
	t.Helper()
	payload := h.ReadProof(t, state.Scope, state.Owner)
	var proof struct {
		Request app.SourceLifecycleRecoveryRequest `json:"request"`
		Digest  string                             `json:"digest"`
	}
	if err := json.Unmarshal([]byte(payload), &proof); err != nil {
		t.Fatal(err)
	}
	expected := request
	expected.RecoveredAt = time.Time{}
	digest, err := app.SourceLifecycleRecoveryProofDigest(request)
	if err != nil || proof.Request != expected || proof.Digest != digest {
		t.Fatalf("proof lost authenticated preimage: %#v %v", proof, err)
	}
	if err := app.ValidateSourceLifecycleRecoveryPayload(state, payload); err != nil {
		t.Fatalf("stored proof is not independently verifiable: %v", err)
	}
	return payload
}
