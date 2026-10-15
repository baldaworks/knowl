// Package retirementcommit seals the host-only canonical retirement call from
// downstream users of the public filesystem workspace API.
package retirementcommit

import knowl "github.com/baldaworks/knowl/pkg/knowl/types"

// Request is an internal call envelope. Downstream packages cannot import this
// package, and the staged change cannot be forged without NewRequest.
type Request struct{ staged knowl.StagedChange }

// NewRequest creates one host-internal canonical retirement call.
func NewRequest(staged knowl.StagedChange) Request { return Request{staged: staged} }

// Stage returns the bounded staged change carried by the internal call.
func (request Request) Stage() knowl.StagedChange { return request.staged }
