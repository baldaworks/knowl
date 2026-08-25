package mcp

import (
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// RetrieveResult is the bounded evidence payload returned by knowl_retrieve.
type RetrieveResult struct {
	Query     string         `json:"query"`
	Evidence  []EvidenceItem `json:"evidence"`
	Citations []app.Citation `json:"citations,omitempty"`
}

// EvidenceItem is one bounded page-derived evidence record.
type EvidenceItem struct {
	PageID     knowl.PageID  `json:"page_id"`
	Title      string        `json:"title"`
	Snippet    string        `json:"snippet"`
	SourceRefs []string      `json:"source_refs,omitempty"`
	SourceID   string        `json:"source_id,omitempty"`
	DocumentID string        `json:"document_id,omitempty"`
	Revision   string        `json:"revision,omitempty"`
	URI        string        `json:"uri,omitempty"`
	OKF        *okf.Metadata `json:"okf,omitempty"`
	Untrusted  bool          `json:"untrusted"`
}

// IngestResult is the simplified MCP-facing write response.
type IngestResult struct {
	OperationID knowl.OperationID `json:"operation_id"`
	Status      string            `json:"status"`
}

// OperationResult is the simplified MCP-facing durable operation model.
type OperationResult struct {
	ID        knowl.OperationID `json:"id"`
	Status    string            `json:"status"`
	UpdatedAt time.Time         `json:"updated_at"`
	Failure   *knowl.Failure    `json:"failure,omitempty"`
}
