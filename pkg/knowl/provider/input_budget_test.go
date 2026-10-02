package provider

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"iter"
	"strings"
	"testing"
)

func TestRuntimeSourceRequestSizeMatchesActualWrapper(t *testing.T) {
	input := testMaintenanceInput()
	input.Schema.Content = []byte("schema 界<>")
	input.SourceText = strings.Repeat("界<>\"\\\n", 100)
	input.Source.Source.ID = "long<>\"source"
	input.Pages = []knowl.PageSnapshot{{Content: "# Full\nbody 界<>\"", Body: "body 界<>\""}}
	var captured string
	factory := &fakeRuntimeFactory{agent: newCapturingOutputAgent(t, &captured, testSourcePlanJSON)}
	m, err := newRuntimeMaintainer(factory, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	size, err := m.RequestBytes(t.Context(), input)
	if err != nil || factory.builds != 0 {
		t.Fatalf("sizing=%d err=%v builds=%d", size, err, factory.builds)
	}
	if _, err := m.Plan(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if len(captured) != size {
		t.Fatalf("actual wrapped bytes=%d measured=%d", len(captured), size)
	}
	var wire struct {
		Input struct {
			Pages []map[string]json.RawMessage `json:"pages"`
		} `json:"input"`
	}
	// The wrapper places the JSON envelope on one complete line.
	decoded := false
	for line := range strings.Lines(captured) {
		if json.Unmarshal([]byte(line), &wire) == nil && wire.Input.Pages != nil {
			decoded = true
			break
		}
	}
	if !decoded {
		t.Fatal("missing encoded source envelope")
	}
	if _, found := wire.Input.Pages[0]["body"]; found {
		t.Fatal("provider still duplicates Body")
	}
}
func TestRuntimeSourceRequestExactBoundary(t *testing.T) {
	input := testMaintenanceInput()
	input.SourceText = strings.Repeat("x", 1000)
	m, err := newRuntimeMaintainer(&fakeRuntimeFactory{agent: newOutputAgent(t, testSourcePlanJSON)}, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	size, err := m.RequestBytes(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, adjust := range []int{0, -1} {
		calls := 0
		var actual string
		inner := newCapturingOutputAgent(t, &actual, testSourcePlanJSON)
		factory := &fakeRuntimeFactory{agent: countRuns(inner, &calls)}
		bounded, err := newRuntimeMaintainer(factory, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{maxInputBytes: size + adjust})
		if err != nil {
			t.Fatal(err)
		}
		_, err = bounded.Plan(t.Context(), input)
		if adjust == 0 {
			if err != nil || calls != 1 || len(actual) != size {
				t.Fatalf("exact err=%v calls=%d", err, calls)
			}
		} else {
			failure, ok := app.ClassifyExecutionFailure(err)
			if !ok || failure.Reason != reasonProviderInputLimit || calls != 0 || factory.builds != 0 {
				t.Fatalf("overflow err=%v calls=%d builds=%d", err, calls, factory.builds)
			}
		}
		if err := bounded.Close(); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.RequestBytes(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

type countedBudgetAgent struct {
	adkagent.Agent
	calls *int
}

func countRuns(a adkagent.Agent, calls *int) adkagent.Agent {
	return &countedBudgetAgent{Agent: a, calls: calls}
}
func (a *countedBudgetAgent) Run(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
	*a.calls++
	return a.Agent.Run(ctx)
}

func TestActualWrapperGuardPreventsSizingDrift(t *testing.T) {
	input := testMaintenanceInput()
	envelope, err := app.EncodeSourceMaintenanceRequest(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	factory := &fakeRuntimeFactory{agent: countRuns(newOutputAgent(t, testSourcePlanJSON), &calls)}
	m, err := newRuntimeMaintainer(factory, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ctx := context.WithValue(t.Context(), sourceRequestBudgetKey{}, len(envelope))
	err = m.runStructuredPlan(ctx, envelope, "maintainer", func(string) error { return nil })
	failure, ok := app.ClassifyExecutionFailure(err)
	if !ok || failure.Reason != reasonProviderInputLimit || calls != 0 {
		t.Fatalf("drift err=%v calls=%d", err, calls)
	}
}
