package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/sourcedoclist"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var _ app.SourceDocumentLister = (*Store)(nil)

// ListSourceDocuments reads saved heads and tombstones in bytewise document order.
func (store *Store) ListSourceDocuments(ctx context.Context, scope knowl.ScopeRef, id knowl.SourceID, options app.OperatorReadOptions) (app.OperatorReadPage[knowl.OperatorDocumentSummary], error) {
	result := app.OperatorReadPage[knowl.OperatorDocumentSummary]{Items: make([]knowl.OperatorDocumentSummary, 0)}
	if err := sourcedoclist.Validate(id, options); err != nil {
		return result, err
	}
	query := `SELECT document.document_id, document.revision,
 CASE WHEN octet_length(document.accepted_source)<=65536 THEN document.accepted_source ELSE '' END,
 document.maintenance_revision, document.maintenance_operation_id, operation.status, document.deleted, document.updated_at
 FROM knowl_source_documents AS document
 LEFT JOIN knowl_operations AS operation ON operation.scope=document.scope AND operation.operation_id=document.maintenance_operation_id
 WHERE document.scope=$1 AND document.source_id=$2`
	args := []any{scope, id}
	if options.Continuation.Key != "" {
		query += " AND document.document_id COLLATE \"C\">$3"
		args = append(args, options.Continuation.Key)
	}
	query += ` ORDER BY document.document_id COLLATE "C" LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, options.Limit+1)
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return result, fmt.Errorf("list source documents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item knowl.OperatorDocumentSummary
		var accepted string
		var status sql.NullString
		if err := rows.Scan(&item.ID, &item.Revision, &accepted, &item.MaintenanceRevision, &item.MaintenanceOperationID, &status, &item.Deleted, &item.UpdatedAt); err != nil {
			return result, fmt.Errorf("scan source document summary: %w", err)
		}
		item.UpdatedAt = item.UpdatedAt.UTC()
		item.MaintenanceStatus = knowl.OperationStatus(status.String)
		item.AcceptedRevision = sourcedoclist.AcceptedRevision(accepted, scope, id, item.ID, item.Revision)
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("iterate source document summaries: %w", err)
	}
	if len(result.Items) > options.Limit {
		result.Items = result.Items[:options.Limit]
		result.NextKey = string(result.Items[len(result.Items)-1].ID)
	}
	return result, nil
}
