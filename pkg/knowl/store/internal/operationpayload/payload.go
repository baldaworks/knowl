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
	Version   int                                 `json:"version"`
	Hierarchy *knowl.HierarchyExecutionDescriptor `json:"hierarchy,omitempty"`
}

// EncodeHierarchy returns the versioned hierarchy-only payload.
func EncodeHierarchy(descriptor knowl.ExecutionDescriptor) (string, error) {
	if descriptor.Kind != knowl.WorkHierarchy || descriptor.Hierarchy == nil {
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
	if payload.Version != Version || payload.Hierarchy == nil {
		return app.ErrExecutionDescriptorUnavailable
	}
	descriptor.Kind = knowl.WorkHierarchy
	descriptor.Hierarchy = payload.Hierarchy
	return nil
}

// Kind validates the durable discriminator.
func Kind(raw string) (knowl.WorkKind, error) {
	kind := knowl.WorkKind(raw)
	if kind != knowl.WorkSourceMaintenance && kind != knowl.WorkHierarchy {
		return "", fmt.Errorf("unknown work kind: %w", app.ErrExecutionDescriptorUnavailable)
	}
	return kind, nil
}
