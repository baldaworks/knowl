package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	MaxCorrectionOutputBytes   = 1 << 20
	MaxCorrectionDeadline      = 5 * time.Minute
	MaxCorrectionFeedbackBytes = 128
)

var ErrOutputCorrectionInvalid = errors.New("invalid output correction policy")

// ValidatingMaintainer shares one correction allowance with the app validator.
type ValidatingMaintainer interface {
	PlanValidated(ctx context.Context, input knowl.MaintenanceInput, limits knowl.OutputCorrectionLimits, validate func(knowl.ModelEditPlan) error) (knowl.ModelEditPlan, knowl.OperationCorrectionReport, error)
}

type ValidatingHierarchyMaintainer interface {
	PlanHierarchyValidated(ctx context.Context, input knowl.HierarchyInput, limits knowl.OutputCorrectionLimits, validate func(knowl.HierarchyModelPlan) error) (knowl.HierarchyModelPlan, knowl.OperationCorrectionReport, error)
}

// NormalizeOutputSettings applies the fixed sequence ceilings and explicit allowance.
func NormalizeOutputSettings(settings knowl.OutputSettings) (knowl.OutputCorrectionLimits, error) {
	allowance := 1
	if settings.MaxCorrections != nil {
		allowance = *settings.MaxCorrections
	}
	limits := knowl.OutputCorrectionLimits{MaxCorrections: allowance, MaxOutputBytes: MaxCorrectionOutputBytes, DeadlineNanos: int64(MaxCorrectionDeadline)}
	return limits, ValidateOutputCorrectionLimits(limits)
}

// ValidateOutputCorrectionLimits enforces hard ceilings on a planning request.
func ValidateOutputCorrectionLimits(limits knowl.OutputCorrectionLimits) error {
	if limits.MaxCorrections < 0 || limits.MaxCorrections > 1 || limits.MaxOutputBytes <= 0 || limits.MaxOutputBytes > MaxCorrectionOutputBytes || limits.DeadlineNanos <= 0 || limits.DeadlineNanos > int64(MaxCorrectionDeadline) {
		return ErrOutputCorrectionInvalid
	}
	return nil
}

func EffectiveOutputCorrectionPolicy(settings knowl.OutputSettings, supported bool) (knowl.OutputCorrectionPolicy, error) {
	limits, err := NormalizeOutputSettings(settings)
	if err != nil {
		return knowl.OutputCorrectionPolicy{}, err
	}
	if !supported {
		limits.MaxCorrections = 0
	}
	return knowl.OutputCorrectionPolicy{Supported: supported, Limits: limits}, nil
}

func validateOutputCorrectionPolicy(policy *knowl.OutputCorrectionPolicy) error {
	if policy == nil {
		return nil
	}
	if ValidateOutputCorrectionLimits(policy.Limits) != nil || (!policy.Supported && policy.Limits.MaxCorrections != 0) {
		return ErrExecutionDescriptorUnavailable
	}
	return nil
}

// HierarchyOutputPlannerVersion binds planning identity to its effective policy.
func HierarchyOutputPlannerVersion(base string, policy knowl.OutputCorrectionPolicy) (string, error) {
	if !validRequestFormat(base) || validateOutputCorrectionPolicy(&policy) != nil {
		return "", ErrExecutionDescriptorUnavailable
	}
	payload, err := json.Marshal(struct {
		Planner string                       `json:"planner"`
		Output  knowl.OutputCorrectionPolicy `json:"output"`
	}{base, policy})
	if err != nil {
		return "", ErrExecutionDescriptorUnavailable
	}
	digest := sha256.Sum256(payload)
	return "output-v1-" + hex.EncodeToString(digest[:]), nil
}

type outputCorrectionFailure string

func (f outputCorrectionFailure) Error() string         { return string(f) }
func (f outputCorrectionFailure) FailureClass() string  { return "provider" }
func (f outputCorrectionFailure) FailureReason() string { return string(f) }
func (f outputCorrectionFailure) Retryable() bool       { return false }

var (
	ErrOutputCorrectionExhausted error = outputCorrectionFailure("provider_output_exhausted")
	ErrCorrectionOutputLimit     error = outputCorrectionFailure("provider_output_limit")
	ErrCorrectionDeadline        error = outputCorrectionFailure("provider_output_deadline")
)
