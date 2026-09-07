package llmstxt

import "errors"

var (
	// ErrInvalidInput identifies malformed metadata, paths, or options.
	ErrInvalidInput = errors.New("invalid llms.txt input")
	// ErrInvalidGraph identifies malformed, cyclic, missing, or unreachable
	// catalog graph members.
	ErrInvalidGraph = errors.New("invalid llms.txt catalog graph")
	// ErrLimitExceeded identifies input or output that exceeds a configured
	// renderer bound.
	ErrLimitExceeded = errors.New("llms.txt limit exceeded")
)
