package provider

import (
	"context"
	"iter"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// The pinned structuredagent v2.0.10 template contributes these fixed bytes
// when instruction/input/output schemas are all nonempty. Actual-wrapper
// behavioral tests and the inner-agent guard protect against formatter drift.
const sourceWrapperFrameBytes = 703
const sourceRequestFormatVersion = "source-json-v1/structuredagent-v2.0.10-v1"

type sourceRequestBudgetKey struct{}

// RequestBudget returns immutable local capacity without starting a runtime.
func (m *RuntimeMaintainer) RequestBudget() knowl.MaintenanceRequestBudget {
	if m == nil {
		return knowl.MaintenanceRequestBudget{}
	}
	return knowl.MaintenanceRequestBudget{MaxBytes: min(m.maxInput, app.MaxMaintenanceRequestBytes), FormatVersion: sourceRequestFormatVersion}
}

// RequestBytes measures the exact source envelope plus the pinned wrapper.
// It does not perform inference or build a runtime/session.
func (m *RuntimeMaintainer) RequestBytes(ctx context.Context, input knowl.MaintenanceInput) (int, error) {
	if ctx == nil {
		return 0, app.ErrMaintenanceInputInvalid
	}
	encoded, err := app.EncodeSourceMaintenanceRequest(ctx, input)
	if err != nil {
		return 0, err
	}
	return sourceWrappedBytes(len(encoded)), nil
}

func sourceWrappedBytes(envelopeBytes int) int {
	return envelopeBytes + sourceWrapperFrameBytes + len(strings.TrimSpace(maintainerInstruction)) + len(strings.TrimSpace(maintainerInputSchema)) + len(strings.TrimSpace(maintainerOutputSchema))
}

type sourceRequestGuard struct{ adkagent.Agent }

func (a *sourceRequestGuard) Run(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		if limit, ok := ctx.Value(sourceRequestBudgetKey{}).(int); ok {
			used := 0
			if content := ctx.UserContent(); content != nil {
				for _, part := range content.Parts {
					if part != nil {
						if len(part.Text) > limit-used {
							yield(nil, app.ErrMaintenanceInputLimit)
							return
						}
						used += len(part.Text)
					}
				}
			}
		}
		for event, err := range a.Agent.Run(ctx) {
			if !yield(event, err) {
				return
			}
		}
	}
}

var _ app.MaintenanceRequestSizer = (*RuntimeMaintainer)(nil)
