package fs

import "errors"

var (
	ErrInvalidSource                 = errors.New("invalid source envelope")
	ErrSourceNotFound                = errors.New("source not found")
	ErrSourceConflict                = errors.New("source version digest conflict")
	ErrDigestMismatch                = errors.New("source digest mismatch")
	ErrWorkspaceInvalid              = errors.New("invalid Knowl workspace")
	ErrPathRejected                  = errors.New("workspace path rejected")
	ErrContentInvalid                = errors.New("workspace content validation failed")
	ErrPlanConflict                  = errors.New("staged plan conflict")
	ErrPrecondition            error = preconditionFailure{}
	ErrExportDestinationExists       = errors.New("export destination exists")
	ErrExportLimitExceeded           = errors.New("export limit exceeded")
	ErrExportSourceChanged           = errors.New("export source changed")
	ErrWorkspaceBusy                 = errors.New("workspace filesystem is busy")
)

type preconditionFailure struct{}

func (preconditionFailure) Error() string         { return "workspace file precondition failed" }
func (preconditionFailure) FailureClass() string  { return "canonical_conflict" }
func (preconditionFailure) FailureReason() string { return "precondition_failed" }
func (preconditionFailure) Retryable() bool       { return false }
