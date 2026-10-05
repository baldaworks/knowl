package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const correctionInvalidOutput = "invalid"

func TestRuntimeCorrectionSequence(t *testing.T) {
	for _, fixture := range []struct {
		name              string
		outputs           []string
		allowance, turns  int
		rejectValidations int
		outcome           knowl.CorrectionOutcome
		code              knowl.OutputValidationCode
	}{
		{"valid first", []string{testSourcePlanJSON}, 1, 1, 0, knowl.CorrectionAccepted, ""},
		{"malformed then valid", []string{"private-invalid-output", testSourcePlanJSON}, 1, 2, 0, knowl.CorrectionAccepted, knowl.StructuredOutputInvalid},
		{"disabled", []string{correctionInvalidOutput, testSourcePlanJSON}, 0, 1, 0, knowl.CorrectionExhausted, knowl.StructuredOutputInvalid},
		{"exhausted", []string{correctionInvalidOutput, correctionInvalidOutput, testSourcePlanJSON}, 1, 2, 0, knowl.CorrectionExhausted, knowl.StructuredOutputInvalid},
		{"application then valid", []string{testSourcePlanJSON, testSourcePlanJSON}, 1, 2, 1, knowl.CorrectionAccepted, knowl.SourcePlanInvalid},
		{"application disabled", []string{testSourcePlanJSON, testSourcePlanJSON}, 0, 1, 1, knowl.CorrectionExhausted, knowl.SourcePlanInvalid},
		{"schema then valid", []string{`{"schema_digest":"schema","source_refs":[],"edits":"private"}`, testSourcePlanJSON}, 1, 2, 0, knowl.CorrectionAccepted, knowl.StructuredOutputInvalid},
		{"branch then valid", []string{`{"schema_digest":"schema","snapshot_digest":"snapshot","catalogs":[]}`, testSourcePlanJSON}, 1, 2, 0, knowl.CorrectionAccepted, knowl.StructuredOutputInvalid},
		{"mixed layers share allowance", []string{correctionInvalidOutput, testSourcePlanJSON, testSourcePlanJSON}, 1, 2, 1, knowl.CorrectionExhausted, knowl.SourcePlanInvalid},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var prompts []string
			m, err := newRuntimeMaintainer(&fakeRuntimeFactory{agent: correctionSequenceAgent(t, fixture.outputs, &prompts)}, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close() })
			capability, ok := any(m).(app.ValidatingMaintainer)
			if !ok {
				t.Fatal("runtime cannot correct with the authoritative app validator")
			}
			limits, err := app.NormalizeOutputSettings(knowl.OutputSettings{MaxCorrections: &fixture.allowance})
			if err != nil {
				t.Fatal(err)
			}
			validations := 0
			_, report, err := capability.PlanValidated(t.Context(), testMaintenanceInput(), limits, func(knowl.ModelEditPlan) error {
				validations++
				if validations <= fixture.rejectValidations {
					return errors.New("private-validator-error-with-secret-path")
				}
				return nil
			})
			if fixture.outcome == knowl.CorrectionAccepted {
				if err != nil || validations != fixture.rejectValidations+1 {
					t.Fatalf("valid result: %v validations=%d", err, validations)
				}
			} else if !errors.Is(err, app.ErrOutputCorrectionExhausted) || validations != fixture.rejectValidations {
				t.Fatalf("finite exhaustion: %v validations=%d", err, validations)
			}
			used := 0
			for _, output := range fixture.outputs[:fixture.turns] {
				used += len(output)
			}
			if len(prompts) != fixture.turns || report.Turns == nil || *report.Turns != fixture.turns || report.Corrections == nil || *report.Corrections != fixture.turns-1 || report.OutputBytes == nil || *report.OutputBytes != used || report.Outcome != fixture.outcome || report.ValidationCode != fixture.code {
				t.Fatalf("actual report: %+v calls=%d", report, len(prompts))
			}
			if _, err := app.EncodeOperationCorrectionReport(report); err != nil {
				t.Fatalf("invalid report: %v", err)
			}
			if len(prompts) > 1 {
				initial, corrected := correctionEnvelope(t, prompts[0]), correctionEnvelope(t, prompts[1])
				feedback := corrected["validation_feedback"]
				delete(corrected, "validation_feedback")
				if !reflect.DeepEqual(initial, corrected) {
					t.Fatal("correction changed original authorized input")
				}
				var parsed struct {
					Code knowl.OutputValidationCode `json:"code"`
				}
				feedbackCode := fixture.code
				if fixture.outputs[0] == correctionInvalidOutput {
					feedbackCode = knowl.StructuredOutputInvalid
				}
				decoder := json.NewDecoder(bytes.NewReader(feedback))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&parsed); err != nil || parsed.Code != feedbackCode {
					t.Fatalf("safe feedback: %s %v", feedback, err)
				}
				if len(prompts[1])-len(prompts[0]) > app.MaxCorrectionFeedbackBytes {
					t.Fatal("feedback exceeded reserve")
				}
			}
		})
	}
}

func correctionSequenceAgent(t *testing.T, outputs []string, prompts *[]string) adkagent.Agent {
	t.Helper()
	agent, err := adkagent.New(adkagent.Config{Name: "correction-sequence", Run: func(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) {
			var prompt strings.Builder
			for _, part := range ctx.UserContent().Parts {
				if part != nil {
					prompt.WriteString(part.Text)
				}
			}
			index := len(*prompts)
			*prompts = append(*prompts, prompt.String())
			if index >= len(outputs) {
				t.Error("unexpected additional inference")
				yield(nil, errors.New("extra inference"))
				return
			}
			event := session.NewEvent(context.Background(), ctx.InvocationID())
			event.Content = genai.NewContentFromText(outputs[index], genai.RoleModel)
			event.TurnComplete = true
			yield(event, nil)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func correctionEnvelope(t *testing.T, prompt string) map[string]json.RawMessage {
	t.Helper()
	for line := range strings.Lines(prompt) {
		var envelope map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &envelope) == nil && envelope["operation"] != nil && envelope["input"] != nil {
			return envelope
		}
	}
	t.Fatal("actual runner request has no envelope")
	return nil
}
