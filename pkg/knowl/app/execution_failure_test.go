package app

import (
	"fmt"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testProviderClass  = "provider"
	testProviderReason = "provider_run"
	testUnsafeProvider = "provider secret"
)

func TestClassifyExecutionFailureValidatesSafeIdentifiersAcrossWrapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "wrapped valid", err: fmt.Errorf("outer: %w", testClassifiedFailure{class: testProviderClass, reason: testProviderReason, retryable: true}), want: true},
		{name: "unsafe class", err: testClassifiedFailure{class: testUnsafeProvider, reason: testProviderReason}},
		{name: "unsafe reason", err: testClassifiedFailure{class: testProviderClass, reason: testUnsafeProvider}},
		{name: "ordinary", err: fmt.Errorf("ordinary")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ClassifyExecutionFailure(test.err)
			if ok != test.want {
				t.Fatalf("ClassifyExecutionFailure() = %#v, %v, want classified %v", got, ok, test.want)
			}
			if ok && (got.Class != testProviderClass || got.Reason != testProviderReason || !got.Retryable) {
				t.Fatalf("ClassifyExecutionFailure() = %#v", got)
			}
		})
	}
}

func TestValidateSafeFailure(t *testing.T) {
	tests := []struct {
		name          string
		failure       knowl.Failure
		requireReason bool
		want          bool
	}{
		{name: "retry metadata", failure: knowl.Failure{Class: testProviderClass, Reason: testProviderReason}, requireReason: true, want: true},
		{name: "legacy terminal metadata", failure: knowl.Failure{Class: testProviderClass}, want: true},
		{name: "missing retry reason", failure: knowl.Failure{Class: testProviderClass}, requireReason: true},
		{name: "unsafe class", failure: knowl.Failure{Class: testUnsafeProvider, Reason: testProviderReason}, requireReason: true},
		{name: "unsafe reason", failure: knowl.Failure{Class: testProviderClass, Reason: testUnsafeProvider}, requireReason: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ValidateSafeFailure(test.failure, test.requireReason); got != test.want {
				t.Fatalf("ValidateSafeFailure() = %v, want %v", got, test.want)
			}
		})
	}
}

type testClassifiedFailure struct {
	class     string
	reason    string
	retryable bool
}

func (failure testClassifiedFailure) Error() string         { return failure.reason }
func (failure testClassifiedFailure) FailureClass() string  { return failure.class }
func (failure testClassifiedFailure) FailureReason() string { return failure.reason }
func (failure testClassifiedFailure) Retryable() bool       { return failure.retryable }
