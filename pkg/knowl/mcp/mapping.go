package mcp

import (
	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

func operationResult(operation knowl.Operation) OperationResult {
	return OperationResult{
		Details:   app.PublicOperationDetails(operation),
		Retrieval: app.PublicRetrievalStatus(operation.Retrieval),
		ID:        operation.ID,
		Status:    publicOperationStatus(operation.Status),
		UpdatedAt: operation.UpdatedAt,
		Failure:   operation.Failure,
	}
}

func publicOperationStatus(status knowl.OperationStatus) string {
	switch status {
	case knowl.StatusApplying:
		return "running"
	case knowl.StatusCommitted:
		return "completed"
	case knowl.StatusFailed:
		return "failed"
	default:
		return "queued"
	}
}
