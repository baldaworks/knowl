package app

// ErrMaintenancePolicyMismatch rejects unplanned work reserved under a
// different effective source-maintenance contract or policy.
var ErrMaintenancePolicyMismatch error = maintenancePolicyMismatch{}

type maintenancePolicyMismatch struct{}

func (maintenancePolicyMismatch) Error() string         { return "maintenance policy mismatch" }
func (maintenancePolicyMismatch) FailureClass() string  { return "maintenance_policy" }
func (maintenancePolicyMismatch) FailureReason() string { return "maintenance_policy_mismatch" }
func (maintenancePolicyMismatch) Retryable() bool       { return false }
