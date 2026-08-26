package mcp

import (
	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

func retrieveResult(result app.QueryResult) RetrieveResult {
	evidence := make([]EvidenceItem, 0, len(result.Pages))
	for _, page := range result.Pages {
		sourceDocuments := append([]knowl.SourceDocument(nil), page.SourceDocuments...)
		if len(sourceDocuments) == 0 && page.SourceDocument != nil {
			sourceDocuments = append(sourceDocuments, *page.SourceDocument)
		}
		item := EvidenceItem{
			PageID:          page.ID,
			Title:           page.Title,
			Snippet:         page.Snippet,
			SourceRefs:      append([]string(nil), page.SourceRefs...),
			SourceDocuments: sourceDocuments,
			OKF:             page.OKF,
			Untrusted:       page.Untrusted,
		}
		if page.SourceDocument != nil {
			item.SourceID = string(page.SourceDocument.SourceID)
			item.DocumentID = string(page.SourceDocument.DocumentID)
			item.Revision = page.SourceDocument.Revision
			item.URI = page.SourceDocument.URI
		}
		evidence = append(evidence, item)
	}
	citations := make([]app.Citation, len(result.Citations))
	copy(citations, result.Citations)
	return RetrieveResult{Query: result.Query, Evidence: evidence, Citations: citations}
}

func operationResult(operation knowl.Operation) OperationResult {
	return OperationResult{
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
