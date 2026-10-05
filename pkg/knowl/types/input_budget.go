package knowl

// MaintenanceInputLimits bounds the complete current source-maintenance user
// prompt, including its envelope and structured wrapper, in UTF-8 bytes.
type MaintenanceInputLimits struct {
	// Zero selects 4 MiB; embedded custom caps are positive and at most 4 MiB.
	MaxRequestBytes int `json:"max_request_bytes"`
}

// MaintenanceRequestBudget declares a maintainer's finite request capacity and
// non-secret serialization/sizing policy identity. It does not identify a URL.
type MaintenanceRequestBudget struct {
	// MaxBytes must be positive; the app uses the smaller local/provider cap.
	MaxBytes int
	// ReservedBytes leaves room for bounded corrective feedback; zero is compatible.
	ReservedBytes int
	// FormatVersion must be nonempty printable text, at most 256 UTF-8 bytes.
	FormatVersion string
}

// MaintenanceBudgetReport contains transient, content-free assembly evidence.
// It is not a durable HTTP/MCP diagnostics contract.
type MaintenanceBudgetReport struct {
	MaxBytes      int
	UsedBytes     int
	IncludedCount int
	OmittedCount  int
}
