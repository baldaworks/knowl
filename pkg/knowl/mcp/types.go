package mcp

import (
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// PageResult is the shared complete page and one-hop navigation payload.
type PageResult = app.GetPageResult

// RetrieveResult is the shared compact public evidence payload.
type RetrieveResult = app.PublicRetrieveResult

// IngestResult is the simplified MCP-facing write response.
type IngestResult struct {
	Retrieval   *knowl.RetrievalStatus `json:"retrieval,omitempty"`
	OperationID knowl.OperationID      `json:"operation_id"`
	Status      string                 `json:"status"`
}

// OperationResult is the simplified MCP-facing durable operation model.
type OperationResult struct {
	Details   *knowl.OperationDetails `json:"details,omitempty"`
	Retrieval *knowl.RetrievalStatus  `json:"retrieval,omitempty"`
	ID        knowl.OperationID       `json:"id"`
	Status    string                  `json:"status"`
	UpdatedAt time.Time               `json:"updated_at"`
	Failure   *knowl.Failure          `json:"failure,omitempty"`
}
