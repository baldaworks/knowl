package knowl

import "time"

// ContextOutcome describes a measured selection/assembly result, not work state.
type ContextOutcome string

const (
	ContextAssembled       ContextOutcome = "assembled"
	ContextSelectionFailed ContextOutcome = "selection_failed"
	ContextAssemblyFailed  ContextOutcome = "assembly_failed"
)

// ContextSelectionReason records the first actual selection phase/channel.
type ContextSelectionReason string

const (
	ContextLexical  ContextSelectionReason = "lexical"
	ContextVector   ContextSelectionReason = "vector"
	ContextHybrid   ContextSelectionReason = "hybrid"
	ContextNeighbor ContextSelectionReason = "neighbor"
	ContextRecent   ContextSelectionReason = "recent"
	ContextUnknown  ContextSelectionReason = "unknown"
)

// ContextDisposition distinguishes completed fitting from unprocessed input.
type ContextDisposition string

const (
	ContextIncluded      ContextDisposition = "included"
	ContextBudgetOmitted ContextDisposition = "budget_omitted"
	ContextPending       ContextDisposition = "pending"
)

// VectorProjectionState describes a check during the original attempt.
type VectorProjectionState string

const (
	VectorNotChecked VectorProjectionState = "not_checked"
	VectorReady      VectorProjectionState = "ready"
	VectorInvalid    VectorProjectionState = "invalid"
)

// VectorProjectionStatus never describes current readiness of an old operation.
type VectorProjectionStatus struct {
	State  VectorProjectionState `json:"state"`
	Reason RetrievalFailure      `json:"reason,omitempty"`
}

// ContextPage contains identifiers and categories, never page/source text.
type ContextPage struct {
	PageID          PageID                 `json:"page_id"`
	SelectionReason ContextSelectionReason `json:"selection_reason"`
	Disposition     ContextDisposition     `json:"disposition"`
}

// ContextBudget retains measured request fitting, including an unknown size.
type ContextBudget struct {
	MaxBytes      int  `json:"max_bytes"`
	UsedBytes     *int `json:"used_bytes,omitempty"`
	IncludedCount int  `json:"included_count"`
	OmittedCount  int  `json:"omitted_count"`
}

// OperationContextReport is one immutable, bounded snapshot per work attempt.
type OperationContextReport struct {
	Version          int                     `json:"version"`
	WorkAttempt      int                     `json:"work_attempt"`
	Outcome          ContextOutcome          `json:"outcome"`
	CandidateCount   *int                    `json:"candidate_count,omitempty"`
	CatalogCount     *int                    `json:"catalog_count,omitempty"`
	Pages            []ContextPage           `json:"pages,omitempty"`
	EntriesOmitted   int                     `json:"entries_omitted"`
	Budget           *ContextBudget          `json:"budget,omitempty"`
	VectorProjection *VectorProjectionStatus `json:"vector_projection,omitempty"`
}

// ContextSelectionDiagnostics is detached metadata from the actual index pass.
type ContextSelectionDiagnostics struct {
	Reasons          map[PageID]ContextSelectionReason
	VectorProjection *VectorProjectionStatus
}

// OperationPlanSummary exposes a stored digest and only a known file count.
type OperationPlanSummary struct {
	Digest    string `json:"digest"`
	FileCount *int   `json:"file_count,omitempty"`
}

// OperationExecutionDetails uses existing counters; apply attempts are not inference calls.
type OperationExecutionDetails struct {
	WorkAttempt      int       `json:"work_attempt"`
	RetryAttempt     int       `json:"retry_attempt"`
	ManualRetryCount int       `json:"manual_retry_count"`
	ApplyAttempt     int       `json:"apply_attempt"`
	ReadyAt          time.Time `json:"ready_at,omitzero"`
}

// OperationDetails is the explicitly allowlisted public operation projection.
type OperationDetails struct {
	Correction       *OperationCorrectionReport `json:"correction,omitempty"`
	Context          *OperationContextReport    `json:"context,omitempty"`
	Retrieval        *RetrievalReport           `json:"retrieval,omitempty"`
	RetrievalAttempt *int                       `json:"retrieval_attempt,omitempty"`
	Plan             *OperationPlanSummary      `json:"plan,omitempty"`
	Warnings         []MaintenanceDiagnostic    `json:"warnings,omitempty"`
	WarningsOmitted  int                        `json:"warnings_omitted,omitempty"`
	Execution        *OperationExecutionDetails `json:"execution,omitempty"`
}
