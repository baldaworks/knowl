// Package operationpayload owns the bounded durable non-source descriptor
// envelope shared by SQLite and PostgreSQL stores.
package operationpayload

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	Version  = 1
	MaxBytes = 64 << 10
)

type envelope struct {
	Version        int                                      `json:"version"`
	Hierarchy      *knowl.HierarchyExecutionDescriptor      `json:"hierarchy,omitempty"`
	RetirementPage *knowl.RetirementPageExecutionDescriptor `json:"retirement_page,omitempty"`
}

// Encode returns the strict versioned payload for one generic work kind.
func Encode(descriptor knowl.ExecutionDescriptor) (string, error) {
	switch descriptor.Kind {
	case knowl.WorkHierarchy:
		return EncodeHierarchy(descriptor)
	case knowl.WorkSourceRetirementPage:
		return EncodeRetirementPage(descriptor)
	default:
		return "", app.ErrExecutionDescriptorUnavailable
	}
}

// Decode attaches the strict payload selected by the durable row kind.
func Decode(kind knowl.WorkKind, raw string, descriptor *knowl.ExecutionDescriptor) error {
	switch kind {
	case knowl.WorkHierarchy:
		return DecodeHierarchy(raw, descriptor)
	case knowl.WorkSourceRetirementPage:
		return DecodeRetirementPage(raw, descriptor)
	default:
		return app.ErrExecutionDescriptorUnavailable
	}
}

// EncodeHierarchy returns the versioned hierarchy-only payload.
func EncodeHierarchy(descriptor knowl.ExecutionDescriptor) (string, error) {
	if descriptor.Kind != knowl.WorkHierarchy || descriptor.Hierarchy == nil || descriptor.RetirementPage != nil {
		return "", app.ErrExecutionDescriptorUnavailable
	}
	encoded, err := json.Marshal(envelope{Version: Version, Hierarchy: descriptor.Hierarchy})
	if err != nil || len(encoded) > MaxBytes {
		return "", app.ErrExecutionDescriptorUnavailable
	}
	return string(encoded), nil
}

// DecodeHierarchy attaches one strict bounded payload to its row-owned fields.
func DecodeHierarchy(raw string, descriptor *knowl.ExecutionDescriptor) error {
	if descriptor == nil || len(raw) == 0 || len(raw) > MaxBytes {
		return app.ErrExecutionDescriptorUnavailable
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var payload envelope
	if err := decoder.Decode(&payload); err != nil {
		return app.ErrExecutionDescriptorUnavailable
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return app.ErrExecutionDescriptorUnavailable
	}
	if payload.Version != Version || payload.Hierarchy == nil || payload.RetirementPage != nil {
		return app.ErrExecutionDescriptorUnavailable
	}
	descriptor.Kind = knowl.WorkHierarchy
	descriptor.Hierarchy = payload.Hierarchy
	return nil
}

// EncodeRetirementPage returns the versioned page-only retirement payload.
func EncodeRetirementPage(descriptor knowl.ExecutionDescriptor) (string, error) {
	if descriptor.Kind != knowl.WorkSourceRetirementPage || descriptor.RetirementPage == nil || descriptor.Hierarchy != nil {
		return "", app.ErrExecutionDescriptorUnavailable
	}
	encoded, err := json.Marshal(envelope{Version: Version, RetirementPage: descriptor.RetirementPage})
	if err != nil || len(encoded) > MaxBytes {
		return "", app.ErrExecutionDescriptorUnavailable
	}
	return string(encoded), nil
}

// DecodeRetirementPage attaches one strict bounded payload to row-owned fields.
func DecodeRetirementPage(raw string, descriptor *knowl.ExecutionDescriptor) error {
	payload, err := decode(raw)
	if err != nil || payload.RetirementPage == nil || payload.Hierarchy != nil {
		return app.ErrExecutionDescriptorUnavailable
	}
	descriptor.Kind = knowl.WorkSourceRetirementPage
	descriptor.RetirementPage = payload.RetirementPage
	return nil
}

func decode(raw string) (envelope, error) {
	if len(raw) == 0 || len(raw) > MaxBytes {
		return envelope{}, app.ErrExecutionDescriptorUnavailable
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var payload envelope
	if err := decoder.Decode(&payload); err != nil {
		return envelope{}, app.ErrExecutionDescriptorUnavailable
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || payload.Version != Version {
		return envelope{}, app.ErrExecutionDescriptorUnavailable
	}
	return payload, nil
}

// Kind validates the durable discriminator.
func Kind(raw string) (knowl.WorkKind, error) {
	kind := knowl.WorkKind(raw)
	if kind != knowl.WorkSourceMaintenance && kind != knowl.WorkHierarchy && kind != knowl.WorkSourceRetirementPage {
		return "", fmt.Errorf("unknown work kind: %w", app.ErrExecutionDescriptorUnavailable)
	}
	return kind, nil
}
