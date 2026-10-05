package knowl

// OutputSettings distinguishes an omitted allowance from explicit zero.
type OutputSettings struct {
	MaxCorrections *int `json:"max_corrections,omitempty" mapstructure:"max_corrections"`
}

// OutputCorrectionLimits bounds one complete planning sequence.
type OutputCorrectionLimits struct {
	MaxCorrections int   `json:"max_corrections"`
	MaxOutputBytes int   `json:"max_output_bytes"`
	DeadlineNanos  int64 `json:"deadline_nanos"`
}

// OutputCorrectionPolicy contains only effective non-secret planning choices.
type OutputCorrectionPolicy struct {
	Supported bool                   `json:"supported"`
	Limits    OutputCorrectionLimits `json:"limits"`
}

type OutputValidationCode string

const (
	StructuredOutputInvalid OutputValidationCode = "structured_output_invalid"
	SourcePlanInvalid       OutputValidationCode = "source_plan_invalid"
	HierarchyPlanInvalid    OutputValidationCode = "hierarchy_plan_invalid"
)

type CorrectionOutcome string

const (
	CorrectionAccepted       CorrectionOutcome = "accepted"
	CorrectionExhausted      CorrectionOutcome = "exhausted"
	CorrectionOutputLimit    CorrectionOutcome = "output_limit"
	CorrectionDeadline       CorrectionOutcome = "deadline"
	CorrectionCanceled       CorrectionOutcome = "canceled"
	CorrectionProviderFailed CorrectionOutcome = "provider_failed"
	CorrectionUnavailable    CorrectionOutcome = "unavailable"
)

// OperationCorrectionReport is one immutable, content-free attempt report.
// OutputBytes counts text accepted by the raw guard; on overflow it is a prefix.
// Nil counters mean unavailable, never a measured zero.
type OperationCorrectionReport struct {
	Version        int                  `json:"version"`
	WorkAttempt    int                  `json:"work_attempt"`
	MaxCorrections int                  `json:"max_corrections"`
	MaxOutputBytes int                  `json:"max_output_bytes"`
	DeadlineNanos  int64                `json:"deadline_nanos"`
	Outcome        CorrectionOutcome    `json:"outcome"`
	Turns          *int                 `json:"turns,omitempty"`
	Corrections    *int                 `json:"corrections,omitempty"`
	OutputBytes    *int                 `json:"output_bytes,omitempty"`
	ValidationCode OutputValidationCode `json:"validation_code,omitempty"`
}
