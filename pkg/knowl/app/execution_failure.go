package app

import (
	"errors"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// ClassifiedExecutionError is a safely renderable execution failure whose
// retry policy does not depend on parsing its Error string.
type ClassifiedExecutionError interface {
	error
	FailureClass() string
	FailureReason() string
	Retryable() bool
}

// ValidateSafeFailure verifies bounded metadata before it crosses a durable
// store boundary. A reason is required for retry scheduling, while terminal
// legacy failures may omit it.
func ValidateSafeFailure(failure knowl.Failure, requireReason bool) bool {
	if len(failure.Class) == 0 || len(failure.Class) > maxFailureClassBytes || !failurePattern.MatchString(failure.Class) {
		return false
	}
	if failure.Reason == "" {
		return !requireReason
	}
	return len(failure.Reason) <= maxFailureClassBytes && failurePattern.MatchString(failure.Reason)
}

// ExecutionFailureInfo is the validated, redacted policy carried by a
// ClassifiedExecutionError.
type ExecutionFailureInfo struct {
	Class     string
	Reason    string
	Retryable bool
}

// ClassifyExecutionFailure returns only bounded identifier-shaped metadata.
// Invalid implementations are ignored rather than trusted as safe output.
func ClassifyExecutionFailure(err error) (ExecutionFailureInfo, bool) {
	var classified ClassifiedExecutionError
	if !errors.As(err, &classified) {
		return ExecutionFailureInfo{}, false
	}
	class := classified.FailureClass()
	reason := classified.FailureReason()
	if len(class) == 0 || len(class) > maxFailureClassBytes || !failurePattern.MatchString(class) ||
		len(reason) == 0 || len(reason) > maxFailureClassBytes || !failurePattern.MatchString(reason) {
		return ExecutionFailureInfo{}, false
	}
	return ExecutionFailureInfo{Class: class, Reason: reason, Retryable: classified.Retryable()}, true
}
