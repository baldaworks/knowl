package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/normahq/runtime/v2/agentfactory"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type correctionBlockingFactory struct{ release <-chan struct{} }

func (f correctionBlockingFactory) Build(ctx context.Context, _ agentfactory.BuildRequest) (adkagent.Agent, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.release:
		return nil, errors.New("released setup")
	}
}

type correctionBlockingSessions struct {
	session.Service
	release <-chan struct{}
}

func (s correctionBlockingSessions) Create(ctx context.Context, _ *session.CreateRequest) (*session.CreateResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return nil, errors.New("released session setup")
	}
}

func TestRuntimeCorrectionDeadlineIncludesSessionSetup(t *testing.T) {
	var prompts []string
	release := make(chan struct{})
	factory := &fakeRuntimeFactory{agent: correctionSequenceAgent(t, []string{testSourcePlanJSON}, &prompts)}
	m, err := newRuntimeMaintainer(factory, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{newSession: func() session.Service {
		return correctionBlockingSessions{Service: session.InMemoryService(), release: release}
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(release); _ = m.Close() })
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	limits.DeadlineNanos = int64(50 * time.Millisecond)
	type outcome struct {
		report knowl.OperationCorrectionReport
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		_, report, err := m.PlanValidated(t.Context(), testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error { return nil })
		done <- outcome{report, err}
	}()
	var report knowl.OperationCorrectionReport
	select {
	case result := <-done:
		report, err = result.report, result.err
	case <-time.After(time.Second):
		t.Fatal("session setup ignored the total planning deadline")
	}
	if !errors.Is(err, app.ErrCorrectionDeadline) || len(prompts) != 0 || *report.Turns != 0 || !factory.closed.Load() || factory.buildContext.Err() == nil {
		t.Fatalf("session setup bound/cleanup: %+v %v closed=%t", report, err, factory.closed.Load())
	}
}

func TestRuntimeCorrectionDeadlineIncludesLazyBuild(t *testing.T) {
	release := make(chan struct{})
	m, err := newRuntimeMaintainer(correctionBlockingFactory{release: release}, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(release); _ = m.Close() }()
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	limits.DeadlineNanos = int64(50 * time.Millisecond)
	done := make(chan error, 1)
	go func() {
		_, _, err := m.PlanValidated(t.Context(), testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error { return nil })
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, app.ErrCorrectionDeadline) {
			t.Fatalf("setup total deadline: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lazy setup ignored the total planning deadline")
	}
}

func TestRuntimeCorrectionDeadlineIncludesSerializationWait(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	agent := correctionEventAgent(t, func(ctx adkagent.InvocationContext, yield func(*session.Event, error) bool) {
		close(started)
		<-release
		event := session.NewEvent(context.Background(), ctx.InvocationID())
		event.Content = genai.NewContentFromText(testSourcePlanJSON, genai.RoleModel)
		event.TurnComplete = true
		yield(event, nil)
	})
	m := correctionMaintainer(t, agent, runtimeMaintainerOptions{})
	first := make(chan error, 1)
	go func() { _, err := m.Plan(t.Context(), testMaintenanceInput()); first <- err }()
	<-started
	defer func() {
		close(release)
		if err := <-first; err != nil {
			t.Error(err)
		}
	}()
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	limits.DeadlineNanos = int64(50 * time.Millisecond)
	done := make(chan error, 1)
	go func() {
		_, _, err := m.PlanValidated(t.Context(), testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error { return nil })
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, app.ErrCorrectionDeadline) {
			t.Fatalf("serialization total deadline: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serialization wait ignored the total planning deadline")
	}
}
