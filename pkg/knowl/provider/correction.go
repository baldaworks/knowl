package provider

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/normahq/runtime/v2/structuredagent"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/genai"
)

type correctionEvidenceKey struct{}

type correctionEvidence struct {
	turns, bytes int
	limit        int
	code         knowl.OutputValidationCode
}

var (
	_ app.ValidatingMaintainer          = (*RuntimeMaintainer)(nil)
	_ app.ValidatingHierarchyMaintainer = (*RuntimeMaintainer)(nil)
)

func (m *RuntimeMaintainer) PlanValidated(ctx context.Context, input knowl.MaintenanceInput, limits knowl.OutputCorrectionLimits, validate func(knowl.ModelEditPlan) error) (knowl.ModelEditPlan, knowl.OperationCorrectionReport, error) {
	if validate == nil {
		return knowl.ModelEditPlan{}, knowl.OperationCorrectionReport{}, app.ErrOutputCorrectionInvalid
	}
	return m.planSource(ctx, input, limits, validate)
}

func (m *RuntimeMaintainer) planSource(ctx context.Context, input knowl.MaintenanceInput, limits knowl.OutputCorrectionLimits, validate func(knowl.ModelEditPlan) error) (knowl.ModelEditPlan, knowl.OperationCorrectionReport, error) {
	bounded, cancel, limits, err := m.correctionContext(ctx, limits)
	if err != nil {
		return knowl.ModelEditPlan{}, correctionReport(limits, correctionEvidence{}, err), err
	}
	defer cancel()
	envelope, capBytes, err := sourceRequest(bounded, input, m.RequestBudget().MaxBytes)
	if err != nil {
		return knowl.ModelEditPlan{}, correctionReport(limits, correctionEvidence{}, err), err
	}
	bounded = context.WithValue(bounded, sourceRequestBudgetKey{}, capBytes)
	var plan knowl.ModelEditPlan
	decode := func(candidate string) error {
		if err := validateOutputBranch(candidate, []string{"source_refs", "edits"}, []string{"snapshot_digest", "catalogs"}); err != nil {
			return err
		}
		var wire maintainerPlanOutput
		if err := json.Unmarshal([]byte(candidate), &wire); err != nil {
			return err
		}
		plan = wire.modelPlan()
		return nil
	}
	var validator func() error
	if validate != nil {
		validator = func() error { return validate(plan) }
	}
	report, err := m.runCorrectedPlan(bounded, envelope, "maintainer", limits, decode, validator, knowl.SourcePlanInvalid)
	if err != nil {
		return knowl.ModelEditPlan{}, report, err
	}
	return plan, report, nil
}

func (m *RuntimeMaintainer) PlanHierarchyValidated(ctx context.Context, input knowl.HierarchyInput, limits knowl.OutputCorrectionLimits, validate func(knowl.HierarchyModelPlan) error) (knowl.HierarchyModelPlan, knowl.OperationCorrectionReport, error) {
	if validate == nil {
		return knowl.HierarchyModelPlan{}, knowl.OperationCorrectionReport{}, app.ErrOutputCorrectionInvalid
	}
	return m.planHierarchy(ctx, input, limits, validate)
}

func (m *RuntimeMaintainer) planHierarchy(ctx context.Context, input knowl.HierarchyInput, limits knowl.OutputCorrectionLimits, validate func(knowl.HierarchyModelPlan) error) (knowl.HierarchyModelPlan, knowl.OperationCorrectionReport, error) {
	bounded, cancel, limits, err := m.correctionContext(ctx, limits)
	if err != nil {
		return knowl.HierarchyModelPlan{}, correctionReport(limits, correctionEvidence{}, err), err
	}
	defer cancel()
	envelope, err := hierarchyRequest(input)
	if err != nil {
		return knowl.HierarchyModelPlan{}, correctionReport(limits, correctionEvidence{}, err), err
	}
	bounded = context.WithValue(bounded, sourceRequestBudgetKey{}, m.RequestBudget().MaxBytes)
	var plan knowl.HierarchyModelPlan
	decode := func(candidate string) error {
		if err := validateOutputBranch(candidate, []string{"snapshot_digest", "catalogs"}, []string{"source_refs", "edits", "rationale", "catalog_additions"}); err != nil {
			return err
		}
		return json.Unmarshal([]byte(candidate), &plan)
	}
	var validator func() error
	if validate != nil {
		validator = func() error { return validate(plan) }
	}
	report, err := m.runCorrectedPlan(bounded, envelope, "hierarchy", limits, decode, validator, knowl.HierarchyPlanInvalid)
	if err != nil {
		return knowl.HierarchyModelPlan{}, report, err
	}
	return plan, report, nil
}

