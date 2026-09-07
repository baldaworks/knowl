// Package git implements the bounded remote Git source adapter.
package git

import (
	"errors"
	"fmt"
)

// Standard stable failure classes matching the Knowl domain vocabulary.
const (
	ClassReachability               = "reachability"
	ClassAuthentication             = "authentication"
	ClassHostVerification           = "host_verification"
	ClassUnsupportedTransport       = "unsupported_transport"
	ClassMissingRef                 = "missing_ref"
	ClassMovedTag                   = "moved_tag"
	ClassRejectedHistoryRewrite     = "rejected_history_rewrite"
	ClassRepositoryIdentityMismatch = "repository_identity_mismatch"
	ClassResourceLimit              = "resource_limit"
	ClassScanInvalid                = "scan_invalid"
	ClassFetch                      = "fetch"
)

var (
	// ErrReachability reports failure to connect to the remote host.
	ErrReachability = errors.New("git repository unreachable")
	// ErrAuthentication reports invalid or rejected credentials.
	ErrAuthentication = errors.New("git authentication failed")
	// ErrHostVerification reports an unknown or mismatched SSH host key.
	ErrHostVerification = errors.New("git host verification failed")
	// ErrUnsupportedTransport reports a rejected or disallowed transport scheme.
	ErrUnsupportedTransport = errors.New("unsupported git transport")
	// ErrMissingRef reports that the tracked branch or tag does not exist on the remote.
	ErrMissingRef = errors.New("tracked git ref not found on remote")
	// ErrMovedTag reports that a tracked tag resolved to a different target without authorization.
	ErrMovedTag = errors.New("tracked git tag target moved without authorization")
	// ErrRejectedHistoryRewrite reports a non-fast-forward branch advance without authorization.
	ErrRejectedHistoryRewrite = errors.New("git branch history rewritten without authorization")
	// ErrRepositoryIdentityMismatch reports a remote repository change without rebind authorization.
	ErrRepositoryIdentityMismatch = errors.New("git remote repository identity changed without authorization")
	// ErrLimit reports that a Git source exceeded a configured limit.
	ErrLimit = errors.New("git source exceeds a limit")
	// ErrDocumentNotFound reports that a listed document blob is missing in the snapshot.
	ErrDocumentNotFound = errors.New("git source document not found")
	// ErrRevisionChanged reports that a document revision does not match the listed descriptor.
	ErrRevisionChanged = errors.New("git source document revision changed")
	// ErrPageToken reports an invalid or corrupted pagination token.
	ErrPageToken = errors.New("git source page token invalid")
	// ErrScanInvalid reports an invalid snapshot or repository state during scan.
	ErrScanInvalid = errors.New("git source scan invalid")
)

// ClassifiedError pairs an error with a stable failure classification.
type ClassifiedError interface {
	error
	FailureClass() string
	SafeDetail() string
}

type classifiedError struct {
	class  string
	cause  error
	detail string
}

func (e *classifiedError) Error() string {
	if e.detail != "" {
		return fmt.Sprintf("%s: %s", e.class, e.detail)
	}
	if e.cause != nil {
		return fmt.Sprintf("%s: %s", e.class, RedactString(e.cause.Error()))
	}
	return e.class
}

func (e *classifiedError) Unwrap() error {
	return e.cause
}

func (e *classifiedError) FailureClass() string {
	return e.class
}

func (e *classifiedError) SafeDetail() string {
	return e.detail
}

// WrapClassified wraps an error with a stable failure class and sanitized detail.
func WrapClassified(class string, cause error, detail string) error {
	return &classifiedError{
		class:  class,
		cause:  cause,
		detail: RedactString(detail),
	}
}

// ClassOfError extracts the failure class from an error if available.
func ClassOfError(err error) string {
	var classified ClassifiedError
	if errors.As(err, &classified) {
		return classified.FailureClass()
	}
	switch {
	case errors.Is(err, ErrReachability):
		return ClassReachability
	case errors.Is(err, ErrAuthentication):
		return ClassAuthentication
	case errors.Is(err, ErrHostVerification):
		return ClassHostVerification
	case errors.Is(err, ErrUnsupportedTransport):
		return ClassUnsupportedTransport
	case errors.Is(err, ErrMissingRef):
		return ClassMissingRef
	case errors.Is(err, ErrMovedTag):
		return ClassMovedTag
	case errors.Is(err, ErrRejectedHistoryRewrite):
		return ClassRejectedHistoryRewrite
	case errors.Is(err, ErrRepositoryIdentityMismatch):
		return ClassRepositoryIdentityMismatch
	case errors.Is(err, ErrLimit):
		return ClassResourceLimit
	case errors.Is(err, ErrScanInvalid):
		return ClassScanInvalid
	default:
		return ""
	}
}
