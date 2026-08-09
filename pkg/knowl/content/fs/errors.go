package fs

import "errors"

var (
	ErrInvalidSource    = errors.New("invalid source envelope")
	ErrSourceNotFound   = errors.New("source not found")
	ErrSourceConflict   = errors.New("source version digest conflict")
	ErrDigestMismatch   = errors.New("source digest mismatch")
	ErrWorkspaceInvalid = errors.New("invalid Knowl workspace")
	ErrPathRejected     = errors.New("workspace path rejected")
	ErrPlanConflict     = errors.New("staged plan conflict")
	ErrPrecondition     = errors.New("workspace file precondition failed")
)
