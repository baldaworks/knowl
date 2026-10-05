package provider

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestRuntimeCorrectionAggregateOutput(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		var prompts []string
		m := correctionMaintainer(t, correctionSequenceAgent(t, []string{correctionInvalidOutput, testSourcePlanJSON}, &prompts), runtimeMaintainerOptions{})
		limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
		limits.MaxOutputBytes = len(correctionInvalidOutput) + len(testSourcePlanJSON)
		if overflow {
			limits.MaxOutputBytes--
		}
		_, report, err := m.PlanValidated(t.Context(), testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error { return nil })
		if overflow {
			if !errors.Is(err, app.ErrCorrectionOutputLimit) || report.Outcome != knowl.CorrectionOutputLimit || *report.OutputBytes != len(correctionInvalidOutput) {
				t.Fatalf("aggregate overflow: %+v %v", report, err)
			}
		} else if err != nil || *report.OutputBytes != limits.MaxOutputBytes {
			t.Fatalf("exact aggregate: %+v %v", report, err)
		}
		if len(prompts) != 2 || *report.Turns != 2 || *report.Corrections != 1 {
			t.Fatalf("actual aggregate turns: %+v", report)
		}
		if _, err := app.EncodeOperationCorrectionReport(report); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntimeCorrectionStreamsAndThoughts(t *testing.T) {
	for _, fixture := range []struct {
		name               string
		repeated, overflow bool
	}{{"thought ignored", false, false}, {"partial plus final exact", true, false}, {"partial plus final overflow", true, true}} {
		t.Run(fixture.name, func(t *testing.T) {
			calls := 0
			agent := correctionEventAgent(t, func(ctx adkagent.InvocationContext, yield func(*session.Event, error) bool) {
				calls++
				if fixture.repeated {
					partial := session.NewEvent(context.Background(), ctx.InvocationID())
					partial.Content = genai.NewContentFromText(testSourcePlanJSON, genai.RoleModel)
					partial.Partial = true
					if !yield(partial, nil) {
						return
					}
				}
				final := session.NewEvent(context.Background(), ctx.InvocationID())
				final.Content = genai.NewContentFromText(testSourcePlanJSON, genai.RoleModel)
				final.Content.Parts = append(final.Content.Parts, &genai.Part{Thought: true, Text: strings.Repeat("thought", 20000)})
				final.TurnComplete = true
				yield(final, nil)
			})
			m := correctionMaintainer(t, agent, runtimeMaintainerOptions{})
			limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
			limits.MaxOutputBytes = len(testSourcePlanJSON)
			if fixture.repeated {
				limits.MaxOutputBytes *= 2
			}
			if fixture.overflow {
				limits.MaxOutputBytes--
			}
			_, report, err := m.PlanValidated(t.Context(), testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error { return nil })
			if fixture.overflow {
				if !errors.Is(err, app.ErrCorrectionOutputLimit) || *report.OutputBytes != len(testSourcePlanJSON) {
					t.Fatalf("stream overflow: %+v %v", report, err)
				}
			} else if err != nil || *report.OutputBytes != limits.MaxOutputBytes {
				t.Fatalf("collector-compatible accounting: %+v %v", report, err)
			}
			if calls != 1 || *report.Turns != 1 {
				t.Fatalf("bound failure corrected: %+v", report)
			}
		})
	}
}

func TestRuntimeCorrectionTransportAndLateError(t *testing.T) {
	for _, late := range []bool{false, true} {
		calls, validations := 0, 0
		agent := correctionEventAgent(t, func(ctx adkagent.InvocationContext, yield func(*session.Event, error) bool) {
			calls++
			if late {
				event := session.NewEvent(context.Background(), ctx.InvocationID())
				event.Content = genai.NewContentFromText(testSourcePlanJSON, genai.RoleModel)
				if !yield(event, nil) {
					return
				}
			}
			yield(nil, errors.New("private-transport-secret"))
		})
		m := correctionMaintainer(t, agent, runtimeMaintainerOptions{})
		limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
		_, report, err := m.PlanValidated(t.Context(), testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error { validations++; return nil })
		failure, ok := app.ClassifyExecutionFailure(err)
		if !ok || !failure.Retryable || failure.Reason != reasonProviderRun || calls != 1 || validations != 0 || report.Outcome != knowl.CorrectionProviderFailed {
			t.Fatalf("transport is not output correction: %+v %v calls=%d validations=%d", report, err, calls, validations)
		}
	}
}

func TestRuntimeCorrectionErrorEventDoesNotChargeRejectedText(t *testing.T) {
	calls := 0
	agent := correctionEventAgent(t, func(ctx adkagent.InvocationContext, yield func(*session.Event, error) bool) {
		calls++
		event := session.NewEvent(context.Background(), ctx.InvocationID())
		event.Content = genai.NewContentFromText(strings.Repeat("x", 1024), genai.RoleModel)
		event.ErrorCode = "transport"
		event.ErrorMessage = "private-provider-error"
		yield(event, nil)
	})
	m := correctionMaintainer(t, agent, runtimeMaintainerOptions{})
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	limits.MaxOutputBytes = 64
	_, report, err := m.PlanValidated(t.Context(), testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error { return nil })
	failure, ok := app.ClassifyExecutionFailure(err)
	if !ok || !failure.Retryable || calls != 1 || *report.OutputBytes != 0 || report.Outcome != knowl.CorrectionProviderFailed {
		t.Fatalf("collector rejected error event before text: %+v %v", report, err)
	}
}

func TestRuntimeCorrectionSingleDeadline(t *testing.T) {
	var deadlines []time.Time
	agent := correctionEventAgent(t, func(ctx adkagent.InvocationContext, yield func(*session.Event, error) bool) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("unbounded planning context")
		}
		deadlines = append(deadlines, deadline)
		if len(deadlines) == 1 {
			event := session.NewEvent(context.Background(), ctx.InvocationID())
			event.Content = genai.NewContentFromText(correctionInvalidOutput, genai.RoleModel)
			event.TurnComplete = true
			yield(event, nil)
			return
		}
		<-ctx.Done()
		yield(nil, ctx.Err())
	})
	m := correctionMaintainer(t, agent, runtimeMaintainerOptions{})
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	limits.DeadlineNanos = int64(time.Second)
	_, report, err := m.PlanValidated(t.Context(), testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error { return nil })
	if !errors.Is(err, app.ErrCorrectionDeadline) || len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) || report.Outcome != knowl.CorrectionDeadline || *report.Turns != 2 {
		t.Fatalf("single total deadline: %+v %v deadlines=%v", report, err, deadlines)
	}
}

