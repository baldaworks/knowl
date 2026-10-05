package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func unavailableCorrectionReport(policy knowl.OutputCorrectionPolicy) knowl.OperationCorrectionReport {
	return knowl.OperationCorrectionReport{Version: 1, MaxCorrections: 0, MaxOutputBytes: policy.Limits.MaxOutputBytes, DeadlineNanos: policy.Limits.DeadlineNanos, Outcome: knowl.CorrectionUnavailable}
}

func saveCorrectionReport(ctx context.Context, operations OperationStore, scope knowl.ScopeRef, id knowl.OperationID, policy knowl.OutputCorrectionPolicy, report knowl.OperationCorrectionReport) error {
	if _, err := EncodeOperationCorrectionReport(report); err != nil {
		return err
	}
	if policy.Supported {
		if err := validateCorrectionPolicyReport(policy, report, false); err != nil {
			return err
		}
	}
	writer, ok := operations.(OperationCorrectionReportStore)
	if !ok {
		return nil
	}
	stateCtx, cancel := context.WithTimeout(durableContext(ctx), 5*time.Second)
	defer cancel()
	return writer.SaveOperationCorrectionReport(stateCtx, scope, id, report.WorkAttempt, report)
}

func supportsCorrectionReports(operations OperationStore) bool {
	_, ok := operations.(OperationCorrectionReportStore)
	return ok
}

func planningError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), ErrCorrectionDeadline) {
			return ErrCorrectionDeadline
		}
		return ctx.Err()
	}
	return err
}

func correctionFailure(report knowl.OperationCorrectionReport, err error, code knowl.OutputValidationCode) knowl.OperationCorrectionReport {
	if report.Outcome == knowl.CorrectionUnavailable {
		return report
	}
	switch {
	case errors.Is(err, context.Canceled):
		report.Outcome = knowl.CorrectionCanceled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrCorrectionDeadline):
		report.Outcome = knowl.CorrectionDeadline
	default:
		if report.Outcome == knowl.CorrectionAccepted || code != "" {
			report.Outcome = knowl.CorrectionProviderFailed
		}
		if code != "" {
			report.ValidationCode = code
		}
	}
	return report
}

func validateCorrectionPolicyReport(policy knowl.OutputCorrectionPolicy, report knowl.OperationCorrectionReport, accepted bool) error {
	if report.MaxCorrections != policy.Limits.MaxCorrections || report.MaxOutputBytes > policy.Limits.MaxOutputBytes || report.DeadlineNanos > policy.Limits.DeadlineNanos || (accepted && report.Outcome != knowl.CorrectionAccepted) {
		return ErrOperationCorrectionReportInvalid
	}
	_, err := EncodeOperationCorrectionReport(report)
	return err
}

func (service *IngestService) generateSourcePlan(ctx context.Context, input knowl.MaintenanceInput, inspection knowl.WorkspaceInspection) (knowl.ValidatedEditPlan, knowl.OperationCorrectionReport, error) {
	bounded, cancel := context.WithTimeoutCause(ctx, time.Duration(service.outputPolicy.Limits.DeadlineNanos), ErrCorrectionDeadline)
	defer cancel()
	report := unavailableCorrectionReport(service.outputPolicy)
	var candidate knowl.ModelEditPlan
	var err error
	validatedByCallback := false
	if maintainer, ok := service.maintainer.(ValidatingMaintainer); ok {
		candidate, report, err = maintainer.PlanValidated(bounded, input, service.outputPolicy.Limits, func(plan knowl.ModelEditPlan) error {
			_, err := ValidateMaintenancePlan(bounded, input, plan, inspection, service.catalogLimits, service.planLimits)
			if err == nil {
				validatedByCallback = true
			}
			return err
		})
		if err == nil && !validatedByCallback {
			err = ErrOperationCorrectionReportInvalid
		}
	} else {
		candidate, err = service.maintainer.Plan(bounded, input)
	}
	err = planningError(bounded, err)
	if err != nil {
		report = correctionFailure(report, err, "")
		return knowl.ValidatedEditPlan{}, report, fmt.Errorf("provider: %w", err)
	}
	validated, err := ValidateMaintenancePlan(bounded, input, candidate, inspection, service.catalogLimits, service.planLimits)
	err = planningError(bounded, err)
	if err != nil {
		report = correctionFailure(report, err, knowl.SourcePlanInvalid)
		return knowl.ValidatedEditPlan{}, report, fmt.Errorf("plan_validation: %w", err)
	}
	if service.outputPolicy.Supported {
		if err := validateCorrectionPolicyReport(service.outputPolicy, report, true); err != nil {
			return knowl.ValidatedEditPlan{}, report, fmt.Errorf("provider: %w", err)
		}
	}
	return validated, report, nil
}

func (service *HierarchyService) generateHierarchyPlan(ctx context.Context, input knowl.HierarchyInput, forbidden []string) (knowl.ValidatedHierarchyPlan, knowl.OperationCorrectionReport, error) {
	bounded, cancel := context.WithTimeoutCause(ctx, time.Duration(service.outputPolicy.Limits.DeadlineNanos), ErrCorrectionDeadline)
	defer cancel()
	report := unavailableCorrectionReport(service.outputPolicy)
	options := HierarchyValidationOptions{ForbiddenCatalogTerms: forbidden}
	var candidate knowl.HierarchyModelPlan
	var err error
	validatedByCallback := false
	if maintainer, ok := service.maintainer.(ValidatingHierarchyMaintainer); ok {
		candidate, report, err = maintainer.PlanHierarchyValidated(bounded, input, service.outputPolicy.Limits, func(plan knowl.HierarchyModelPlan) error {
			_, err := ValidateHierarchyPlan(bounded, input, plan, options)
			if err == nil {
				validatedByCallback = true
			}
			return err
		})
		if err == nil && !validatedByCallback {
			err = ErrOperationCorrectionReportInvalid
		}
	} else {
		candidate, err = service.maintainer.PlanHierarchy(bounded, input)
	}
	err = planningError(bounded, err)
	if err != nil {
		report = correctionFailure(report, err, "")
		return knowl.ValidatedHierarchyPlan{}, report, fmt.Errorf("provider: %w", err)
	}
	validated, err := ValidateHierarchyPlan(bounded, input, candidate, options)
	err = planningError(bounded, err)
	if err != nil {
		report = correctionFailure(report, err, knowl.HierarchyPlanInvalid)
		return knowl.ValidatedHierarchyPlan{}, report, fmt.Errorf("plan_validation: %w", err)
	}
	if service.outputPolicy.Supported {
		if err := validateCorrectionPolicyReport(service.outputPolicy, report, true); err != nil {
			return knowl.ValidatedHierarchyPlan{}, report, fmt.Errorf("provider: %w", err)
		}
	}
	return validated, report, nil
}
