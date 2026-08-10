// Package knowl continues to export the public domain contracts during the
// transition to pkg/knowl/types. New code should import the types subpackage
// directly.
package knowl

import knowltypes "github.com/baldaworks/knowl/pkg/knowl/types"

type (
	// Deprecated: use knowltypes.ScopeRef from pkg/knowl/types.
	ScopeRef = knowltypes.ScopeRef
	// Deprecated: use knowltypes.SourceRef from pkg/knowl/types.
	SourceRef = knowltypes.SourceRef
	// Deprecated: use knowltypes.SourceVersion from pkg/knowl/types.
	SourceVersion = knowltypes.SourceVersion
	// Deprecated: use knowltypes.SourceEnvelope from pkg/knowl/types.
	SourceEnvelope = knowltypes.SourceEnvelope
	// Deprecated: use knowltypes.AcceptedSource from pkg/knowl/types.
	AcceptedSource = knowltypes.AcceptedSource
	// Deprecated: use knowltypes.SchemaDocument from pkg/knowl/types.
	SchemaDocument = knowltypes.SchemaDocument
	// Deprecated: use knowltypes.PageID from pkg/knowl/types.
	PageID = knowltypes.PageID
	// Deprecated: use knowltypes.PageSnapshot from pkg/knowl/types.
	PageSnapshot = knowltypes.PageSnapshot
	// Deprecated: use knowltypes.ReadLimits from pkg/knowl/types.
	ReadLimits = knowltypes.ReadLimits
	// Deprecated: use knowltypes.FileEdit from pkg/knowl/types.
	FileEdit = knowltypes.FileEdit
	// Deprecated: use knowltypes.ValidatedEditPlan from pkg/knowl/types.
	ValidatedEditPlan = knowltypes.ValidatedEditPlan
	// Deprecated: use knowltypes.StagedChange from pkg/knowl/types.
	StagedChange = knowltypes.StagedChange
	// Deprecated: use knowltypes.ContentCommit from pkg/knowl/types.
	ContentCommit = knowltypes.ContentCommit
	// Deprecated: use knowltypes.RecoveryResult from pkg/knowl/types.
	RecoveryResult = knowltypes.RecoveryResult
	// Deprecated: use knowltypes.OperationID from pkg/knowl/types.
	OperationID = knowltypes.OperationID
	// Deprecated: use knowltypes.OperationKey from pkg/knowl/types.
	OperationKey = knowltypes.OperationKey
	// Deprecated: use knowltypes.OperationMeta from pkg/knowl/types.
	OperationMeta = knowltypes.OperationMeta
	// Deprecated: use knowltypes.OperationStatus from pkg/knowl/types.
	OperationStatus = knowltypes.OperationStatus
	// Deprecated: use knowltypes.Operation from pkg/knowl/types.
	Operation = knowltypes.Operation
	// Deprecated: use knowltypes.PlanSummary from pkg/knowl/types.
	PlanSummary = knowltypes.PlanSummary
	// Deprecated: use knowltypes.Lease from pkg/knowl/types.
	Lease = knowltypes.Lease
	// Deprecated: use knowltypes.Failure from pkg/knowl/types.
	Failure = knowltypes.Failure
	// Deprecated: use knowltypes.SourceSummary from pkg/knowl/types.
	SourceSummary = knowltypes.SourceSummary
	// Deprecated: use knowltypes.PageReference from pkg/knowl/types.
	PageReference = knowltypes.PageReference
	// Deprecated: use knowltypes.LinkReference from pkg/knowl/types.
	LinkReference = knowltypes.LinkReference
	// Deprecated: use knowltypes.WorkspaceSnapshot from pkg/knowl/types.
	WorkspaceSnapshot = knowltypes.WorkspaceSnapshot
	// Deprecated: use knowltypes.RawSourceRecord from pkg/knowl/types.
	RawSourceRecord = knowltypes.RawSourceRecord
	// Deprecated: use knowltypes.WorkspaceInspection from pkg/knowl/types.
	WorkspaceInspection = knowltypes.WorkspaceInspection
	// Deprecated: use knowltypes.LintFinding from pkg/knowl/types.
	LintFinding = knowltypes.LintFinding
	// Deprecated: use knowltypes.LintReport from pkg/knowl/types.
	LintReport = knowltypes.LintReport
	// Deprecated: use knowltypes.MaintenanceInput from pkg/knowl/types.
	MaintenanceInput = knowltypes.MaintenanceInput
	// Deprecated: use knowltypes.ModelEditPlan from pkg/knowl/types.
	ModelEditPlan = knowltypes.ModelEditPlan
)

const (
	// Deprecated: use knowltypes.StatusReceived from pkg/knowl/types.
	StatusReceived = knowltypes.StatusReceived
	// Deprecated: use knowltypes.StatusPlanned from pkg/knowl/types.
	StatusPlanned = knowltypes.StatusPlanned
	// Deprecated: use knowltypes.StatusAwaitingReview from pkg/knowl/types.
	StatusAwaitingReview = knowltypes.StatusAwaitingReview
	// Deprecated: use knowltypes.StatusApplying from pkg/knowl/types.
	StatusApplying = knowltypes.StatusApplying
	// Deprecated: use knowltypes.StatusCommitted from pkg/knowl/types.
	StatusCommitted = knowltypes.StatusCommitted
	// Deprecated: use knowltypes.StatusFailed from pkg/knowl/types.
	StatusFailed = knowltypes.StatusFailed
)
