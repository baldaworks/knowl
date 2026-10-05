package knowl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/normahq/runtime/v2/agentfactory"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestExecutionSlotsBoundOwnershipAndStop(t *testing.T) {
	pool := newExecutionSlots([]*executionSlot{{}, {}})
	first, err := pool.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := pool.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := pool.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("occupied acquisition: %v", err)
	}
	if err := pool.Close(); !errors.Is(err, errExecutionInUse) {
		t.Fatalf("closed live owners: %v", err)
	}
	if _, err := pool.acquire(t.Context()); !errors.Is(err, ErrHostClosed) {
		t.Fatalf("post-stop acquisition: %v", err)
	}
	select {
	case <-first.ctx.Done():
		t.Fatal("close canceled a draining owner")
	default:
	}
	pool.cancel()
	select {
	case <-first.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("owner lifetime not canceled")
	}
	first.release()
	first.release()
	second.release()
	if err := pool.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionSlotsReuseOnlyReleasedOwner(t *testing.T) {
	pool := newExecutionSlots([]*executionSlot{{}, {}})
	first, err := pool.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := pool.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first.release()
	replacement, err := pool.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if replacement.slot != first.slot || replacement.slot == second.slot {
		t.Fatal("busy owner was reused")
	}
	replacement.release()
	second.release()
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHostComposesDistinctRuntimeSessions(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.Workers = 2
	factory := &slotRuntimeFactory{}
	host, err := New(t.Context(), Options{Config: config, RuntimeFactory: factory, ProviderID: "isolated"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	first, err := host.slots.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := host.slots.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	schema, generation, err := first.slot.source.CurrentMaintenancePolicy(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	otherSchema, otherGeneration, err := second.slot.source.CurrentMaintenancePolicy(t.Context(), "local")
	if err != nil || schema.Digest != otherSchema.Digest || generation != otherGeneration {
		t.Fatalf("slot policy drift: %v", err)
	}
	firstHierarchy, err := first.slot.hierarchy.Reserve(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	secondHierarchy, err := second.slot.hierarchy.Reserve(t.Context(), "local")
	if err != nil || firstHierarchy.ID != secondHierarchy.ID {
		t.Fatalf("hierarchy identity drift: %v", err)
	}
	input := domain.MaintenanceInput{ContractVersion: app.SourceMaintenanceContractVersion, Schema: domain.SchemaDocument{Digest: "schema"}, Source: domain.AcceptedSource{Source: domain.SourceRef{Adapter: "fixture", ID: "source"}, Version: domain.SourceVersion{Version: "1"}}}
	results := make(chan error, 2)
	for _, use := range []*executionUse{first, second} {
		go func() { _, err := use.slot.maintainer.Plan(use.ctx, input); results <- err }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if factory.builds.Load() != 2 || factory.bindings.Load() != 2 {
		t.Fatalf("owners built=%d bindings=%d", factory.builds.Load(), factory.bindings.Load())
	}
	for _, use := range []*executionUse{first, second} {
		if _, err := use.slot.maintainer.Plan(use.ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	if factory.builds.Load() != 2 || factory.bindings.Load() != 2 {
		t.Fatal("owner session binding not retained locally")
	}
	first.release()
	second.release()
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if factory.closes.Load() != 2 || factory.calls.Load() != 4 {
		t.Fatalf("provider closes=%d", factory.closes.Load())
	}
	encoded, err := json.Marshal(struct {
		CaseID      string `json:"case_id"`
		Owners      int32  `json:"owners"`
		Bindings    int32  `json:"bindings"`
		Closes      int32  `json:"closes"`
		SourceCalls int32  `json:"source_calls"`
		Outcome     string `json:"outcome"`
	}{"isolated-runtime-owners", factory.builds.Load(), factory.bindings.Load(), factory.closes.Load(), factory.calls.Load(), "met"})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(encoded))
}

type slotRuntimeFactory struct {
	builds, bindings, closes, calls atomic.Int32
}

func (factory *slotRuntimeFactory) Build(_ context.Context, request agentfactory.BuildRequest) (adkagent.Agent, error) {
	owner := factory.builds.Add(1)
	if request.AgentID != "isolated" || len(request.MCPServerIDs) != 0 {
		return nil, errors.New("unexpected provider selection")
	}
	agent, err := adkagent.New(adkagent.Config{Name: fmt.Sprintf("owner_%d", owner), Run: func(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) {
			factory.calls.Add(1)
			binding, err := ctx.Session().State().Get("owner_binding")
			if errors.Is(err, session.ErrStateKeyNotExist) {
				factory.bindings.Add(1)
				if err := ctx.Session().State().Set("owner_binding", owner); err != nil {
					yield(nil, err)
					return
				}
			} else if err != nil || binding != owner {
				yield(nil, errors.New("session crossed owners"))
				return
			}
			event := session.NewEvent(context.Background(), ctx.InvocationID())
			event.Content = genai.NewContentFromText(`{"schema_digest":"schema","source_refs":[],"edits":[]}`, genai.RoleModel)
			event.TurnComplete = true
			yield(event, nil)
		}
	}})
	if err != nil {
		return nil, err
	}
	return &slotOwnedAgent{Agent: agent, closes: &factory.closes}, nil
}

type slotOwnedAgent struct {
	adkagent.Agent
	closes *atomic.Int32
}

func (agent *slotOwnedAgent) Close() error { agent.closes.Add(1); return nil }

func TestExecutionSlotsRetainFailedCloseOwnership(t *testing.T) {
	failure := errors.New("test close failure")
	first := &slotTestCloser{failure: failure}
	second := &slotTestCloser{}
	pool := newExecutionSlots([]*executionSlot{{closer: first}, {closer: second}})
	if err := pool.Close(); !errors.Is(err, failure) {
		t.Fatalf("close failure=%v", err)
	}
	first.failure = nil
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if first.calls != 2 || second.calls != 1 {
		t.Fatalf("resource close attempts=%d/%d", first.calls, second.calls)
	}
}

type slotTestCloser struct {
	failure error
	calls   int
}

func (closer *slotTestCloser) Close() error { closer.calls++; return closer.failure }
