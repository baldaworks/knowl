package provider

const (
	providerFailureClass        = "provider"
	reasonProviderBuild         = "provider_build"
	reasonProviderSession       = "provider_session"
	reasonProviderRun           = "provider_run"
	reasonProviderInput         = "provider_input"
	reasonProviderInputLimit    = "provider_input_limit"
	reasonProviderOutputEmpty   = "provider_output_empty"
	reasonProviderOutputLimit   = "provider_output_limit"
	reasonProviderOutputInvalid = "provider_output_invalid"
	reasonProviderSetup         = "provider_setup"
)

type executionFailure struct {
	reason    string
	retryable bool
}

func (failure *executionFailure) Error() string         { return failure.reason }
func (failure *executionFailure) FailureClass() string  { return providerFailureClass }
func (failure *executionFailure) FailureReason() string { return failure.reason }
func (failure *executionFailure) Retryable() bool       { return failure.retryable }

func transientProviderFailure(reason string) error {
	return &executionFailure{reason: reason, retryable: true}
}

func permanentProviderFailure(reason string) error {
	return &executionFailure{reason: reason}
}
