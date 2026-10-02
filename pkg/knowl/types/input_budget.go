package knowl

// MaintenanceInputLimits bounds the complete current source-maintenance user
// prompt, including its envelope and structured wrapper, in UTF-8 bytes.
type MaintenanceInputLimits struct {
	MaxRequestBytes int `json:"max_request_bytes"`
}

// MaintenanceRequestBudget declares a maintainer's finite request capacity and
// non-secret serialization/sizing policy identity. It does not identify a URL.
type MaintenanceRequestBudget struct {
	MaxBytes      int
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