func (m *RuntimeMaintainer) correctionContext(ctx context.Context, limits knowl.OutputCorrectionLimits) (context.Context, context.CancelFunc, knowl.OutputCorrectionLimits, error) {
	if err := app.ValidateOutputCorrectionLimits(limits); err != nil {
		return nil, nil, limits, err
	}
	limits.MaxOutputBytes = min(limits.MaxOutputBytes, m.maxOutput)
	if err := validatePlanContext(ctx); err != nil {
		return nil, nil, limits, err
	}
	bounded, cancel := context.WithTimeoutCause(ctx, time.Duration(limits.DeadlineNanos), app.ErrCorrectionDeadline)
	return bounded, cancel, limits, nil
}

// runCorrectedPlan owns the sole finite loop; wrapper validation retries remain zero.
func (m *RuntimeMaintainer) runCorrectedPlan(ctx context.Context, envelope []byte, operation string, limits knowl.OutputCorrectionLimits, decode func(string) error, validate func() error, appCode knowl.OutputValidationCode) (report knowl.OperationCorrectionReport, resultErr error) {
	evidence := correctionEvidence{limit: limits.MaxOutputBytes}
	defer func() { report = correctionReport(limits, evidence, resultErr) }()
	ctx = context.WithValue(ctx, correctionEvidenceKey{}, &evidence)
	select {
	case m.mu <- struct{}{}:
		defer func() { <-m.mu }()
	case <-ctx.Done():
		return report, correctionContextError(ctx)
	}
	if err := correctionContextError(ctx); err != nil {
		return report, err
	}
	if m.closed {
		return report, permanentProviderFailure(reasonProviderSetup)
	}
	// Check the complete initial envelope before any lazy setup or inference.
	if err := checkCorrectionRequest(ctx, envelope, m.RequestBudget().MaxBytes); err != nil {
		return report, err
	}
	runtime, err := m.ensureRuntime(ctx)
	if err != nil {
		if ctxErr := correctionContextError(ctx); ctxErr != nil {
			return report, ctxErr
		}
		return report, err
	}
	for turn := 0; turn <= limits.MaxCorrections; turn++ {
		if err := correctionContextError(ctx); err != nil {
			return report, err
		}
		request := envelope
		if turn > 0 {
			request, err = correctionRequest(envelope, evidence.code)
			if err != nil {
				return report, err
			}
		}
		if err := checkCorrectionRequest(ctx, request, m.RequestBudget().MaxBytes); err != nil {
			return report, err
		}
		invalid, err := runCorrectionTurn(ctx, runtime, request, decode)
		if err != nil {
			return report, err
		}
		if err := correctionContextError(ctx); err != nil {
			return report, err
		}
		if invalid {
			evidence.code = knowl.StructuredOutputInvalid
		} else if validate != nil && validate() != nil {
			if err := correctionContextError(ctx); err != nil {
				return report, err
			}
			evidence.code = appCode
			invalid = true
		}
		if err := correctionContextError(ctx); err != nil {
			return report, err
		}
		if !invalid {
			return report, nil
		}
	}
	return report, app.ErrOutputCorrectionExhausted
}

