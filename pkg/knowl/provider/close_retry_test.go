package provider

import (
	"context"
	"errors"
	"github.com/normahq/runtime/v2/agentfactory"
	"testing"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

func TestRuntimeCloseRetriesOnlyFailedResources(t *testing.T) {
	for _, deleteFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider", true: "session"}[deleteFailure], func(t *testing.T) {
			failure := errors.New("close fixture")
			sessions := &retryCloseSessions{Service: session.InMemoryService()}
			if deleteFailure {
				sessions.failure = failure
			}
			agent := &retryCloseAgent{Agent: newCapturingOutputAgent(t, new(string), testSourcePlanJSON)}
			if !deleteFailure {
				agent.failure = failure
			}
			maintainer, err := newRuntimeMaintainer(&closeRetryFactory{agent: agent}, "retry-close", t.TempDir(), runtimeMaintainerOptions{newSession: func() session.Service { return sessions }})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = maintainer.Plan(t.Context(), testMaintenanceInput()); err != nil {
				t.Fatal(err)
			}
			if err = maintainer.Close(); !errors.Is(err, failure) {
				t.Fatalf("first close=%v", err)
			}
			agent.failure = nil
			sessions.failure = nil
			if err = maintainer.Close(); err != nil {
				t.Fatal(err)
			}
			wantAgent, wantSession := 2, 1
			if deleteFailure {
				wantAgent, wantSession = 1, 2
			}
			if agent.calls != wantAgent || sessions.calls != wantSession {
				t.Fatalf("cleanup attempts provider%d/session%d want%d/%d", agent.calls, sessions.calls, wantAgent, wantSession)
			}
			if err = maintainer.Close(); err != nil || agent.calls != wantAgent || sessions.calls != wantSession {
				t.Fatalf("successful resource reclosed=%v", err)
			}
		})
	}
}

type closeRetryFactory struct {
	agent  adkagent.Agent
	err    error
	builds int
}

// Build returns a provider with controlled cleanup failure through the real factory boundary.
func (factory *closeRetryFactory) Build(context.Context, agentfactory.BuildRequest) (adkagent.Agent, error) {
	factory.builds++
	return factory.agent, factory.err
}

type retryCloseAgent struct {
	adkagent.Agent
	failure error
	calls   int
}

func (agent *retryCloseAgent) Close() error { agent.calls++; return agent.failure }

type retryCloseSessions struct {
	session.Service
	failure error
	calls   int
}

func (service *retryCloseSessions) Delete(ctx context.Context, request *session.DeleteRequest) error {
	service.calls++
	if service.failure != nil {
		return service.failure
	}
	return service.Service.Delete(ctx, request)
}

func TestRuntimeFailedSetupRetainsCleanupAndBoundsBuilds(t *testing.T) {
	failure := errors.New("partial setup fixture")
	agent := &retryCloseAgent{Agent: newCapturingOutputAgent(t, new(string), testSourcePlanJSON), failure: failure}
	factory := &closeRetryFactory{agent: agent, err: failure}
	maintainer, err := newRuntimeMaintainer(factory, "partial", t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = maintainer.Plan(t.Context(), testMaintenanceInput()); err == nil {
		t.Fatal("partial build accepted")
	}
	if _, err = maintainer.Plan(t.Context(), testMaintenanceInput()); err == nil {
		t.Fatal("pending cleanup accepted")
	}
	if factory.builds != 1 || agent.calls != 1 {
		t.Fatalf("unreleased resources grew builds%d closes%d", factory.builds, agent.calls)
	}
	agent.failure = nil
	if err = maintainer.Close(); err != nil {
		t.Fatal(err)
	}
	if agent.calls != 2 {
		t.Fatalf("partial cleanup not retried=%d", agent.calls)
	}
	if err = maintainer.Close(); err != nil || agent.calls != 2 {
		t.Fatalf("partial cleanup repeated=%d %v", agent.calls, err)
	}
}