func TestRuntimeCorrectionCancellationStopsExtraTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var prompts []string
	m := correctionMaintainer(t, correctionSequenceAgent(t, []string{testSourcePlanJSON, testSourcePlanJSON}, &prompts), runtimeMaintainerOptions{})
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	_, report, err := m.PlanValidated(ctx, testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error { cancel(); return app.ErrPlanInvalid })
	if !errors.Is(err, context.Canceled) || len(prompts) != 1 || report.Outcome != knowl.CorrectionCanceled || *report.Turns != 1 {
		t.Fatalf("cancellation: %+v %v calls=%d", report, err, len(prompts))
	}
}

func TestRuntimeCorrectionReportsCanceledBeforeGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var prompts []string
	m := correctionMaintainer(t, correctionSequenceAgent(t, []string{testSourcePlanJSON}, &prompts), runtimeMaintainerOptions{})
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	_, report, err := m.PlanValidated(ctx, testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error { return nil })
	if !errors.Is(err, context.Canceled) || len(prompts) != 0 || report.Outcome != knowl.CorrectionCanceled || report.Turns == nil || *report.Turns != 0 {
		t.Fatalf("known zero before generation: %+v %v", report, err)
	}
	if _, err := app.EncodeOperationCorrectionReport(report); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCorrectionSetupFailureHasNoTurn(t *testing.T) {
	factory := &fakeRuntimeFactory{err: errors.New("private-runtime-setup-error")}
	m, err := newRuntimeMaintainer(factory, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	_, report, err := m.PlanValidated(t.Context(), testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error { return nil })
	failure, ok := app.ClassifyExecutionFailure(err)
	if !ok || !failure.Retryable || factory.builds != 1 || report.Outcome != knowl.CorrectionProviderFailed || report.Turns == nil || *report.Turns != 0 {
		t.Fatalf("setup: %+v %v builds=%d", report, err, factory.builds)
	}
	if _, err := app.EncodeOperationCorrectionReport(report); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCorrectionRequestUsesOriginalCap(t *testing.T) {
	var prompts []string
	agent := correctionSequenceAgent(t, []string{correctionInvalidOutput, testSourcePlanJSON}, &prompts)
	m := correctionMaintainer(t, agent, runtimeMaintainerOptions{})
	size, err := m.RequestBytes(t.Context(), testMaintenanceInput())
	if err != nil {
		t.Fatal(err)
	}
	m.maxInput = size
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	_, report, err := m.PlanValidated(t.Context(), testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error { return nil })
	failure, ok := app.ClassifyExecutionFailure(err)
	if !ok || failure.Reason != reasonProviderInputLimit || len(prompts) != 1 || report.Outcome != knowl.CorrectionProviderFailed || *report.Turns != 1 {
		t.Fatalf("feedback exceeded original cap: %+v %v", report, err)
	}
}

func TestRuntimeHierarchyCorrectionUsesFullValidator(t *testing.T) {
	input, plan := testHierarchyInputAndPlan()
	invalid := plan
	invalid.SchemaDigest = "incorrect"
	bad, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	good, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var prompts []string
	m := correctionMaintainer(t, correctionSequenceAgent(t, []string{string(bad), string(good)}, &prompts), runtimeMaintainerOptions{})
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	_, report, err := m.PlanHierarchyValidated(t.Context(), input, limits, func(candidate knowl.HierarchyModelPlan) error {
		_, err := app.ValidateHierarchyPlan(t.Context(), input, candidate, app.HierarchyValidationOptions{})
		return err
	})
	if err != nil || len(prompts) != 2 || report.ValidationCode != knowl.HierarchyPlanInvalid || report.Outcome != knowl.CorrectionAccepted {
		t.Fatalf("hierarchy correction: %+v %v", report, err)
	}
}

func correctionMaintainer(t *testing.T, agent adkagent.Agent, options runtimeMaintainerOptions) *RuntimeMaintainer {
	t.Helper()
	m, err := newRuntimeMaintainer(&fakeRuntimeFactory{agent: agent}, providerFailureClass, t.TempDir(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func correctionEventAgent(t *testing.T, run func(adkagent.InvocationContext, func(*session.Event, error) bool)) adkagent.Agent {
	t.Helper()
	agent, err := adkagent.New(adkagent.Config{Name: "correction-events", Run: func(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) { run(ctx, yield) }
	}})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}