func runCorrectionTurn(ctx context.Context, runtime *maintainerRuntime, request []byte, decode func(string) error) (bool, error) {
	invalid, found := false, false
	for event, runErr := range runtime.runner.Run(ctx, maintainerUserID, runtime.sessionID, genai.NewContentFromText(string(request), genai.RoleUser), adkagent.RunConfig{}) {
		if runErr != nil {
			if err := correctionContextError(ctx); err != nil {
				return false, err
			}
			switch {
			case errors.Is(runErr, app.ErrCorrectionOutputLimit):
				return false, app.ErrCorrectionOutputLimit
			case errors.Is(runErr, app.ErrMaintenanceInputLimit):
				return false, permanentProviderFailure(reasonProviderInputLimit)
			case errors.Is(runErr, structuredagent.ErrStructuredInputSchemaValidation):
				return false, permanentProviderFailure(reasonProviderInput)
			case errors.Is(runErr, structuredagent.ErrStructuredOutputSchemaValidation):
				return true, nil
			default:
				return false, transientProviderFailure(reasonProviderRun)
			}
		}
		candidate := planEventText(event)
		if candidate == "" {
			continue
		}
		found = true
		invalid = decode(candidate) != nil
	}
	return !found || invalid, nil
}

func correctionRequest(envelope []byte, code knowl.OutputValidationCode) ([]byte, error) {
	if code != knowl.StructuredOutputInvalid && code != knowl.SourcePlanInvalid && code != knowl.HierarchyPlanInvalid {
		return nil, app.ErrOutputCorrectionInvalid
	}
	feedback, err := json.Marshal(struct {
		Code knowl.OutputValidationCode `json:"code"`
	}{code})
	if err != nil {
		return nil, app.ErrOutputCorrectionInvalid
	}
	delta := append([]byte(`,"validation_feedback":`), feedback...)
	if len(delta) > app.MaxCorrectionFeedbackBytes || len(envelope) == 0 || envelope[len(envelope)-1] != '}' {
		return nil, app.ErrOutputCorrectionInvalid
	}
	request := make([]byte, 0, len(envelope)+len(delta))
	request = append(request, envelope[:len(envelope)-1]...)
	request = append(request, delta...)
	return append(request, '}'), nil
}

func checkCorrectionRequest(ctx context.Context, envelope []byte, fallback int) error {
	limit := fallback
	if declared, ok := ctx.Value(sourceRequestBudgetKey{}).(int); ok {
		limit = min(limit, declared)
	}
	if sourceWrappedBytes(len(envelope)) > limit {
		return permanentProviderFailure(reasonProviderInputLimit)
	}
	return correctionContextError(ctx)
}

func correctionContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		if errors.Is(context.Cause(ctx), app.ErrCorrectionDeadline) {
			return app.ErrCorrectionDeadline
		}
		return err
	}
	return nil
}

func correctionReport(limits knowl.OutputCorrectionLimits, evidence correctionEvidence, err error) knowl.OperationCorrectionReport {
	outcome := knowl.CorrectionAccepted
	switch {
	case errors.Is(err, app.ErrOutputCorrectionExhausted):
		outcome = knowl.CorrectionExhausted
	case errors.Is(err, app.ErrCorrectionOutputLimit):
		outcome = knowl.CorrectionOutputLimit
	case errors.Is(err, app.ErrCorrectionDeadline), errors.Is(err, context.DeadlineExceeded):
		outcome = knowl.CorrectionDeadline
	case errors.Is(err, context.Canceled):
		outcome = knowl.CorrectionCanceled
	case err != nil:
		outcome = knowl.CorrectionProviderFailed
	}
	turns, corrections, used := evidence.turns, max(0, evidence.turns-1), evidence.bytes
	return knowl.OperationCorrectionReport{Version: 1, MaxCorrections: limits.MaxCorrections, MaxOutputBytes: limits.MaxOutputBytes, DeadlineNanos: limits.DeadlineNanos, Outcome: outcome, Turns: &turns, Corrections: &corrections, OutputBytes: &used, ValidationCode: evidence.code}
}

func legacyCorrectionError(err error) error {
	if errors.Is(err, app.ErrOutputCorrectionExhausted) {
		return permanentProviderFailure(reasonProviderOutputInvalid)
	}
	return err
}
